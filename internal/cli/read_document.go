package cli

// The read-surface resource families: document, entity, tag and chunk.
// Registration lives here with the document commands; the other families live
// in read_entity.go, read_tag.go and read_chunk.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// RegisterReadCommands wires the document, entity, tag and chunk families.
func RegisterReadCommands(r *Registry) {
	document := r.Sub("document", "inspect and manage documents")
	registerDocumentList(document.Sub("list", "list a bank's documents"))
	registerDocumentGet(document.Sub("get", "show one document"))
	registerDocumentDelete(document.Sub("delete", "delete one document"))
	registerDocumentUpdate(document.Sub("update", "update a document's tags"))

	registerEntityCommands(r.Sub("entity", "inspect entities"))
	registerTagCommands(r.Sub("tag", "inspect tags"))
	registerChunkCommands(r.Sub("chunk", "inspect chunks"))
}

func registerDocumentList(parent *SubCmd) {
	f := parent.Flags()
	query := f.String("query", "", "full-text filter")
	f.StringVar(query, "q", "", "full-text filter (short)")
	date := f.String("date", "", "group by date: yesterday, today, all, or YYYY-MM-DD")
	f.StringVar(date, "d", "", "date filter (short)")
	limit := f.Int("limit", 100, "page size")
	f.IntVar(limit, "l", 100, "page size (short)")
	offset := f.Int("offset", 0, "page offset")
	f.IntVar(offset, "s", 0, "page offset (short)")
	parent.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("document list", d.Args, 1); err != nil {
			return err
		}
		return documentList(ctx, d, documentListOptions{
			bankID:  d.Args[0],
			query:   *query,
			date:    *date,
			dateSet: flagWasProvidedAny(d.Flags, "date", "d"),
			limit:   *limit,
			offset:  *offset,
		})
	})
}

type documentListOptions struct {
	bankID string
	query  string
	date   string
	// dateSet reports the --date flag was given; an empty value is then an
	// invalid date, not "no filter".
	dateSet bool
	limit   int
	offset  int
}

type documentPage struct {
	Items []map[string]any `json:"items"`
	Total int              `json:"total"`
}

// documentString reads a string field from a decoded document map.
func documentString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// documentInt reads an integer field from a decoded document map.
func documentInt(m map[string]any, key string) int {
	f, _ := m[key].(float64)
	return int(f)
}

// documentList lists documents. With --date the CLI fetches every page at
// size 500, filters by the created_at date prefix, and groups newest first;
// the machine output of that mode is the flat filtered array. The date mode
// is entered whenever the flag is given, so `--date ""` is an invalid date
// rather than a silent plain listing.
func documentList(ctx context.Context, d *Deps, o documentListOptions) error {
	if !o.dateSet {
		return documentListPlain(ctx, d, o)
	}
	prefix, err := documentDatePrefix(o.date)
	if err != nil {
		return err
	}
	docs, err := fetchAllDocuments(ctx, d, o.bankID)
	if err != nil {
		return err
	}
	if prefix != "" {
		filtered := make([]map[string]any, 0, len(docs))
		for _, doc := range docs {
			if strings.HasPrefix(documentString(doc, "created_at"), prefix) {
				filtered = append(filtered, doc)
			}
		}
		docs = filtered
	}
	if d.Format.IsMachine() {
		if docs == nil {
			docs = []map[string]any{}
		}
		return d.Format.Render(d.UI.Stdout, docs)
	}
	renderDocumentGroups(d, o, docs)
	return nil
}

// documentDatePrefix resolves the --date value to a created_at prefix; ""
// means no filtering. An invalid literal is a client-side error.
func documentDatePrefix(value string) (string, error) {
	switch value {
	case "all":
		return "", nil
	case "today":
		return time.Now().UTC().Format("2006-01-02"), nil
	case "yesterday":
		return time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02"), nil
	}
	if _, err := time.Parse("2006-01-02", value); err != nil {
		return "", fmt.Errorf("invalid date format '%s': %s. use YYYY-MM-DD, 'yesterday', 'today', or 'all'", value, err)
	}
	return value, nil
}

// fetchAllDocuments pulls every page at size 500 until an empty page or the
// running count reaches the envelope total. The offset advances by the page
// size, as the reference steps it.
func fetchAllDocuments(ctx context.Context, d *Deps, bankID string) ([]map[string]any, error) {
	const pageSize = 500
	var docs []map[string]any
	for offset := 0; ; offset += pageSize {
		params := url.Values{}
		params.Set("limit", fmt.Sprintf("%d", pageSize))
		params.Set("offset", fmt.Sprintf("%d", offset))
		var raw json.RawMessage
		if err := d.Client.Do(ctx, "GET", bankPath(bankID, "/documents?"+params.Encode()), nil, &raw); err != nil {
			return nil, err
		}
		var page documentPage
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, &DecodeError{Reason: err.Error(), Body: string(raw)}
		}
		docs = append(docs, page.Items...)
		if len(page.Items) == 0 || len(docs) >= page.Total {
			return docs, nil
		}
	}
}

// documentDateLabel renders the filter description of the pretty header; the
// "all" spelling is shown as "all dates".
func documentDateLabel(value string) string {
	if value == "all" {
		return "all dates"
	}
	return value
}

