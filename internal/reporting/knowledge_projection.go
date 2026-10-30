package reporting

import (
	"context"
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/mentalmodels"
	"github.com/studyzy/wenyuan/internal/storage"
)

// nestKnowledge arranges a flat node list into a tree. A node whose parent is
// not in the list becomes a root, so the caller still sees it rather than
// losing it to a dangling reference.
func nestKnowledge(nodes []KnowledgeNode) []KnowledgeNode {
	present := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		present[node.ID] = true
	}
	byParent := map[string][]KnowledgeNode{}
	for _, node := range nodes {
		parent := derefString(node.ParentID)
		if parent != "" && !present[parent] {
			parent = ""
		}
		byParent[parent] = append(byParent[parent], node)
	}
	var build func(parentID string) []KnowledgeNode
	build = func(parentID string) []KnowledgeNode {
		children := byParent[parentID]
		out := make([]KnowledgeNode, 0, len(children))
		for _, child := range children {
			child.Children = build(child.ID)
			out = append(out, child)
		}
		return out
	}
	return build("")
}

// pruneKnowledgeNodes keeps the pages a tag filter matches and the folders above
// them; a folder the filter leaves empty is dropped, since its name can say as
// much as the pages under it.
func pruneKnowledgeNodes(nodes []KnowledgeNode, filter knowledgeTagFilter) []KnowledgeNode {
	if !filter.active() {
		return nodes
	}
	parentOf := make(map[string]string, len(nodes))
	for _, node := range nodes {
		parentOf[node.ID] = derefString(node.ParentID)
	}
	keep := map[string]bool{}
	for _, node := range nodes {
		if node.Kind != string(knowledgeKindPage) || !filter.matches(node.Tags) {
			continue
		}
		for id := node.ID; id != "" && !keep[id]; id = parentOf[id] {
			keep[id] = true
		}
	}
	out := make([]KnowledgeNode, 0, len(keep))
	for _, node := range nodes {
		if keep[node.ID] {
			out = append(out, node)
		}
	}
	return out
}

// pruneKnowledgeNodesToScope keeps the knowledge nodes a caller confined to
// `scope` may see: a page whose tags satisfy the scope, every folder on the
// path to one, and every folder the scope leaves empty. A folder holding only
// out-of-scope pages is someone else's and is dropped; an empty folder is no
// one's yet, so its creator still sees it to put pages in it. The caller's own
// tag filter and its resolved scope are applied as separate passes, as the
// reference does: the filter drops a folder it leaves empty, the scope does
// not. An empty scope leaves every node visible.
func pruneKnowledgeNodesToScope(nodes []KnowledgeNode, scope []storage.TagGroup) []KnowledgeNode {
	if len(scope) == 0 {
		return nodes
	}
	byID := make(map[string]KnowledgeNode, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}
	holdsPages := map[string]bool{}
	holdsVisible := map[string]bool{}
	for _, node := range nodes {
		if node.Kind != string(knowledgeKindPage) {
			continue
		}
		visible := storage.TagsVisibleUnderScope(node.Tags, scope)
		// Mark the page and every ancestor; `seen` guards a corrupted, cyclic tree.
		seen := map[string]bool{}
		for id := node.ID; id != "" && !seen[id]; {
			seen[id] = true
			holdsPages[id] = true
			if visible {
				holdsVisible[id] = true
			}
			next, ok := byID[id]
			if !ok {
				break
			}
			id = derefString(next.ParentID)
		}
	}
	keep := map[string]bool{}
	for _, node := range nodes {
		if holdsVisible[node.ID] || !holdsPages[node.ID] {
			keep[node.ID] = true
		}
	}
	// Every kept node keeps its ancestors, so the tree the caller builds stays
	// connected even when a parent was otherwise dropped.
	for _, node := range nodes {
		if !keep[node.ID] {
			continue
		}
		for id := derefString(node.ParentID); id != "" && !keep[id]; {
			ancestor, ok := byID[id]
			if !ok {
				break
			}
			keep[id] = true
			id = derefString(ancestor.ParentID)
		}
	}
	out := make([]KnowledgeNode, 0, len(keep))
	for _, node := range nodes {
		if keep[node.ID] {
			out = append(out, node)
		}
	}
	return out
}

