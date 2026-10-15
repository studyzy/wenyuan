package cli

// The `memory reflect` action: schema handling, the lenient enum parsing the
// recall and reflect flags share, and the pretty rendering of the reflection.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// memoryBudget resolves --budget leniently: any unrecognized value, including
// the empty one, becomes mid.
func memoryBudget(value string) string {
	switch strings.ToLower(value) {
	case "low":
		return "low"
	case "high":
		return "high"
	default:
		return "mid"
	}
}

// memoryTagsMatch resolves --tags-match leniently: any unrecognized value
// becomes any.
func memoryTagsMatch(value string) string {
	switch strings.ToLower(value) {
	case "all":
		return "all"
	case "any_strict":
		return "any_strict"
	case "all_strict":
		return "all_strict"
	default:
		return "any"
	}
}

// mapFactTypes lowercases each entry and maps it onto the supported fact
// types; unrecognized entries are silently dropped, as the reference does.
func mapFactTypes(entries []string) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		switch strings.ToLower(entry) {
		case "world":
			out = append(out, "world")
		case "experience":
			out = append(out, "experience")
		case "observation":
			out = append(out, "observation")
		}
	}
	return out
}

func registerMemoryReflect(parent *SubCmd) {
	f := parent.Flags()
	budget := f.String("budget", "mid", "token budget: low, mid, or high")
	f.StringVar(budget, "b", "mid", "token budget (short)")
	contextText := f.String("context", "", "context for the reflection")
	f.StringVar(contextText, "c", "", "context (short)")
	maxTokens := f.Int("max-tokens", 0, "token budget for the answer (default 4096)")
	f.IntVar(maxTokens, "m", 0, "token budget (short)")
	schemaPath := f.String("schema", "", "path to a JSON response schema")
	f.StringVar(schemaPath, "s", "", "schema path (short)")
	tags := f.String("tags", "", "tags to filter by (comma list)")
	tagsMatch := f.String("tags-match", "any", "how tags match: any, all, any_strict, all_strict")
	includeFacts := f.Bool("include-facts", false, "include the facts used in the answer")
	factTypes := f.String("fact-types", "", "fact types to search (comma list)")
	excludeMentalModels := f.Bool("exclude-mental-models", false, "leave mental models out")
	excludeMentalModelIDs := f.String("exclude-mental-model-ids", "", "mental model ids to leave out (comma list)")
	parent.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 2 {
			return NewUsageError("memory reflect requires a bank id and a query")
		}
		var responseSchema json.RawMessage
		if *schemaPath != "" {
			schema, err := loadResponseSchema(*schemaPath)
			if err != nil {
				return err
			}
			responseSchema = schema
		}
		return memoryReflect(ctx, d, reflectOptions{
			bankID:                d.Args[0],
			query:                 d.Args[1],
			budget:                *budget,
			context:               *contextText,
			maxTokens:             *maxTokens,
			maxTokensSet:          flagWasProvidedAny(d.Flags, "max-tokens", "m"),
			responseSchema:        responseSchema,
			tags:                  commaList(*tags),
			tagsMatch:             *tagsMatch,
			includeFacts:          *includeFacts,
			factTypes:             mapFactTypes(commaList(*factTypes)),
			factTypesSet:          flagWasProvided(d.Flags, "fact-types"),
			excludeMentalModels:   *excludeMentalModels,
			excludeMentalModelIDs: commaList(*excludeMentalModelIDs),
		})
	})
}

type reflectOptions struct {
	bankID                string
	query                 string
	budget                string
	context               string
	maxTokens             int
	maxTokensSet          bool
	responseSchema        json.RawMessage
	tags                  []string
	tagsMatch             string
	includeFacts          bool
	factTypes             []string
	factTypesSet          bool
	excludeMentalModels   bool
	excludeMentalModelIDs []string
}

type reflectFactsInclude struct {
	Facts struct{} `json:"facts"`
}

type reflectRequestBody struct {
	Query                 string               `json:"query"`
	Budget                string               `json:"budget"`
	Context               string               `json:"context,omitempty"`
	MaxTokens             int                  `json:"max_tokens"`
	Include               *reflectFactsInclude `json:"include,omitempty"`
	ResponseSchema        json.RawMessage      `json:"response_schema,omitempty"`
	Tags                  []string             `json:"tags,omitempty"`
	TagsMatch             string               `json:"tags_match"`
	ApplyAllDirectives    bool                 `json:"apply_all_directives"`
	FactTypes             *[]string            `json:"fact_types,omitempty"`
	ExcludeMentalModels   bool                 `json:"exclude_mental_models"`
	ExcludeMentalModelIDs []string             `json:"exclude_mental_model_ids,omitempty"`
}

// loadResponseSchema reads and parses the schema file before the request is
// built; both failure shapes are the CLI's own error texts.
func loadResponseSchema(path string) (json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("Failed to read schema file: %s", path) //nolint:staticcheck // exact copy text of the CLI contract
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("Failed to parse JSON schema from: %s", path) //nolint:staticcheck // exact copy text of the CLI contract
	}
	return json.RawMessage(data), nil
}

// memoryReflect posts the reflection and renders the answer. The max-tokens
// default substitutes 4096 only when the flag was omitted, so an explicit
// --max-tokens 0 is sent as given, as the reference does.
func memoryReflect(ctx context.Context, d *Deps, o reflectOptions) error {
	maxTokens := o.maxTokens
	if !o.maxTokensSet {
		maxTokens = 4096
	}
	body := reflectRequestBody{
		Query:                 o.query,
		Budget:                memoryBudget(o.budget),
		Context:               o.context,
		MaxTokens:             maxTokens,
		ResponseSchema:        o.responseSchema,
		Tags:                  o.tags,
		TagsMatch:             memoryTagsMatch(o.tagsMatch),
		ApplyAllDirectives:    false,
		ExcludeMentalModels:   o.excludeMentalModels,
		ExcludeMentalModelIDs: o.excludeMentalModelIDs,
	}
	if o.factTypesSet {
		body.FactTypes = &o.factTypes
	}
	if o.includeFacts {
		body.Include = &reflectFactsInclude{}
	}
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "POST", bankPath(o.bankID, "/reflect"), &body, &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	var resp struct {
		Text             string          `json:"text"`
		BasedOnMemories  json.RawMessage `json:"based_on"`
		StructuredOutput json.RawMessage `json:"structured_output"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	ui := d.UI
	ui.Plain("Reflection")
	ui.Plain("%s", resp.Text)
	ui.Plain("")
	if memories := basedOnMemories(resp.BasedOnMemories); len(memories) > 0 {
		ui.Plain("%s", ui.Dim(fmt.Sprintf("Based on %d memory units", len(memories))))
		ui.Plain("")
	}
	if len(resp.StructuredOutput) > 0 && string(resp.StructuredOutput) != "null" {
		ui.Plain("%s", ui.rule("Structured Output"))
		ui.Plain("")
		var out bytes.Buffer
		if err := json.Indent(&out, resp.StructuredOutput, "", "  "); err != nil {
			return &DecodeError{Reason: err.Error(), Body: string(resp.StructuredOutput)}
		}
		out.WriteByte('\n')
		_, _ = ui.Stdout.Write(out.Bytes())
	}
	return nil
}

// basedOnMemories decodes the based_on.memories array; any malformed or
// missing envelope counts as none.
func basedOnMemories(raw json.RawMessage) []json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var envelope struct {
		Memories []json.RawMessage `json:"memories"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil
	}
	return envelope.Memories
}
