package cli

// The `memory recall` action: the 15-argument search surface with its
// client-side window validation and the flat pretty rendering.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func registerMemoryRecall(parent *SubCmd) {
	f := parent.Flags()
	factType := f.String("fact-type", "world,experience,observation", "fact types to search (comma list)")
	f.StringVar(factType, "t", "world,experience,observation", "fact types to search (short)")
	budget := f.String("budget", "mid", "token budget: low, mid, or high")
	f.StringVar(budget, "b", "mid", "token budget (short)")
	maxTokens := f.Int("max-tokens", 4096, "token budget for the answer")
	trace := f.Bool("trace", false, "request and render the retrieval trace")
	includeChunks := f.Bool("include-chunks", false, "include source chunks in the results")
	chunkMaxTokens := f.Int("chunk-max-tokens", 8192, "token budget for included chunks")
	tags := f.String("tags", "", "tags to filter by (comma list)")
	tagsMatch := f.String("tags-match", "any", "how tags match: any, all, any_strict, all_strict")
	queryTimestamp := f.String("query-timestamp", "", "timestamp the query is evaluated at")
	preferObservations := f.Bool("prefer-observations", false, "prefer observations in the results")
	windowStart := f.String("window-start", "", "temporal window start")
	windowEnd := f.String("window-end", "", "temporal window end")
	parent.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 2 {
			return NewUsageError("memory recall requires a bank id and a query")
		}
		return memoryRecall(ctx, d, recallOptions{
			bankID:             d.Args[0],
			query:              d.Args[1],
			factType:           *factType,
			budget:             *budget,
			maxTokens:          *maxTokens,
			trace:              *trace,
			includeChunks:      *includeChunks,
			chunkMaxTokens:     *chunkMaxTokens,
			tags:               *tags,
			tagsMatch:          *tagsMatch,
			queryTimestamp:     *queryTimestamp,
			preferObservations: *preferObservations,
			windowStart:        *windowStart,
			windowEnd:          *windowEnd,
		})
	})
}

type recallOptions struct {
	bankID             string
	query              string
	factType           string
	budget             string
	maxTokens          int
	trace              bool
	includeChunks      bool
	chunkMaxTokens     int
	tags               string
	tagsMatch          string
	queryTimestamp     string
	preferObservations bool
	windowStart        string
	windowEnd          string
}

// recallWindow is the validated temporal window.
type recallWindow struct {
	start time.Time
	end   time.Time
}

// parseWindowBound accepts an RFC 3339 timestamp with offset (normalized to
// UTC) or a naive value read as UTC. The flag name feeds the error text.
func parseWindowBound(flagName, value string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.UTC(), nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, NewCommandError("%s must be an ISO 8601 datetime (e.g. 2023-04-01T00:00:00Z or 2023-04-01), got %q", flagName, value)
}

// validateRecallWindow enforces the pairing, ordering and parse rules. Equal
// bounds are a valid degenerate window.
func validateRecallWindow(startFlag, endFlag string) (*recallWindow, error) {
	switch {
	case startFlag != "" && endFlag == "":
		return nil, NewCommandError("--window-start requires --window-end")
	case endFlag != "" && startFlag == "":
		return nil, NewCommandError("--window-end requires --window-start")
	case startFlag == "":
		return nil, nil
	}
	start, err := parseWindowBound("--window-start", startFlag)
	if err != nil {
		return nil, err
	}
	end, err := parseWindowBound("--window-end", endFlag)
	if err != nil {
		return nil, err
	}
	if end.Before(start) {
		return nil, NewCommandError("--window-end must not be earlier than --window-start")
	}
	return &recallWindow{start: start, end: end}, nil
}

type recallInclude struct {
	Chunks recallIncludeChunks `json:"chunks"`
}

type recallIncludeChunks struct {
	MaxTokens int `json:"max_tokens"`
}

type recallTemporalWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type recallRequestBody struct {
	Query              string                `json:"query"`
	Types              []string              `json:"types,omitempty"`
	Budget             string                `json:"budget"`
	MaxTokens          int                   `json:"max_tokens"`
	Trace              bool                  `json:"trace"`
	QueryTimestamp     string                `json:"query_timestamp,omitempty"`
	PreferObservations bool                  `json:"prefer_observations"`
	Include            *recallInclude        `json:"include,omitempty"`
	Tags               []string              `json:"tags,omitempty"`
	TagsMatch          string                `json:"tags_match"`
	TemporalWindow     *recallTemporalWindow `json:"temporal_window,omitempty"`
}

