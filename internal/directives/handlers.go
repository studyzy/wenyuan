// Package directives serves the directive route family: standing instructions
// the reflection loop follows, ordered by priority.
package directives

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// maxBodyBytes bounds a request body on this route family.
const maxBodyBytes = 1 << 20

// list page bounds, matching the reference's query constraints.
const (
	defaultListLimit = 100
	maxListLimit     = 1000
)

// directiveTagsMatch is the set of tag-match modes the list query accepts.
var directiveTagsMatch = []string{"any", "all", "exact"}

// Directive is the wire shape. Field names are public surface.
type Directive struct {
	ID        string   `json:"id"`
	BankID    string   `json:"bank_id"`
	Name      string   `json:"name"`
	Content   string   `json:"content"`
	Priority  int      `json:"priority"`
	IsActive  bool     `json:"is_active"`
	Tags      []string `json:"tags"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

// CreateRequest is the create body. name and content are required; the pointer
// fields distinguish an absent key from an empty string, which the reference
// accepts.
type CreateRequest struct {
	Name     *string  `json:"name"`
	Content  *string  `json:"content"`
	Priority int      `json:"priority"`
	IsActive *bool    `json:"is_active"`
	Tags     []string `json:"tags"`
}

// UpdateRequest is the partial-update body.
type UpdateRequest struct {
	Name     *string   `json:"name"`
	Content  *string   `json:"content"`
	Priority *int      `json:"priority"`
	IsActive *bool     `json:"is_active"`
	Tags     *[]string `json:"tags"`
}

// ListResponse is the collection envelope.
type ListResponse struct {
	Items  []Directive `json:"items"`
	Total  int         `json:"total"`
	Limit  int         `json:"limit"`
	Offset int         `json:"offset"`
}

// DeleteResponse is the delete acknowledgement.
type DeleteResponse struct {
	Status string `json:"status"`
}

// Handlers serves the directive route family.
type Handlers struct{ Store storage.Store }

// NewHandlers builds the handlers over the store.
func NewHandlers(store storage.Store) *Handlers { return &Handlers{Store: store} }

// List returns the bank's directives, highest priority first, filtered by the
// optional tags and active_only selectors and paged by limit/offset.
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	// The query selectors are validated before the bank is looked up: in the
	// reference they are `Query(...)` parameters and a `Depends`, which the
	// framework resolves ahead of the handler body, so a bad selector is the 422
	// even when the bank does not exist.
	// The reference's framework resolves every query parameter in one pass, so a
	// request with several bad selectors reports all of them in a single 422 — in
	// the order the endpoint declares them: tags_match, active_only, limit,
	// offset. Reporting only the first would tell the caller half the story.
	selectors, items, unrepresentable := parseListSelectors(r)
	if unrepresentable != nil {
		httpx.WriteValidationFailure(w, unrepresentable)
		return
	}
	if len(items) > 0 {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: items})
		return
	}
	activeOnly, mode, limit, offset := selectors.activeOnly, selectors.mode, selectors.limit, selectors.offset
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}

	records, err := h.Store.ListDirectives(r.Context(), bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	tags := httpx.QueryValues(r, "tags")
	filtered := make([]storage.Directive, 0, len(records))
	for _, record := range records {
		if activeOnly && !record.IsActive {
			continue
		}
		if len(tags) > 0 && !directiveMatchesTags(record.Tags, tags, mode) {
			continue
		}
		filtered = append(filtered, record)
	}
	// Highest priority first, breaking ties newest-first then by id, so paging
	// stays stable across pages. The order is decided on the stored records:
	// the rendered DTO carries the timestamps as strings, which do not compare.
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Priority != filtered[j].Priority {
			return filtered[i].Priority > filtered[j].Priority
		}
		if !filtered[i].CreatedAt.Equal(filtered[j].CreatedAt) {
			return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
		}
		return filtered[i].ID > filtered[j].ID
	})
	matched := make([]Directive, 0, len(filtered))
	for _, record := range filtered {
		matched = append(matched, toDirective(record))
	}
	total := len(matched)
	matched = pageSlice(matched, offset, limit)
	httpx.WriteJSON(w, http.StatusOK, ListResponse{
		Items:  matched,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}

// directiveMatchesTags reports whether a directive is in scope for the
// requested tags. An untagged (global) directive always applies; a tagged one
// must match the requested scope under the given mode.
func directiveMatchesTags(recordTags, want []string, mode string) bool {
	if len(recordTags) == 0 {
		return true
	}
	have := make(map[string]struct{}, len(recordTags))
	for _, tag := range recordTags {
		have[tag] = struct{}{}
	}
	switch mode {
	case "all":
		for _, tag := range want {
			if _, ok := have[tag]; !ok {
				return false
			}
		}
		return true
	case "exact":
		if len(recordTags) != len(want) {
			return false
		}
		for _, tag := range want {
			if _, ok := have[tag]; !ok {
				return false
			}
		}
		return true
	default: // any
		for _, tag := range want {
			if _, ok := have[tag]; ok {
				return true
			}
		}
		return false
	}
}

// Get returns one directive.
func (h *Handlers) Get(w http.ResponseWriter, r *http.Request) {
	bankID, id, ok := h.uuidMember(w, r)
	if !ok {
		return
	}
	record, err := h.Store.GetDirective(r.Context(), bankID, id)
	if err != nil {
		httpx.WriteErrorFor(w, r, directiveError(id, err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toDirective(record))
}

// Create adds a directive.
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	// The reference does not check that the bank exists on this route: it issues
	// the INSERT and lets the foreign key on `directives.bank_id` reject an
	// unknown bank, so the caller sees a 500 rather than a 404. The body is
	// validated first (a malformed body is a 422 even for an unknown bank), and
	// the bank parameter is canonicalised through the alias table so a
	// registered alias inserts against the bank it points at.
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return
	}
	if canonical, err := h.Store.ResolveBankID(r.Context(), bankID); err == nil {
		bankID = canonical
	}
	var body CreateRequest
	object, failure := decodeBody(r, &body)
	if failure != nil {
		httpx.WriteValidationFailure(w, failure)
		return
	}
	// name and content are required strings with no length floor: an empty
	// value is accepted, only an absent key (or an explicit null) is rejected.
	// Every missing required field is reported in one envelope, in the model's
	// declaration order, as the reference's validator does.
	var missing []httpx.ValidationError
	if body.Name == nil {
		missing = append(missing, httpx.Missing([]string{"body", "name"}, object))
	}
	if body.Content == nil {
		missing = append(missing, httpx.Missing([]string{"body", "content"}, object))
	}
	if len(missing) > 0 {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: missing})
		return
	}
	// A directive is active unless the caller explicitly deactivates it.
	active := true
	if body.IsActive != nil {
		active = *body.IsActive
	}
	if failure := priorityRangeFailure(createDirectivePriorityArg, body.Priority); failure != nil {
		httpx.WriteValidationFailure(w, failure)
		return
	}
	record, err := h.Store.PutDirective(r.Context(), storage.Directive{
		ID:       newID(),
		BankID:   bankID,
		Name:     *body.Name,
		Content:  *body.Content,
		Priority: body.Priority,
		IsActive: active,
		Tags:     body.Tags,
	})
	if err != nil {
		// An unknown bank surfaces here as the foreign-key rejection, which the
		// reference reports in the driver's own wording as a 500.
		if detail, rejected := foreignKeyReport(err, bankID); rejected {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindInternal, detail))
			return
		}
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toDirective(record))
}

// Update applies a partial update.
func (h *Handlers) Update(w http.ResponseWriter, r *http.Request) {
	bankID, id, ok := h.member(w, r)
	if !ok {
		return
	}
	var body UpdateRequest
	if _, failure := decodeBody(r, &body); failure != nil {
		httpx.WriteValidationFailure(w, failure)
		return
	}
	// The id's argument position follows the members the body supplied, so it
	// can only be worked out once the body has been read — and a malformed body
	// is the 422 above, before the id is ever bound.
	if detail, bad := directiveUUIDReport(id, updateDirectiveArgNumber([5]bool{
		body.Name != nil,
		body.Content != nil,
		body.Priority != nil,
		body.IsActive != nil,
		body.Tags != nil,
	})); bad {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindInternal, detail))
		return
	}
	if body.Priority != nil {
		arg := updateDirectivePriorityArg(body.Name != nil, body.Content != nil)
		if failure := priorityRangeFailure(arg, *body.Priority); failure != nil {
			httpx.WriteValidationFailure(w, failure)
			return
		}
	}
	record, err := h.Store.UpdateDirective(r.Context(), bankID, id, storage.DirectivePatch{
		Name:     body.Name,
		Content:  body.Content,
		Priority: body.Priority,
		IsActive: body.IsActive,
		Tags:     body.Tags,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, directiveError(id, err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toDirective(record))
}

// Delete removes a directive.
func (h *Handlers) Delete(w http.ResponseWriter, r *http.Request) {
	bankID, id, ok := h.uuidMember(w, r)
	if !ok {
		return
	}
	if err := h.Store.DeleteDirective(r.Context(), bankID, id); err != nil {
		httpx.WriteErrorFor(w, r, directiveError(id, err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, DeleteResponse{Status: "deleted"})
}

// BindRoutes registers the family's route table.
func (h *Handlers) BindRoutes(handle func(method, pattern string, handler http.HandlerFunc)) {
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixDirectives), httpx.QueryParams(h.List,
		"tags", "tags_match", "active_only", "limit", "offset"))
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixDirectives), h.Create)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixDirectiveMember), h.Get)
	handle(http.MethodPatch, httpx.BankPath(httpx.SuffixDirectiveMember), h.Update)
	handle(http.MethodDelete, httpx.BankPath(httpx.SuffixDirectiveMember), h.Delete)
}

// member reads the bank and the directive identifier from the path. The member
// routes never probe bank existence: like the reference, an unknown bank yields
// the same not-found as a missing directive.
func (h *Handlers) member(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return "", "", false
	}
	id := strings.TrimSpace(r.PathValue("directive_id"))
	if id == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "Directive '' not found"))
		return "", "", false
	}
	return bankID, id, true
}

// directiveError names the requested directive in a not-found detail, matching
// the reference's `Directive '<id>' not found`.
func directiveError(id string, err error) *httpx.HTTPError {
	if errors.Is(err, storage.ErrDirectiveNotFound) || errors.Is(err, storage.ErrBankNotFound) {
		return httpx.NewHTTPError(httpx.KindNotFound, fmt.Sprintf("Directive '%s' not found", id))
	}
	return asHTTPError(err)
}

// requireBank reports not-found for an unknown bank.
func (h *Handlers) requireBank(w http.ResponseWriter, r *http.Request) (string, bool) {
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return "", false
	}
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return "", false
	}
	return bankID, true
}

// pageSlice applies offset and limit to a matched list, tolerating an offset
// past the end.
func pageSlice[T any](items []T, offset, limit int) []T {
	if offset >= len(items) {
		return []T{}
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}

// queryItem builds a one-item validation failure for a query parameter.
func queryItem(item httpx.ValidationError) *httpx.ValidationFailure {
	return &httpx.ValidationFailure{Errors: []httpx.ValidationError{item}}
}

// decodeBody decodes a JSON body with a size bound, reporting a rejected body
// as the framework's structured items. The shared schema walk names every value
// the reference's model refuses — a missing required field, a `null` on a field
// that does not admit one, a wrong-typed value, and every bad member of a list —
// in the model's declaration order, so anything it reports is the whole answer.
// Go's own decoder stops at the first failure, so it is consulted only once the
// walk has found nothing.
func decodeBody(r *http.Request, target any) (map[string]any, *httpx.ValidationFailure) {
	limited := http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	defer func() { _ = limited.Close() }()
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, httpx.BodyDecodeFailure(err, body)
	}
	httpx.RecordUnknownBodyFields(r, body, target)
	if len(strings.TrimSpace(string(body))) == 0 {
		// An absent body is the framework's missing-field item whose input is
		// null: there is no object for the field to be missing from.
		return nil, httpx.BodyDecodeFailure(httpx.ErrEmptyBody, body)
	}
	if items := httpx.ValidationItemsAgainstRequest(r, target, body); len(items) > 0 {
		return nil, &httpx.ValidationFailure{Errors: items}
	}
	if _, err := httpx.DecodeLax(body, target); err != nil {
		return nil, httpx.BodyDecodeFailure(err, body)
	}
	object := map[string]any{}
	if err := json.Unmarshal(body, &object); err != nil {
		return nil, httpx.BodyDecodeFailure(err, body)
	}
	return object, nil
}

// toDirective renders a stored record for the wire.
func toDirective(record storage.Directive) Directive {
	tags := record.Tags
	if tags == nil {
		tags = []string{}
	}
	return Directive{
		ID:        record.ID,
		BankID:    record.BankID,
		Name:      record.Name,
		Content:   record.Content,
		Priority:  record.Priority,
		IsActive:  record.IsActive,
		Tags:      tags,
		CreatedAt: httpx.FormatTimestamp(record.CreatedAt),
		UpdatedAt: httpx.FormatTimestamp(record.UpdatedAt),
	}
}

// asHTTPError maps a storage error to the shared envelope.
func asHTTPError(err error) *httpx.HTTPError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrBankNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank)
	case errors.Is(err, storage.ErrDirectiveNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "directive not found")
	case errors.Is(err, storage.ErrWritesUnavailable):
		return httpx.NewHTTPError(httpx.KindUnavailable, httpx.DetailWritesDown)
	default:
		// Anything the storage layer did not recognise is a server fault, not a
		// malformed request: the reference's catch-all answers 500 and reports the
		// error it caught.
		return httpx.NewHTTPError(httpx.KindInternal, err.Error())
	}
}

// containsString reports whether values holds want.
func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// newID builds the identifier of a new directive: a random UUID in the
// canonical dashed form, which is what the reference's column default generates
// and what every read renders it as.
func newID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("dir-%d", time.Now().UnixNano())
	}
	raw[6] = (raw[6] & 0x0f) | 0x40 // version 4
	raw[8] = (raw[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}
