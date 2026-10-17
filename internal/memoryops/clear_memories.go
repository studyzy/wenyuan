package memoryops

import (
	"fmt"
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// lastQueryValue returns the last value of a repeated query parameter, which is
// the value the reference's framework binds for a scalar query parameter.
func lastQueryValue(r *http.Request, name string) string {
	values, ok := r.URL.Query()[name]
	if !ok || len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}

// ClearMemoriesResponse is the clear-memories envelope. Every field name is
// public surface: success, message and the unit count that was removed.
type ClearMemoriesResponse struct {
	Success      bool   `json:"success"`
	Message      string `json:"message"`
	DeletedCount int    `json:"deleted_count"`
}

// ClearMemories serves the destructive clear-memories route. An empty type is
// the full clear: the bank row survives, but its memory units, documents,
// attachments, observation history, curation archive and entity registry all
// go. A non-empty type narrows the removal to one memory type, leaving the
// other types and the bank's documents and entities in place.
//
// A tag-scoped caller may not run a bank-wide clear at all, because the
// operation reaches every memory regardless of tags.
func (h *Handlers) ClearMemories(w http.ResponseWriter, r *http.Request) {
	// A tag-scoped caller may not run a bank-wide clear at all, because the
	// operation reaches every memory regardless of tags.
	if httpx.RefuseBankWideIfScoped(w, r, "delete or clear the bank") {
		return
	}
	bankID := r.PathValue("bank_id")
	factType := lastQueryValue(r, "type")
	removed, err := h.Store.ClearBankMemories(r.Context(), bankID, factType)
	if err != nil {
		h.writeReadError(w, r, err)
		return
	}
	// The reference takes the type verbatim: a whitespace-only value is a real
	// (non-empty) type that simply matches nothing, so it stays in the message.
	scope := ""
	if factType != "" {
		scope = fmt.Sprintf(" of type '%s'", factType)
	}
	httpx.WriteJSON(w, http.StatusOK, ClearMemoriesResponse{
		Success:      true,
		Message:      fmt.Sprintf("Cleared %d memory unit(s)%s from bank '%s'", removed, scope, bankID),
		DeletedCount: removed,
	})
}
