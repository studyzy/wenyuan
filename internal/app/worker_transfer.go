package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/transfer"
	"github.com/studyzy/wenyuan/internal/transferarchive"
	"github.com/studyzy/wenyuan/internal/worker"
)

// applyExport builds an archive for a queued export operation, stashes it in
// stored-file storage and publishes the download handle on the operation's
// result_metadata. The bytes are produced here rather than by the export route
// because a whole-bank archive can be large: building it on the request path
// would pin the API on the size of the bank.
func (p archiveProcessor) applyExport(ctx context.Context, op storage.Operation) error {
	options := exportOptionsFrom(op)
	archive, err := transfer.BuildArchive(ctx, p.store, op.BankID, options)
	if err != nil {
		return err
	}
	data, err := transferarchive.Write(archive)
	if err != nil {
		return err
	}
	// A fresh key per export keeps two concurrent exports of the same bank from
	// clobbering each other's archive. The two export routes key their archives
	// differently in the reference, and the key is published in the operation's
	// result_metadata, so the difference is observable.
	key := storage.ExportStorageKey(op.BankID, op.OperationID)
	if op.OperationType == storage.OperationTypeDocumentExport {
		key = transfer.DocumentExportStorageKey(p.storeSchema, op.BankID, op.OperationID)
	}
	if err := p.store.PutStoredFile(ctx, storage.StoredFile{
		Key:         key,
		ContentType: "application/zip",
		Data:        data,
	}); err != nil {
		return err
	}
	worker.ReportOutcome(ctx, map[string]any{
		"storage_key":  key,
		"download_url": downloadURLFor(key),
		"byte_size":    len(data),
		"filename":     exportFilename(op),
	})
	return nil
}

// applyImport restores a stashed archive into the operation's bank.
func (p archiveProcessor) applyImport(ctx context.Context, op storage.Operation) error {
	key, _ := op.Payload[storage.OperationPayloadKeyStoredFile].(string)
	if key == "" {
		return worker.Permanent(errors.New("operation carries no stored archive key"))
	}
	file, err := p.store.GetStoredFile(ctx, key)
	if err != nil {
		// A missing payload cannot appear by retrying, so it is permanent.
		return worker.Permanent(fmt.Errorf("stored archive is not readable: %w", err))
	}
	archive, err := transfer.ParseArchive(file.Data)
	if err != nil {
		// The archive was validated when it was accepted, so an unreadable one
		// now is permanent rather than transient.
		return worker.Permanent(err)
	}
	// The task payload follows the reference: a bank restore records an
	// import_bank operation with no mode member, a document merge records an
	// import_documents operation, so the operation type is the discriminator.
	mode := transfer.ImportMode(payloadString(op, "mode"))
	if mode == "" {
		if op.OperationType == storage.OperationTypeTransferImport {
			mode = transfer.ImportRestore
		} else {
			mode = transfer.ImportMerge
		}
	}
	target, _ := op.Payload["target_bank_id"].(string)
	if target == "" {
		target = op.BankID
	}
	report, err := p.importApplier().Apply(ctx, transfer.ImportRequest{
		Archive:      archive,
		BankID:       op.BankID,
		TargetBankID: target,
		Mode:         mode,
		OnConflict:   payloadString(op, "on_conflict"),
		Scope:        scopeFromPayload(op),
	})
	if err != nil {
		if errors.Is(err, transfer.ErrTargetBankExists) {
			// The target existed when the operation ran even though it did not
			// when it was submitted: retrying would fail the same way.
			return worker.Permanent(err)
		}
		return err
	}
	worker.ReportOutcome(ctx, report.Metadata())
	return nil
}

// applyClone copies a bank by exporting it and restoring the copy under the
// target identifier, in one background operation with no archive for the
// caller to carry.
func (p archiveProcessor) applyClone(ctx context.Context, op storage.Operation) error {
	target, _ := op.Payload["target_bank_id"].(string)
	if target == "" {
		return worker.Permanent(errors.New("clone carries no target bank"))
	}
	if _, err := p.store.GetBank(ctx, target); err == nil {
		return worker.Permanent(fmt.Errorf("target bank %q already exists", target))
	}
	scope := scopeFromPayload(op)
	if !scope.Any() {
		scope = transfer.DefaultExportScope()
	}
	archive, err := transfer.BuildArchive(ctx, p.store, op.BankID, transfer.ExportOptions{Scope: scope})
	if err != nil {
		return err
	}
	// The clone never stashes its archive, but the reference still measures the
	// copy it made and publishes its size, so the caller reads the same key off
	// a clone operation as off an export.
	archiveBytes, err := transferarchive.Write(archive)
	if err != nil {
		return err
	}
	report, err := p.importApplier().Apply(ctx, transfer.ImportRequest{
		Archive:      archive,
		BankID:       op.BankID,
		TargetBankID: target,
		Mode:         transfer.ImportRestore,
		Scope:        scope,
	})
	if err != nil {
		return err
	}
	// The clone is measured against its source, so the operation's counts name
	// the bank the copy landed in.
	report.TargetBankID = target
	metadata := report.Metadata()
	metadata["archive_byte_size"] = len(archiveBytes)
	worker.ReportOutcome(ctx, metadata)
	return nil
}

