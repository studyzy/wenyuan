package memoryops

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// Observation scopes are writes too. Consolidation writes its observations at
// the scopes a retain or a document update names, so a caller confined to a
// write scope may not send scopes it could not write to. The reference refuses
// such a write with a 403 naming the scope tags outside the caller's scope —
// at retain (its _check_retain_writes) and on a document update
// (update_document's _refuse_unwritable_scopes call).

// RefuseUnwritableScopes reports the 403 the reference answers when a caller
// sends an observation_scopes value it may not write. "shared" writes the
// untagged scope, which a confined caller may not write at all; an explicit
// list checks only its literal (non-glob) entries, because a glob only ever
// picks tags the memory itself carries and those are checked as item tags. A
// group written empty on purpose checks the empty set, which is the same
// refusal. An absent value, a mode name other than shared, and a value in
// neither accepted shape check nothing.
func RefuseUnwritableScopes(raw json.RawMessage, scope []string, confined bool) error {
	if !confined {
		return nil
	}
	groups, ok := decodeScopeGroups(raw)
	if !ok {
		return nil
	}
	for _, group := range groups {
		literal := literalScopeTags(group)
		// A group of only globs checks nothing: its globs can only ever pick
		// tags the memory itself carries, which the item-tag check covers — a
		// glob that matches no tag drops its scope rather than leaving the
		// untagged one. A group written empty checks the empty set.
		if len(literal) == 0 && len(group) > 0 {
			continue
		}
		if unwritable := unwritableScopeTags(literal, scope); len(unwritable) > 0 {
			return httpx.NewHTTPError(httpx.KindForbidden,
				"You can't write observations tagged "+strings.Join(unwritable, ", "))
		}
	}
	return nil
}

// decodeScopeGroups reads the scope groups an observation_scopes value names.
// "shared" reads as one empty group — the untagged scope, which a confined
// caller may not write — and an explicit list as the groups it carries. It
// reports false for everything else: another mode name derives scopes from the
// item's own tags, which the item-tag check already covers, and a value in
// neither accepted shape is not a scope list at all.
func decodeScopeGroups(raw json.RawMessage) ([][]string, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, false
	}
	switch typed := decoded.(type) {
	case string:
		if typed == "shared" {
			return [][]string{{}}, true
		}
		return nil, false
	case []any:
		groups := make([][]string, 0, len(typed))
		for _, element := range typed {
			entries, ok := element.([]any)
			if !ok {
				return nil, false
			}
			group := make([]string, 0, len(entries))
			for _, entry := range entries {
				text, ok := entry.(string)
				if !ok {
					return nil, false
				}
				group = append(group, text)
			}
			groups = append(groups, group)
		}
		return groups, true
	}
	return nil, false
}

// literalScopeTags returns the entries of one scope group that are not globs.
// A `*` anywhere in an entry marks it a glob, which is the reference's own
// glob test.
func literalScopeTags(group []string) []string {
	literal := make([]string, 0, len(group))
	for _, entry := range group {
		if strings.Contains(entry, "*") {
			continue
		}
		literal = append(literal, entry)
	}
	return literal
}

// unwritableScopeTags names the tags the caller may not write, sorted, or
// `(untagged)` when the write carries none — an untagged write is nobody's to
// make inside a write scope, which is what "shared" asks for.
func unwritableScopeTags(tags, scope []string) []string {
	outside := make([]string, 0, len(tags))
	for _, tag := range tags {
		if !httpx.TagsWritable([]string{tag}, scope, true) {
			outside = append(outside, tag)
		}
	}
	if len(outside) == 0 {
		if len(tags) == 0 {
			return []string{"(untagged)"}
		}
		return nil
	}
	sort.Strings(outside)
	return outside
}
