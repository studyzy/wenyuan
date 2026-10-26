// Package banktemplate applies a bank template manifest to a bank: the bank's
// configuration overrides, its pinned mental models and its directives. One
// implementation serves both the archive import route and the server-owned
// default template, so a manifest is interpreted the same way wherever it
// arrives.
package banktemplate

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"

	"github.com/studyzy/wenyuan/internal/storage"
)

// CurrentTemplateVersion is the manifest schema version this build accepts. A
// manifest that names a newer version is refused rather than guessed at.
const CurrentTemplateVersion = "1"

// templateIDPattern is the mental-model identifier shape the manifest requires:
// alphanumeric lowercase with hyphens, starting with a letter or digit.
var templateIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// retainExtractionModes are the modes the engine implements, which the
// reference accepts in a template's bank section.
var retainExtractionModes = []string{"concise", "verbose", "custom", "verbatim", "chunks"}

// Manifest is the JSON bank template manifest: the body the import endpoint
// accepts and the document the export endpoint produces. The field names and
// nullability are public surface.
type Manifest struct {
	Version      string         `json:"version"`
	Bank         map[string]any `json:"bank"`
	MentalModels []MentalModel  `json:"mental_models"`
	Directives   []Directive    `json:"directives"`
}

// MentalModel is one mental-model definition within a manifest.
type MentalModel struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	SourceQuery string         `json:"source_query"`
	Tags        []string       `json:"tags"`
	MaxTokens   int            `json:"max_tokens"`
	Trigger     map[string]any `json:"trigger"`
}

// defaultMaxTokens is the manifest model's default, applied when a model does
// not name one so an import stores the same value the reference's model does
// rather than the Go zero value.
const defaultMaxTokens = 2048

// UnmarshalJSON fills the manifest model's field defaults, so an absent
// max_tokens reads as the reference's default rather than as zero.
func (m *MentalModel) UnmarshalJSON(raw []byte) error {
	type alias MentalModel
	var typed alias
	if err := json.Unmarshal(raw, &typed); err != nil {
		return err
	}
	*m = MentalModel(typed)
	var probe struct {
		MaxTokens *int `json:"max_tokens"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return err
	}
	if probe.MaxTokens == nil {
		m.MaxTokens = defaultMaxTokens
	}
	return nil
}

// Directive is one directive definition within a manifest. Directives are
// matched by name on re-import.
type Directive struct {
	Name     string   `json:"name"`
	Content  string   `json:"content"`
	Priority int      `json:"priority"`
	IsActive bool     `json:"is_active"`
	Tags     []string `json:"tags"`
}

// UnmarshalJSON fills the manifest model's field defaults, so an absent
// is_active reads as the reference's default (true) rather than as false.
func (d *Directive) UnmarshalJSON(raw []byte) error {
	type alias Directive
	var typed alias
	if err := json.Unmarshal(raw, &typed); err != nil {
		return err
	}
	*d = Directive(typed)
	var probe struct {
		IsActive *bool `json:"is_active"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return err
	}
	if probe.IsActive == nil {
		d.IsActive = true
	}
	return nil
}

// ValidationError reports a manifest the loader refused. Structural failures
// come from the manifest's shape; semantic ones from its contents. The two are
// kept apart because the reference reports them under different messages and a
// caller logging a bad server-owned template names which kind failed.
type ValidationError struct {
	// Structural reports a shape failure (a field of the wrong type, a missing
	// required field, an unsupported version) rather than a content failure.
	Structural bool
	// Errors are the individual failures, each already phrased for the wire or
	// the log.
	Errors []string
}

func (e *ValidationError) Error() string { return strings.Join(e.Errors, "; ") }

