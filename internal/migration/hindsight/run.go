package hindsight

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/studyzy/wenyuan/internal/hindsightarchive"
	"github.com/studyzy/wenyuan/internal/storage"
)

// Mode selects how a migration treats a target bank that already has rows.
type Mode string

const (
	// Replace deletes the target bank's rows first, so a re-run is idempotent.
	Replace Mode = "replace"
	// Append keeps existing rows and adds the archive's rows alongside them.
	Append Mode = "append"
	// Incremental keeps existing rows and writes only the documents whose
	// identity the target does not already hold, so a migration can be re-run
	// after the source instance took more writes without duplicating what was
	// already moved and without losing what is new.
	Incremental Mode = "incremental"
)

// Options controls a migration run.
type Options struct {
	Mode Mode
	// DryRun parses and maps the archive and reports counts without writing.
	DryRun bool
	// DiscardDocumentText drops the replayed chunks' text, keeping only their
	// content hashes. The reference's import reads its process-level default for
	// this setting, so the caller resolves the deployment value; the zero value
	// keeps the text, so an unwired caller loses nothing.
	DiscardDocumentText bool
	// Now is injectable for deterministic tests.
	Now func() time.Time
}

// Run migrates the archive into the store and returns a report.
func Run(ctx context.Context, store storage.Store, mapping Mapping, opts Options) (Report, error) {
	if mapping.Archive == nil {
		return Report{}, errors.New("migration requires a parsed archive")
	}
	bankID := mapping.targetBankID()
	if bankID == "" {
		return Report{}, errors.New("migration requires a bank id (archive source or explicit target)")
	}
	mode := opts.Mode
	if mode == "" {
		mode = Replace
	}

	report := Report{BankID: bankID, Unmapped: unmappedFields()}
	for _, doc := range mapping.Archive.Documents {
		report.Documents++
		report.Chunks += len(doc.Chunks)
		report.Facts += len(doc.Facts)
		report.Entities += countEntities(doc)
		report.Links += countLinks(doc)
	}
	if opts.DryRun {
		return report, nil
	}

	if mode == Replace {
		if _, err := store.DeleteBank(ctx, bankID); err != nil {
			return Report{}, fmt.Errorf("clear target bank %s: %w", bankID, err)
		}
	}
	if err := ensureBank(ctx, store, bankID, opts); err != nil {
		return Report{}, err
	}
	for _, doc := range mapping.Archive.Documents {
		if mode == Incremental && documentPresent(ctx, store, bankID, doc.ID) {
			// The target already holds this document, so this run leaves it
			// alone. That is what makes a re-run after further source writes
			// both non-duplicating and non-destructive.
			report.Skipped++
			continue
		}
		if err := writeDocument(ctx, store, bankID, doc, !opts.DiscardDocumentText); err != nil {
			return Report{}, err
		}
		report.Written++
	}
	return report, nil
}

// documentPresent reports whether the target bank already holds a document. An
// error is treated as absent so the document is written rather than skipped,
// because losing data is worse than writing it twice.
func documentPresent(ctx context.Context, store storage.Store, bankID, documentID string) bool {
	if documentID == "" {
		return false
	}
	_, err := store.GetDocument(ctx, bankID, documentID)
	return err == nil
}

