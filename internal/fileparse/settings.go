package fileparse

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider/vlm"
)

// The environment keys this subsystem reads. They are the reference's own
// names, spelled identically so a copied deployment configuration is honoured.
// The OCR keys are owned by the vision adapter that consumes them, so the
// setting a validation error names is the key the adapter resolves.
const (
	EnvFileParser                     = "HINDSIGHT_API_FILE_PARSER"
	EnvFileParserAllowlist            = "HINDSIGHT_API_FILE_PARSER_ALLOWLIST"
	EnvFileParserMarkitdownOCREnabled = vlm.EnvOCREnabled
	EnvFileParserMarkitdownOCRAPIKey  = vlm.EnvOCRAPIKey
	EnvFileParserMarkitdownOCRBaseURL = vlm.EnvOCRBaseURL
	EnvFileParserMarkitdownOCRModel   = vlm.EnvOCRModel
	EnvFileParserMarkitdownOCRPrompt  = vlm.EnvOCRPrompt
	EnvFileParserMarkitdownOCRHeaders = vlm.EnvOCRHeaders
	EnvFileParserIrisToken            = "HINDSIGHT_API_FILE_PARSER_IRIS_TOKEN"
	EnvFileParserIrisOrgID            = "HINDSIGHT_API_FILE_PARSER_IRIS_ORG_ID"
	EnvFileParserLlamaParseAPIKey     = "HINDSIGHT_API_FILE_PARSER_LLAMA_PARSE_API_KEY"
	EnvFileConversionMaxBatchSizeMB   = "HINDSIGHT_API_FILE_CONVERSION_MAX_BATCH_SIZE_MB"
	EnvFileConversionMaxBatchSize     = "HINDSIGHT_API_FILE_CONVERSION_MAX_BATCH_SIZE"
	EnvFileDeleteAfterRetain          = "HINDSIGHT_API_FILE_DELETE_AFTER_RETAIN"
)

// Reference defaults for the file-conversion settings.
const (
	defaultParserChain          = "markitdown"
	defaultMaxBatchSizeMB       = 100
	defaultMaxBatchCount        = 10
	defaultDeleteAfterRetain    = true
	defaultMarkitdownOCREnabled = false
)

// Settings is the resolved configuration of the file-parsing subsystem.
type Settings struct {
	// Chain is the ordered fallback chain of parser names, tried in order.
	Chain []string
	// Allowlist bounds the parser names a client may request. Nil means every
	// registered parser is requestable.
	Allowlist []string
	// IrisToken and IrisOrgID enable the iris parser when both are set.
	IrisToken string
	IrisOrgID string
	// LlamaParseAPIKey enables the llama_parse parser when set.
	LlamaParseAPIKey string
	// MaxBatchCount bounds how many files one request may carry.
	MaxBatchCount int
	// MaxBatchBytes bounds the combined size of one request's files.
	MaxBatchBytes int64
	// DeleteAfterRetain drops an upload's stored bytes once it has been
	// converted and retained, keeping only the retained text.
	DeleteAfterRetain bool
	// OCREnabled turns on the markitdown parser's image transcription: with it
	// on, a document's embedded images are handed to the vision endpoint and
	// their transcription joins the parsed markdown, and an image file is
	// transcribed in place of being refused.
	OCREnabled bool
	// OCRAPIKey, OCRBaseURL and OCRModel configure the OpenAI-compatible vision
	// endpoint. All three are required when OCR is enabled.
	OCRAPIKey  string
	OCRBaseURL string
	OCRModel   string
	// OCRPrompt is the transcription instruction; empty applies the adapter's
	// default prompt.
	OCRPrompt string
	// OCRHeaders are extra request headers sent to the vision endpoint.
	OCRHeaders map[string]string
}

