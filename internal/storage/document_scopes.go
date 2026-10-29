package storage

import (
	"encoding/json"
	"sort"
	"strings"
)

// Observation scoping of a document's memories.
//
// A PATCH of a document may move its memories to another observation scoping
// (the reference's #5309). Only the memories whose RESOLVED scopes move have
// their observations rebuilt (#5593), and the resolved scopes are what
// consolidation writes to — internal/consolidation's resolveScopes. That
// package imports this one, so the set cannot be called from here; the mirror
// below is the same resolution, and a cross-package test pins the two together.

// documentScopeModes are the mode names retain accepts for observation scopes.
var documentScopeModes = map[string]bool{
	"combined": true, "per_tag": true, "all_combinations": true, "shared": true,
}

// writeScopesFor returns the set of observation scopes a memory with the given
// tags and raw observation_scopes value will write to, keyed by scopeKey — the
// set identity two scopes are compared in. An absent, null or unrecognised
// value is the combined default: the memory's own tag set.
func writeScopesFor(tags []string, raw json.RawMessage) map[string]bool {
	out := map[string]bool{}
	for _, scope := range resolveDocumentScopes(tags, raw) {
		out[scopeKey(scope)] = true
	}
	return out
}

// resolveDocumentScopes resolves an observation_scopes value to the concrete,
// canonical scopes it folds into, sorted — the storage-side mirror of the
// consolidator's resolveScopes: combined is the tag set, per_tag one scope per
// tag, all_combinations one per non-empty subset, shared the untagged scope, and
// an explicit list one per group, its tag globs expanded against the memory's
// own tags. An explicit list whose every scope drops falls back to combined,
// never to the untagged scope.
func resolveDocumentScopes(tags []string, raw json.RawMessage) [][]string {
	mode, groups := parseDocumentScopes(raw)
	canonical := canonicalScope(tags)
	switch mode {
	case "per_tag":
		if len(canonical) == 0 {
			return [][]string{{}}
		}
		out := make([][]string, 0, len(canonical))
		for _, tag := range canonical {
			out = append(out, []string{tag})
		}
		return dedupeDocumentScopes(out)
	case "all_combinations":
		if len(canonical) == 0 {
			return [][]string{{}}
		}
		return dedupeDocumentScopes(documentCombinations(canonical))
	case "shared":
		return [][]string{{}}
	case "explicit":
		expanded := expandDocumentScopeGlobs(groups, canonical)
		if len(expanded) == 0 {
			return [][]string{canonical}
		}
		return dedupeDocumentScopes(expanded)
	default: // combined, and every unset or unrecognised value
		return [][]string{canonical}
	}
}

