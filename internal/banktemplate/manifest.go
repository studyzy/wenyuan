// Package banktemplate applies a bank template manifest to a bank: the bank's
// configuration overrides, its pinned mental models and its directives. One
// implementation serves both the archive import route and the server-owned
// default template, so a manifest is interpreted the same way wherever it
// arrives.
package banktemplate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
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

// Directive is one directive definition within a manifest. Directives are
// matched by name on re-import.
type Directive struct {
	Name     string   `json:"name"`
	Content  string   `json:"content"`
	Priority int      `json:"priority"`
	IsActive bool     `json:"is_active"`
	Tags     []string `json:"tags"`
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
	version := strings.TrimSpace(manifest.Version)
	parsed, err := strconv.Atoi(version)
	switch {
	case version == "":
		structural = append(structural, "version: Field required")
	case err != nil:
		structural = append(structural,
			fmt.Sprintf("version: version must be a numeric string, got '%s'", version))
	case parsed < 1:
		structural = append(structural, "version: version must be >= 1")
	case parsed > atoiVersion(CurrentTemplateVersion):
		structural = append(structural, fmt.Sprintf(
			"version '%s' is not supported by this server (max supported: %s). Please upgrade Wenyuan.",
			version, CurrentTemplateVersion))
	}

	seenModels := map[string]bool{}
	for i, model := range manifest.MentalModels {
		if !templateIDPattern.MatchString(model.ID) {
			structural = append(structural, fmt.Sprintf(
				"mental_models[%d].id: Mental model id '%s' must be alphanumeric lowercase with hyphens, starting with a letter or digit.",
				i, model.ID))
		}
		if model.MaxTokens != 0 && (model.MaxTokens < 256 || model.MaxTokens > 8192) {
			structural = append(structural, fmt.Sprintf(
				"mental_models[%d].max_tokens: must be between 256 and 8192", i))
		}
		if strings.TrimSpace(model.Name) == "" {
			semantic = append(semantic, fmt.Sprintf("mental_models[%d].name: must not be empty", i))
		}
		if strings.TrimSpace(model.SourceQuery) == "" {
			semantic = append(semantic, fmt.Sprintf("mental_models[%d].source_query: must not be empty", i))
		}
		if seenModels[model.ID] {
			semantic = append(semantic, fmt.Sprintf("mental_models[%d].id: duplicate id '%s'", i, model.ID))
		}
		seenModels[model.ID] = true
	}

	seenDirectives := map[string]bool{}
	for i, directive := range manifest.Directives {
		if strings.TrimSpace(directive.Name) == "" {
			semantic = append(semantic, fmt.Sprintf("directives[%d].name: must not be empty", i))
		}
		if strings.TrimSpace(directive.Content) == "" {
			semantic = append(semantic, fmt.Sprintf("directives[%d].content: must not be empty", i))
		}
		if seenDirectives[directive.Name] {
			semantic = append(semantic, fmt.Sprintf("directives[%d].name: duplicate name '%s'", i, directive.Name))
		}
		seenDirectives[directive.Name] = true
	}

	// The bank's own settings have to agree with each other before they are
	// written: an extraction mode is one of the modes the engine implements, and
	// custom instructions are only read in the mode that consumes them.
	if bank := manifest.Bank; bank != nil {
		mode, _ := bank["retain_extraction_mode"].(string)
		custom, _ := bank["retain_custom_instructions"].(string)
		if mode != "" && !retainExtractionMode(mode) {
			semantic = append(semantic, fmt.Sprintf(
				"bank.retain_extraction_mode: must be one of (%s), got '%s'",
				strings.Join(retainExtractionModes, ", "), mode))
		}
		if strings.TrimSpace(custom) != "" && mode != "custom" {
			semantic = append(semantic, "bank.retain_custom_instructions: requires retain_extraction_mode='custom'")
		}
	}
	return structural, semantic
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

// BankPatch splits a manifest's bank section into the bank patch it applies.
// The disposition traits and the reflect mission live on the bank row rather
// than in the override map, so they are routed to their own columns; every
// other key is a configuration override.
func BankPatch(updates map[string]any) storage.BankPatch {
	patch := storage.BankPatch{}
	traits := map[string]int{}
	config := map[string]any{}
	for key, value := range updates {
		switch key {
		case "disposition_skepticism", "disposition_literalism", "disposition_empathy":
			if raw, ok := asIntValue(value); ok {
				traits[strings.TrimPrefix(key, "disposition_")] = raw
			}
		case "reflect_mission":
			if text, ok := value.(string); ok {
				mission := text
				patch.Mission = &mission
			}
		default:
			config[key] = value
		}
	}
	if len(traits) > 0 {
		patch.Disposition = &traits
	}
	if len(config) > 0 {
		patch.Config = &config
	}
	return patch
}

// asIntValue decodes a JSON number that arrived as float64 (or a Go int) into
// an int, reporting whether the value was numeric.
func asIntValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	default:
		return 0, false
	}
}
