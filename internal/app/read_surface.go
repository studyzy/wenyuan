package app

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/readsurface"
)

// mountReadSurface wires the document, chunk, tag, entity, and graph route
// families into the existing composition root.
//
// The write-side handlers are handed to the read surface for the one operation
// that both reads and writes: a document reprocess re-extracts from stored text
// through the same pipeline the write route uses, so it takes the boundary that
// owns extraction rather than reaching the store directly.
func (a *App) mountReadSurface(handle func(method, pattern string, handler http.HandlerFunc), writes *memoryops.Handlers) {
	handlers := readsurface.NewHandlers(a.Store)
	handlers.Reprocess = writes
	handlers.AutoConsolidationEnabled = a.Config.EnableAutoConsolidation
	handlers.BindRoutes(handle)
}
