package app

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/directives"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/mentalmodels"
)

// mountMentalModels wires the mental-model route family into the existing
// composition root.
func (a *App) mountMentalModels(handle func(method, pattern string, handler http.HandlerFunc)) {
	handlers := mentalmodels.NewHandlers(a.Store)
	// With no LLM provider the reference refuses refresh work before any
	// lookup, so the route is armed to refuse rather than queue it.
	handlers.RefuseWithoutRefreshModel = !reflectModelConfigured(a.Provider)
	// The dry run runs the same reflect pipeline a refresh does, so it is
	// supplied from the composition root, which holds the model boundary.
	runner := newMentalModelRefresh(a.Store, a.Provider, newReflectRunner(a.Provider))
	handlers.DryRun = runner.dryRun
	handlers.BindRoutes(handle)
}

// mountDirectives wires the directive route family into the composition root.
func (a *App) mountDirectives(handle func(method, pattern string, handler http.HandlerFunc)) {
	directives.NewHandlers(a.Store).BindRoutes(handle)
}

// ensure the httpx import is used for the shared route helpers.
var _ = httpx.BankPath
