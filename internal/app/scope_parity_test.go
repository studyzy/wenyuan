package app

// The observation-scope resolution exists twice: the consolidator resolves it to
// decide which facts fold together, and internal/storage resolves it to decide
// whether a document update moves a memory's observations. They cannot share one
// implementation — internal/consolidation imports internal/storage, so the
// dependency only runs that way — so the two are pinned to the same answer here,
// where both packages can be imported.
//
// This is the cross-package gate docs-dev's mirror in internal/storage is
// written against: the table below is the v1.1.0 resolution contract, and a
// change to either side that is not made to both fails here.

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/consolidation"
	"github.com/studyzy/wenyuan/internal/storage"
)

// scopeResolutionCases is the shared table: a memory's tags, its raw
// observation_scopes value, and (for the changed-pair cases) a second state.
var scopeResolutionCases = []struct {
	name    string
	tags    []string
	raw     string
	newTags []string
	newRaw  string
	want    []string
}{
	{name: "unset is combined", tags: []string{"b", "a"}, raw: "", want: []string{"a\x1fb"}},
	{name: "null is combined", tags: []string{"a"}, raw: "null", want: []string{"a"}},
	{name: "combined", tags: []string{"a", "b"}, raw: `"combined"`, want: []string{"a\x1fb"}},
	{name: "untagged", raw: "", want: []string{""}},
	{name: "per_tag", tags: []string{"b", "a"}, raw: `"per_tag"`, want: []string{"a", "b"}},
	{name: "per_tag untagged", raw: `"per_tag"`, want: []string{""}},
	{name: "all_combinations", tags: []string{"a", "b"}, raw: `"all_combinations"`,
		want: []string{"a", "a\x1fb", "b"}},
	{name: "all_combinations untagged", raw: `"all_combinations"`, want: []string{""}},
	{name: "shared", tags: []string{"a", "b"}, raw: `"shared"`, want: []string{""}},
	{name: "explicit group", raw: `[["y","x"]]`, want: []string{"x\x1fy"}},
	{name: "explicit groups", raw: `[["session:x"],[]]`, want: []string{"", "session:x"}},
	{name: "explicit empty falls back to combined", tags: []string{"a"}, raw: `[]`, want: []string{"a"}},
	{name: "unknown mode is combined", tags: []string{"a"}, raw: `"bogus"`, want: []string{"a"}},
	{
		name: "glob matches its own tags", tags: []string{"topic:go", "session:s1", "topic:rust"},
		raw: `[["topic:*"]]`, want: []string{"topic:go\x1ftopic:rust"},
	},
	{
		name: "a scope whose globs all miss is dropped", tags: []string{"session:s1", "user:u1"},
		raw: `[["topic:*"],["user:*"]]`, want: []string{"user:u1"},
	},
	{
		name: "every scope dropped falls back to combined", tags: []string{"a", "b"},
		raw: `[["nope:*"]]`, want: []string{"a\x1fb"},
	},
	{name: "a deliberate empty scope is kept", tags: []string{"a"}, raw: `[[]]`, want: []string{""}},
	{name: "literal ? and [ are not metacharacters", tags: []string{"t?pic:x", "t[1"},
		raw: `[["t?pic:x","t[1"]]`, want: []string{"t?pic:x\x1ft[1"}},
}

// TestScopeResolutionAgreesAcrossPackages is the mirror gate: for every case the
// consolidator's write-scope set equals the storage mirror's.
func TestScopeResolutionAgreesAcrossPackages(t *testing.T) {
	for _, tc := range scopeResolutionCases {
		t.Run(tc.name, func(t *testing.T) {
			raw := rawScope(tc.raw)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			consolidating := sortedKeys(consolidation.WriteScopes(tc.tags, raw))
			if !reflect.DeepEqual(consolidating, want) {
				t.Fatalf("consolidation.WriteScopes = %q, want %q", consolidating, want)
			}
			mirrored := sortedKeys(storage.WriteScopesFor(tc.tags, raw))
			if !reflect.DeepEqual(mirrored, want) {
				t.Fatalf("storage mirror = %q, want %q", mirrored, want)
			}
		})
	}
}

// TestScopeChangeAgreesAcrossPackages pins the "did it move" answer too, which is
// what decides whether a document update rebuilds a memory's observations.
func TestScopeChangeAgreesAcrossPackages(t *testing.T) {
	cases := []struct {
		name             string
		oldTags, newTags []string
		oldRaw, newRaw   string
	}{
		{"combined tag rename moves", []string{"a"}, []string{"b"}, "", ""},
		{"shared ignores tags", []string{"a"}, []string{"b"}, `"shared"`, `"shared"`},
		{"explicit ignores unrelated tags", []string{"a", "s1"}, []string{"a", "s2"}, `[["a"]]`, `[["a"]]`},
		{"combined over a is explicit a", []string{"a"}, []string{"a"}, "", `[["a"]]`},
		{"the glob moves with its tag", []string{"topic:a", "s1"}, []string{"topic:b", "s1"}, `[["topic:*"]]`, `[["topic:*"]]`},
		{"glob to a miss falls back to combined", []string{"s1"}, []string{"s1"}, `[["topic:*"]]`, `[]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			consolidating := consolidation.WriteScopesChanged(tc.oldTags, rawScope(tc.oldRaw), tc.newTags, rawScope(tc.newRaw))
			mirrored := storage.DocumentScopesChanged(tc.oldTags, rawScope(tc.oldRaw), tc.newTags, rawScope(tc.newRaw))
			if consolidating != mirrored {
				t.Fatalf("write_scopes_changed disagrees: consolidator=%v mirror=%v", consolidating, mirrored)
			}
		})
	}
}

// rawScope turns a case's raw observation_scopes literal into the JSON value the
// store carries; an absent value stays nil.
func rawScope(literal string) json.RawMessage {
	if literal == "" {
		return nil
	}
	return json.RawMessage(literal)
}

// sortedKeys renders a scope set deterministically for comparison.
func sortedKeys(scopes map[string]bool) []string {
	out := make([]string, 0, len(scopes))
	for key := range scopes {
		out = append(out, strings.Clone(key))
	}
	sort.Strings(out)
	return out
}
