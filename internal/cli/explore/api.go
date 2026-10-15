package explore

// The TUI's data layer: typed fetch functions over the shared CLI client.
// Every method is synchronous; the query view runs recall/reflect inside a
// tea.Cmd for its async path. Tests back the layer with an httptest server
// through cli.Client, so no test reaches a live endpoint.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/studyzy/wenyuan/internal/cli"
)

// Fixed page sizes per the behavior spec: the banks view walks pages of 100
// eagerly, memories load one page of 500, entities and documents load a
// single fixed page of 100.
const (
	bankPageSize      = 100
	memoriesPageSize  = 500
	entitiesPageSize  = 100
	documentsPageSize = 100
)

// reflectTokens is the max_tokens the reflect body pins: the +/- control
// only affects recall.
const reflectTokens = 4096

// apiClient is the TUI's view over the resolved CLI client.
type apiClient struct{ c *cli.Client }

// bankRow is one line of the Banks view.
type bankRow struct {
	ID   string
	Name string // missing/empty renders as Unnamed
}

// memoryRow is one line of the Memories view. Raw keeps the whole list-row
// object for the detail overlay's JSON pane.
type memoryRow struct {
	Raw         json.RawMessage
	ID          string
	FactType    string
	MentionedAt string
	OccStart    string
	OccEnd      string
	Text        string
}

// entityRow is one line of the Entities view.
type entityRow struct {
	Name     string
	Type     string // metadata.type or unknown
	Mentions int
	First    string
	Last     string
}

// docRow is one line of the Documents view.
type docRow struct {
	ID      string
	Type    string
	Created string
}

// documentDetail is the fetched full document behind the Documents overlay.
type documentDetail struct {
	ID      string
	Type    string
	Created string
	Content string // the stored source text; empty means none was stored
}

// recallRow is one line of the recall results table.
type recallRow struct {
	ID          string
	Type        string
	MentionedAt string
	OccStart    string
	OccEnd      string
	Text        string
}

// listBanks walks every page of the bank list: page size 100, the offset
// advancing by the rows fetched so far, until a page comes back empty or the
// envelope total is reached. The q parameter exists on the endpoint but is
// never sent.
func (a apiClient) listBanks(ctx context.Context) ([]bankRow, error) {
	var rows []bankRow
	fetched := 0
	for {
		var page struct {
			Banks []struct {
				BankID string `json:"bank_id"`
				Name   string `json:"name"`
			} `json:"banks"`
			Total int `json:"total"`
		}
		path := fmt.Sprintf("/v1/default/banks?limit=%d&offset=%d", bankPageSize, fetched)
		if err := a.c.Do(ctx, "GET", path, nil, &page); err != nil {
			return nil, err
		}
		for _, b := range page.Banks {
			rows = append(rows, bankRow{ID: b.BankID, Name: b.Name})
		}
		fetched += len(page.Banks)
		if len(page.Banks) == 0 || fetched >= page.Total {
			return rows, nil
		}
	}
}

// listMemories fetches one fixed page of 500 memories at the given offset.
// No type or q filter is sent; the order is the server default. The list is
// replaced, never appended.
func (a apiClient) listMemories(ctx context.Context, bankID string, offset int) ([]memoryRow, error) {
	path := fmt.Sprintf("/v1/default/banks/%s/memories/list?limit=%d&offset=%d", bankID, memoriesPageSize, offset)
	var page struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := a.c.Do(ctx, "GET", path, nil, &page); err != nil {
		return nil, err
	}
	rows := make([]memoryRow, 0, len(page.Items))
	for _, raw := range page.Items {
		var f struct {
			ID          string  `json:"id"`
			Text        string  `json:"text"`
			FactType    *string `json:"fact_type"`
			MentionedAt *string `json:"mentioned_at"`
			OccStart    *string `json:"occurred_start"`
			OccEnd      *string `json:"occurred_end"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, err
		}
		rows = append(rows, memoryRow{
			Raw:         raw,
			ID:          f.ID,
			FactType:    deref(f.FactType),
			MentionedAt: deref(f.MentionedAt),
			OccStart:    deref(f.OccStart),
			OccEnd:      deref(f.OccEnd),
			Text:        f.Text,
		})
	}
	return rows, nil
}

// listEntities fetches the single fixed page of 100 entities.
func (a apiClient) listEntities(ctx context.Context, bankID string) ([]entityRow, error) {
	path := fmt.Sprintf("/v1/default/banks/%s/entities?limit=%d", bankID, entitiesPageSize)
	var page struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := a.c.Do(ctx, "GET", path, nil, &page); err != nil {
		return nil, err
	}
	rows := make([]entityRow, 0, len(page.Items))
	for _, raw := range page.Items {
		var f struct {
			CanonicalName string         `json:"canonical_name"`
			MentionCount  int            `json:"mention_count"`
			FirstSeen     *string        `json:"first_seen"`
			LastSeen      *string        `json:"last_seen"`
			Metadata      map[string]any `json:"metadata"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, err
		}
		rows = append(rows, entityRow{
			Name:     f.CanonicalName,
			Type:     metadataType(f.Metadata),
			Mentions: f.MentionCount,
			First:    deref(f.FirstSeen),
			Last:     deref(f.LastSeen),
		})
	}
	return rows, nil
}

