package memoryops

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// A tag group the reference's union cannot read matches none of its four
// branches, which is reported as one item per branch at the group's own path
// rather than as a body-level message. A group carrying only `tag` is such a
// case: the leaf's member is `tags`, and `tag` belongs to no branch.
func TestTagGroupsRejectUnconstrainedNodes(t *testing.T) {
	cases := map[string]string{
		"empty object":    `{}`,
		"unknown wrapper": `{"groups":[{"tags":["ops"]}]}`,
		"bare tag list":   `[[]]`,
		"empty leaf tags": `{"tags":[]}`,
		"empty boolean":   `{"and":[]}`,
		"bare tag":        `{"tag":"team:alpha"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			var field TagGroupsField
			if err := json.Unmarshal([]byte(body), &field); err != nil {
				t.Fatalf("%s must decode its own failure: %v", body, err)
			}
			if len(field.Groups) != 0 {
				t.Fatalf("%s decoded to %+v, want no groups", body, field.Groups)
			}
			if len(field.failures) != len(tagGroupUnionBranches) {
				t.Fatalf("%s produced %d items, want one per union branch", body, len(field.failures))
			}
			branch := field.failures[0].Loc[len(field.failures[0].Loc)-1]
			if branch != "tags" && branch != "TagGroupLeaf" {
				t.Fatalf("%s first item ends at %q, want the leaf's member or branch", body, branch)
			}
		})
	}
}

// The shapes the contract does accept keep decoding, so the refusal above does
// not narrow the surface.
func TestTagGroupsAcceptContractShapes(t *testing.T) {
	cases := []string{
		`[{"tags":["ops"]}]`,
		`[{"tags":["ops"],"tag":"ignored"}]`,
		`[{"tags":["ops"],"match":"all","resolve":"exact"}]`,
		`[{"and":[{"tags":["ops"]},{"tags":["eu"]}]}]`,
		`[{"or":[{"tags":["ops"]}]}]`,
		`[{"not":{"tags":["secret"]}}]`,
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

// A `not` given as a list is refused, and reported the way the reference does:
// the list satisfies the NOT branch's own shape check, so the four items land
// on that branch's child union rather than as a missing `not` member. The
// reference emits seven items for this body — the three members the other
// branches require plus these four (verified against the live service).
func TestTagGroupNotRefusesAList(t *testing.T) {
	var field TagGroupsField
	if err := json.Unmarshal([]byte(`[{"not":[{"tags":["secret"]}]}]`), &field); err != nil {
		t.Fatalf("the field must decode its own failure: %v", err)
	}
	if len(field.Groups) != 0 {
		t.Fatalf("a list-shaped not decoded to %+v, want no groups", field.Groups)
	}
	if len(field.failures) != 7 {
		t.Fatalf("produced %d items, want 7", len(field.failures))
	}
	for _, want := range []string{"TagGroupLeaf", "TagGroupAnd", "TagGroupOr", "TagGroupNot"} {
		found := false
		for _, item := range field.failures {
			if len(item.Loc) == 6 && item.Loc[3] == "TagGroupNot" && item.Loc[5] == want {
				found = true
				if item.Type != "model_attributes_type" {
					t.Fatalf("item for %s is %q, want model_attributes_type", want, item.Type)
				}
			}
		}
		if !found {
			t.Fatalf("no item reports the not's %s child", want)
		}
	}
}

// One node is what `not` takes, and it decodes and negates.
func TestTagGroupNotAcceptsASingleNode(t *testing.T) {
	var field TagGroupsField
	if err := json.Unmarshal([]byte(`[{"not":{"tags":["secret"]}}]`), &field); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(field.Groups) != 1 || field.Groups[0].Not.Child == nil {
		t.Fatalf("groups = %+v, want one group with a not child", field.Groups)
	}
	if got := field.Groups[0].Not.Child.Tags; len(got) != 1 || got[0] != "secret" {
		t.Fatalf("not child tags = %v", got)
	}
}

// The served endpoint reports the list-shaped `not` as a 422 whose envelope
// matches the reference's, rather than silently dropping the filter.
func TestRecallRefusesAListShapedNot(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall",
		`{"query":"deploy","tag_groups":[{"not":[{"tags":["secret"]}]}]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Detail []struct {
			Type string `json:"type"`
			Loc  []any  `json:"loc"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if len(envelope.Detail) != 7 {
		t.Fatalf("detail has %d items, want 7 (body %s)", len(envelope.Detail), rec.Body.String())
	}
}