// renderDocumentGroups prints the date-grouped listing, newest date first.
func renderDocumentGroups(d *Deps, o documentListOptions, docs []map[string]any) {
	ui := d.UI
	ui.Plain("Documents for bank '%s' (filter: %s, showing: %d)", o.bankID, documentDateLabel(o.date), len(docs))
	ui.Plain("")
	groups := map[string][]map[string]any{}
	for _, doc := range docs {
		date := documentString(doc, "created_at")
		if i := strings.Index(date, "T"); i >= 0 {
			date = date[:i]
		}
		groups[date] = append(groups[date], doc)
	}
	dates := make([]string, 0, len(groups))
	for date := range groups {
		dates = append(dates, date)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))
	for _, date := range dates {
		ui.Plain("  %s (%d documents)", date, len(groups[date]))
		for _, doc := range groups[date] {
			ui.Plain("    - %s (%d memories)", documentString(doc, "id"), documentInt(doc, "memory_unit_count"))
		}
		ui.Plain("")
	}
}

// documentListPlain renders one plain page without date grouping.
func documentListPlain(ctx context.Context, d *Deps, o documentListOptions) error {
	params := url.Values{}
	if o.query != "" {
		params.Set("q", o.query)
	}
	params.Set("limit", fmt.Sprintf("%d", o.limit))
	params.Set("offset", fmt.Sprintf("%d", o.offset))
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "GET", bankPath(o.bankID, "/documents?"+params.Encode()), nil, &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	var page documentPage
	if err := json.Unmarshal(raw, &page); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	ui := d.UI
	ui.Plain("Documents for bank '%s' (total: %d)", o.bankID, page.Total)
	for _, doc := range page.Items {
		ui.Plain("")
		ui.Plain("Document ID: %s", documentString(doc, "id"))
		ui.Plain("Created: %s", documentString(doc, "created_at"))
		ui.Plain("Updated: %s", documentString(doc, "updated_at"))
		ui.Plain("Text Length: %d", documentInt(doc, "text_length"))
		ui.Plain("Memory Units: %d", documentInt(doc, "memory_unit_count"))
	}
	return nil
}

func registerDocumentGet(parent *SubCmd) {
	parent.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("document get", d.Args, 2); err != nil {
			return err
		}
		return documentGet(ctx, d, d.Args[0], d.Args[1])
	})
}

// documentGet renders one document. The member route is a subtree, so an id
// containing slashes resolves.
func documentGet(ctx context.Context, d *Deps, bankID, documentID string) error {
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "GET", bankPath(bankID, "/documents/"+documentID), nil, &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	var body struct {
		ID              string  `json:"id"`
		BankID          string  `json:"bank_id"`
		OriginalText    *string `json:"original_text"`
		CreatedAt       string  `json:"created_at"`
		UpdatedAt       string  `json:"updated_at"`
		MemoryUnitCount int     `json:"memory_unit_count"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	ui := d.UI
	ui.Plain("Document: %s", body.ID)
	ui.Plain("  Bank ID: %s", body.BankID)
	ui.Plain("  Created: %s", body.CreatedAt)
	ui.Plain("  Updated: %s", body.UpdatedAt)
	ui.Plain("  Memory Units: %d", body.MemoryUnitCount)
	ui.Plain("")
	ui.Plain("  Text:")
	if body.OriginalText != nil {
		ui.Plain("%s", *body.OriginalText)
	} else {
		ui.Plain("(not stored)")
	}
	return nil
}

func registerDocumentDelete(parent *SubCmd) {
	parent.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("document delete", d.Args, 2); err != nil {
			return err
		}
		return documentDelete(ctx, d, d.Args[0], d.Args[1])
	})
}

type documentDeleteOut struct {
	Success      bool   `json:"success"`
	Message      string `json:"message"`
	DeletedCount int    `json:"deleted_count"`
}

// documentDelete removes one document without a confirmation prompt. The
// machine output renames the server's memory_units_deleted to the CLI's
// deleted_count.
func documentDelete(ctx context.Context, d *Deps, bankID, documentID string) error {
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "DELETE", bankPath(bankID, "/documents/"+documentID), nil, &raw); err != nil {
		return err
	}
	var body struct {
		Success            bool   `json:"success"`
		Message            string `json:"message"`
		MemoryUnitsDeleted int    `json:"memory_units_deleted"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, documentDeleteOut{
			Success:      body.Success,
			Message:      body.Message,
			DeletedCount: body.MemoryUnitsDeleted,
		})
	}
	if body.Success {
		d.UI.Infof("Document deleted successfully")
	} else {
		d.UI.Errorf("Failed to delete document")
	}
	return nil
}

func registerDocumentUpdate(parent *SubCmd) {
	tags := parent.Flags().String("tags", "", "the document's tags (comma list; empty clears them)")
	parent.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("document update", d.Args, 2); err != nil {
			return err
		}
		if !flagWasProvided(d.Flags, "tags") {
			return NewCommandError("At least one of --tags must be provided")
		}
		tagList := commaList(*tags)
		if tagList == nil {
			tagList = []string{}
		}
		return documentUpdate(ctx, d, d.Args[0], d.Args[1], tagList)
	})
}

// documentUpdate patches a document's tags. An explicitly empty --tags value
// clears every tag.
func documentUpdate(ctx context.Context, d *Deps, bankID, documentID string, tags []string) error {
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "PATCH", bankPath(bankID, "/documents/"+documentID), map[string]any{"tags": tags}, &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	d.UI.Infof("Document '%s' updated", documentID)
	d.UI.Plain("")
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	out.WriteByte('\n')
	_, err := fmt.Fprint(d.UI.Stdout, out.String())
	return err
}
