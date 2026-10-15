package cli

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// triStateBool is an optional boolean flag: it stays unset until the flag is
// given, distinguishing an absent flag from an explicit false.
type triStateBool struct {
	set   bool
	value bool
}

func (b *triStateBool) String() string {
	if !b.set {
		return ""
	}
	return strconv.FormatBool(b.value)
}

func (b *triStateBool) Set(s string) error {
	v, err := strconv.ParseBool(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("invalid boolean value %q: want true or false", s)
	}
	b.set, b.value = true, v
	return nil
}

// triStateString is an optional string flag. An empty value passed
// explicitly still counts as set: it is the clear spelling for clearable
// members.
type triStateString struct {
	set   bool
	value string
}

func (s *triStateString) String() string { return s.value }

func (s *triStateString) Set(v string) error {
	s.set, s.value = true, v
	return nil
}

// triStateInt is an optional integer flag.
type triStateInt struct {
	set   bool
	value int
}

func (i *triStateInt) String() string {
	if !i.set {
		return ""
	}
	return strconv.Itoa(i.value)
}

func (i *triStateInt) Set(s string) error {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("invalid integer value %q", s)
	}
	i.set, i.value = true, v
	return nil
}

// splitFlagList splits a comma-separated flag value, trimming spaces and
// dropping empty members.
func splitFlagList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// printSyntheticSuccess prints the client-synthesized delete acknowledgement
// used by mental-model and directive deletes in machine modes.
func printSyntheticSuccess(ui *UI) error {
	_, err := fmt.Fprintln(ui.Stdout, `{"success": true}`)
	return err
}

// mmModel is the wire shape of a mental model.
type mmModel struct {
	ID                  string         `json:"id" yaml:"id"`
	BankID              string         `json:"bank_id" yaml:"bank_id"`
	Subtype             string         `json:"subtype" yaml:"subtype"`
	Name                string         `json:"name" yaml:"name"`
	Description         string         `json:"description" yaml:"description"`
	SourceQuery         string         `json:"source_query,omitempty" yaml:"source_query,omitempty"`
	Content             string         `json:"content,omitempty" yaml:"content,omitempty"`
	Tags                []string       `json:"tags" yaml:"tags"`
	MaxTokens           int            `json:"max_tokens" yaml:"max_tokens"`
	Trigger             map[string]any `json:"trigger,omitempty" yaml:"trigger,omitempty"`
	Observations        map[string]any `json:"observations" yaml:"observations"`
	ReflectResponse     map[string]any `json:"reflect_response,omitempty" yaml:"reflect_response,omitempty"`
	LastMemorySeenAt    *string        `json:"last_memory_seen_at,omitempty" yaml:"last_memory_seen_at,omitempty"`
	LastRefreshFailedAt *string        `json:"last_refresh_failed_at,omitempty" yaml:"last_refresh_failed_at,omitempty"`
	IsStale             bool           `json:"is_stale" yaml:"is_stale"`
	LastUpdated         *string        `json:"last_updated,omitempty" yaml:"last_updated,omitempty"`
	LastRefreshedAt     *string        `json:"last_refreshed_at,omitempty" yaml:"last_refreshed_at,omitempty"`
	CreatedAt           string         `json:"created_at" yaml:"created_at"`
}

// mmListOut is the mental-model listing envelope.
type mmListOut struct {
	Items  []mmModel `json:"items" yaml:"items"`
	Total  int       `json:"total" yaml:"total"`
	Limit  int       `json:"limit" yaml:"limit"`
	Offset int       `json:"offset" yaml:"offset"`
}

// mmCreateBody is the create request body.
type mmCreateBody struct {
	ID          string         `json:"id,omitempty"`
	Name        string         `json:"name"`
	SourceQuery string         `json:"source_query"`
	MaxTokens   int            `json:"max_tokens"`
	Tags        []string       `json:"tags"`
	Trigger     map[string]any `json:"trigger,omitempty"`
}

// mmCreateOut is the create acknowledgement.
type mmCreateOut struct {
	MentalModelID string `json:"mental_model_id" yaml:"mental_model_id"`
	OperationID   string `json:"operation_id,omitempty" yaml:"operation_id,omitempty"`
}

// mmUpdateBody is the update request body; absent fields are omitted.
type mmUpdateBody struct {
	Name        *string        `json:"name,omitempty"`
	SourceQuery *string        `json:"source_query,omitempty"`
	MaxTokens   *int           `json:"max_tokens,omitempty"`
	Tags        *[]string      `json:"tags,omitempty"`
	Trigger     map[string]any `json:"trigger,omitempty"`
}

// mmSubmitOut is the refresh/dry-run acknowledgement.
type mmSubmitOut struct {
	OperationID string `json:"operation_id" yaml:"operation_id"`
	Status      string `json:"status" yaml:"status"`
}

