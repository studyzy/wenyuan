package storage

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ErrChunkNotFound is returned when a chunk identifier is unknown to a bank.
var ErrChunkNotFound = errors.New("chunk not found")

// ErrEntityNotFound is returned when an entity identifier is unknown to a bank.
var ErrEntityNotFound = errors.New("entity not found")

// DocumentQuery pages and filters a document listing.
type DocumentQuery struct {
	BankID string
	// Tags narrows to documents whose tags satisfy TagsMatch.
	Tags []string
	// TagsMatch is one of any, all, any_strict, all_strict, exact. Empty means
	// any_strict, the reference's default for the document listing.
	TagsMatch string
	// TagGroups, when set, replaces the flat tag filter with a compound tree,
	// which is how a caller's resolved read scope is AND-ed into the listing.
	TagGroups []TagGroup
	// Query is a case-insensitive substring match on the document id or its
	// stored text, matched literally: a `%` or `_` in it is that character, not
	// a wildcard. A store may stop counting at DocumentSearchCap matches (see
	// Page.TotalIsEstimate).
	Query  string
	Limit  int
	Offset int
	// TimeField selects which timestamp the bounds apply to: created_at
	// (default) or updated_at.
	TimeField string
	// TimeStart and TimeEnd bound the selected timestamp when set.
	TimeStart *time.Time
	TimeEnd   *time.Time
}

// ChunkQuery pages a chunk listing.
type ChunkQuery struct {
	BankID     string
	DocumentID string
	Limit      int
	Offset     int
}

// EntityQuery pages and filters an entity listing. A tag filter restricts the
// listing to entities a matching memory mentions, and recomputes each entity's
// mention count over the matching memories only.
type EntityQuery struct {
	BankID string
	// ID, when set, restricts the listing to that one entity (applied after the
	// tag filter, so an unmatched filter yields an empty page).
	ID string
	// Tags, TagsMatch and TagGroups restrict which memories count.
	Tags      []string
	TagsMatch string
	TagGroups []TagGroup
	Limit     int
	Offset    int
}

// tagFilterActive reports whether a tag predicate narrows a read: explicit
// tags, tag groups, or an exact-match mode (whose empty request selects the
// untagged scope).
func tagFilterActive(tags []string, mode string, groups []TagGroup) bool {
	return len(tags) > 0 || len(groups) > 0 || mode == TagsMatchExact
}

// matchesTagFilter applies a read's tag predicate to one unit's tags. With no
// explicit tags and a non-exact mode no filter is applied, so the row stays
// visible regardless of what it carries.
func matchesTagFilter(present, tags []string, mode string, groups []TagGroup) bool {
	if len(groups) > 0 {
		return matchTagGroups(present, groups)
	}
	if len(tags) == 0 && mode != TagsMatchExact {
		return true
	}
	return matchFlatTags(present, tags, mode)
}

// DocumentSearchCap bounds how many documents a `q` search counts and serves.
// Without it the count would scan every document's text on every page; with it
// the scan stops at the cap's worth of rows (engine/memories/pg/documents.py
// DOCUMENT_SEARCH_CAP).
const DocumentSearchCap = 1000

// DocumentPage is one page of documents with the pre-paging total.
type DocumentPage struct {
	Documents []Document
	// Total is how many documents matched — or, for a `q` search that hit
	// DocumentSearchCap, the cap itself, with TotalIsEstimate set.
	Total int
	Limit int
	// Offset is the requested offset. A `q` search never pages past the cap, so
	// Limit and Offset here stay the caller's values while the page itself holds
	// at most DocumentSearchCap-offset rows.
	Offset int
	// TotalIsEstimate reports that a `q` search stopped at its match cap, so
	// Total is a lower bound.
	TotalIsEstimate bool
}

// documentSearchMatches reports whether a document's id or text contains q,
// case-insensitively, and which fields matched, in the reference's order:
// id first, then original_text.
func documentSearchMatches(doc Document, q string) []string {
	if q == "" {
		return []string{}
	}
	needle := strings.ToLower(q)
	matched := make([]string, 0, 2)
	if strings.Contains(strings.ToLower(doc.ID), needle) {
		matched = append(matched, "id")
	}
	if strings.Contains(strings.ToLower(doc.OriginalText), needle) {
		matched = append(matched, "original_text")
	}
	return matched
}

