package transfer

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/banktemplate"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/transferarchive"
)

// validateExportSelection rejects a whole-bank transfer export that combines a
// document subset with a bank-level section, because the two describe different
// things: the bank sections describe the whole of it. The check reads the
// resolved selector, so the default of include_bank_config (on) applies exactly
// as it does on the reference.
func validateExportSelection(documents []string, bankConfig, history bool) error {
	if len(documents) == 0 {
		return nil
	}
	if bankConfig || history {
		return errors.New("include_bank_config and include_history are only supported for a whole-bank " +
			"export (omit document_id)")
	}
	return nil
}

// validateDocumentExportSelection rejects a document export that combines a
// subset with a bank-level section. Observations are bank-level and can be
// derived from facts spanning several documents, so they are only coherent
// when the whole bank is exported.
func validateDocumentExportSelection(documents []string, includeObservations, includeKnowledgeBase bool) error {
	if len(documents) == 0 {
		return nil
	}
	if includeObservations || includeKnowledgeBase {
		return errors.New("include_observations and include_knowledge_base are only supported when exporting " +
			"the whole bank (omit document_id)")
	}
	return nil
}

// queryEnum reads a query parameter constrained to a fixed vocabulary. An
// absent parameter yields the caller's default; a present one is taken
// verbatim — including an empty or whitespace-padded value, which the
// reference rejects — and a repeated parameter takes its last occurrence, as
// the reference's request parsing does.
func queryEnum(r *http.Request, name, fallback string, allowed ...string) (string, bool) {
	values, present := httpx.QueryPresent(r, name)
	if !present || len(values) == 0 {
		return fallback, true
	}
	value := lastQueryValue(values)
	for _, candidate := range allowed {
		if value == candidate {
			return value, true
		}
	}
	return value, false
}

// lastQueryValue returns the value a repeated query parameter resolves to. The
// reference's parser takes the last occurrence, so the same is done here.
func lastQueryValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}

// queryValue reads a query parameter, taking its last occurrence when it
// repeats, and records it as read for the route's parameter audit.
func queryValue(r *http.Request, name string) string {
	values, _ := httpx.QueryPresent(r, name)
	return lastQueryValue(values)
}

// boolSpellings are the strings the reference's request validation maps to a
// boolean: the value is lower-cased and matched without trimming, so a padded
// or otherwise unrecognised token is a validation failure rather than false.
var boolSpellings = map[string]bool{
	"1": true, "true": true, "on": true, "yes": true, "t": true, "y": true,
	"0": false, "false": false, "off": false, "no": false, "f": false, "n": false,
}

// queryBool reads a query parameter as a strict boolean. present reports
// whether the parameter was supplied at all, so an absent one leaves the
// caller's default in place; a present value outside the accepted spellings is
// a validation failure, matching the reference's request validation.
func queryBool(r *http.Request, name string) (value, present bool, failure *httpx.ValidationFailure) {
	raw, present := httpx.QueryPresent(r, name)
	if !present || len(raw) == 0 {
		return false, false, nil
	}
	text := lastQueryValue(raw)
	parsed, ok := boolSpellings[strings.ToLower(text)]
	if !ok {
		return false, true, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.TypeMismatch("bool_parsing", []string{"query", name}, text),
		}}
	}
	return parsed, true, nil
}