// parseDocumentScopes decodes an observation_scopes value. A JSON string names
// a mode; a JSON array is an explicit list of tag groups. Anything else — an
// absent or null value, an unknown mode, a malformed value — is the combined
// default, because the reference's mode vocabulary is closed.
func parseDocumentScopes(raw json.RawMessage) (string, [][]string) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", nil
	}
	if strings.HasPrefix(trimmed, `"`) {
		var mode string
		if err := json.Unmarshal(raw, &mode); err == nil && documentScopeModes[mode] {
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

// expandDocumentScopeGlobs replaces each tag glob in an explicit scope list with
// the tags of the memory it matches; only * is a metacharacter. A scope whose
// globs all match nothing is dropped — an empty scope is the untagged, bank-wide
// one, which a memory the pattern missed must not join. A scope written empty on
// purpose has no globs and is kept.
func expandDocumentScopeGlobs(scopes [][]string, tags []string) [][]string {
	resolved := make([][]string, 0, len(scopes))
	for _, scope := range scopes {
		hasGlob := false
		for _, entry := range scope {
			if strings.Contains(entry, "*") {
				hasGlob = true
				break
			}
		}
		if !hasGlob {
			resolved = append(resolved, scope)
			continue
		}
		expanded := make([]string, 0, len(scope)+len(tags))
		for _, entry := range scope {
			if !strings.Contains(entry, "*") {
				expanded = append(expanded, entry)
				continue
			}
			for _, tag := range tags {
				if documentGlobMatch(entry, tag) {
					expanded = append(expanded, tag)
				}
			}
		}
		if len(expanded) > 0 {
			resolved = append(resolved, expanded)
		}
	}
	return resolved
}

// documentGlobMatch is fnmatchcase for a pattern whose only metacharacter is *,
// which is the subset an observation scope treats as a glob.
func documentGlobMatch(pattern, name string) bool {
	px, nx := 0, 0
	star, mark := -1, 0
	for nx < len(name) {
		switch {
		case px < len(pattern) && pattern[px] == name[nx]:
			px++
			nx++
		case px < len(pattern) && pattern[px] == '*':
			star, mark = px, nx
			px++
		case star >= 0:
			px = star + 1
			mark++
			nx = mark
		default:
			return false
		}
	}
	for px < len(pattern) && pattern[px] == '*' {
		px++
	}
	return px == len(pattern)
}

// documentCombinations returns every non-empty subset of tags, in a
// deterministic order (by size, then by position).
func documentCombinations(tags []string) [][]string {
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

// dedupeDocumentScopes canonicalizes each scope and drops repeats, then sorts by
// scope identity so the same request resolves identically.
func dedupeDocumentScopes(scopes [][]string) [][]string {
	seen := map[string]bool{}
	out := make([][]string, 0, len(scopes))
	for _, scope := range scopes {
		canonical := canonicalScope(scope)
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

// scopeKey is the canonical string identity of a scope, the key the
// consolidator's WriteScopes buckets its scopes by.
func scopeKey(scope []string) string {
	return strings.Join(scope, "\x1f")
}

// documentScopesChanged reports whether moving a memory from one tag set and
// scoping to another changes the scopes it writes observations to — the only
// case in which its observations must be rebuilt (write_scopes_changed).
func documentScopesChanged(oldTags []string, oldScopes json.RawMessage, newTags []string, newScopes json.RawMessage) bool {
	before, after := writeScopesFor(oldTags, oldScopes), writeScopesFor(newTags, newScopes)
	if len(before) != len(after) {
		return true
	}
	for key := range before {
		if !after[key] {
			return true
		}
	}
	return false
}

// DocumentScopeChange is one document update's effect on the tags and
// observation scopes of the document's memories — the input to
// RetagDocumentMemories.
type DocumentScopeChange struct {
	// OldTags are the document's tags before the update, as read at its start.
	OldTags []string
	// NewTags replaces each unit's document-derived tags; nil when the update
	// changes only observation scopes, which writes no tags at all.
	NewTags []string
	// NewScopes is the raw observation_scopes value the changed memories end
	// with, or nil to keep each unit's own. It only rewrites units whose
	// resolved scopes it moves.
	NewScopes json.RawMessage
	// LabelKeys names the bank's `entity_labels` groups flagged tag:true, whose
	// per-memory tag projection each unit keeps across the retag.
	LabelKeys map[string]bool
}

// RemovedObservation is one observation a scope change swept, with the memories
// it was consolidated from — the record the document's change history keeps.
type RemovedObservation struct {
	ObservationID string
	SourceFactIDs []string
}

// RetagOutcome reports what a RetagDocumentMemories cascade changed: how many
// observations were deleted, how many memories were requeued for
// re-consolidation, and which observations went (for the change history).
type RetagOutcome struct {
	InvalidatedObservations int
	RequeuedUnits           int
	Removed                 []RemovedObservation
}

// DocumentUnitScope is one memory unit's scope inputs, as a retag reads them
// before writing anything: the unit's tags and its raw observation_scopes.
type DocumentUnitScope struct {
	ID       string
	Tags     []string
	Scopes   json.RawMessage
	FactType MemoryUnitType
}

// documentRetagPlan says, for one unit, what its update writes: the final tag
// list it ends with, the raw scopes to write (nil to keep its own), and whether
// the scopes it resolves to move — in which case its observations go and it is
// requeued for re-consolidation.
type documentRetagPlan struct {
	unit        DocumentUnitScope
	finalTags   []string
	newScopes   json.RawMessage
	scopesMoved bool
}

// planDocumentRetag resolves each unit's final tags and scopes. A unit's tags
// end as the document's new tags plus the label tags it already carries; its
// scopes end as the update's scopes when they are set, or as its own scopes with
// a single-tag rename carried in. A scope-only update (NewTags nil) keeps every
// unit's tags exactly as they are.
func planDocumentRetag(units []DocumentUnitScope, change DocumentScopeChange) []documentRetagPlan {
	documentTags := normalizeTags(change.NewTags)
	plans := make([]documentRetagPlan, 0, len(units))
	for _, unit := range units {
		final := unit.Tags
		if change.NewTags != nil {
			final = appendTagLabels(documentTags, labelDerivedTags(unit.Tags, change.LabelKeys))
		}
		scopes := change.NewScopes
		if scopes == nil {
			scopes = renamedScopes(unit.Scopes, change.OldTags, change.NewTags)
		}
		plans = append(plans, documentRetagPlan{
			unit:      unit,
			finalTags: final,
			newScopes: scopes,
			// Compared against the scopes the unit will END with: a unit keeping
			// its label projection, or an update that resolves to the same
			// scopes ("combined" over [a] -> [["a"]]), has moved nothing.
			scopesMoved: documentScopesChanged(unit.Tags, unit.Scopes, final, currentOrNewScopes(scopes, unit.Scopes)),
		})
	}
	return plans
}

// currentOrNewScopes returns the scopes a plan writes when it writes one, or the
// unit's own scopes otherwise, so a comparison sees the value the unit ends with.
func currentOrNewScopes(newScopes, current json.RawMessage) json.RawMessage {
	if newScopes != nil {
		return newScopes
	}
	return current
}

// movedSourceUnits returns the ids of the source facts (world/experience) whose
// observation scopes an update moves, in plan order. An observation is derived,
// so it is never requeued for re-consolidation.
func movedSourceUnits(plans []documentRetagPlan) []string {
	out := make([]string, 0, len(plans))
	for _, plan := range plans {
		if !plan.scopesMoved {
			continue
		}
		if plan.unit.FactType == TypeWorld || plan.unit.FactType == TypeExperience {
			out = append(out, plan.unit.ID)
		}
	}
	return out
}

// WriteScopesFor is the exported form of writeScopesFor, for the cross-package
// parity gate: internal/consolidation resolves the same scopes from its own
// code (it imports this package, so the dependency cannot run the other way),
// and internal/app asserts the two agree.
func WriteScopesFor(tags []string, raw json.RawMessage) map[string]bool {
	return writeScopesFor(tags, raw)
}

// DocumentScopesChanged is the exported form of documentScopesChanged, for the
// same cross-package parity gate.
func DocumentScopesChanged(oldTags []string, oldScopes json.RawMessage, newTags []string, newScopes json.RawMessage) bool {
	return documentScopesChanged(oldTags, oldScopes, newTags, newScopes)
}
