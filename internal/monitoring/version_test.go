package monitoring

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// ts-0003: the body contains the API version string and a feature map whose
// values reflect the effective configuration.
func TestVersionEndpointReflectsConfiguration(t *testing.T) {
	h := &VersionHandler{
		APIVersion: httpx.APIVersion,
		Features:   map[string]bool{"bank_config_api": true, "mcp": false},
	}
	mux := http.NewServeMux()
	BindVersionRoutes(h, func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpx.PathVersion, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body VersionBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.APIVersion != httpx.APIVersion {
		t.Fatalf("api_version = %q", body.APIVersion)
	}
	if !body.Features["bank_config_api"] {
		t.Fatalf("feature map must reflect enablement: %v", body.Features)
	}
	if body.Features["mcp"] {
		t.Fatalf("feature map must reflect disablement: %v", body.Features)
	}
}

// ts-0028: toggling the gate changes the feature map.
func TestVersionFeatureMapTogglesGate(t *testing.T) {
	off := &VersionHandler{APIVersion: httpx.APIVersion, Features: map[string]bool{"bank_config_api": false, "mcp": false}}
	mux := http.NewServeMux()
	BindVersionRoutes(off, func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpx.PathVersion, nil))
	var body VersionBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Features["bank_config_api"] {
		t.Fatalf("disabled gate must be reported false: %v", body.Features)
	}
}

func TestVersionFeatureMapNeverNil(t *testing.T) {
	h := &VersionHandler{APIVersion: httpx.APIVersion}
	rec := httptest.NewRecorder()
	h.Serve(rec, httptest.NewRequest(http.MethodGet, httpx.PathVersion, nil))
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["features"]; !ok {
		t.Fatalf("features must always be present: %s", rec.Body.String())
	}
}