// resolveImportSelection enforces the coherence rules for a transfer import and
// returns the bank a restore writes into (empty in merge mode), plus the parsed
// archive so the import gate reads what the selection already validated.
//
// Modes and flags follow the reference: mode defaults to restore and must be
// restore or merge; document_conflict defaults to skip and must be skip,
// replace or new-id; and the three include flags default to data + bank config
// on and history off, so a body that omits them is a valid whole-bank restore.
//
// The archive is parsed in both modes — a malformed one is a caller error, not
// a background failure — and a restore additionally refuses a target that
// already exists, whether or not it is the bank the operation was recorded
// against, because restoring onto a live bank would mix two configurations
// silently.
func (h *Handlers) resolveImportSelection(r *http.Request, data []byte, mode string) (string, *transferarchive.Archive, error) {
	target := queryValue(r, "target_bank_id")
	if mode == "merge" {
		// A merge takes the archive's documents only, so a target or any
		// include flag is incoherent with it.
		if target != "" {
			return "", nil, errors.New("target_bank_id is only valid in restore mode; merge imports into {bank_id}")
		}
		for _, flag := range []string{"include_data", "include_bank_config", "include_history"} {
			if _, present := httpx.QueryPresent(r, flag); present {
				return "", nil, errors.New("include_data / include_bank_config / include_history apply to mode=restore; a merge imports the archive's documents only")
			}
		}
		// A merge feeds the document importer, so its archive is the documents
		// archive the document export produces.
		archive, err := validateArchive(data, producedByDocumentExport)
		if err != nil {
			return "", nil, err
		}
		return "", archive, nil
	}

	// A restore writes a whole bank, so its archive is the whole-bank archive.
	archive, err := validateArchive(data, producedByBankExport)
	if err != nil {
		return "", nil, err
	}
	if target == "" {
		target = archive.Manifest.SourceBankID
	}
	if target == "" {
		return "", nil, errors.New("the archive names no source bank and no target was given")
	}
	if _, err := h.Store.GetBank(r.Context(), target); err == nil {
		return "", nil, wireMessage(fmt.Sprintf("Target bank '%s' already exists; a bank restore writes into a fresh bank (it is not a merge). Delete it first, or choose a different target bank id.", target))
	}
	// The restore re-checks the identifier on the resolved target — including one
	// derived from the archive's manifest — so the caller gets the 400 now, not a
	// failed background operation (engine/memory_engine.py:8240).
	if err := validateNewBankID(target); err != nil {
		return "", nil, err
	}
	return target, archive, nil
}

// resolved fills the reference's defaults into the tri-state selector: data and
// bank config default on, history off.
func (s scope) resolved() (data, bankConfig, history bool) {
	data, bankConfig, history = true, true, false
	if s.Present["include_data"] {
		data = s.Data
	}
	if s.Present["include_bank_config"] {
		bankConfig = s.BankConfig
	}
	if s.Present["include_history"] {
		history = s.History
	}
	return data, bankConfig, history
}

// scopeFromQuery reads the tri-state section selector, validating each present
// flag as a strict boolean so an unparseable token is a request-validation
// failure rather than a silent false.
func scopeFromQuery(r *http.Request) (scope, *httpx.ValidationFailure) {
	s := scope{Present: map[string]bool{}}
	// Every present flag is validated, and EVERY failure is reported together in
	// declaration order. The reference's framework binds the whole signature
	// before the handler runs, so `?include_data=x&include_history=y` answers with
	// two items, not one; stopping at the first would under-report. The
	// declaration order here is the reference's parameter order
	// (`include_data`, then `include_bank_config`, then `include_history`).
	merged := &httpx.ValidationFailure{}
	failed := false
	read := func(name string) bool {
		value, present, failure := queryBool(r, name)
		if failure != nil {
			failed = true
			merged.Errors = append(merged.Errors, failure.Errors...)
			return false
		}
		if present {
			s.Present[name] = true
		}
		return value
	}
	s.Data = read("include_data")
	s.BankConfig = read("include_bank_config")
	s.History = read("include_history")
	if failed {
		return scope{}, merged
	}
	return s, nil
}

// scopePayload renders the scope selector for an operation payload, resolving
// the tri-state selector to concrete booleans the way the reference records
// them: an absent flag carries its default rather than staying unset.
func scopePayload(s scope) map[string]any {
	data, bankConfig, history := s.resolved()
	return map[string]any{
		"include_data":        data,
		"include_bank_config": bankConfig,
		"include_history":     history,
	}
}

// itoa renders a length for the Content-Length header.
func itoa(n int) string { return strconv.Itoa(n) }

// downloadFilename is the name the caller's download carries. It is derived
// from the owning bank, exactly as the reference names it.
func downloadFilename(bankID string) string {
	return bankID + "-documents.zip"
}

