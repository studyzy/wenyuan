package readsurface

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/storage"
)

// defaultPageLimit is the page size used when the caller does not ask for one.
const defaultPageLimit = 100

// defaultGraphLimit is the page size the graph reads use when the caller does
// not ask for one.
const defaultGraphLimit = 1000

// Handlers serves the read-surface route families.
type Handlers struct {
	Store storage.Store
	// Reprocess re-runs the retain pipeline over one document's stored text. It
	// is supplied by the composition root, which owns the extraction and
	// embedding boundaries the pipeline needs; a nil value refuses reprocess.
	Reprocess DocumentReprocessor
}

// DocumentReprocessor re-runs the retain pipeline over one document's stored
// text and replayable parameters.
type DocumentReprocessor interface {
	ReprocessDocument(ctx context.Context, bankID, documentID string) (storage.RetainOutcome, error)
}

// NewHandlers builds the handlers over the store.
func NewHandlers(store storage.Store) *Handlers { return &Handlers{Store: store} }

// BindRoutes registers every route in the family.
func (h *Handlers) BindRoutes(handle func(method, pattern string, handler http.HandlerFunc)) {
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixDocuments), httpx.QueryParams(h.ListDocuments,
		"q", "tags", "tags_match", "time_field", "start_date", "end_date", "limit", "offset"))
	// A document id may itself contain slashes, which a single ServeMux path
	// parameter cannot capture, so the member routes are registered as a
	// subtree and dispatched by the trailing action word.
	docMember := httpx.BankPath(httpx.SuffixDocumentMember)
	handle(http.MethodGet, docMember, h.dispatchDocumentMember)
	handle(http.MethodPatch, docMember, h.dispatchDocumentMember)
	handle(http.MethodDelete, docMember, h.dispatchDocumentMember)
	handle(http.MethodPost, docMember, h.dispatchDocumentMember)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixTags), httpx.QueryParams(h.ListTags,
		"q", "source", "limit", "offset"))
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixGraph), httpx.QueryParams(h.Graph,
		"type", "limit", "q", "tags", "tags_match", "document_id", "chunk_id"))
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixEntities), httpx.QueryParams(h.ListEntities,
		"limit", "offset", "tags", "tags_match", "tag_groups"))
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixEntitiesGraph), httpx.QueryParams(h.EntityGraph,
		"limit", "min_count", "tags", "tags_match", "tag_groups"))
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixEntityMember), httpx.QueryParams(h.GetEntity,
		"tags", "tags_match", "tag_groups"))
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixEntityRegenerate), h.RegenerateEntity)
	handle(http.MethodGet, httpx.PrefixChunksV1+"{chunk_id...}", h.GetChunk)
}

// dispatchDocumentMember routes a request under a bank's document subtree to
// the handler that owns its trailing action word. The handler still reads the
// document identifier from the request's path values, which this sets.
func (h *Handlers) dispatchDocumentMember(w http.ResponseWriter, r *http.Request) {
	bankID := r.PathValue("bank_id")
	base := httpx.PrefixBanksV1 + "/" + bankID + httpx.SuffixDocumentMember
	remainder := strings.TrimPrefix(r.URL.Path, base)
	switch r.Method {
	case http.MethodGet:
		if trimmed, ok := strings.CutSuffix(remainder, "/chunks"); ok {
			httpx.DeclareQuery(r, "limit", "offset")
			r.SetPathValue("document_id", trimmed)
			h.ListDocumentChunks(w, r)
			return
		}
		r.SetPathValue("document_id", remainder)
		h.GetDocument(w, r)
	case http.MethodPost:
		trimmed, ok := strings.CutSuffix(remainder, "/reprocess")
		if !ok {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "not found"))
			return
		}
		r.SetPathValue("document_id", trimmed)
		h.ReprocessDocument(w, r)
	case http.MethodPatch:
		r.SetPathValue("document_id", remainder)
		h.UpdateDocument(w, r)
	case http.MethodDelete:
		r.SetPathValue("document_id", remainder)
		h.DeleteDocument(w, r)
	default:
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "not found"))
	}
}