// importApplier builds the archive applier over the worker's store and the
// deployment's embedding model, so an imported fact is embedded with the target
// bank's model exactly as a retained one is.
func (p archiveProcessor) importApplier() transfer.ImportApplier {
	return transfer.ImportApplier{Store: p.store, Embeddings: p.embeddings}
}

// exportOptionsFrom translates a queued export operation into the archive
// builder's options.
func exportOptionsFrom(op storage.Operation) transfer.ExportOptions {
	scope := scopeFromPayload(op)
	if scope.Any() {
		// A whole-bank export records which sections were asked for.
		return transfer.ExportOptions{
			DocumentIDs:          payloadStrings(op, "document_ids"),
			IncludeObservations:  scope.Data,
			IncludeKnowledgeBase: scope.Data,
			Scope:                scope,
		}
	}
	// A document export records its own two flags and no bank-level section.
	return transfer.ExportOptions{
		DocumentIDs:          payloadStrings(op, "document_ids"),
		IncludeObservations:  payloadBool(op, "include_observations"),
		IncludeKnowledgeBase: payloadBool(op, "include_knowledge_base"),
	}
}

// scopeFromPayload reads the section selector a queued export or restore
// carries. An absent scope means the reference's default: data plus bank
// configuration.
func scopeFromPayload(op storage.Operation) transfer.ExportScope {
	raw, ok := op.Payload["scope"].(map[string]any)
	if !ok {
		if _, present := op.Payload["include_data"]; !present {
			if _, present := op.Payload["include_bank_config"]; !present {
				if _, present := op.Payload["include_history"]; !present {
					return transfer.ExportScope{}
				}
			}
		}
		return transfer.ExportScope{
			Data:       payloadBool(op, "include_data"),
			BankConfig: payloadBool(op, "include_bank_config"),
			History:    payloadBool(op, "include_history"),
		}
	}
	return transfer.ExportScope{
		Data:       mapBool(raw, "data"),
		BankConfig: mapBool(raw, "bank_config"),
		History:    mapBool(raw, "history"),
	}
}

// exportFilename is the name the caller's download carries.
func exportFilename(op storage.Operation) string {
	suffix := "documents"
	if op.OperationType == storage.OperationTypeTransferExport {
		suffix = "bank"
	}
	return op.BankID + "-" + suffix + ".zip"
}

// downloadURLFor renders the API-relative URL an archive is fetched from. It is
// the path the download route serves, so a client can fetch it with its own
// credentials and no absolute host.
//
// The key is escaped for a URL path. A stored key percent-encodes each of its
// own segments, so a bank identifier carrying a reserved character leaves the
// key holding an escape of its own, and the download route splits the path it
// receives rather than decoding it. Handing out the key unescaped therefore
// advertises a URL that resolves to a different key and answers not-found; a
// key with no escape in it is rendered unchanged.
func downloadURLFor(key string) string {
	return (&url.URL{Path: downloadPathPrefix + key}).EscapedPath()
}

// downloadPathPrefix is the route prefix the download URL is built from; it is
// the same prefix the download route serves under.
const downloadPathPrefix = "/v1/default/files/download/"

// payloadString reads a string field from an operation payload.
func payloadString(op storage.Operation, key string) string {
	value, _ := op.Payload[key].(string)
	return strings.TrimSpace(value)
}

// payloadBool reads a boolean field from an operation payload.
func payloadBool(op storage.Operation, key string) bool {
	value, ok := op.Payload[key]
	if !ok {
		return false
	}
	flag, _ := value.(bool)
	return flag
}

// payloadStrings reads a string list from an operation payload.
func payloadStrings(op storage.Operation, key string) []string {
	switch typed := op.Payload[key].(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

// mapBool reads a boolean from a decoded scope map.
func mapBool(values map[string]any, key string) bool {
	flag, _ := values[key].(bool)
	return flag
}
