package httpx

import (
	"encoding/json"
	"strings"
	"testing"
)

// failureFor decodes body into target the way a handler does and returns the
// rendered validation failure's location for the first item.
func failureFor(t *testing.T, body string, target any) []string {
	t.Helper()
	err := json.Unmarshal([]byte(body), target)
	if err == nil {
		t.Fatalf("%s: decoded without error", body)
	}
	failure := BodyDecodeFailure(err, []byte(body))
	if len(failure.Errors) == 0 {
		t.Fatalf("%s: no validation item", body)
	}
	return failure.Errors[0].Loc
}

// The framework's loc carries an index for every list level. The Go decoder's
// error has no indices, so the raw body is walked to recover them.
func TestDecodeFailureLocCarriesListIndices(t *testing.T) {
	type scopes struct {
		Tags []string `json:"tags"`
	}
	type strategy struct {
		Scopes []scopes `json:"scopes"`
	}
	cases := []struct {
		name   string
		body   string
		target func() any
		want   string
	}{
		{"list element", `{"tags":["ok",1]}`, func() any {
			return &struct {
				Tags []string `json:"tags"`
			}{}
		}, "body,tags,1"},
		{"nested list element", `{"strategies":[{"scopes":[{"tags":["a",2]}]}]}`,
			func() any {
				return &struct {
					Strategies []strategy `json:"strategies"`
				}{}
			}, "body,strategies,0,scopes,0,tags,1"},
		{"second element", `{"tags":["a","b",3]}`, func() any {
			return &struct {
				Tags []string `json:"tags"`
			}{}
		}, "body,tags,2"},
		{"string with a comma", `{"tags":["a,b",1]}`, func() any {
			return &struct {
				Tags []string `json:"tags"`
			}{}
		}, "body,tags,1"},
		{"top-level field, no list", `{"count":"x"}`, func() any {
			return &struct {
				Count int `json:"count"`
			}{}
		}, "body,count"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(failureFor(t, tc.body, tc.target()), ",")
			if got != tc.want {
				t.Fatalf("loc = %q, want %q", got, tc.want)
			}
		})
	}
}