// matchedDocumentFields renders a search's per-row match flags — the store's
// `CASE WHEN ... THEN 1 ELSE 0` columns — as the field names a listing serves.
func matchedDocumentFields(idMatched, textMatched int) []string {
	matched := make([]string, 0, 2)
	if idMatched != 0 {
		matched = append(matched, "id")
	}
	if textMatched != 0 {
		matched = append(matched, "original_text")
	}
	return matched
}

// documentsPageBounds resolves the page of a document listing. A `q` search
// stops where its count does, so paging never serves a row Total disowns:
// limit becomes max(0, min(limit, DocumentSearchCap-offset)).
func documentsPageBounds(total, offset, limit int, searched bool) (start, end int) {
	if limit < 0 {
		limit = 100
	}
	start = offset
	if start > total {
		start = total
	}
	end = start + limit
	if searched {
		end = offset + limit
		if capEnd := DocumentSearchCap; end > capEnd {
			end = capEnd
		}
		if end < start {
			end = start
		}
	}
	if end > total {
		end = total
	}
	return start, end
}

// ChunkPage is one page of chunks with the pre-paging total.
type ChunkPage struct {
	Chunks []Chunk
	Total  int
	Limit  int
	Offset int
}

// EntityPage is one page of entities with the pre-paging total.
type EntityPage struct {
	Entities []Entity
	Total    int
	Limit    int
	Offset   int
}

// TagCount is one tag with how many documents and units carry it.
type TagCount struct {
	Tag   string
	Count int
}

// GraphNodeData is the payload of a memory-graph node.
type GraphNodeData struct {
	ID         string
	Text       string
	Context    string
	FactType   string
	Entities   []string
	DocumentID string
	ChunkID    string
	Tags       []string
	Mentioned  *time.Time
	Date       *time.Time
	CreatedAt  *time.Time
	// SourceMemoryIDs are the memories an observation was consolidated from, so
	// the graph can inherit its links and entities from them.
	SourceMemoryIDs []string
}

// GraphEdgeData is the payload of a memory-graph edge.
type GraphEdgeData struct {
	ID     string
	From   string
	To     string
	Type   string
	Entity string
	Weight float64
}

// GraphTableRow is one memory rendered as a flat row, so a client can present
// the same data as a table without walking the edges.
type GraphTableRow struct {
	ID            string
	Text          string
	Context       string
	OccurredStart *time.Time
	OccurredEnd   *time.Time
	MentionedAt   *time.Time
	Date          *time.Time
	Entities      string
	DocumentID    string
	ChunkID       string
	FactType      string
	Tags          []string
	CreatedAt     *time.Time
	ProofCount    int
}

// GraphQuery filters and bounds a memory-graph read. The filters select the
// units that become nodes and table rows before the limit is applied.
type GraphQuery struct {
	BankID string
	// Type narrows to one fact type: world|experience|observation; "" = all.
	Type string
	// Query is a case-insensitive substring match on unit text.
	Query string
	// Tags and TagsMatch narrow by tag; empty TagsMatch means all_strict.
	Tags      []string
	TagsMatch string
	// TagGroups, when set, replaces the flat tag filter.
	TagGroups []TagGroup
	// DocumentID and ChunkID narrow to units from one document or chunk.
	DocumentID string
	ChunkID    string
	// Limit caps the node set; negative means the store default.
	Limit int
}

// GraphData is the memory graph of one bank.
type GraphData struct {
	Nodes     []GraphNodeData
	Edges     []GraphEdgeData
	TableRows []GraphTableRow
	Total     int
	Limit     int
}

// EntityGraphQuery filters and bounds an entity co-occurrence graph read. A tag
// filter restricts which memories count, so node mention counts and edges are
// recomputed from the matching memories only.
type EntityGraphQuery struct {
	BankID string
	// Limit caps the edge set; negative means the store default.
	Limit int
	// MinCount is the minimum cooccurrence weight an edge needs; <1 means 1.
	MinCount  int
	Tags      []string
	TagsMatch string
	TagGroups []TagGroup
}