// ListTags returns the bank's tags with usage counts.
func (h *Handlers) ListTags(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	limit, ok := intParam(w, r, "limit", defaultPageLimit, 0, -1)
	if !ok {
		return
	}
	offset, ok := intParam(w, r, "offset", 0, 0, -1)
	if !ok {
		return
	}
	counts, err := h.Store.ListTags(r.Context(), bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	items := make([]TagItem, 0, len(counts))
	pattern := strings.ToLower(strings.TrimSpace(httpx.Query(r, "q")))
	for _, entry := range counts {
		if pattern != "" && !tagMatchesPattern(entry.Tag, pattern) {
			continue
		}
		items = append(items, TagItem{Tag: entry.Tag, Count: entry.Count})
	}
	total := len(items)
	items = pageItems(items, offset, limit)
	httpx.WriteJSONFor(w, r, http.StatusOK, TagListResponse{
		Items: items, Total: total, Limit: limit, Offset: offset,
	})
}

// tagMatchesPattern reports whether a tag matches a lowercase wildcard pattern,
// where "*" matches any run of characters.
func tagMatchesPattern(tag, pattern string) bool {
	lower := strings.ToLower(tag)
	if !strings.Contains(pattern, "*") {
		return strings.Contains(lower, pattern)
	}
	parts := strings.Split(pattern, "*")
	position := 0
	for i, part := range parts {
		if part == "" {
			continue
		}
		index := strings.Index(lower[position:], part)
		if index < 0 {
			return false
		}
		if i == 0 && !strings.HasPrefix(pattern, "*") && index != 0 {
			return false
		}
		position += index + len(part)
	}
	if !strings.HasSuffix(pattern, "*") && !strings.HasSuffix(lower, parts[len(parts)-1]) {
		return false
	}
	return true
}

// pageItems applies offset and limit to a matched list.
func pageItems[T any](items []T, offset, limit int) []T {
	if offset >= len(items) {
		return []T{}
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}

// Graph returns the bank's memory graph.
func (h *Handlers) Graph(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	limit, ok := intParam(w, r, "limit", defaultGraphLimit, 0, -1)
	if !ok {
		return
	}
	tags := queryValues(r, "tags")
	tagsMatch := strings.TrimSpace(httpx.Query(r, "tags_match"))
	if tagsMatch == "" {
		tagsMatch = storage.TagsMatchAllStrict
	}
	// The caller's resolved read scope narrows the graph's unit set, so a
	// scoped reader only sees the memories it may read; the rows' source
	// documents are then checked the same way the document and chunk doors
	// check theirs.
	tagGroups, tags, tagsMatch := scopedTagFilter(r, nil, tags, tagsMatch)
	graph, err := h.Store.Graph(r.Context(), storage.GraphQuery{
		BankID:     bankID,
		Type:       strings.TrimSpace(httpx.Query(r, "type")),
		Query:      strings.TrimSpace(httpx.Query(r, "q")),
		Tags:       tags,
		TagsMatch:  tagsMatch,
		TagGroups:  tagGroups,
		DocumentID: strings.TrimSpace(httpx.Query(r, "document_id")),
		ChunkID:    strings.TrimSpace(httpx.Query(r, "chunk_id")),
		Limit:      limit,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	graph = h.scopeGraphData(r, bankID, graph)
	nodes := make([]GraphNode, 0, len(graph.Nodes))
	for _, node := range graph.Nodes {
		nodes = append(nodes, GraphNode{Data: GraphNodeData{
			ID: node.ID, Label: node.Text, Text: node.Text,
			Date: formatTimePtr(node.Date), Context: node.Context,
			Entities: strings.Join(node.Entities, ", "),
		}})
	}
	edges := make([]GraphEdge, 0, len(graph.Edges))
	for _, edge := range graph.Edges {
		edges = append(edges, GraphEdge{Data: GraphEdgeData{
			ID: edge.ID, Source: edge.From, Target: edge.To,
			LinkType: edge.Type, Weight: edge.Weight,
			EntityName: edge.Entity, LineStyle: edge.LineType,
		}})
	}
	rows := make([]GraphTableRow, 0, len(graph.TableRows))
	for _, row := range graph.TableRows {
		tags := row.Tags
		if tags == nil {
			tags = []string{}
		}
		rows = append(rows, GraphTableRow{
			ID: row.ID, Text: row.Text, Context: row.Context,
			OccurredStart: formatTimePtr(row.OccurredStart),
			OccurredEnd:   formatTimePtr(row.OccurredEnd),
			MentionedAt:   formatTimePtr(row.MentionedAt),
			Date:          formatTimePtr(row.Date),
			CreatedAt:     formatTimePtr(row.CreatedAt),
			Entities:      row.Entities, DocumentID: row.DocumentID,
			ChunkID: row.ChunkID, FactType: row.FactType, Tags: tags,
			ProofCount: row.ProofCount,
		})
	}
	httpx.WriteJSONOpenRow(w, r, http.StatusOK, GraphResponse{
		Nodes: nodes, Edges: edges, TableRows: rows,
		TotalUnits: graph.Total, Limit: graph.Limit,
	})
}

// scopeGraphData withholds the memory-graph rows the caller's resolved read
// scope does not admit. The store has already narrowed the units by their own
// tags; a graph row's text is derived from its source document, so — the same
// shape the document and chunk doors use — each row's document must itself be
// visible under the scope, or the row (and any node or edge that depends on it)
// is withheld. A row whose document cannot be resolved is withheld too, failing
// closed. A caller with no resolved scope reads the graph unchanged.
func (h *Handlers) scopeGraphData(r *http.Request, bankID string, graph storage.GraphData) storage.GraphData {
	scope := memoryops.ScopeFromRequest(r)
	if len(scope) == 0 {
		return graph
	}
	visible := map[string]bool{}
	checked := map[string]bool{}
	documentVisible := func(id string) bool {
		if checked[id] {
			return visible[id]
		}
		checked[id] = true
		if id == "" {
			return false
		}
		doc, err := h.Store.GetDocument(r.Context(), bankID, id)
		if err != nil {
			return false
		}
		visible[id] = storage.TagsVisibleUnderScope(doc.Tags, scope)
		return visible[id]
	}

	nodes := make([]storage.GraphNodeData, 0, len(graph.Nodes))
	for _, node := range graph.Nodes {
		if documentVisible(node.DocumentID) {
			nodes = append(nodes, node)
		}
	}
	kept := map[string]bool{}
	rows := make([]storage.GraphTableRow, 0, len(graph.TableRows))
	for _, row := range graph.TableRows {
		if !documentVisible(row.DocumentID) {
			continue
		}
		kept[row.ID] = true
		rows = append(rows, row)
	}
	edges := make([]storage.GraphEdgeData, 0, len(graph.Edges))
	for _, edge := range graph.Edges {
		if kept[edge.From] && kept[edge.To] {
			edges = append(edges, edge)
		}
	}
	graph.Nodes, graph.TableRows, graph.Edges = nodes, rows, edges
	// With rows withheld, the total reports what is actually visible so the
	// page and the total agree.
	graph.Total = len(rows)
	return graph
}

// ListEntities returns one page of the bank's entities.
func (h *Handlers) ListEntities(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	limit, ok := intParam(w, r, "limit", defaultPageLimit, 0, -1)
	if !ok {
		return
	}
	offset, ok := intParam(w, r, "offset", 0, 0, -1)
	if !ok {
		return
	}
	tags := queryValues(r, "tags")
	tagGroups, ok := tagGroupsQuery(w, r, tags)
	if !ok {
		return
	}
	tagsMatch := strings.TrimSpace(httpx.Query(r, "tags_match"))
	if tagsMatch == "" {
		tagsMatch = storage.TagsMatchAny
	}
	// The caller's resolved read scope narrows the listing: a scoped reader
	// sees only the entities of the memories it may read.
	tagGroups, tags, tagsMatch = scopedTagFilter(r, tagGroups, tags, tagsMatch)
	page, err := h.Store.ListEntities(r.Context(), storage.EntityQuery{
		BankID:    bankID,
		Tags:      tags,
		TagsMatch: tagsMatch,
		TagGroups: tagGroups,
		Limit:     limit, Offset: offset,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	items := make([]EntityListItem, 0, len(page.Entities))
	for _, entity := range page.Entities {
		items = append(items, toEntityListItem(entity))
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, EntityListResponse{
		Items: items, Total: page.Total, Limit: limit, Offset: offset,
	})
}

// GetEntity returns one entity's detail. A tag filter that no matching memory
// satisfies leaves the entity unknown to this read, so it answers not-found.
func (h *Handlers) GetEntity(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	entityID := strings.TrimSpace(r.PathValue("entity_id"))
	if entityID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "entity not found"))
		return
	}
	tags := queryValues(r, "tags")
	tagGroups, ok := tagGroupsQuery(w, r, tags)
	if !ok {
		return
	}
	tagsMatch := strings.TrimSpace(httpx.Query(r, "tags_match"))
	if tagsMatch == "" {
		tagsMatch = storage.TagsMatchAny
	}
	// A by-id read still honours the caller's resolved scope, so an entity only
	// another scope names answers not-found rather than leaking its existence.
	tagGroups, tags, tagsMatch = scopedTagFilter(r, tagGroups, tags, tagsMatch)
	page, err := h.Store.ListEntities(r.Context(), storage.EntityQuery{
		BankID: bankID, ID: entityID,
		Tags: tags, TagsMatch: tagsMatch, TagGroups: tagGroups,
		Limit: 1,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	if len(page.Entities) == 0 {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "Entity "+entityID+" not found"))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, h.entityDetail(r.Context(), page.Entities[0], bankID))
}

// RegenerateEntity is the retired observation-regeneration route: the reference
// deprecated it in favour of mental models and answers every call with a
// permanent gone, so a caller is told to stop rather than silently served.
func (h *Handlers) RegenerateEntity(w http.ResponseWriter, r *http.Request) {
	httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindGone,
		"This endpoint is deprecated. Entity observations are no longer supported."))
}

// EntityGraph returns the bank's entity graph.
func (h *Handlers) EntityGraph(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	limit, ok := intParam(w, r, "limit", defaultGraphLimit, 0, -1)
	if !ok {
		return
	}
	minCount, ok := freeIntParam(w, r, "min_count", 1)
	if !ok {
		return
	}
	tags := queryValues(r, "tags")
	tagGroups, ok := tagGroupsQuery(w, r, tags)
	if !ok {
		return
	}
	tagsMatch := strings.TrimSpace(httpx.Query(r, "tags_match"))
	if tagsMatch == "" {
		tagsMatch = storage.TagsMatchAny
	}
	tagGroups, tags, tagsMatch = scopedTagFilter(r, tagGroups, tags, tagsMatch)
	graph, err := h.Store.EntityGraph(r.Context(), storage.EntityGraphQuery{
		BankID: bankID, Limit: limit, MinCount: minCount,
		Tags: tags, TagsMatch: tagsMatch, TagGroups: tagGroups,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	nodes := make([]EntityGraphNode, 0, len(graph.Nodes))
	for _, node := range graph.Nodes {
		// A frequently mentioned entity is drawn in the darker colour, which is
		// the reference's own threshold and palette.
		color := "#90caf9"
		if node.MentionCount > 1 {
			color = "#42a5f5"
		}
		nodes = append(nodes, EntityGraphNode{Data: EntityGraphNodeData{
			ID: node.ID, Label: node.Label, MentionCount: node.MentionCount, Color: color,
		}})
	}
	edges := make([]EntityGraphEdge, 0, len(graph.Edges))
	for _, edge := range graph.Edges {
		// The relation's presentation is fixed by the reference: a cooccurrence
		// relation drawn as a solid gold line, with an id derived from its
		// endpoints.
		edges = append(edges, EntityGraphEdge{Data: EntityGraphEdgeData{
			ID:        edge.Source + "-" + edge.Target,
			Source:    edge.Source,
			Target:    edge.Target,
			Weight:    edge.Weight,
			Color:     "#ffd700",
			LinkType:  "cooccurrence",
			LineStyle: "solid",
		}})
	}
	httpx.WriteJSONOpenRow(w, r, http.StatusOK, EntityGraphResponse{
		Nodes: nodes, Edges: edges, TotalEntities: graph.TotalEntities,
		TotalEdges: graph.TotalEdges, Limit: graph.Limit,
	})
}

// entityDetail assembles an entity's detail, including the observations that
// mention it.
func (h *Handlers) entityDetail(ctx context.Context, entity storage.Entity, bankID string) EntityDetailResponse {
	observations := make([]EntityObservation, 0)
	mentions, err := h.Store.EntityMentions(ctx, bankID, entity.ID, 100)
	if err != nil {
		return EntityDetailResponse{ID: entity.ID, CanonicalName: entity.CanonicalName, MentionCount: entity.MentionCount}
	}
	for _, mention := range mentions {
		observations = append(observations, EntityObservation{Text: mention.Text, MentionedAt: formatTimePtr(mention.MentionedAt)})
	}
	return EntityDetailResponse{
		ID:            entity.ID,
		CanonicalName: entity.CanonicalName,
		MentionCount:  entity.MentionCount,
		FirstSeen:     formatTimePtr(entity.FirstSeen),
		LastSeen:      formatTimePtr(entity.LastSeen),
		Metadata:      entity.Metadata,
		Observations:  observations,
	}
}

// requireBank reports not-found for an unknown bank.
func (h *Handlers) requireBank(w http.ResponseWriter, r *http.Request) (string, bool) {
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return "", false
	}
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return "", false
	}
	return bankID, true
}

// documentMember resolves the bank and the document identifier.
func (h *Handlers) documentMember(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return "", "", false
	}
	docID := strings.TrimSpace(r.PathValue("document_id"))
	if docID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "document not found"))
		return "", "", false
	}
	return bankID, docID, true
}

// queryValues collects a repeatable query parameter.
func queryValues(r *http.Request, name string) []string {
	values := httpx.QueryValues(r, name)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// parseDateParam reads an optional RFC 3339 query parameter. It writes a
// bad-request error and reports false for a malformed value.
func parseDateParam(w http.ResponseWriter, r *http.Request, key string) (*time.Time, bool) {
	raw := strings.TrimSpace(httpx.Query(r, key))
	if raw == "" {
		return nil, true
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, key+" must be an RFC 3339 timestamp"))
		return nil, false
	}
	utc := parsed.UTC()
	return &utc, true
}

// asHTTPError maps a storage error to the shared envelope.
func asHTTPError(err error) *httpx.HTTPError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrBankNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank)
	case errors.Is(err, storage.ErrDocumentNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "document not found")
	case errors.Is(err, storage.ErrChunkNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "chunk not found")
	case errors.Is(err, storage.ErrEntityNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "entity not found")
	default:
		return httpx.NewHTTPError(httpx.KindBadRequest, err.Error())
	}
}
