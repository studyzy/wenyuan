package transfer

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/hindsightarchive"
	"github.com/studyzy/wenyuan/internal/httpx"
)

// validateExportSelection rejects a whole-bank transfer export that combines a
// document subset with a bank-level section, because the two describe different
// things: the bank sections describe the whole of it.
func validateExportSelection(r *http.Request) error {
	hasDocuments := len(httpx.QueryValues(r, "document_id")) > 0
	if !hasDocuments {
		return nil
	}
	for _, whole := range []string{"include_bank_config", "include_history"} {
		if isTruthy(httpx.Query(r, whole)) {
			return errors.New("include_bank_config and include_history are only supported for a whole-bank " +
				"export (omit document_id)")
		}
	}
	return nil
}

// validateDocumentExportSelection rejects a document export that combines a
// subset with a bank-level section. Observations are bank-level and can be
// derived from facts spanning several documents, so they are only coherent
// when the whole bank is exported.
func validateDocumentExportSelection(r *http.Request) error {
	if len(httpx.QueryValues(r, "document_id")) == 0 {
		return nil
	}
	if isTruthy(httpx.Query(r, "include_observations")) || isTruthy(httpx.Query(r, "include_knowledge_base")) {
		return errors.New("include_observations and include_knowledge_base are only supported when exporting " +
			"the whole bank (omit document_id)")
	}
	return nil
}

// validConflict reports whether a document-conflict mode is one the importer
// understands.
func validConflict(mode string) bool {
	switch mode {
	case ConflictSkip, ConflictReplace, ConflictNewID:
		return true
	default:
		return false
	}
}

// validateImportSelection enforces the coherence rules for a restore and
// returns the requested-and-carried scope intersection.
//
// Modes and flags follow the reference: mode defaults to restore and must be
// restore or merge; document_conflict defaults to skip and must be skip,
// replace or new-id; and the three include flags default to data + bank config
// on and history off, so a body that omits them is a valid whole-bank restore.
func (h *Handlers) validateImportSelection(r *http.Request, data []byte, bankID string) (map[string]any, error) {
	mode := strings.TrimSpace(httpx.Query(r, "mode"))
	if mode == "" {
		mode = "restore"
	}
	if mode != "restore" && mode != "merge" {
		return nil, wireMessage(fmt.Sprintf("Invalid mode '%s' (expected restore|merge)", mode))
	}
	conflict := strings.TrimSpace(httpx.Query(r, "document_conflict"))
	if conflict == "" {
		conflict = "skip"
	}
	if conflict != "skip" && conflict != "replace" && conflict != "new-id" {
		return nil, wireMessage(fmt.Sprintf("Invalid document_conflict '%s' (expected skip|replace|new-id)", conflict))
	}
	target := strings.TrimSpace(httpx.Query(r, "target_bank_id"))
	if mode == "merge" {
		// A merge takes the archive's documents only, so a target or any
		// include flag is incoherent with it.
		if target != "" {
			return nil, errors.New("target_bank_id is only valid in restore mode; merge imports into {bank_id}")
		}
		for _, flag := range []string{"include_data", "include_bank_config", "include_history"} {
			if _, present := httpx.QueryPresent(r, flag); present {
				return nil, errors.New("include_data / include_bank_config / include_history apply to mode=restore; a merge imports the archive's documents only")
			}
		}
		return map[string]any{"mode": mode, "document_conflict": conflict}, nil
	}

	// A restore writes a whole bank: the target must not already exist, whether
	// or not it is the bank the operation was recorded against, because
	// restoring onto a live bank would mix two configurations silently.
	if target == "" {
		target = archiveSourceBank(data)
	}
	if target == "" {
		return nil, errors.New("the archive names no source bank and no target was given")
	}
	effectiveBank := target
	if _, err := h.Store.GetBank(r.Context(), target); err == nil {
		return nil, errors.New("the target bank already exists")
	}

	// The include flags default to the reference's restore defaults. An absent
	// flag is the default, not "none selected"; only an explicit value turns a
	// section on or off.
	supplied := scopeFromQuery(r)
	effective := scope{Data: true, BankConfig: true, History: false}
	if supplied.Present["include_data"] {
		effective.Data = supplied.Data
	}
	if supplied.Present["include_bank_config"] {
		effective.BankConfig = supplied.BankConfig
	}
	if supplied.Present["include_history"] {
		effective.History = supplied.History
	}

	archive, err := validateArchive(data)
	if err != nil {
		return nil, err
	}
	intersection := intersectScope(effective, archive)
	out := map[string]any{"mode": mode, "document_conflict": conflict, "scope": intersection}
	out["target_bank_id"] = target
	out["effective_bank_id"] = effectiveBank
	return out, nil
}

// archiveSourceBank reads the source bank id from an archive payload without
// interpreting its documents, so a restore can name the bank it will create.
func archiveSourceBank(data []byte) string {
	archive, err := validateArchive(data)
	if err != nil {
		return ""
	}
	return archive.Manifest.SourceBankID
}

// scopeFromQuery reads the tri-state section selector.
func scopeFromQuery(r *http.Request) scope {
	s := scope{Present: map[string]bool{}}
	read := func(name string) bool {
		raw, ok := httpx.QueryPresent(r, name)
		if !ok || len(raw) == 0 {
			return false
		}
		s.Present[name] = true
		return isTruthy(raw[0])
	}
	s.Data = read("include_data")
	s.BankConfig = read("include_bank_config")
	s.History = read("include_history")
	s.AnySelected = s.Data || s.BankConfig || s.History
	return s
}

// scopePayload renders the scope selector for an operation payload.
func scopePayload(r *http.Request) map[string]any {
	s := scopeFromQuery(r)
	payload := map[string]any{}
	if s.Present["include_data"] {
		payload["include_data"] = s.Data
	}
	if s.Present["include_bank_config"] {
		payload["include_bank_config"] = s.BankConfig
	}
	if s.Present["include_history"] {
		payload["include_history"] = s.History
	}
	if len(payload) == 0 {
		payload["include_data"] = true
	}
	return payload
}

// intersectScope narrows the requested scope to what the archive actually
// carries, so a restore never claims more than it can deliver.
func intersectScope(requested scope, archive *hindsightarchive.Archive) map[string]any {
	carriesHistory := archive.Manifest.IncludesHistory
	hasDocuments := len(archive.Documents) > 0
	out := map[string]any{
		"data":        requested.Data && hasDocuments,
		"bank_config": requested.BankConfig,
		"history":     requested.History && carriesHistory,
	}
	return out
}

// isTruthy reports whether a parameter value reads as an enabled flag. An
// unparseable value is treated as disabled rather than as an error, matching
// the destination's configuration convention.
func isTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// itoa renders a length for the Content-Length header.
func itoa(n int) string { return strconv.Itoa(n) }

// downloadFilename builds the bank-derived download filename.
func downloadFilename(bankID string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, bankID)
	if safe == "" {
		safe = "bank"
	}
	return safe + ".zip"
}

// decodeJSONBody decodes a JSON request body with a size bound.
func decodeJSONBody(r *http.Request, limit int64, target any) error {
	data, err := readAllBounded(r.Body, limit)
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return errors.New("request body is required")
	}
	return json.Unmarshal(data, target)
}
