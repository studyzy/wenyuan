package app

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/readsurface"
)

// mountReadSurface wires the document, chunk, tag, entity, and graph route
// families into the existing composition root.
func (a *App) mountReadSurface(handle func(method, pattern string, handler http.HandlerFunc)) {
	readsurface.NewHandlers(a.Store).BindRoutes(handle)
}
