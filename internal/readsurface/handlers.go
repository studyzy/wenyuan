package readsurface

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"sort"
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
	// AutoConsolidationEnabled is the deployment's enable_auto_consolidation
	// setting. A document retag that invalidates observations re-consolidates
	// only when it is on (a bank's own override wins), the same gate the
	// reference applies; the composition root supplies the deployment value.
	AutoConsolidationEnabled bool
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

// ListTags returns the bank's tags with usage counts. `source` selects the
// table the histogram is read from: memory_units by default, or mental_models.
func (h *Handlers) ListTags(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	if !literalParam(w, r, "source", []string{"memories", "mental_models"}) {
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
	var (
		counts []storage.TagCount
		err    error
	)
	if strings.TrimSpace(httpx.Query(r, "source")) == "mental_models" {
		counts, err = h.mentalModelTagCounts(r.Context(), bankID)
	} else {
		counts, err = h.Store.ListTags(r.Context(), bankID)
	}
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	items := make([]TagItem, 0, len(counts))
	if pattern := httpx.Query(r, "q"); pattern != "" {
		matcher, err := compileTagPattern(pattern)
		if err != nil {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "Invalid pattern"))
			return
		}
		for _, entry := range counts {
			if !matcher.MatchString(entry.Tag) {
				continue
			}
			items = append(items, TagItem{Tag: entry.Tag, Count: entry.Count})
		}
	} else {
		for _, entry := range counts {
			items = append(items, TagItem{Tag: entry.Tag, Count: entry.Count})
		}
	}
	total := len(items)
	items = pageItems(items, offset, limit)
	httpx.WriteJSON(w, http.StatusOK, TagListResponse{
		Items: items, Total: total, Limit: limit, Offset: offset,
	})
}

// mentalModelTagCounts builds the tag histogram of a bank's mental models, most
// used first, with the same ordering the memory histogram uses.
func (h *Handlers) mentalModelTagCounts(ctx context.Context, bankID string) ([]storage.TagCount, error) {
	models, err := h.Store.ListMentalModels(ctx, bankID, "")
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, model := range models {
		for _, tag := range model.Tags {
			counts[tag]++
		}
	}
	out := make([]storage.TagCount, 0, len(counts))
	for tag, count := range counts {
		out = append(out, storage.TagCount{Tag: tag, Count: count})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Tag < out[j].Tag
	})
	return out, nil
}