// decodeJSONBody decodes a JSON request body with a size bound.
//
// The body is decoded through the shared decoder's schema walk, so the values
// the reference's model refuses are reported with the framework's own items and
// the values its lax mode accepts (a numeric string for an integer field, a
// boolean word for a boolean) are accepted rather than refused. This route is
// the only caller, and the one whose body is a template manifest, so the walk's
// items are rendered the way that route's handler renders them — as the single
// "Template schema validation failed: …" detail string — and merged with the
// manifest model's own checks, which the walk does not carry (its bounds and
// the custom validators over ids and match keys).
func decodeJSONBody(r *http.Request, limit int64, target any) error {
	data, err := readAllBounded(r.Body, limit)
	if err != nil {
		return err
	}
	if !json.Valid(data) {
		// A body that is not JSON is reported the way the reference's
		// `await request.json()` reports it: CPython's message, not Go's. An
		// empty body is one of these -- `json.loads("")` raises the same
		// "Expecting value" at char 0, so it needs no message of its own.
		return pythonJSONError(data)
	}
	items := httpx.ValidationItemsAgainstRequest(r, target, data)
	manifest, isManifest := target.(*banktemplate.Manifest)
	if !isManifest {
		if _, err := httpx.DecodeLax(data, target); err != nil && len(items) == 0 {
			// The walk could not describe this body but the decoder still
			// refused it; its item is rendered the same way.
			items = httpx.BodyDecodeFailure(err, data).Errors
		}
		if len(items) == 0 {
			return nil
		}
		return templateStructuralFailure(r, target, items, nil)
	}
	// The manifest model's own checks run on the members the walk accepted: a
	// member the walk rejected is dropped rather than left at its zero value,
	// which a later check would misread (an id that failed to decode would
	// otherwise look empty and be reported as one).
	cleaned := httpx.StripRejectedValues(data, itemLocations(items))
	if _, err := httpx.DecodeLax(cleaned, manifest); err != nil && len(items) == 0 {
		items = httpx.BodyDecodeFailure(err, cleaned).Errors
	}
	structural, semantic := banktemplate.Validate(*manifest)
	if len(items) == 0 && len(structural) == 0 {
		if len(semantic) == 0 {
			return nil
		}
		return wireMessage("Template validation failed: " + strings.Join(semantic, "; "))
	}
	return templateStructuralFailure(r, target, items, structural)
}

// itemLocations collects the locations the walk rejected, which are the values
// the manifest model's own checks must not see.
func itemLocations(items []httpx.ValidationError) [][]string {
	locs := make([][]string, 0, len(items))
	for _, item := range items {
		locs = append(locs, item.Loc)
	}
	return locs
}

// templateStructuralFailure renders the framework's items, merged with the
// manifest model's own structural failures, the way this route's handler
// serves them: one line per item, "<loc>: <msg>", joined with "; " under the
// schema-validation prefix.
//
// Both sets are ordered by the manifest's field-declaration order, which is the
// order the reference reports them in. A structural line the walk already
// reported at the same location is dropped, because the walk's item is the one
// the API serves for that field.
func templateStructuralFailure(r *http.Request, target any, walk []httpx.ValidationError, structural []string) error {
	items := append([]httpx.ValidationError(nil), walk...)
	for _, line := range structural {
		loc, msg := splitTemplateLine(line)
		if loc == nil {
			items = append(items, httpx.ValidationError{Loc: []string{"body"}, Msg: line})
			continue
		}
		full := append([]string{"body"}, loc...)
		if templateLocationCovered(full, walk) {
			continue
		}
		items = append(items, httpx.ValidationError{Loc: full, Msg: msg})
	}
	httpx.OrderItemsByDeclaration(r, target, items)
	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, templateItemLine(r, target, item))
	}
	return wireMessage("Template schema validation failed: " + strings.Join(lines, "; "))
}

// templateItemLine renders one item as the "<loc>: <msg>" line this route
// serves, with the implicit body segment dropped from the location.
func templateItemLine(r *http.Request, target any, item httpx.ValidationError) string {
	msg := item.Msg
	if item.Type == "model_attributes_type" || (item.Type == "missing" && len(item.Loc) == 1) {
		// A value that is not the object the model extracts its members from —
		// the payload itself, or a nested member — is named the way this route
		// serves it, with the class the declaration wanted.
		if named := httpx.ModelShapeMessage(r, target, item.Loc); named != "" {
			msg = named
		}
	}
	return strings.Join(item.Loc[1:], ".") + ": " + msg
}

// splitTemplateLine separates a manifest check's rendered line back into the
// field path it names and the message, so the line can be ordered alongside the
// walk's items. A line that names no path yields no location.
func splitTemplateLine(line string) ([]string, string) {
	at := strings.Index(line, ": ")
	if at <= 0 {
		return nil, ""
	}
	return strings.Split(line[:at], "."), line[at+2:]
}

// templateLocationCovered reports whether the walk already reported the field
// at loc, either exactly there or on a path it encloses.
func templateLocationCovered(loc []string, walk []httpx.ValidationError) bool {
	for _, item := range walk {
		if len(item.Loc) <= len(loc) && slices.Equal(item.Loc, loc[:len(item.Loc)]) {
			return true
		}
	}
	return false
}
