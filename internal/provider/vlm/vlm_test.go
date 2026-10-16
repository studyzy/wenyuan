package vlm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// capturedRequest records the last request a stub endpoint received.
type capturedRequest struct {
	mu      sync.Mutex
	path    string
	headers http.Header
	body    map[string]any
}

// newStubEndpoint stands up a chat-completions endpoint that records the last
// request and replies with the supplied transcription.
func newStubEndpoint(t *testing.T, reply string) (*httptest.Server, *capturedRequest) {
	t.Helper()
	captured := &capturedRequest{}
	encoded, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": reply}}},
	})
	if err != nil {
		t.Fatalf("encode reply: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.mu.Lock()
		defer captured.mu.Unlock()
		captured.path = r.URL.Path
		captured.headers = r.Header.Clone()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		captured.body = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(encoded)
	}))
	t.Cleanup(server.Close)
	return server, captured
}

// TestNewValidatesRequiredSettings asserts OCR enabled without one of its
// required settings fails at construction, naming the key that is missing.
func TestNewValidatesRequiredSettings(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		key  string
	}{
		{"model", Config{APIKey: "k", BaseURL: "https://example.test/v1"}, EnvOCRModel},
		{"api key", Config{Model: "m", BaseURL: "https://example.test/v1"}, EnvOCRAPIKey},
		{"base url", Config{Model: "m", APIKey: "k"}, EnvOCRBaseURL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.cfg)
			if err == nil {
				t.Fatalf("New with no %s should fail", tc.name)
			}
			var missing *MissingConfigError
			if !errors.As(err, &missing) {
				t.Fatalf("error = %v, want *MissingConfigError", err)
			}
			if missing.Key != tc.key {
				t.Fatalf("missing key = %q, want %q", missing.Key, tc.key)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("error %q does not name %q", err, tc.key)
			}
		})
	}
}

// TestTranscribeSendsModelPromptHeadersAndImage asserts the request body and
// headers the endpoint receives carry the configured model, the transcription
// instruction, the custom headers and the image itself.
func TestTranscribeSendsModelPromptHeadersAndImage(t *testing.T) {
	server, captured := newStubEndpoint(t, "TRANSCRIBED TEXT")
	transcriber, err := New(Config{
		BaseURL: server.URL,
		APIKey:  "secret-key",
		Model:   "vision-1",
		Prompt:  "read the receipt",
		Headers: map[string]string{"X-Tenant": "acme"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	text, err := transcriber.Transcribe(context.Background(), Image{Data: []byte("PNGBYTES"), MediaType: "image/png"})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if text != "TRANSCRIBED TEXT" {
		t.Fatalf("transcription = %q", text)
	}
	if captured.path != "/chat/completions" {
		t.Fatalf("path = %q", captured.path)
	}
	if got := captured.headers.Get("Authorization"); got != "Bearer secret-key" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := captured.headers.Get("X-Tenant"); got != "acme" {
		t.Fatalf("X-Tenant header = %q", got)
	}
	if got, _ := captured.body["model"].(string); got != "vision-1" {
		t.Fatalf("model = %q", got)
	}
	parts := capturedContentParts(t, captured.body)
	if len(parts) != 2 {
		t.Fatalf("content parts = %d, want 2", len(parts))
	}
	if parts[0]["type"] != "text" || parts[0]["text"] != "read the receipt" {
		t.Fatalf("text part = %v", parts[0])
	}
	imageURL, _ := parts[1]["image_url"].(map[string]any)
	if parts[1]["type"] != "image_url" || imageURL["url"] != "data:image/png;base64,UE5HQllURVM=" {
		t.Fatalf("image part = %v", parts[1])
	}
}

// TestTranscribeUsesDefaultPrompt asserts an unset prompt falls back to the
// reference's default transcription instruction.
func TestTranscribeUsesDefaultPrompt(t *testing.T) {
	server, captured := newStubEndpoint(t, "ok")
	transcriber, err := New(Config{BaseURL: server.URL, APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := transcriber.Transcribe(context.Background(), Image{Data: []byte("x"), MediaType: "image/png"}); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	parts := capturedContentParts(t, captured.body)
	if parts[0]["text"] != DefaultPrompt {
		t.Fatalf("prompt = %q, want the default prompt", parts[0]["text"])
	}
}

// capturedContentParts pulls the first message's ordered content parts out of a
// captured request body.
func capturedContentParts(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	messages, _ := body["messages"].([]any)
	if len(messages) == 0 {
		t.Fatal("request carried no messages")
	}
	message, _ := messages[0].(map[string]any)
	raw, _ := message["content"].([]any)
	parts := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		part, _ := item.(map[string]any)
		parts = append(parts, part)
	}
	return parts
}