// projectKnowledgeNodes renders every stored node for the wire.
func projectKnowledgeNodes(nodes []storage.KnowledgeNode, models map[string]storage.MentalModel) []KnowledgeNode {
	out := make([]KnowledgeNode, 0, len(nodes))
	for _, node := range nodes {
		model, ok := models[node.MentalModelID]
		out = append(out, projectKnowledgeNode(node, modelPtr(model, ok)))
	}
	return out
}

// projectKnowledgeNode renders a stored node for the wire. A page's
// description, tags, trigger and last refresh outcome come from its backing
// model when one is given; every member is always emitted, so a folder reports
// null for the page-only ones. Staleness is left null here: only the tree
// computes it.
func projectKnowledgeNode(node storage.KnowledgeNode, model *storage.MentalModel) KnowledgeNode {
	parentID := optionalString(node.ParentID)
	out := KnowledgeNode{
		ID: node.ID, Kind: string(node.Kind), Name: node.Title,
		ParentID: parentID, Managed: node.Managed,
		Tags: []string{}, Children: []KnowledgeNode{},
	}
	if node.Kind != knowledgeKindPage {
		out.Timestamp = httpx.FormatTimestampPtr(&node.UpdatedAt)
		return out
	}
	out.MentalModelID = optionalString(node.MentalModelID)
	if model != nil {
		out.Description = &model.SourceQuery
		out.Tags = normalizeKnowledgeTags(model.Tags)
		out.Timestamp = httpx.FormatTimestampPtr(&model.LastRefreshedAt)
		out.LastRefreshFailedAt = httpx.FormatTimestampPtr(model.LastRefreshFailedAt)
		out.Trigger = mentalmodels.NormalizeTrigger(model.Trigger)
	}
	if out.Timestamp == nil {
		out.Timestamp = httpx.FormatTimestampPtr(&node.CreatedAt)
	}
	return out
}

// optionalString renders a stored identifier as the wire's nullable string: an
// empty column value is the reference's null, not an empty string.
func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// knowledgeModels returns the bank's mental models keyed by identifier.
func (h *Handlers) knowledgeModels(ctx context.Context, bankID string) (map[string]storage.MentalModel, error) {
	models, err := h.Store.ListMentalModels(ctx, bankID, "")
	if err != nil {
		return nil, err
	}
	out := make(map[string]storage.MentalModel, len(models))
	for _, model := range models {
		out[model.ID] = model
	}
	return out, nil
}

// knowledgeModel returns the mental model backing a page, and whether it exists.
func (h *Handlers) knowledgeModel(ctx context.Context, bankID string, node storage.KnowledgeNode) (storage.MentalModel, bool) {
	if node.Kind != knowledgeKindPage || node.MentalModelID == "" {
		return storage.MentalModel{}, false
	}
	model, err := h.Store.GetMentalModel(ctx, bankID, node.MentalModelID)
	if err != nil {
		return storage.MentalModel{}, false
	}
	return model, true
}

// knowledgeDocFields builds the ordered frontmatter fields of a page document.
func knowledgeDocFields(node storage.KnowledgeNode, model storage.MentalModel) []knowledgeField {
	pageType := knowledgePageType(node.Tags)
	timestamp := httpx.FormatTimestamp(node.CreatedAt)
	if !model.LastRefreshedAt.IsZero() {
		timestamp = httpx.FormatTimestamp(model.LastRefreshedAt)
	}
	// The description is the mental model's source_query, which the store hands
	// back as a string: absent and empty are the same thing at this layer. The
	// reference renders the key for both (its mapping carries "" rather than
	// None), and the schema records no separate "unset" state to distinguish.
	description := knowledgeOptional{text: model.SourceQuery, known: true}
	return knowledgeDocumentFields(node.ID, pageType.Type, node.Title, description, timestamp, pageType.DisplayTags)
}

