package app

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/reporting"
)

// mountReporting wires the observability, memory sub-route, knowledge-base,
// observation and consolidation families into the existing composition root.
func (a *App) mountReporting(handle func(method, pattern string, handler http.HandlerFunc)) {
	reporting.NewHandlers(a.Store).BindRoutes(handle)
}
