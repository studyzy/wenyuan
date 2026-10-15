package reporting

import (
	"context"
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
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
		parent := node.ParentID
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
		parentOf[node.ID] = node.ParentID
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
// description, tags and trigger come from its backing model when one is given.
func projectKnowledgeNode(node storage.KnowledgeNode, model *storage.MentalModel) KnowledgeNode {
	out := KnowledgeNode{
		ID: node.ID, Kind: string(node.Kind), Name: node.Title,
		ParentID: node.ParentID, MentalModelID: node.MentalModelID,
		Managed: node.Managed, Tags: []string{}, Children: []KnowledgeNode{},
	}
	if node.Kind != knowledgeKindPage {
		out.Timestamp = formatTime(node.UpdatedAt)
		return out
	}
	if model != nil {
		out.Description = model.SourceQuery
		out.Trigger = model.Trigger
		out.Tags = normalizeKnowledgeTags(model.Tags)
		out.Timestamp = formatTime(model.LastRefreshedAt)
	}
	if out.Timestamp == "" {
		out.Timestamp = formatTime(node.CreatedAt)
	}
	return out
}

// pageTags returns the tags a page is scoped by, falling back to any tags on the
// node when its model is not in the map.
func pageTags(node storage.KnowledgeNode, models map[string]storage.MentalModel) []string {
	if model, ok := models[node.MentalModelID]; ok {
		return model.Tags
	}
	return node.Tags
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
	timestamp := formatTime(node.CreatedAt)
	if !model.LastRefreshedAt.IsZero() {
		timestamp = formatTime(model.LastRefreshedAt)
	}
	return knowledgeDocumentFields(node.ID, pageType.Type, node.Title, model.SourceQuery, timestamp, pageType.DisplayTags)
}

// writeKnowledgePage renders a page as a markdown document, adding the
// empty-page notice to `markdown` when the body is empty.
func writeKnowledgePage(w http.ResponseWriter, r *http.Request, node storage.KnowledgeNode, pageType knowledgePageTypeInfo, description, timestamp string, body *string) {
	bodyText := ""
	if body != nil {
		bodyText = *body
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, KnowledgePageResponse{
		ID: node.ID, Name: node.Title, Type: pageType.Type,
		Description: description, Tags: pageType.DisplayTags, Timestamp: timestamp,
		Body:     body,
		Markdown: renderKnowledgeDocument(knowledgeDocumentFields(node.ID, pageType.Type, node.Title, description, timestamp, pageType.DisplayTags), bodyText, true),
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

// firstStringPtr returns the first non-nil pointer, or nil when both are nil.
func firstStringPtr(values ...*string) *string {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
