package memoryops

import (
	"encoding/json"
	"testing"
)

// windowFromJSON decodes a temporal window from its JSON object form, the way a
// request body does, so a test states a window in the caller's own spelling.
func windowFromJSON(t *testing.T, body string) *TemporalWindowRequest {
	t.Helper()
	var window TemporalWindowRequest
	if err := json.Unmarshal([]byte(body), &window); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return &window
}
