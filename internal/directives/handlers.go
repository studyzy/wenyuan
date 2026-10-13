// Package directives serves the directive route family: standing instructions
// the reflection loop follows, ordered by priority.
package directives

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// maxBodyBytes bounds a request body on this route family.
const maxBodyBytes = 1 << 20

// Directive is the wire shape. Field names are public surface.
type Directive struct {
	ID        string    `json:"id"`
	BankID    string    `json:"bank_id"`
	Name      string    `json:"name"`
	Content   string    `json:"content"`
	Priority  int       `json:"priority"`
	IsActive  bool      `json:"is_active"`
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CreateRequest is the create body.
type CreateRequest struct {
	Name     string   `json:"name"`
	Content  string   `json:"content"`
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
	BankID     string      `json:"bank_id"`
	Directives []Directive `json:"directives"`
}

// Handlers serves the directive route family.
type Handlers struct{ Store storage.Store }

// NewHandlers builds the handlers over the store.
func NewHandlers(store storage.Store) *Handlers { return &Handlers{Store: store} }

// List returns the bank's directives, highest priority first.
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	records, err := h.Store.ListDirectives(r.Context(), bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	out := make([]Directive, 0, len(records))
	for _, record := range records {
		out = append(out, toDirective(record))
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, ListResponse{BankID: bankID, Directives: out})
}

// Get returns one directive.
func (h *Handlers) Get(w http.ResponseWriter, r *http.Request) {
	bankID, id, ok := h.member(w, r)
	if !ok {
		return
	}
	record, err := h.Store.GetDirective(r.Context(), bankID, id)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, toDirective(record))
}

// Create adds a directive.
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	var body CreateRequest
	if err := decodeBody(r, &body); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "name is required"))
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "content is required"))
		return
	}
	// A directive is active unless the caller explicitly deactivates it.
	active := true
	if body.IsActive != nil {
		active = *body.IsActive
	}
	record, err := h.Store.PutDirective(r.Context(), storage.Directive{
		ID:       newID(),
		BankID:   bankID,
		Name:     body.Name,
		Content:  body.Content,
		Priority: body.Priority,
		IsActive: active,
		Tags:     body.Tags,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusCreated, toDirective(record))
}

// Update applies a partial update.
func (h *Handlers) Update(w http.ResponseWriter, r *http.Request) {
	bankID, id, ok := h.member(w, r)
	if !ok {
		return
	}
	var body UpdateRequest
	if err := decodeBody(r, &body); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
		return
	}
	record, err := h.Store.UpdateDirective(r.Context(), bankID, id, storage.DirectivePatch{
		Name:     body.Name,
		Content:  body.Content,
		Priority: body.Priority,
		IsActive: body.IsActive,
		Tags:     body.Tags,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, toDirective(record))
}

// Delete removes a directive.
func (h *Handlers) Delete(w http.ResponseWriter, r *http.Request) {
	bankID, id, ok := h.member(w, r)
	if !ok {
		return
	}
	if err := h.Store.DeleteDirective(r.Context(), bankID, id); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// BindRoutes registers the family's route table.
func (h *Handlers) BindRoutes(handle func(method, pattern string, handler http.HandlerFunc)) {
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixDirectives), h.List)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixDirectives), h.Create)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixDirectiveMember), h.Get)
	handle(http.MethodPatch, httpx.BankPath(httpx.SuffixDirectiveMember), h.Update)
	handle(http.MethodDelete, httpx.BankPath(httpx.SuffixDirectiveMember), h.Delete)
}

// member resolves the bank and the directive identifier.
func (h *Handlers) member(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return "", "", false
	}
	id := strings.TrimSpace(r.PathValue("directive_id"))
	if id == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "directive not found"))
		return "", "", false
	}
	return bankID, id, true
}

// requireBank reports not-found for an unknown bank.
func (h *Handlers) requireBank(w http.ResponseWriter, r *http.Request) (string, bool) {
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "bank not found"))
		return "", false
	}
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "bank not found"))
		return "", false
	}
	return bankID, true
}

// decodeBody decodes a JSON body with a size bound.
func decodeBody(r *http.Request, target any) error {
	limited := http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	defer func() { _ = limited.Close() }()
	if err := json.NewDecoder(limited).Decode(target); err != nil {
		return fmt.Errorf("request body must be JSON: %w", err)
	}
	return nil
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
		CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt,
	}
}

// asHTTPError maps a storage error to the shared envelope.
func asHTTPError(err error) *httpx.HTTPError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrBankNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "bank not found")
	case errors.Is(err, storage.ErrDirectiveNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "directive not found")
	default:
		return httpx.NewHTTPError(httpx.KindBadRequest, err.Error())
	}
}

// newID builds an opaque identifier.
func newID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("dir-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}
