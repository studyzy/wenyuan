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
	mentalmodels.NewHandlers(a.Store).BindRoutes(handle)
}

// mountDirectives wires the directive route family into the composition root.
func (a *App) mountDirectives(handle func(method, pattern string, handler http.HandlerFunc)) {
	directives.NewHandlers(a.Store).BindRoutes(handle)
}

// ensure the httpx import is used for the shared route helpers.
var _ = httpx.BankPath
