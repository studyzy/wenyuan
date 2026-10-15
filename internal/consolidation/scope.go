package consolidation

import (
	"encoding/json"
	"sort"
	"strings"
)

// Observation scopes.
//
// A retained fact may carry an observation_scopes request: a mode name or an
// explicit list of tag groups (see storage.RetainItem.ObservationScopes). Left
// unset, the fact folds under the combined default — its own tag set. A pass
// must fold a fact only with facts of the same scope and stamp the observation
// it produces with that scope, so one pass over a bank never mixes two scopes
// into a single conclusion.
//
// The modes mirror the reference:
//
//	combined (default) — one scope: the fact's own tag set.
//	per_tag            — one scope per tag.
//	all_combinations   — one scope per non-empty subset of the tags.
//	shared             — the single untagged scope, whatever the fact's tags.
//	[["a"],["b"]]      — one scope per group in the explicit list.
//
// An empty tag set resolves to the untagged scope in every mode, so untagged
// facts never fan out.

// scopeModeCombined is the default mode: a fact folds with the others sharing
// its exact tag set.
const scopeModeCombined = "combined"

// resolveScopes returns the concrete scopes a fact folds into, one tag list per
// scope. Each returned scope is canonical (sorted, deduplicated) and the list
// is sorted, so the same request always resolves to the same scopes.
func resolveScopes(tags []string, raw json.RawMessage) [][]string {
	mode, groups := parseObservationScopes(raw)
	canonicalTags := canonicalTags(tags)
	switch mode {
	case "per_tag":
		if len(canonicalTags) == 0 {
			return [][]string{{}}
		}
		out := make([][]string, 0, len(canonicalTags))
		for _, tag := range canonicalTags {
			out = append(out, []string{tag})
		}
		return dedupeScopes(out)
	case "all_combinations":
		if len(canonicalTags) == 0 {
			return [][]string{{}}
		}
		return dedupeScopes(combinations(canonicalTags))
	case "shared":
		return [][]string{{}}
	case scopeModeCombined, "":
		return [][]string{canonicalTags}
	default:
		// An explicit list of tag groups. An empty list is treated as the
		// combined default, matching the reference's fall-through.
		if len(groups) == 0 {
			return [][]string{canonicalTags}
		}
		return dedupeScopes(groups)
	}
}

// parseObservationScopes decodes a raw observation_scopes value. A JSON string
// is a mode name; a JSON array is an explicit list of tag groups. An absent,
// null or unparseable value is the combined default. An unrecognised scalar is
// also the default, because the reference's mode vocabulary is closed.
func parseObservationScopes(raw json.RawMessage) (string, [][]string) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", nil
	}
	if strings.HasPrefix(trimmed, `"`) {
		var mode string
		if err := json.Unmarshal(raw, &mode); err == nil {
			return mode, nil
		}
		return "", nil
	}
	var groups [][]string
	if err := json.Unmarshal(raw, &groups); err == nil {
		return "explicit", groups
	}
	return "", nil
}

// combinations returns every non-empty subset of tags, in a deterministic order
// (by size, then by position). The result backs the all_combinations mode.
func combinations(tags []string) [][]string {
	out := make([][]string, 0)
	for size := 1; size <= len(tags); size++ {
		indices := make([]int, size)
		for i := range indices {
			indices[i] = i
		}
		for {
			scope := make([]string, size)
			for i, index := range indices {
				scope[i] = tags[index]
			}
			out = append(out, scope)
			// Advance the index combination; stop when it wraps past the end.
			position := size - 1
			for position >= 0 && indices[position] == position+len(tags)-size {
				position--
			}
			if position < 0 {
				break
			}
			indices[position]++
			for i := position + 1; i < size; i++ {
				indices[i] = indices[i-1] + 1
			}
		}
	}
	return out
}

// canonicalTags returns tags sorted and deduplicated.
func canonicalTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	out := append([]string(nil), tags...)
	sort.Strings(out)
	kept := out[:1]
	for _, tag := range out[1:] {
		if tag != kept[len(kept)-1] {
			kept = append(kept, tag)
		}
	}
	return kept
}

// dedupeScopes canonicalizes each scope and drops repeats, then sorts the list
// so a request with the same scopes in a different order resolves identically.
func dedupeScopes(scopes [][]string) [][]string {
	seen := map[string]bool{}
	out := make([][]string, 0, len(scopes))
	for _, scope := range scopes {
		canonical := canonicalTags(scope)
		key := scopeKey(canonical)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, canonical)
	}
	sort.Slice(out, func(i, j int) bool { return scopeKey(out[i]) < scopeKey(out[j]) })
	return out
}

// scopeKey is the canonical string identity of a scope, used to bucket facts.
func scopeKey(scope []string) string {
	return strings.Join(scope, "\x1f")
}

// sameScope reports whether two scopes name the same canonical tag set.
func sameScope(a, b []string) bool {
	return scopeKey(canonicalTags(a)) == scopeKey(canonicalTags(b))
}

// factGroup is the set of facts that fold together at one observation scope.
type factGroup struct {
	scope []string
	facts []Fact
}

// groupFactsByScope buckets facts by the concrete scope each folds into. A fact
// that resolves to several scopes (per_tag with more than one tag,
// all_combinations, or an explicit multi-group list) appears in each of its
// scopes' groups, which is what produces one observation per requested scope.
// The groups are ordered by scope, so a run is deterministic.
func groupFactsByScope(facts []Fact) []factGroup {
	byKey := map[string]*factGroup{}
	keys := make([]string, 0)
	for _, fact := range facts {
		for _, scope := range resolveScopes(fact.Tags, fact.ObservationScopes) {
			key := scopeKey(scope)
			group, ok := byKey[key]
			if !ok {
				group = &factGroup{scope: scope}
				byKey[key] = group
				keys = append(keys, key)
			}
			group.facts = append(group.facts, fact)
		}
	}
	sort.Strings(keys)
	groups := make([]factGroup, 0, len(keys))
	for _, key := range keys {
		groups = append(groups, *byKey[key])
	}
	return groups
}
