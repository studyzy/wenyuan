package reporting

import "encoding/json"

// KnowledgeNode is one node of the bank's knowledge tree: a folder grouping
// pages, or a page carrying text. The field set mirrors the reference's node
// model — a page's description/tags/trigger come from its backing mental model.
// Every member is always on the wire: the reference's response model carries
// them all, so a folder reports null for the page-only ones rather than
// dropping the key.
type KnowledgeNode struct {
	ID                  string          `json:"id"`
	Kind                string          `json:"kind"`
	Name                string          `json:"name"`
	ParentID            *string         `json:"parent_id"`
	MentalModelID       *string         `json:"mental_model_id"`
	Managed             bool            `json:"managed"`
	Description         *string         `json:"description"`
	Tags                []string        `json:"tags"`
	Timestamp           *string         `json:"timestamp"`
	IsStale             *bool           `json:"is_stale"`
	LastRefreshFailedAt *string         `json:"last_refresh_failed_at"`
	Trigger             map[string]any  `json:"trigger"`
	Children            []KnowledgeNode `json:"children"`
}

// KnowledgeTreeResponse is the knowledge tree envelope.
type KnowledgeTreeResponse struct {
	Roots []KnowledgeNode `json:"roots"`
}

// CreateKnowledgeNodeRequest is the create body for a folder or a page. It
// declares exactly the reference's CreateFolderRequest/CreatePageRequest
// members: name, parent_id, and — for a page — source_query, tags, max_tokens
// and trigger. Any other key a client sends is ignored, never honoured, so a
// `content`/`title` spelling from an older client has no effect.
type CreateKnowledgeNodeRequest struct {
	Name        *string         `json:"name"`
	ParentID    json.RawMessage `json:"parent_id"`
	SourceQuery *string         `json:"source_query"`
	Tags        *[]string       `json:"tags"`
	MaxTokens   *int            `json:"max_tokens"`
	Trigger     map[string]any  `json:"trigger"`
}

// CreateKnowledgePageResponse is the page-create result: the page is generated
// in the background, so the caller polls the operation. The backing mental
// model id is always present, because every page has one.
type CreateKnowledgePageResponse struct {
	PageID        string `json:"page_id"`
	MentalModelID string `json:"mental_model_id"`
	OperationID   string `json:"operation_id"`
}

// KnowledgePageResponse is one page rendered as a markdown document. Every
// member is always on the wire; `body` is null only when the backing model has
// no content column value at all, while the stored-but-empty body renders as
// the empty-page notice in `markdown` alone.
type KnowledgePageResponse struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description *string  `json:"description"`
	Tags        []string `json:"tags"`
	Timestamp   *string  `json:"timestamp"`
	Body        *string  `json:"body"`
	Markdown    string   `json:"markdown"`
}

// KnowledgeFile is one file of an exported knowledge bundle.
type KnowledgeFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// KnowledgePageBundleResponse is an exported bundle of pages.
type KnowledgePageBundleResponse struct {
	Files []KnowledgeFile `json:"files"`
}

// KnowledgeSearchResult is one search result. Every member is always on the
// wire; the three optional ones carry null rather than being dropped.
type KnowledgeSearchResult struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	MentalModelID *string `json:"mental_model_id"`
	SourceQuery   *string `json:"source_query"`
	Snippet       string  `json:"snippet"`
	Score         float64 `json:"score"`
	UpdatedAt     *string `json:"updated_at"`
}

// KnowledgePageSearchResponse is the search envelope.
type KnowledgePageSearchResponse struct {
	Results []KnowledgeSearchResult `json:"results"`
	Total   int                     `json:"total"`
}

// UpdateKnowledgeNodeRequest is the partial-update body, declaring exactly the
// reference's UpdateNodeRequest members. `name` and `parent_id` rename and move
// the node; `source_query`, `tags`, `max_tokens` and `trigger` update a page's
// options on its backing mental model. `parent_id` is raw because a presence
// test decides whether the node moves at all: an explicit null is the tree
// root, an absent key leaves the parent alone.
type UpdateKnowledgeNodeRequest struct {
	Name        *string         `json:"name"`
	ParentID    json.RawMessage `json:"parent_id"`
	SourceQuery *string         `json:"source_query"`
	Tags        *[]string       `json:"tags"`
	MaxTokens   *int            `json:"max_tokens"`
	Trigger     json.RawMessage `json:"trigger"`
}