func ensureBank(ctx context.Context, store storage.Store, bankID string, opts Options) error {
	if _, err := store.GetBank(ctx, bankID); err == nil {
		return nil
	}
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now()
	}
	bank := storage.Bank{
		BankID: bankID,
		Name:   bankID,
		Disposition: map[string]int{
			storage.DispositionKeySkepticism: 3,
			storage.DispositionKeyLiteralism: 3,
			storage.DispositionKeyEmpathy:    3,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := store.CreateBank(ctx, bank); err != nil {
		return fmt.Errorf("create target bank %s: %w", bankID, err)
	}
	return nil
}

func writeDocument(ctx context.Context, store storage.Store, bankID string, doc hindsightarchive.Document, storeText bool) error {
	created := time.Now().UTC()
	if parsed := hindsightarchive.ParseTime(doc.CreatedAt); parsed != nil {
		created = *parsed
	}
	document := storage.Document{
		ID:           doc.ID,
		BankID:       bankID,
		OriginalText: doc.OriginalText,
		RetainParams: doc.RetainParams,
		CreatedAt:    created,
	}
	if _, err := store.PutDocument(ctx, bankID, doc.ID, document); err != nil {
		return fmt.Errorf("write document %s: %w", doc.ID, err)
	}

	// The document's chunks are replayed before its facts, because a fact names
	// the chunk it came from: the archive carries that text and the target
	// rebuilds the rows rather than importing them.
	chunks := make([]storage.DocumentChunk, 0, len(doc.Chunks))
	for _, chunk := range doc.Chunks {
		chunks = append(chunks, storage.DocumentChunk{ChunkIndex: chunk.ChunkIndex, ChunkText: chunk.ChunkText})
	}
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].ChunkIndex < chunks[j].ChunkIndex })
	chunkIDs, err := store.PutDocumentChunks(ctx, bankID, doc.ID, chunks, storeText)
	if err != nil {
		return fmt.Errorf("write chunks of %s: %w", doc.ID, err)
	}

	for index, fact := range doc.Facts {
		unit := unitFromFact(bankID, doc.ID, index, fact)
		// A fact that names a chunk is addressed by the identifier the row was
		// just written under; a fact naming an index the archive carried no
		// chunk for keeps no reference.
		if fact.ChunkIndex != nil {
			unit.ChunkID = chunkIDs[*fact.ChunkIndex]
		}
		stored, err := store.UpsertUnit(ctx, unit)
		if err != nil {
			return fmt.Errorf("write fact %d of %s: %w", index, doc.ID, err)
		}
		// The entities this fact mentions, de-duplicated by identity, so the
		// co-occurrence cross-product counts each pair once for this unit.
		mentioned := make([]string, 0, len(fact.Entities))
		seen := map[string]bool{}
		for _, name := range fact.Entities {
			entity, err := store.ResolveEntity(ctx, bankID, name, storage.EntityKindRegular, true)
			if err != nil {
				return fmt.Errorf("resolve entity %q: %w", name, err)
			}
			if entity.ID == "" {
				continue
			}
			if err := store.UpsertUnitEntity(ctx, unit.ID, entity.ID); err != nil {
				return fmt.Errorf("link unit %s to entity %s: %w", unit.ID, entity.ID, err)
			}
			if !seen[entity.ID] {
				seen[entity.ID] = true
				mentioned = append(mentioned, entity.ID)
			}
		}
		// Entity co-occurrences are derived data the archive does not carry, so
		// the migration rebuilds them here, exactly as the retain path and the
		// served import do: without this the entity graph of a migrated bank has
		// no edges.
		if err := store.UpsertUnitCooccurrences(ctx, bankID, mentioned, storage.UnitSeenAt(stored)); err != nil {
			return fmt.Errorf("write co-occurrences of %s: %w", unit.ID, err)
		}
		for _, relation := range fact.CausalRelations {
			if relation.TargetFactIndex < 0 || relation.TargetFactIndex >= len(doc.Facts) {
				continue
			}
			link := storage.MemoryLink{
				BankID:     bankID,
				FromUnitID: unit.ID,
				ToUnitID:   factUnitID(bankID, doc.ID, relation.TargetFactIndex),
				LinkType:   storage.MemoryLinkType(relation.RelationType),
				Weight:     1,
			}
			if err := store.UpsertLink(ctx, link); err != nil {
				return fmt.Errorf("write causal link from %s: %w", unit.ID, err)
			}
		}
	}
	return nil
}

func countEntities(doc hindsightarchive.Document) int {
	seen := map[string]bool{}
	for _, fact := range doc.Facts {
		for _, name := range fact.Entities {
			seen[name] = true
		}
	}
	return len(seen)
}

func countLinks(doc hindsightarchive.Document) int {
	total := 0
	for _, fact := range doc.Facts {
		total += len(fact.CausalRelations)
	}
	return total
}