// EntityGraphNode is one node of the entity graph, in the shape the client
// renders directly.
type EntityGraphNode struct {
	ID           string
	Label        string
	Color        string
	MentionCount int
}

// EntityGraphEdge is one weighted relation between two entities.
type EntityGraphEdge struct {
	ID     string
	Source string
	Target string
	Weight int
	Color  string
	// LastCooccurred is when the two entities were last seen together, or nil
	// when the row carries no stamp.
	LastCooccurred *time.Time
}

// EntityGraph is the entity graph of one bank.
type EntityGraph struct {
	Nodes         []EntityGraphNode
	Edges         []EntityGraphEdge
	TotalEntities int
	TotalEdges    int
	Limit         int
}

// EntityMention is one memory text that mentions an entity.
type EntityMention struct {
	Text        string
	MentionedAt *time.Time
}

// MaintenanceTarget is one bank-scoped identifier queued for maintenance.
type MaintenanceTarget struct {
	BankID string
	ID     string
}

// jsonColumn encodes a map for a jsonb column, defaulting to an empty object so
// the column's not-null shape holds.
func jsonColumn(value map[string]any) ([]byte, error) {
	if value == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(value)
}

// DocumentMetadata returns the value a document serves as its
// `document_metadata`: the `metadata` member of its retain_params, as it
// stands. The document has no metadata column, so that member is where the
// value lives, written there by the retain that received it; a document that
// never carried metadata has no member and reads as nil.
func DocumentMetadata(retainParams map[string]any) any {
	return DocumentMetadataValue(retainParams["metadata"])
}

// DocumentMetadataValue returns the value to serve for a document's metadata
// member, or nil when it reads as absent.
//
// The reference serves the member through a truth test (`metadata or None`)
// rather than a type test, so a value that is present but empty — null, false,
// zero, "", {} or [] — is reported as no metadata, while a string, array or
// number that is present is served as it stands even though it is not an
// object.
func DocumentMetadataValue(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case bool:
		if !typed {
			return nil
		}
	case string:
		if typed == "" {
			return nil
		}
	case float64:
		if typed == 0 {
			return nil
		}
	case []any:
		if len(typed) == 0 {
			return nil
		}
	case map[string]any:
		if len(typed) == 0 {
			return nil
		}
	}
	return value
}

// decodePostgresTextArray parses a text[] value that arrived as its literal
// form. The driver returns the array textually for a column selected through a
// plain query, so it is decoded here rather than left to a type the scanner
// cannot accept.
func decodePostgresTextArray(raw []byte) []string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "{}" || trimmed == "NULL" {
		return nil
	}
	trimmed = strings.TrimPrefix(trimmed, "{")
	trimmed = strings.TrimSuffix(trimmed, "}")
	if trimmed == "" {
		return nil
	}
	parts := splitArrayElements(trimmed)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		// Only the quotes that fence the element come off. Trimming every quote
		// would also eat one an element literally carries — the tag
		// `say "hi"` arrives as `"say ""hi"""`, with the inner quotes doubled —
		// and turn it into a different tag. PostgreSQL folds the doubling as it
		// reads the literal or casts the array to text itself, so both dialect
		// forms decode to the element as stored.
		part = trimArrayQuotes(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// trimArrayQuotes removes the double quotes fencing an array element and folds
// each doubled quote inside it back into one.
func trimArrayQuotes(element string) string {
	if !strings.HasPrefix(element, `"`) {
		return element
	}
	element = element[1:]
	if !strings.HasSuffix(element, `"`) {
		return `"` + element
	}
	return strings.ReplaceAll(element[:len(element)-1], `""`, `"`)
}

// splitArrayElements splits on commas that are not inside a quoted element, so
// a value containing a comma survives intact.
func splitArrayElements(input string) []string {
	var out []string
	var current strings.Builder
	inQuotes := false
	for i := 0; i < len(input); i++ {
		c := input[i]
		switch {
		case c == '"':
			inQuotes = !inQuotes
			current.WriteByte(c)
		case c == ',' && !inQuotes:
			out = append(out, current.String())
			current.Reset()
		default:
			current.WriteByte(c)
		}
	}
	out = append(out, current.String())
	return out
}
