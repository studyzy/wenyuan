// Package vlm transcribes the images a document embeds through an
// OpenAI-compatible vision chat endpoint. It is the OCR half of the file
// parser: the parser extracts a document's images and this adapter turns each
// one into text.
//
// It reuses the OpenAI-compatible chat adapter rather than speaking the wire
// protocol itself, because a vision endpoint is that protocol with an
// image_url content part. It is deliberately not a member of the provider
// registry: that registry selects the deployment's chat, embedding and rerank
// backends from the provider configuration, whereas OCR is a file-parsing
// concern with its own endpoint, credential and model configured independently.
package vlm

import (
	"context"
	"encoding/base64"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/provider/openai"
)

// The environment keys this subsystem reads. They are the reference's own
// names, spelled identically so a copied deployment configuration is honoured.
const (
	EnvOCREnabled = "HINDSIGHT_API_FILE_PARSER_MARKITDOWN_OCR_ENABLED"
	EnvOCRAPIKey  = "HINDSIGHT_API_FILE_PARSER_MARKITDOWN_OCR_API_KEY"
	EnvOCRBaseURL = "HINDSIGHT_API_FILE_PARSER_MARKITDOWN_OCR_BASE_URL"
	EnvOCRModel   = "HINDSIGHT_API_FILE_PARSER_MARKITDOWN_OCR_MODEL"
	EnvOCRPrompt  = "HINDSIGHT_API_FILE_PARSER_MARKITDOWN_OCR_PROMPT"
	EnvOCRHeaders = "HINDSIGHT_API_FILE_PARSER_MARKITDOWN_OCR_DEFAULT_HEADERS"
)

// DefaultPrompt is the transcription instruction applied when the operator
// configures none. It is the reference's default OCR prompt verbatim, so a
// deployment that sets only the endpoint still asks the model for a
// transcription rather than a description.
const DefaultPrompt = `You are a precise OCR transcription engine.

Transcribe only the visible text in the image. Do not describe the image, summarize it, translate it, infer missing content, or add commentary. Preserve the original language, wording, numbers, punctuation, capitalization, and reading order. Reconstruct headings, lists, key-value fields, stamps, and tables as clean Markdown when the layout is clear. If text is unreadable or uncertain, write [unclear] for that span. Return only the extracted Markdown.`

// Config is the resolved OCR endpoint configuration.
type Config struct {
	// BaseURL is the OpenAI-compatible endpoint; required.
	BaseURL string
	// APIKey authenticates the endpoint; required.
	APIKey string
	// Model is the vision model with image-input support; required.
	Model string
	// Prompt is the transcription instruction; empty applies DefaultPrompt.
	Prompt string
	// Headers are extra request headers sent with every call.
	Headers map[string]string
}

// Image is one image handed to the vision model.
type Image struct {
	// Data is the encoded image bytes.
	Data []byte
	// MediaType is the image's IANA type, for example "image/jpeg".
	MediaType string
}

// MissingConfigError reports OCR enabled without one of the settings it needs.
// It names the environment key the operator must set, so startup fails with an
// actionable message rather than a parser that silently runs without OCR.
type MissingConfigError struct {
	// Setting is the human name of the missing setting, for example "model".
	Setting string
	// Key is the environment key that carries the setting.
	Key string
	// Hint describes what the setting should hold, appended to the message.
	Hint string
}

func (e *MissingConfigError) Error() string {
	message := "Markitdown OCR is enabled but no " + e.Setting + " is configured. Set " + e.Key
	if e.Hint != "" {
		message += " to " + e.Hint
	}
	return message + "."
}

// Transcriber transcribes images through one vision endpoint. It holds no
// process configuration beyond the endpoint it was built with.
type Transcriber struct {
	client *openai.Client
	prompt string
}

// New builds a transcriber, refusing a configuration that cannot work: OCR
// enabled without a model, an API key or a base URL fails here, at construction
// (i.e. at startup), rather than on the first image a user uploads.
func New(cfg Config) (*Transcriber, error) {
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return nil, &MissingConfigError{
			Setting: "model", Key: EnvOCRModel,
			Hint: "an OpenAI-compatible OCR/vision model with image-input support",
		}
	}
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, &MissingConfigError{Setting: "API key", Key: EnvOCRAPIKey}
	}
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		return nil, &MissingConfigError{
			Setting: "base URL", Key: EnvOCRBaseURL,
			Hint: "an OpenAI-compatible OCR/vision endpoint",
		}
	}
	prompt := cfg.Prompt
	if strings.TrimSpace(prompt) == "" {
		prompt = DefaultPrompt
	}
	client := openai.NewClient(provider.Settings{
		Provider: openai.NameOpenAI,
		BaseURL:  baseURL,
		APIKey:   apiKey,
		Model:    model,
		Timeout:  openai.DefaultTimeout,
		Headers:  cfg.Headers,
	}, nil)
	return &Transcriber{client: client, prompt: prompt}, nil
}

// Transcribe returns the model's transcription of one image. The transcription
// instruction and the image travel as the ordered content parts of a single
// user turn, which is the shape an OpenAI-compatible vision endpoint reads.
func (t *Transcriber) Transcribe(ctx context.Context, image Image) (string, error) {
	result, err := t.client.Complete(ctx, provider.CompletionRequest{
		Messages: []provider.Message{{
			Role: provider.RoleUser,
			Parts: []provider.ContentPart{
				{Type: "text", Text: t.prompt},
				{Type: "image_url", ImageURL: dataURI(image)},
			},
		}},
		Scope: "file_ocr",
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Content), nil
}

// dataURI encodes an image as the inline data URI an image_url part carries.
func dataURI(image Image) string {
	mediaType := image.MediaType
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(image.Data)
}
