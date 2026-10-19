package monitoring

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sort"

	"github.com/studyzy/wenyuan/internal/config"
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

// MarshalJSON serialises the features object in the reference's declaration
// order. The reference emits its feature model's field order, and a Go map
// would order the same keys alphabetically instead; the key set is identical,
// only the JSON object order differs.
func (b VersionBody) MarshalJSON() ([]byte, error) {
	version, err := json.Marshal(b.APIVersion)
	if err != nil {
		return nil, err
	}
	features, err := marshalFeatureMap(b.Features)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString(`{"api_version":`)
	buf.Write(version)
	buf.WriteString(`,"features":`)
	buf.Write(features)
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// marshalFeatureMap writes the feature flags as a JSON object. The reference's
// declaration order comes first; a key outside that set (a caller's own map)
// follows in sorted order so the output stays deterministic.
func marshalFeatureMap(features map[string]bool) ([]byte, error) {
	ordered := make([]string, 0, len(features))
	seen := make(map[string]struct{}, len(features))
	for _, key := range config.FeatureKeys {
		if _, ok := features[key]; ok {
			ordered = append(ordered, key)
			seen[key] = struct{}{}
		}
	}
	if len(seen) != len(features) {
		extra := make([]string, 0, len(features)-len(seen))
		for key := range features {
			if _, ok := seen[key]; !ok {
				extra = append(extra, key)
			}
		}
		sort.Strings(extra)
		ordered = append(ordered, extra...)
	}

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range ordered {
		if i > 0 {
			buf.WriteByte(',')
		}
		name, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(features[key])
		if err != nil {
			return nil, err
		}
		buf.Write(name)
		buf.WriteByte(':')
		buf.Write(value)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
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
