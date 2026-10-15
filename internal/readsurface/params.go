package readsurface

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/storage"
)

// scopedTagFilter AND-s the caller's resolved read scope into a listing's tag
// filter, so a scoped reader narrows but never widens what it sees. The
// request's own flat tags are folded into one leaf group first, because a group
// list takes precedence over the flat fields; an empty scope leaves the filter
// exactly as the request wrote it.
//
// It mirrors memoryops.andTagScope for the readsurface's own listings: the
// combined result replaces the flat fields it was built from.
func scopedTagFilter(r *http.Request, groups []storage.TagGroup, tags []string, tagsMatch string) ([]storage.TagGroup, []string, string) {
	scope := memoryops.ScopeFromRequest(r)
	if len(scope) == 0 {
		return groups, tags, tagsMatch
	}
	requestGroups := groups
	if len(requestGroups) == 0 && (len(tags) > 0 || tagsMatch == storage.TagsMatchExact) {
		mode := tagsMatch
		if mode == "" {
			mode = storage.TagsMatchAny
		}
		requestGroups = []storage.TagGroup{{Tags: tags, Match: mode}}
	}
	combined := make([]storage.TagGroup, 0, len(requestGroups)+len(scope))
	combined = append(combined, requestGroups...)
	combined = append(combined, scope...)
	return combined, nil, ""
}

// intParam reads an optional integer query parameter. The parameter's absence
// yields def; a malformed value, or one outside [min,max], writes the reference
// 422 envelope and reports false. A max below min means no upper bound.
func intParam(w http.ResponseWriter, r *http.Request, name string, def, min, max int) (int, bool) {
	raw := strings.TrimSpace(httpx.Query(r, name))
	if raw == "" {
		return def, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		writeQueryItem(w, r, httpx.TypeMismatch("int_parsing", []string{"query", name}, raw))
		return 0, false
	}
	if value < min {
		writeQueryItem(w, r, httpx.GreaterThanEqual([]string{"query", name}, value, min))
		return 0, false
	}
	if max >= min && value > max {
		writeQueryItem(w, r, httpx.LessThanEqual([]string{"query", name}, value, max))
		return 0, false
	}
	return value, true
}

// freeIntParam reads an optional integer query parameter with no value bounds;
// only a malformed value is rejected.
func freeIntParam(w http.ResponseWriter, r *http.Request, name string, def int) (int, bool) {
	raw := strings.TrimSpace(httpx.Query(r, name))
	if raw == "" {
		return def, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		writeQueryItem(w, r, httpx.TypeMismatch("int_parsing", []string{"query", name}, raw))
		return 0, false
	}
	return value, true
}

// writeQueryItem emits the framework's structured validation body for one
// rejected query parameter.
func writeQueryItem(w http.ResponseWriter, r *http.Request, item httpx.ValidationError) {
	httpx.WriteValidationFor(w, r, &httpx.ValidationFailure{Errors: []httpx.ValidationError{item}})
}

// tagGroupsQuery parses the JSON-encoded `tag_groups` filter. A GET carries no
// body, so the same tree recall takes as a body arrives here as a string; an
// invalid tree, or sending it alongside `tags`, is a bad request, mirroring the
// reference.
func tagGroupsQuery(w http.ResponseWriter, r *http.Request, tags []string) ([]storage.TagGroup, bool) {
	raw := strings.TrimSpace(httpx.Query(r, "tag_groups"))
	if raw == "" {
		return nil, true
	}
	if len(tags) > 0 {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			"'tags' and 'tag_groups' are mutually exclusive. Use 'tag_groups' for compound filtering."))
		return nil, false
	}
	var nodes []tagGroupNode
	if err := json.Unmarshal([]byte(raw), &nodes); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "Invalid tag_groups: "+err.Error()))
		return nil, false
	}
	return nodesToStorageTagGroups(nodes), true
}

// tagGroupNode is the wire shape of one compound tag-filter node: a leaf names
// `tags`, a compound node names `and`/`or`/`not` children.
type tagGroupNode struct {
	And     []tagGroupNode `json:"and"`
	Or      []tagGroupNode `json:"or"`
	Not     *tagGroupNode  `json:"not"`
	Tags    []string       `json:"tags"`
	Resolve string         `json:"resolve"`
	Match   string         `json:"match"`
}

func nodesToStorageTagGroups(nodes []tagGroupNode) []storage.TagGroup {
	out := make([]storage.TagGroup, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, node.toStorage())
	}
	return out
}

func (n tagGroupNode) toStorage() storage.TagGroup {
	group := storage.TagGroup{Tags: n.Tags, Resolve: n.Resolve, Match: n.Match}
	for _, child := range n.And {
		group.And = append(group.And, child.toStorage())
	}
	for _, child := range n.Or {
		group.Or = append(group.Or, child.toStorage())
	}
	if n.Not != nil {
		group.Not = append(group.Not, n.Not.toStorage())
	}
	return group
}

// bankAttachmentURL is the bank-scoped API path serving one retained
// attachment's bytes. The identifier is quoted so a value carrying a slash or
// space cannot escape its path segment.
func bankAttachmentURL(bankID, attachmentID string) string {
	return httpx.PrefixBanksV1 + "/" + url.PathEscape(bankID) + "/attachments/" + url.PathEscape(attachmentID)
}
