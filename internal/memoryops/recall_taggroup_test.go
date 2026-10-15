package memoryops

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// A tag-group filter that constrains nothing is refused, so a client trying to
// narrow a search can never silently receive a wider one.
func TestTagGroupsRejectUnconstrainedNodes(t *testing.T) {
	cases := map[string]string{
		"empty object":    `{}`,
		"unknown wrapper": `{"groups":[{"tags":["ops"]}]}`,
		"empty tag list":  `[[]]`,
		"empty leaf tags": `[{"tags":[]}]`,
		"empty boolean":   `[{"and":[]}]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			var field TagGroupsField
			err := json.Unmarshal([]byte(body), &field)
			if err == nil {
				t.Fatalf("%s decoded to %+v, want a validation error", body, field.Groups)
			}
			if !strings.Contains(err.Error(), "tag group") {
				t.Fatalf("error = %v, want the tag-group message", err)
			}
		})
	}
}

// The shapes the contract does accept keep decoding, so the refusal above does
// not narrow the surface.
func TestTagGroupsAcceptContractShapes(t *testing.T) {
	cases := []string{
		`[{"tags":["ops"]}]`,
		`[["ops","personal"]]`,
		`[{"tags":["ops"],"match":"all","resolve":"exact"}]`,
		`[{"and":[{"tags":["ops"]},{"tags":["eu"]}]}]`,
		`[{"or":[{"tags":["ops"]}]}]`,
		`[{"not":[{"tags":["secret"]}]}]`,
		`{"tags":["ops"]}`,
	}
	for _, body := range cases {
		var field TagGroupsField
		if err := json.Unmarshal([]byte(body), &field); err != nil {
			t.Fatalf("%s must decode: %v", body, err)
		}
		if len(field.Groups) == 0 {
			t.Fatalf("%s decoded to no groups", body)
		}
	}
}

// A leaf whose match mode is unset narrows rather than widens: untagged
// memories are excluded, which is the reference's leaf default. The flat field
// keeps its own default of "any", which includes them.
func TestLeafDefaultMatchIsStrict(t *testing.T) {
	mux, store := newServer(t)
	store.SeedMemory("b1", storage.RecallMatch{ID: "tagged", Text: "the deploy runbook is blue-green", Type: "world", Tags: []string{"ops"}})
	store.SeedMemory("b1", storage.RecallMatch{ID: "untagged", Text: "the deploy runbook is canary", Type: "world"})

	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"deploy runbook","tag_groups":[{"tags":["ops"]}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var leaf RecallResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &leaf); err != nil {
		t.Fatal(err)
	}
	if len(leaf.Results) != 1 || leaf.Results[0].ID != "tagged" {
		t.Fatalf("an unset leaf mode must exclude untagged memories: %s", rec.Body.String())
	}

	// The flat field's own default still includes them.
	flat := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"deploy runbook","tags":["ops"]}`)
	var flatResponse RecallResponse
	if err := json.Unmarshal(flat.Body.Bytes(), &flatResponse); err != nil {
		t.Fatal(err)
	}
	if len(flatResponse.Results) != 2 {
		t.Fatalf("the flat match mode includes untagged memories by default: %s", flat.Body.String())
	}
}

// An explicit strict mode excludes untagged memories, and an explicit
// non-strict leaf mode includes them.
func TestExplicitMatchModes(t *testing.T) {
	mux, store := newServer(t)
	store.SeedMemory("b1", storage.RecallMatch{ID: "tagged", Text: "the deploy runbook is blue-green", Type: "world", Tags: []string{"ops"}})
	store.SeedMemory("b1", storage.RecallMatch{ID: "untagged", Text: "the deploy runbook is canary", Type: "world"})

	strict := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"deploy runbook","tags":["ops"],"tags_match":"any_strict"}`)
	var strictResponse RecallResponse
	if err := json.Unmarshal(strict.Body.Bytes(), &strictResponse); err != nil {
		t.Fatal(err)
	}
	if len(strictResponse.Results) != 1 || strictResponse.Results[0].ID != "tagged" {
		t.Fatalf("any_strict must exclude untagged memories: %s", strict.Body.String())
	}

	leafAny := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"deploy runbook","tag_groups":[{"tags":["ops"],"match":"any"}]}`)
	var leafResponse RecallResponse
	if err := json.Unmarshal(leafAny.Body.Bytes(), &leafResponse); err != nil {
		t.Fatal(err)
	}
	if len(leafResponse.Results) != 2 {
		t.Fatalf("an explicit non-strict leaf mode includes untagged memories: %s", leafAny.Body.String())
	}
}