// memoryRecall validates the window, posts the search, and renders the
// results. Verbose mode prints the request body before the call.
func memoryRecall(ctx context.Context, d *Deps, o recallOptions) error {
	window, err := validateRecallWindow(o.windowStart, o.windowEnd)
	if err != nil {
		return err
	}
	body := recallRequestBody{
		Query:              o.query,
		Types:              commaList(o.factType),
		Budget:             memoryBudget(o.budget),
		MaxTokens:          o.maxTokens,
		Trace:              o.trace,
		QueryTimestamp:     o.queryTimestamp,
		PreferObservations: o.preferObservations,
		Tags:               commaList(o.tags),
		TagsMatch:          memoryTagsMatch(o.tagsMatch),
	}
	if o.includeChunks {
		body.Include = &recallInclude{Chunks: recallIncludeChunks{MaxTokens: o.chunkMaxTokens}}
	}
	if window != nil {
		body.TemporalWindow = &recallTemporalWindow{
			Start: window.start.Format(time.RFC3339),
			End:   window.end.Format(time.RFC3339),
		}
	}
	if d.Verbose {
		if pretty, mErr := json.MarshalIndent(body, "", "  "); mErr == nil {
			_, _ = fmt.Fprintf(d.UI.Stderr, "Request body: %s\n", pretty)
		}
	}
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "POST", bankPath(o.bankID, "/memories/recall"), &body, &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	return renderRecall(d, raw, o)
}

type recallResultItem struct {
	ID            string  `json:"id"`
	Text          string  `json:"text"`
	Type          *string `json:"type"`
	Context       *string `json:"context"`
	OccurredStart *string `json:"occurred_start"`
	OccurredEnd   *string `json:"occurred_end"`
	ChunkID       *string `json:"chunk_id"`
	DocumentID    *string `json:"document_id"`
}

type recallChunkBody struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	ChunkIndex int    `json:"chunk_index"`
	Truncated  bool   `json:"truncated"`
}

type recallResponseBody struct {
	Results []recallResultItem         `json:"results"`
	Chunks  map[string]recallChunkBody `json:"chunks"`
	Trace   map[string]any             `json:"trace"`
}

// renderRecall draws the flat numbered result list plus the optional source
// chunk and trace blocks.
func renderRecall(d *Deps, raw json.RawMessage, o recallOptions) error {
	var body recallResponseBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	ui := d.UI
	ui.Plain("Search Results (%d)", len(body.Results))
	if len(body.Results) == 0 {
		ui.Plain("  No results found.")
		return nil
	}
	for i, item := range body.Results {
		ui.Plain("%s", ui.Dim(fmt.Sprintf("Result #%d", i+1)))
		factType := ""
		if item.Type != nil {
			factType = *item.Type
		}
		ui.Plain("%s", ui.typeBadge(factType))
		ui.Plain("  %s", item.Text)
		if item.Context != nil && *item.Context != "" {
			ui.Plain("  %s", ui.Dim("context: "+*item.Context))
		}
		if line := recallDateLine(item); line != "" {
			ui.Plain("  date: %s", line)
		}
		if item.DocumentID != nil && *item.DocumentID != "" {
			ui.Plain("  document: %s", *item.DocumentID)
		}
		if o.includeChunks && item.ChunkID != nil {
			if chunk, ok := body.Chunks[*item.ChunkID]; ok {
				ui.Plain("  %s", ui.rule("Source Chunk"))
				for _, line := range strings.Split(chunk.Text, "\n") {
					ui.Plain("  %s", line)
				}
				if chunk.Truncated {
					ui.Plain("  [Truncated due to token limit]")
				}
				ui.Plain("  %s", ui.Dim(fmt.Sprintf("Chunk ID: %s | Index: %d", chunk.ID, chunk.ChunkIndex)))
			}
		}
		ui.Plain("")
	}
	if o.trace && body.Trace != nil {
		renderRecallTrace(ui, body.Trace)
	}
	return nil
}

// recallDateLine joins the occurred bounds: both when present, else the one
// that is.
func recallDateLine(item recallResultItem) string {
	start, end := "", ""
	if item.OccurredStart != nil {
		start = *item.OccurredStart
	}
	if item.OccurredEnd != nil {
		end = *item.OccurredEnd
	}
	switch {
	case start != "" && end != "":
		return start + " - " + end
	case start != "":
		return start
	case end != "":
		return end
	default:
		return ""
	}
}

// renderRecallTrace prints the trace block: the total time and the activation
// count when the trace object carries them. Keys are probed at the top level
// and inside the summary block, since both spellings occur in the wild.
func renderRecallTrace(ui *UI, trace map[string]any) {
	ui.Plain("Trace")
	summary, _ := trace["summary"].(map[string]any)
	if v, ok := traceNumber([]map[string]any{trace, summary}, "total_duration_ms", "total_time_ms"); ok {
		ui.Plain("  total time: %.2fms", v)
	}
	if v, ok := traceNumber([]map[string]any{trace, summary}, "activation_count", "activations"); ok {
		ui.Plain("  activation count: %d", int(v))
	}
}

// traceNumber finds the first present numeric value among the keys, scanning
// the candidate maps in order.
func traceNumber(candidates []map[string]any, keys ...string) (float64, bool) {
	for _, m := range candidates {
		if m == nil {
			continue
		}
		for _, k := range keys {
			if f, ok := m[k].(float64); ok {
				return f, true
			}
		}
	}
	return 0, false
}