// FromEnv resolves the settings from the environment, using the supplied lookup
// (normally os.Getenv) so the resolution is testable and honours a loaded .env.
func FromEnv(lookup func(string) string) Settings {
	settings := Settings{
		Chain:             parseList(lookup(EnvFileParser)),
		Allowlist:         optionalList(lookup(EnvFileParserAllowlist)),
		IrisToken:         strings.TrimSpace(lookup(EnvFileParserIrisToken)),
		IrisOrgID:         strings.TrimSpace(lookup(EnvFileParserIrisOrgID)),
		LlamaParseAPIKey:  strings.TrimSpace(lookup(EnvFileParserLlamaParseAPIKey)),
		MaxBatchCount:     intFromEnv(lookup, EnvFileConversionMaxBatchSize, defaultMaxBatchCount),
		MaxBatchBytes:     int64(intFromEnv(lookup, EnvFileConversionMaxBatchSizeMB, defaultMaxBatchSizeMB)) << 20,
		DeleteAfterRetain: boolFromEnv(lookup, EnvFileDeleteAfterRetain, defaultDeleteAfterRetain),
		OCREnabled:        boolFromEnv(lookup, EnvFileParserMarkitdownOCREnabled, defaultMarkitdownOCREnabled),
		OCRAPIKey:         strings.TrimSpace(lookup(EnvFileParserMarkitdownOCRAPIKey)),
		OCRBaseURL:        strings.TrimSpace(lookup(EnvFileParserMarkitdownOCRBaseURL)),
		OCRModel:          strings.TrimSpace(lookup(EnvFileParserMarkitdownOCRModel)),
		OCRPrompt:         lookup(EnvFileParserMarkitdownOCRPrompt),
		OCRHeaders:        parseHeaderMap(lookup(EnvFileParserMarkitdownOCRHeaders)),
	}
	if len(settings.Chain) == 0 {
		settings.Chain = []string{defaultParserChain}
	}
	return settings
}

// Registry builds the parser registry for these settings: the local parser
// always, and each remote parser whose credentials are present. A configured
// OCR endpoint that cannot work (enabled without a base URL, key or model)
// fails construction here, at startup, rather than at the first image a user
// uploads.
func (s Settings) Registry() (*Registry, error) {
	registry := NewRegistry()
	parser, err := newMarkitdownParser(vlm.Config{
		BaseURL: s.OCRBaseURL,
		APIKey:  s.OCRAPIKey,
		Model:   s.OCRModel,
		Prompt:  s.OCRPrompt,
		Headers: s.OCRHeaders,
	}, s.OCREnabled)
	if err != nil {
		return nil, err
	}
	registry.Register(parser)
	if s.IrisToken != "" && s.IrisOrgID != "" {
		registry.Register(NewIrisParser(s.IrisToken, s.IrisOrgID))
	}
	if s.LlamaParseAPIKey != "" {
		registry.Register(NewLlamaParseParser(s.LlamaParseAPIKey))
	}
	return registry, nil
}

// parseHeaderMap reads a JSON object of string headers, which is how the
// reference carries the OCR default-headers setting through the environment. A
// value that is not a JSON object resolves to no headers, matching the
// reference's fallback.
func parseHeaderMap(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil || parsed == nil {
		return nil
	}
	out := make(map[string]string, len(parsed))
	for name, value := range parsed {
		if text, ok := value.(string); ok {
			out[name] = text
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseList splits a comma-separated setting into its non-empty trimmed tokens.
func parseList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// optionalList returns nil when the setting is unset or empty, matching the
// reference's "no allowlist means every parser" semantics.
func optionalList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return parseList(value)
}

// intFromEnv reads a positive integer setting, falling back to the default on
// an unset or unparseable value.
func intFromEnv(lookup func(string) string, key string, fallback int) int {
	raw := strings.TrimSpace(lookup(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

// boolFromEnv reads a boolean setting, falling back to the default when unset.
func boolFromEnv(lookup func(string) string, key string, fallback bool) bool {
	raw := strings.TrimSpace(lookup(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return value
}