// mmDryRunOut carries the documented dry-run fields.
type mmDryRunOut struct {
	RequestedMode      string         `json:"requested_mode" yaml:"requested_mode"`
	EffectiveMode      string         `json:"effective_mode" yaml:"effective_mode"`
	ModeFallbackReason string         `json:"mode_fallback_reason,omitempty" yaml:"mode_fallback_reason,omitempty"`
	Outcome            string         `json:"outcome" yaml:"outcome"`
	WouldPersist       bool           `json:"would_persist" yaml:"would_persist"`
	Facts              mmDryRunFacts  `json:"facts" yaml:"facts"`
	DeltaOperations    *mmDryRunDelta `json:"delta_operations,omitempty" yaml:"delta_operations,omitempty"`
	DurationMS         int64          `json:"duration_ms" yaml:"duration_ms"`
	Warnings           []string       `json:"warnings" yaml:"warnings"`
	Diff               string         `json:"diff" yaml:"diff"`
}

// mmDryRunFacts are the fact-type counts of a dry run.
type mmDryRunFacts struct {
	Retrieved map[string]int `json:"retrieved" yaml:"retrieved"`
	Used      map[string]int `json:"used" yaml:"used"`
}

// mmDryRunDelta are the delta-operation counts of a dry run.
type mmDryRunDelta struct {
	Applied []any `json:"applied" yaml:"applied"`
	Skipped []any `json:"skipped" yaml:"skipped"`
}

// mmHistoryEntry is one recorded refresh: a version snapshot or a failure
// record.
type mmHistoryEntry struct {
	PreviousContent         *string        `json:"previous_content" yaml:"previous_content"`
	PreviousReflectResponse map[string]any `json:"previous_reflect_response" yaml:"previous_reflect_response"`
	ChangedAt               string         `json:"changed_at" yaml:"changed_at"`
	Kind                    string         `json:"kind,omitempty" yaml:"kind,omitempty"`
	Outcome                 string         `json:"outcome,omitempty" yaml:"outcome,omitempty"`
	FailureReason           string         `json:"failure_reason,omitempty" yaml:"failure_reason,omitempty"`
	ErrorMessage            string         `json:"error_message,omitempty" yaml:"error_message,omitempty"`
}

// RegisterMentalModelFamily wires the mental-model commands onto the
// registry. There is deliberately no `clear` subcommand.
func RegisterMentalModelFamily(r *Registry) {
	mm := r.Sub("mental-model", "manage mental models")

	list := mm.Sub("list", "list mental models of a bank")
	list.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 1 {
			return NewUsageError("mental-model list requires exactly one <bank_id>")
		}
		return mentalModelList(ctx, d, d.Args[0])
	})

	get := mm.Sub("get", "show one mental model")
	get.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 2 {
			return NewUsageError("mental-model get requires <bank_id> and <mental_model_id>")
		}
		return mentalModelGet(ctx, d, d.Args[0], d.Args[1])
	})

	create := mm.Sub("create", "create a mental model")
	var cf mmCreateFlags
	create.Flags().StringVar(&cf.id, "id", "", "client-supplied identifier")
	create.Flags().StringVar(&cf.tags, "tags", "", "comma-separated tags")
	create.Flags().IntVar(&cf.maxTokens, "max-tokens", 2048, "content budget in tokens")
	create.Flags().StringVar(&cf.tagsMatch, "tags-match", "", "tag matching mode (any|all|any_strict|all_strict|exact)")
	create.Flags().BoolVar(&cf.consol, "trigger-refresh-after-consolidation", false, "refresh after consolidation")
	create.Flags().StringVar(&cf.mode, "trigger-mode", "", "refresh mode (full|delta)")
	create.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 3 {
			return NewUsageError("mental-model create requires <bank_id>, <name> and <source_query>")
		}
		return mentalModelCreate(ctx, d, d.Args[0], d.Args[1], d.Args[2], cf)
	})

	update := mm.Sub("update", "update a mental model")
	var uf mmUpdateFlags
	update.Flags().StringVar(&uf.name, "name", "", "new name")
	update.Flags().StringVar(&uf.sourceQuery, "source-query", "", "new source query")
	update.Flags().Var(&uf.maxTokens, "max-tokens", "content budget in tokens")
	update.Flags().StringVar(&uf.tags, "tags", "", "comma-separated tags (replaces the set)")
	update.Flags().StringVar(&uf.mode, "trigger-mode", "", "refresh mode (full|delta)")
	update.Flags().Var(&uf.consol, "trigger-refresh-after-consolidation", "refresh after consolidation (true|false)")
	update.Flags().Var(&uf.cron, "trigger-refresh-cron", "refresh cron schedule; empty clears it")
	update.Flags().Var(&uf.minInterval, "trigger-min-refresh-interval-seconds", "minimum seconds between refreshes")
	update.Flags().Var(&uf.tagsMatch, "trigger-tags-match", "tag matching mode; empty clears it")
	update.Flags().Var(&uf.keepTrace, "trigger-keep-trace", "keep reflect trace (true|false)")
	update.Flags().Var(&uf.excludeMM, "trigger-exclude-mental-models", "exclude other mental models (true|false)")
	update.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 2 {
			return NewUsageError("mental-model update requires <bank_id> and <mental_model_id>")
		}
		return mentalModelUpdate(ctx, d, d.Args[0], d.Args[1], &uf)
	})

	del := mm.Sub("delete", "delete a mental model")
	delYes := del.Flags().Bool("yes", false, "skip the confirmation prompt")
	del.Flags().BoolVar(delYes, "y", false, "skip the confirmation prompt (short)")
	del.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 2 {
			return NewUsageError("mental-model delete requires <bank_id> and <mental_model_id>")
		}
		return mentalModelDelete(ctx, d, d.Args[0], d.Args[1], *delYes)
	})

	refresh := mm.Sub("refresh", "submit an asynchronous refresh")
	refresh.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 2 {
			return NewUsageError("mental-model refresh requires <bank_id> and <mental_model_id>")
		}
		return mentalModelRefresh(ctx, d, d.Args[0], d.Args[1])
	})

	dryRun := mm.Sub("dry-run-refresh", "run the refresh pipeline without persisting")
	dryRun.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 2 {
			return NewUsageError("mental-model dry-run-refresh requires <bank_id> and <mental_model_id>")
		}
		return mentalModelDryRun(ctx, d, d.Args[0], d.Args[1])
	})

	history := mm.Sub("history", "show the refresh history")
	history.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 2 {
			return NewUsageError("mental-model history requires <bank_id> and <mental_model_id>")
		}
		return mentalModelHistory(ctx, d, d.Args[0], d.Args[1])
	})
}

