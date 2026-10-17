package extraction

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A forced output language is appended to the extraction prompt, replacing the
// prompt's own preserve-the-source-language rule; with none set the prompt is
// unchanged.
func TestOutputLanguageForcesThePrompt(t *testing.T) {
	stub := &stubProvider{name: "openai", structured: map[string]any{"facts": []any{}}}
	if _, err := New(stub).ExtractWith(context.Background(), "text", Options{OutputLanguage: "French"}); err != nil {
		t.Fatal(err)
	}
	system := stub.lastReq.Messages[0].Content
	if !strings.Contains(system, "Respond exclusively in French") {
		t.Fatalf("output-language directive missing from the prompt:\n%s", system)
	}
}

// An attachment-bearing call is refused unless the model is known to read
// images, and the refusal names the setting that fixes it.
func TestVisionRefusalMessages(t *testing.T) {
	no := false
	unknown := New(&stubProvider{name: "gateway"})
	if err := unknown.VisionRefusal(); err == nil || !strings.Contains(err.Error(), "HINDSIGHT_API_LLM_VISION=true") {
		t.Fatalf("unknown-provider refusal = %v", err)
	}
	cannot := New(&stubProvider{name: "gateway"})
	cannot.Vision = &no
	if err := cannot.VisionRefusal(); err == nil || !strings.Contains(err.Error(), "cannot read images") {
		t.Fatalf("cannot-read refusal = %v", err)
	}
	yes := true
	can := New(&stubProvider{name: "openai"})
	can.Vision = &yes
	if err := can.VisionRefusal(); err != nil {
		t.Fatalf("vision-capable model refused: %v", err)
	}
}

// ExtractWith returns the typed refusal for an attachment-bearing call the model
// cannot serve, before any model call runs.
func TestExtractWithRefusesAttachmentsWithoutVision(t *testing.T) {
	stub := &stubProvider{name: "gateway", structured: map[string]any{"facts": []any{}}}
	extractor := New(stub)
	_, err := extractor.ExtractWith(context.Background(), "text with ⟦hs-att:abcd⟧", Options{
		Attachments: []Attachment{{ShortID: "abcd", MediaType: "image/png", Kind: "image", Data: []byte("x")}},
	})
	var refusal *VisionNotSupportedError
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want a VisionNotSupportedError", err)
	}
	if stub.lastReq.Messages != nil {
		t.Fatal("the model was called despite the vision refusal")
	}
}
