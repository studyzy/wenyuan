package monitoring

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// VersionHandler serves the version endpoint with the API version string and
// the feature map reflecting effective configuration.
type VersionHandler struct {
	// API version string advertised to clients.
	APIVersion string
	// Features is the feature-flag map whose keys gate client behavior.
	Features map[string]bool
}

// VersionBody is the JSON body of the version response.
type VersionBody struct {
	APIVersion string          `json:"api_version"`
	Features   map[string]bool `json:"features"`
}

// Serve writes the version response.
func (h *VersionHandler) Serve(w http.ResponseWriter, r *http.Request) {
	features := h.Features
	if features == nil {
		features = map[string]bool{}
	}
	httpx.WriteJSON(w, http.StatusOK, VersionBody{
		APIVersion: h.APIVersion,
		Features:   features,
	})
}

// BindVersionRoutes registers the version route.
func BindVersionRoutes(h *VersionHandler, handle func(method, pattern string, handler http.HandlerFunc)) {
	handle(http.MethodGet, httpx.PathVersion, h.Serve)
}