// compileTagPattern turns a caller's tag pattern into the matcher SQL ILIKE
// applies: '*' is the documented wildcard and an alias for '%', a '%' matches
// any run of characters, an '_' any single character, and everything else is
// literal. The match is anchored and case-insensitive.
func compileTagPattern(pattern string) (*regexp.Regexp, error) {
	var builder strings.Builder
	builder.WriteString("(?is)^")
	for _, r := range pattern {
		switch r {
		case '*', '%':
			builder.WriteString(".*")
		case '_':
			builder.WriteString(".")
		default:
			builder.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	builder.WriteString("$")
	return regexp.Compile(builder.String())
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
		entities := "None"
		if len(node.Entities) > 0 {
			entities = strings.Join(node.Entities, ", ")
		}
		nodes = append(nodes, GraphNode{Data: GraphNodeData{
			ID: node.ID, Label: graphLabel(node.Text), Text: node.Text,
			Date: graphNodeDate(node.Date), Context: node.Context,
			Entities: entities, Color: graphNodeColor(len(node.Entities)),
		}})
	}
	edges := make([]GraphEdge, 0, len(graph.Edges))
	for _, edge := range graph.Edges {
		color, lineStyle := graphEdgeStyle(edge.Type)
		edges = append(edges, GraphEdge{Data: GraphEdgeData{
			ID: edge.From + "-" + edge.To + "-" + edge.Type, Source: edge.From, Target: edge.To,
			LinkType: edge.Type, Weight: edge.Weight, EntityName: edge.Entity,
			Color: color, LineStyle: lineStyle,
		}})
	}
	rows := make([]GraphTableRow, 0, len(graph.TableRows))
	for _, row := range graph.TableRows {
		tags := row.Tags
		if tags == nil {
			tags = []string{}
		}
		entities := "None"
		if row.Entities != "" {
			entities = row.Entities
		}
		rows = append(rows, GraphTableRow{
			ID: row.ID, Text: row.Text, Context: graphRowContext(row.Context),
			OccurredStart: httpx.FormatTimestampPtr(row.OccurredStart),
			OccurredEnd:   httpx.FormatTimestampPtr(row.OccurredEnd),
			MentionedAt:   httpx.FormatTimestampPtr(row.MentionedAt),
			Date:          graphRowDate(row.Date),
			Entities:      entities,
			DocumentID:    optionalString(row.DocumentID),
			ChunkID:       optionalString(row.ChunkID),
			FactType:      optionalString(row.FactType),
			Tags:          tags,
			CreatedAt:     httpx.FormatTimestampPtr(row.CreatedAt),
			ProofCount:    graphProofCount(row.ProofCount),
		})
	}
	httpx.WriteJSON(w, http.StatusOK, GraphResponse{
		Nodes: nodes, Edges: edges, TableRows: rows,
		TotalUnits: graph.Total, Limit: graph.Limit,
	})
}

// graphLabel is a node's display label: the text, truncated to 30 characters
// with an ellipsis when it is longer.
func graphLabel(text string) string {
	runes := []rune(text)
	if len(runes) > 30 {
		return string(runes[:30]) + "..."
	}
	return text
}

// graphNodeDate renders a node's event date, empty when unknown.
func graphNodeDate(date *time.Time) string {
	if date == nil {
		return ""
	}
	return httpx.FormatTimestamp(*date)
}

// graphNodeColor colours a node by how many entities it mentions.
func graphNodeColor(entityCount int) string {
	switch entityCount {
	case 0:
		return "#e0e0e0"
	case 1:
		return "#90caf9"
	default:
		return "#42a5f5"
	}
}

// graphEdgeStyle returns the colour and line style a link type is drawn with.
func graphEdgeStyle(linkType string) (string, string) {
	switch linkType {
	case "temporal":
		return "#00bcd4", "dashed"
	case "semantic":
		return "#ff69b4", "solid"
	case "entity":
		return "#ffd700", "solid"
	default:
		return "#999999", "solid"
	}
}

// graphRowContext renders a table row's context, "N/A" when absent.
func graphRowContext(context string) string {
	if context == "" {
		return "N/A"
	}
	return context
}

// graphRowDate renders a table row's event date in the deprecated
// "%Y-%m-%d %H:%M" form, "N/A" when unknown.
func graphRowDate(date *time.Time) string {
	if date == nil {
		return "N/A"
	}
	return date.UTC().Format("2006-01-02 15:04")
}

// graphProofCount renders a row's evidence count, null when it is not a real
// positive count.
func graphProofCount(count int) *int {
	if count <= 0 {
		return nil
	}
	return &count
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
	// The total stays the store's scoped count of matching units, even for rows
	// the document-visibility gate then withholds: the reference reports the
	// count of units matching the filter and tag scope, not the page size.
	return graph
}

// ListEntities returns one page of the bank's entities.
func (h *Handlers) ListEntities(w http.ResponseWriter, r *http.Request) {
	tagsMatch := strings.TrimSpace(httpx.Query(r, "tags_match"))
	if tagsMatch != "" && !literalParam(w, r, "tags_match", storage.RecallTagMatchModes()) {
		return
	}
	if tagsMatch == "" {
		tagsMatch = storage.TagsMatchAny
	}
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
	httpx.WriteJSON(w, http.StatusOK, EntityListResponse{
		Items: items, Total: page.Total, Limit: limit, Offset: offset,
	})
}

// GetEntity returns one entity's detail. The identifier is parsed as a UUID
// before the bank is consulted, and a tag filter that no matching memory
// satisfies leaves the entity unknown to this read, so it answers not-found.
// The bank is not pre-checked: an unknown bank answers the entity's own
// not-found, the same detail an absent entity gets.
func (h *Handlers) GetEntity(w http.ResponseWriter, r *http.Request) {
	tagsMatch := strings.TrimSpace(httpx.Query(r, "tags_match"))
	if tagsMatch != "" && !literalParam(w, r, "tags_match", storage.RecallTagMatchModes()) {
		return
	}
	if tagsMatch == "" {
		tagsMatch = storage.TagsMatchAny
	}
	entityID := strings.TrimSpace(r.PathValue("entity_id"))
	if !validUUIDShape(entityID) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			"Invalid entity_id: '"+entityID+"' is not a valid UUID"))
		return
	}
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	tags := queryValues(r, "tags")
	tagGroups, ok := tagGroupsQuery(w, r, tags)
	if !ok {
		return
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
	httpx.WriteJSON(w, http.StatusOK, h.entityDetail(r.Context(), page.Entities[0], bankID))
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
	tagsMatch := strings.TrimSpace(httpx.Query(r, "tags_match"))
	if tagsMatch != "" && !literalParam(w, r, "tags_match", storage.RecallTagMatchModes()) {
		return
	}
	if tagsMatch == "" {
		tagsMatch = storage.TagsMatchAny
	}
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
			ID:             edge.Source + "-" + edge.Target,
			Source:         edge.Source,
			Target:         edge.Target,
			Weight:         edge.Weight,
			Color:          "#ffd700",
			LinkType:       "cooccurrence",
			LineStyle:      "solid",
			LastCooccurred: httpx.FormatTimestampPtr(edge.LastCooccurred),
		}})
	}
	httpx.WriteJSON(w, http.StatusOK, EntityGraphResponse{
		Nodes: nodes, Edges: edges, TotalEntities: graph.TotalEntities,
		TotalEdges: graph.TotalEdges, Limit: graph.Limit,
	})
}

