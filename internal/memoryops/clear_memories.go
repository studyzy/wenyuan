package memoryops

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// ClearMemoriesResponse is the clear-memories envelope. Every field name is
// public surface: success, message and the unit count that was removed.
type ClearMemoriesResponse struct {
	Success      bool   `json:"success"`
	Message      string `json:"message"`
	DeletedCount int    `json:"deleted_count"`
}

// ClearMemories serves the destructive clear-memories route. The bank itself
// survives — its entities and documents are untouched — so only the memory
// units and what hangs off them go. An optional "type" query parameter narrows
// the removal to one memory type.
func (h *Handlers) ClearMemories(w http.ResponseWriter, r *http.Request) {
	bankID := r.PathValue("bank_id")
	factType := strings.TrimSpace(r.URL.Query().Get("type"))
	removed, err := h.Store.ClearBankMemories(r.Context(), bankID, factType)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapReadError(err))
		return
	}
	scope := ""
	if factType != "" {
		scope = fmt.Sprintf(" of type '%s'", factType)
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, ClearMemoriesResponse{
		Success:      true,
		Message:      fmt.Sprintf("Cleared %d memory unit(s)%s from bank '%s'", removed, scope, bankID),
		DeletedCount: removed,
	})
}
