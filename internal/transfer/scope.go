package transfer

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/hindsightarchive"
)

// validateExportSelection rejects a request that combines a whole-bank-only
// section with a document subset, because the two describe different things.
func validateExportSelection(r *http.Request) error {
	query := r.URL.Query()
	hasDocuments := len(query["document_id"]) > 0
	if !hasDocuments {
		return nil
	}
	for _, whole := range []string{"include_data", "include_bank_config", "include_history"} {
		if isTruthy(query.Get(whole)) {
			return errors.New("a document subset cannot be combined with a whole-bank section")
		}
	}
	return nil
}

// validateImportSelection enforces the coherence rules for a restore and
// returns the requested-and-carried scope intersection.
func (h *Handlers) validateImportSelection(r *http.Request, data []byte, bankID string) (map[string]any, error) {
	query := r.URL.Query()
	mode := strings.TrimSpace(query.Get("mode"))
	if mode == "" {
		mode = "replace"
	}
	target := strings.TrimSpace(query.Get("target_bank_id"))
	if mode == "merge" {
		// A merge takes the archive's documents only, so a target or any
		// include flag is incoherent with it.
		if target != "" {
			return nil, errors.New("merge mode does not accept a target bank")
		}
		for _, flag := range []string{"include_data", "include_bank_config", "include_history"} {
			if query.Has(flag) {
				return nil, errors.New("merge mode does not accept an include flag")
			}
		}
		return map[string]any{"mode": mode}, nil
	}

	effectiveBank := bankID
	if target != "" {
		effectiveBank = target
		if target != bankID {
			if _, err := h.Store.GetBank(r.Context(), target); err == nil {
				return nil, errors.New("the target bank already exists")
			}
		}
	}

	selected := scopeFromQuery(query)
	if !selected.AnySelected {
		return nil, errors.New("at least one of include_data, include_bank_config or include_history must be true")
	}

	archive, err := validateArchive(data)
	if err != nil {
		return nil, err
	}
	intersection := intersectScope(selected, archive)
	out := map[string]any{"mode": mode, "scope": intersection}
	if target != "" {
		out["target_bank_id"] = target
	}
	out["effective_bank_id"] = effectiveBank
	return out, nil
}

// scopeFromQuery reads the tri-state section selector.
func scopeFromQuery(query map[string][]string) scope {
	s := scope{Present: map[string]bool{}}
	read := func(name string) bool {
		raw, ok := query[name]
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
	s := scopeFromQuery(r.URL.Query())
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