// entityDetail assembles an entity's detail. Observations are always empty:
// they were retired in favour of mental models, and the reference's own store
// hardcodes the empty list on both backends. Metadata is always present, an
// empty object when the entity carries none.
func (h *Handlers) entityDetail(_ context.Context, entity storage.Entity, _ string) EntityDetailResponse {
	metadata := entity.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	return EntityDetailResponse{
		ID:            entity.ID,
		CanonicalName: entity.CanonicalName,
		MentionCount:  entity.MentionCount,
		FirstSeen:     httpx.FormatTimestampPtr(entity.FirstSeen),
		LastSeen:      httpx.FormatTimestampPtr(entity.LastSeen),
		Metadata:      metadata,
		Observations:  []EntityObservation{},
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

// documentMember resolves the bank and the document identifier. It does not
// check that the bank exists: a document member route answers with the
// document's own not-found whether the document is absent or its bank is, so a
// caller cannot tell the two apart.
func (h *Handlers) documentMember(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	// The reference captures the document id as the raw `{document_id:path}`
	// remainder and hands it to the store verbatim, so whitespace around an id
	// is part of the id rather than something to strip.
	docID := r.PathValue("document_id")
	if docID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "Document not found"))
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

// parseDateParam reads an optional ISO-8601 query parameter. It writes a
// bad-request error and reports false for a malformed value. An offset is
// optional — a value without one is read as UTC, the same reading the reference
// gives it — and a trailing Z is accepted.
func parseDateParam(w http.ResponseWriter, r *http.Request, key string) (*time.Time, bool) {
	raw := strings.TrimSpace(httpx.Query(r, key))
	if raw == "" {
		return nil, true
	}
	parsed, err := parseISODateTime(raw)
	if err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			"Invalid "+key+": '"+raw+"' is not an ISO-8601 datetime."))
		return nil, false
	}
	return &parsed, true
}

// isoDateLayouts are the offsets-free forms a caller may send; a value without
// an offset is read as UTC.
var isoDateLayouts = []string{
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// parseISODateTime parses an ISO-8601 timestamp, accepting an explicit offset,
// a trailing Z, or no offset at all (read as UTC).
func parseISODateTime(raw string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed.UTC(), nil
	}
	for _, layout := range isoDateLayouts {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, errors.New("not an ISO-8601 datetime")
}

// asHTTPError maps a storage error to the shared envelope.
func asHTTPError(err error) *httpx.HTTPError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrBankNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank)
	case errors.Is(err, storage.ErrDocumentNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "Document not found")
	case errors.Is(err, storage.ErrChunkNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "Chunk not found")
	case errors.Is(err, storage.ErrEntityNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "entity not found")
	case errors.Is(err, storage.ErrWritesUnavailable):
		return httpx.NewHTTPError(httpx.KindUnavailable, httpx.DetailWritesDown)
	default:
		// Anything the storage layer did not recognise is a server fault, not a
		// malformed request: the reference's catch-all answers 500 and reports the
		// error it caught.
		return httpx.NewHTTPError(httpx.KindInternal, err.Error())
	}
}