// KnowledgePageDocument is one page rendered as a markdown document, without the
// HTTP route's extra `body` member: id, name, type, description, tags,
// timestamp and the rendered markdown. It is the shape the reference's MCP
// `get_knowledge_page` tool returns (`_do_get_knowledge_page`), and the HTTP read
// route is the same document plus the raw body.
type KnowledgePageDocument struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description *string  `json:"description"`
	Tags        []string `json:"tags"`
	Timestamp   *string  `json:"timestamp"`
	Markdown    string   `json:"markdown"`
}

// RenderKnowledgePage builds the document for one page: the description and
// timestamp from the model backing it, the ordered frontmatter fields the
// reference's renderer emits, and the empty-page notice when the body is empty.
func RenderKnowledgePage(node storage.KnowledgeNode, model storage.MentalModel, hasModel bool, body *string) KnowledgePageDocument {
	pageType := knowledgePageType(node.Tags)
	var description *string
	timestamp := httpx.FormatTimestampPtr(&node.CreatedAt)
	if hasModel {
		description = &model.SourceQuery
		if !model.LastRefreshedAt.IsZero() {
			timestamp = httpx.FormatTimestampPtr(&model.LastRefreshedAt)
		}
	}
	bodyText := ""
	if body != nil {
		bodyText = *body
	}
	return KnowledgePageDocument{
		ID: node.ID, Name: node.Title, Type: pageType.Type,
		Description: description, Tags: pageType.DisplayTags, Timestamp: timestamp,
		Markdown: renderKnowledgeDocument(
			knowledgeDocumentFields(node.ID, pageType.Type, node.Title,
				knowledgeOptionalString(description), derefString(timestamp), pageType.DisplayTags),
			bodyText, true),
	}
}

// writeKnowledgePage renders a page as a markdown document, adding the
// empty-page notice to `markdown` when the body is empty. The frontmatter is
// rendered from the non-null spelling of each nullable member, because the
// reference drops a null key from the block while keeping it on the wire.
func writeKnowledgePage(w http.ResponseWriter, r *http.Request, node storage.KnowledgeNode, pageType knowledgePageTypeInfo, description, timestamp *string, body *string) {
	bodyText := ""
	if body != nil {
		bodyText = *body
	}
	httpx.WriteJSON(w, http.StatusOK, KnowledgePageResponse{
		ID: node.ID, Name: node.Title, Type: pageType.Type,
		Description: description, Tags: pageType.DisplayTags, Timestamp: timestamp,
		Body:     body,
		Markdown: renderKnowledgeDocument(knowledgeDocumentFields(node.ID, pageType.Type, node.Title, knowledgeOptionalString(description), derefString(timestamp), pageType.DisplayTags), bodyText, true),
	})
}

// subtreeKnowledgeModels returns the backing mental model ids of the given node
// and every node beneath it.
func (h *Handlers) subtreeKnowledgeModels(ctx context.Context, bankID, nodeID string) []string {
	nodes, err := h.Store.ListKnowledgeNodes(ctx, bankID)
	if err != nil {
		return nil
	}
	children := map[string][]string{}
	modelOf := map[string]string{}
	for _, node := range nodes {
		children[node.ParentID] = append(children[node.ParentID], node.ID)
		if node.MentalModelID != "" {
			modelOf[node.ID] = node.MentalModelID
		}
	}
	models := make([]string, 0)
	queue := []string{nodeID}
	seen := map[string]bool{}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		if modelID := modelOf[id]; modelID != "" {
			models = append(models, modelID)
		}
		queue = append(queue, children[id]...)
	}
	return models
}

// normalizeKnowledgeTags returns a non-nil tag list.
func normalizeKnowledgeTags(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return tags
}

// modelPtr returns a pointer to the model when present.
func modelPtr(model storage.MentalModel, ok bool) *storage.MentalModel {
	if !ok {
		return nil
	}
	return &model
}