// Parse converts an already JSON-decoded manifest object into a typed manifest.
// It performs no validation beyond JSON typing, so a caller that has the bytes
// may decode straight into Manifest instead and skip the round trip.
func Parse(raw map[string]any) (Manifest, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Load parses and validates a manifest object that arrived as decoded JSON,
// which is how the server-owned default template is configured. A malformed
// manifest yields a *ValidationError rather than a partial manifest, so the
// caller can log it and fall back exactly as the reference does.
func Load(raw map[string]any) (Manifest, error) {
	manifest, err := Parse(raw)
	if err != nil {
		return Manifest{}, &ValidationError{Structural: true, Errors: []string{err.Error()}}
	}
	structural, semantic := Validate(manifest)
	if len(structural) > 0 {
		return Manifest{}, &ValidationError{Structural: true, Errors: structural}
	}
	if len(semantic) > 0 {
		return Manifest{}, &ValidationError{Structural: false, Errors: semantic}
	}
	return manifest, nil
}

// Validate enforces the rules the reference applies beyond JSON typing, and
// reports them the way the reference does: the structural failures separately
// from the semantic ones, each listing every failure it found rather than
// stopping at the first.
func Validate(manifest Manifest) (structural, semantic []string) {
	// The messages below are the reference's Pydantic output, rendered the way
	// its handler renders it: "<'.'.join(loc)>: <msg>". A value_error carries the
	// framework's "Value error, " prefix and a missing required field is
	// "Field required". The order follows the model's field declaration — the
	// version, then the bank section, then the mental models, then the
	// directives — because the framework reports every failure it found in that
	// order rather than stopping at the first.
	version := strings.TrimSpace(manifest.Version)
	if version == "" {
		structural = append(structural, "version: Field required")
	} else {
		// The reference validates this field with `int(v)` — a Python integer,
		// which has no width — and compares the value it produced with the two
		// bounds. The version is therefore parsed at arbitrary precision: a value
		// past int64 is still a number there, so it falls to the upper-bound arm
		// rather than being reported as non-numeric, and the message quotes the
		// digits as the caller sent them.
		parsed, ok := new(big.Int).SetString(version, 10)
		switch {
		case !ok:
			structural = append(structural,
				fmt.Sprintf("version: Value error, version must be a numeric string, got '%s'", version))
		case parsed.Cmp(big.NewInt(1)) < 0:
			structural = append(structural, "version: Value error, version must be >= 1")
		case parsed.Cmp(big.NewInt(int64(atoiVersion(CurrentTemplateVersion)))) > 0:
			structural = append(structural, fmt.Sprintf(
				"version: Value error, version '%s' is not supported by this server (max supported: %s). Please upgrade Hindsight.",
				version, CurrentTemplateVersion))
		}
	}

	structural = append(structural, bankConfigStructural(manifest.Bank)...)

	seenModels := map[string]bool{}
	duplicateModelIDs := map[string]bool{}
	for i, model := range manifest.MentalModels {
		if !templateIDPattern.MatchString(model.ID) {
			structural = append(structural, fmt.Sprintf(
				"mental_models.%d.id: Value error, Mental model id '%s' must be alphanumeric lowercase with hyphens, starting with a letter or digit.",
				i, model.ID))
		}
		switch {
		case model.MaxTokens < 256:
			structural = append(structural, fmt.Sprintf(
				"mental_models.%d.max_tokens: Input should be greater than or equal to 256", i))
		case model.MaxTokens > 8192:
			structural = append(structural, fmt.Sprintf(
				"mental_models.%d.max_tokens: Input should be less than or equal to 8192", i))
		}
		if strings.TrimSpace(model.Name) == "" {
			semantic = append(semantic, fmt.Sprintf("mental_models[%d].name: must not be empty", i))
		}
		if strings.TrimSpace(model.SourceQuery) == "" {
			semantic = append(semantic, fmt.Sprintf("mental_models[%d].source_query: must not be empty", i))
		}
		if seenModels[model.ID] {
			duplicateModelIDs[model.ID] = true
		}
		seenModels[model.ID] = true
	}
	// Duplicate ids are caught by the model's own list validator, so they are a
	// structural failure surfaced under the schema message, not one of the
	// semantic checks below.
	if len(duplicateModelIDs) > 0 {
		structural = append(structural, fmt.Sprintf(
			"mental_models: Value error, Duplicate mental model ids: %s", pythonStringList(duplicateModelIDs)))
	}

	seenDirectives := map[string]bool{}
	duplicateDirectiveNames := map[string]bool{}
	for i, directive := range manifest.Directives {
		if strings.TrimSpace(directive.Name) == "" {
			semantic = append(semantic, fmt.Sprintf("directives[%d].name: must not be empty", i))
		}
		if strings.TrimSpace(directive.Content) == "" {
			semantic = append(semantic, fmt.Sprintf("directives[%d].content: must not be empty", i))
		}
		if seenDirectives[directive.Name] {
			duplicateDirectiveNames[directive.Name] = true
		}
		seenDirectives[directive.Name] = true
	}
	if len(duplicateDirectiveNames) > 0 {
		structural = append(structural, fmt.Sprintf(
			"directives: Value error, Duplicate directive names: %s", pythonStringList(duplicateDirectiveNames)))
	}

	// The bank's own settings have to agree with each other before they are
	// written: an extraction mode is one of the modes the engine implements, and
	// custom instructions are only read in the mode that consumes them.
	if bank := manifest.Bank; bank != nil {
		mode, _ := bank["retain_extraction_mode"].(string)
		custom, _ := bank["retain_custom_instructions"].(string)
		if mode != "" && !retainExtractionMode(mode) {
			semantic = append(semantic, fmt.Sprintf(
				"bank.retain_extraction_mode: must be one of %s, got '%s'",
				pythonTuple(retainExtractionModes), mode))
		}
		if strings.TrimSpace(custom) != "" && mode != "custom" {
			semantic = append(semantic, "bank.retain_custom_instructions: requires retain_extraction_mode='custom'")
		}
	}
	return structural, semantic
}

// bankConfigStructural enforces the numeric bounds the template's bank model
// declares, reported the way the framework words a failed constraint. Only the
// fields with a declared bound are checked here; the rest of the bank model is
// free-form on this path.
func bankConfigStructural(bank map[string]any) []string {
	if bank == nil {
		return nil
	}
	var out []string
	for _, field := range []string{"disposition_skepticism", "disposition_literalism", "disposition_empathy"} {
		value, present := bank[field]
		if !present || value == nil {
			continue
		}
		number, ok := configNumber(value)
		if !ok {
			out = append(out, fmt.Sprintf("bank.%s: %s", field, integerTypeMessage(value)))
			continue
		}
		switch {
		case number < 1:
			out = append(out, fmt.Sprintf("bank.%s: Input should be greater than or equal to 1", field))
		case number > 5:
			out = append(out, fmt.Sprintf("bank.%s: Input should be less than or equal to 5", field))
		}
	}
	if value, present := bank["mental_model_min_refresh_interval_seconds"]; present && value != nil {
		number, ok := configNumber(value)
		if !ok {
			out = append(out, "bank.mental_model_min_refresh_interval_seconds: "+integerTypeMessage(value))
		} else if number < 0 {
			out = append(out, "bank.mental_model_min_refresh_interval_seconds: Input should be greater than or equal to 0")
		}
	}
	return out
}

// configNumber reports a decoded JSON value as a whole number.
func configNumber(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		if typed == float64(int64(typed)) {
			return int64(typed), true
		}
		return 0, false
	default:
		return 0, false
	}
}