// listDocuments fetches the single fixed page of 100 documents at offset 0.
func (a apiClient) listDocuments(ctx context.Context, bankID string) ([]docRow, error) {
	path := fmt.Sprintf("/v1/default/banks/%s/documents?limit=%d&offset=0", bankID, documentsPageSize)
	var page struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := a.c.Do(ctx, "GET", path, nil, &page); err != nil {
		return nil, err
	}
	rows := make([]docRow, 0, len(page.Items))
	for _, raw := range page.Items {
		var f struct {
			ID          string         `json:"id"`
			CreatedAt   string         `json:"created_at"`
			ContentType string         `json:"content_type"`
			Metadata    map[string]any `json:"document_metadata"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, err
		}
		rows = append(rows, docRow{
			ID:      f.ID,
			Type:    documentType(f.ContentType, f.Metadata),
			Created: f.CreatedAt,
		})
	}
	return rows, nil
}

// getDocument fetches the full document for the Documents overlay.
func (a apiClient) getDocument(ctx context.Context, bankID, documentID string) (documentDetail, error) {
	var f struct {
		ID           string         `json:"id"`
		CreatedAt    string         `json:"created_at"`
		OriginalText *string        `json:"original_text"`
		Metadata     map[string]any `json:"document_metadata"`
	}
	path := fmt.Sprintf("/v1/default/banks/%s/documents/%s", bankID, documentID)
	if err := a.c.Do(ctx, "GET", path, nil, &f); err != nil {
		return documentDetail{}, err
	}
	return documentDetail{
		ID:      f.ID,
		Type:    metadataType(f.Metadata),
		Created: f.CreatedAt,
		Content: deref(f.OriginalText),
	}, nil
}

// deleteDocument removes one document. No confirmation exists at this layer.
func (a apiClient) deleteDocument(ctx context.Context, bankID, documentID string) error {
	path := fmt.Sprintf("/v1/default/banks/%s/documents/%s", bankID, documentID)
	return a.c.Do(ctx, "DELETE", path, nil, nil)
}

// recallBody is the recall request the TUI sends: the query text, the budget
// level, the max-token cap, tracing disabled, and any-match tag filtering.
// Everything else is omitted and left at the server default.
type recallBody struct {
	Query     string `json:"query"`
	Budget    string `json:"budget"`
	MaxTokens int    `json:"max_tokens"`
	Trace     bool   `json:"trace"`
	TagsMatch string `json:"tags_match"`
}

// recall runs a recall query and flattens the result objects.
func (a apiClient) recall(ctx context.Context, bankID, query, budget string, maxTokens int) ([]recallRow, error) {
	path := fmt.Sprintf("/v1/default/banks/%s/memories/recall", bankID)
	var resp struct {
		Results []json.RawMessage `json:"results"`
	}
	body := recallBody{Query: query, Budget: budget, MaxTokens: maxTokens, Trace: false, TagsMatch: "any"}
	if err := a.c.Do(ctx, "POST", path, body, &resp); err != nil {
		return nil, err
	}
	rows := make([]recallRow, 0, len(resp.Results))
	for _, raw := range resp.Results {
		var f struct {
			ID          string  `json:"id"`
			Text        string  `json:"text"`
			Type        *string `json:"type"`
			MentionedAt *string `json:"mentioned_at"`
			OccStart    *string `json:"occurred_start"`
			OccEnd      *string `json:"occurred_end"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, err
		}
		rows = append(rows, recallRow{
			ID:          f.ID,
			Type:        deref(f.Type),
			MentionedAt: deref(f.MentionedAt),
			OccStart:    deref(f.OccStart),
			OccEnd:      deref(f.OccEnd),
			Text:        f.Text,
		})
	}
	return rows, nil
}

// reflectBody is the reflect request the TUI sends. max_tokens is pinned at
// reflectTokens regardless of the recall +/- control; every optional feature
// is off, so the remaining fields are omitted and default to disabled.
type reflectBody struct {
	Query     string `json:"query"`
	Budget    string `json:"budget"`
	MaxTokens int    `json:"max_tokens"`
	TagsMatch string `json:"tags_match"`
}

// reflect runs a reflection and returns the response text.
func (a apiClient) reflect(ctx context.Context, bankID, query, budget string) (string, error) {
	path := fmt.Sprintf("/v1/default/banks/%s/reflect", bankID)
	var resp struct {
		Text string `json:"text"`
	}
	body := reflectBody{Query: query, Budget: budget, MaxTokens: reflectTokens, TagsMatch: "any"}
	if err := a.c.Do(ctx, "POST", path, body, &resp); err != nil {
		return "", err
	}
	return resp.Text, nil
}

// deref renders an optional string field as its value or the empty string.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// metadataType reads metadata.type, falling back to unknown.
func metadataType(metadata map[string]any) string {
	if v, ok := metadata["type"].(string); ok && v != "" {
		return v
	}
	return "unknown"
}

// documentType resolves the Documents TYPE column. The upstream field
// content_type is absent from this server's document list, so the column
// falls back to document_metadata.type, then unknown.
func documentType(contentType string, metadata map[string]any) string {
	if contentType != "" {
		return contentType
	}
	if v, ok := metadata["type"].(string); ok && v != "" {
		return v
	}
	return "unknown"
}

// datePart renders a timestamp as its date: the part before "T". An absent
// value renders as the fallback: "-" for table cells, "unknown" for detail
// fields.
func datePart(s, missing string) string {
	if s == "" {
		return missing
	}
	if i := strings.IndexByte(s, 'T'); i >= 0 {
		return s[:i]
	}
	return s
}