// mmCreateFlags holds the create flag values.
type mmCreateFlags struct {
	id        string
	tags      string
	maxTokens int
	tagsMatch string
	consol    bool
	mode      string
}

// mmUpdateFlags holds the update flag values.
type mmUpdateFlags struct {
	name        string
	sourceQuery string
	maxTokens   triStateInt
	tags        string
	mode        string
	consol      triStateBool
	cron        triStateString
	minInterval triStateInt
	tagsMatch   triStateString
	keepTrace   triStateBool
	excludeMM   triStateBool
}

func (f *mmUpdateFlags) any() bool {
	return f.name != "" || f.sourceQuery != "" || f.maxTokens.set || f.tags != "" ||
		f.mode != "" || f.consol.set || f.cron.set || f.minInterval.set ||
		f.tagsMatch.set || f.keepTrace.set || f.excludeMM.set
}

func (f *mmUpdateFlags) triggerAny() bool {
	return f.mode != "" || f.consol.set || f.cron.set || f.minInterval.set ||
		f.tagsMatch.set || f.keepTrace.set || f.excludeMM.set
}

func mentalModelPath(bank, id string) string {
	return fmt.Sprintf("%s/%s/mental-models/%s", batchDBase, bank, id)
}

// parseTriggerMode resolves a refresh mode case-insensitively.
func parseTriggerMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "full":
		return "full", nil
	case "delta":
		return "delta", nil
	}
	return "", NewCommandError("invalid --trigger-mode %q: want full or delta", value)
}

// parseTagsMatch resolves a tag matching mode case-insensitively.
func parseTagsMatch(value string) (string, error) {
	if v := strings.ToLower(strings.TrimSpace(value)); v != "" {
		switch v {
		case "any", "all", "any_strict", "all_strict", "exact":
			return v, nil
		}
	}
	return "", NewCommandError("invalid tags-match value %q: want any, all, any_strict, all_strict, or exact", value)
}

// countSummary renders a fact-type count map sorted by key, or the word
// "none" when empty.
func countSummary(counts map[string]int) string {
	if len(counts) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s: %d", key, counts[key]))
	}
	return strings.Join(parts, ", ")
}

// mentalModelDetail renders the full get-style view of a model.
func mentalModelDetail(ui *UI, m mmModel) {
	ui.Infof("%s", m.Name)
	ui.Plain("  ID: %s", m.ID)
	if m.SourceQuery != "" {
		ui.Plain("  Source Query: %s", m.SourceQuery)
	}
	if m.Content != "" {
		ui.Plain("─── Content ───")
		ui.Plain("%s", m.Content)
	}
}