// integerTypeMessage is the framework's wording for a value that is not a
// whole number: a string that cannot be parsed says so, any other kind is
// simply the wrong type.
func integerTypeMessage(value any) string {
	if _, isString := value.(string); isString {
		return "Input should be a valid integer, unable to parse string as an integer"
	}
	return "Input should be a valid integer"
}

// pythonStringList renders a set of strings the way Python's repr renders a
// sorted list, e.g. ['a', 'b'], because the message is served verbatim.
func pythonStringList(values map[string]bool) string {
	ordered := make([]string, 0, len(values))
	for value := range values {
		ordered = append(ordered, value)
	}
	sort.Strings(ordered)
	quoted := make([]string, 0, len(ordered))
	for _, value := range ordered {
		quoted = append(quoted, "'"+value+"'")
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// pythonTuple renders a list of strings the way Python's repr renders a tuple,
// e.g. ('a', 'b'), because the message is served verbatim.
func pythonTuple(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "'"+value+"'")
	}
	return "(" + strings.Join(quoted, ", ") + ")"
}

// retainExtractionMode reports whether the mode is one the engine implements.
func retainExtractionMode(mode string) bool {
	for _, known := range retainExtractionModes {
		if known == mode {
			return true
		}
	}
	return false
}

// atoiVersion parses a trusted constant version.
func atoiVersion(raw string) int {
	value := 0
	for _, char := range raw {
		if char < '0' || char > '9' {
			break
		}
		value = value*10 + int(char-'0')
	}
	return value
}

// ConfigValues drops keys whose value is JSON null, mirroring the reference's
// "only explicitly set fields" projection.
func ConfigValues(in map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range in {
		if value != nil {
			out[key] = value
		}
	}
	return out
}

// BankPatch turns a manifest's bank section into the bank patch it applies.
// Every key — the disposition traits and the reflect mission included — is a
// configuration override in the bank's config column, which is where the
// reference's template import persists the whole bank section and the only
// place its configuration read consults. A manifest therefore round-trips
// through exactly the column the config endpoint reads and writes.
func BankPatch(updates map[string]any) storage.BankPatch {
	patch := storage.BankPatch{}
	config := map[string]any{}
	for key, value := range updates {
		config[key] = value
	}
	if len(config) > 0 {
		patch.Config = &config
	}
	return patch
}
