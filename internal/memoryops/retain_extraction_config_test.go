package memoryops

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

// The bank's retain configuration populates the extraction prompt and chunk
// budgets on the outgoing write.
func TestApplyExtractionConfigReadsBankOverrides(t *testing.T) {
	write := storage.RetainRequest{}
	bank := storage.Bank{Config: map[string]any{
		"retain_mission":               "remember the user's appointments",
		"retain_extraction_mode":       "custom",
		"retain_custom_instructions":   "keep dates",
		"retain_chunk_size":            float64(1200),
		"retain_structured_chunk_size": 400,
	}}
	applyExtractionConfig(&write, bank, extractionDefaults{mode: config.DefaultRetainExtractionMode})
	if write.ExtractionMission != "remember the user's appointments" {
		t.Fatalf("mission = %q", write.ExtractionMission)
	}
	if write.ExtractionMode != "custom" {
		t.Fatalf("mode = %q", write.ExtractionMode)
	}
	if write.ExtractionInstructions != "keep dates" {
		t.Fatalf("instructions = %q", write.ExtractionInstructions)
	}
	if write.ChunkSize != 1200 {
		t.Fatalf("chunk size = %d, want 1200", write.ChunkSize)
	}
	if write.StructuredChunkSize != 400 {
		t.Fatalf("structured chunk size = %d, want 400", write.StructuredChunkSize)
	}
}

// An unset or invalid override leaves the field at its zero value, so an
// unconfigured bank keeps the shipped prompt and chunking.
func TestApplyExtractionConfigLeavesDefaultsUnset(t *testing.T) {
	write := storage.RetainRequest{}
	bank := storage.Bank{Config: map[string]any{
		"retain_chunk_size": float64(0),
	}}
	applyExtractionConfig(&write, bank, extractionDefaults{mode: config.DefaultRetainExtractionMode})
	if write.ExtractionMission != "" || write.ExtractionInstructions != "" {
		t.Fatalf("empty config must leave the prompt unset: %+v", write)
	}
	if write.ChunkSize != 0 || write.StructuredChunkSize != 0 {
		t.Fatalf("non-positive size must read as unset: %+v", write)
	}
}

// A bank that stores custom instructions without selecting custom mode keeps
// them inert: the reference consults them only in custom mode, so the write
// must not carry them into a prompt that never asked for them.
func TestApplyExtractionConfigKeepsInstructionsInertOutsideCustomMode(t *testing.T) {
	write := storage.RetainRequest{}
	bank := storage.Bank{Config: map[string]any{
		"retain_extraction_mode":     "concise",
		"retain_custom_instructions": "keep dates",
	}}
	applyExtractionConfig(&write, bank, extractionDefaults{mode: config.DefaultRetainExtractionMode})
	if write.ExtractionMode != "concise" {
		t.Fatalf("mode = %q", write.ExtractionMode)
	}
	if write.ExtractionInstructions != "" {
		t.Fatalf("instructions must be inert outside custom mode, got %q", write.ExtractionInstructions)
	}
}

// The optional-dimensions flag is read off the bank config onto the write.
func TestApplyExtractionConfigReadsOptionalDimensions(t *testing.T) {
	write := storage.RetainRequest{}
	bank := storage.Bank{Config: map[string]any{
		"retain_optional_fact_dimensions": true,
	}}
	applyExtractionConfig(&write, bank, extractionDefaults{})
	if !write.OptionalFactDimensions {
		t.Fatal("optional fact dimensions must reach the write")
	}
}

// A bank chunk-size override reaches segmentation through the retain route, so
// the config is wired into the handler rather than only parsed.
func TestRetainAppliesBankChunkSize(t *testing.T) {
	mux, store := newServer(t)
	config := map[string]any{"retain_chunk_size": float64(5)}
	if _, err := store.UpdateBank(context.TODO(), "b1", storage.BankPatch{Config: &config}); err != nil {
		t.Fatal(err)
	}
	rec := post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"content":"alpha bravo charlie delta"}]}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	page, err := store.ListChunks(context.TODO(), storage.ChunkQuery{BankID: "b1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total <= 1 {
		t.Fatalf("chunks = %d, want the bank's small chunk size to split the text", page.Total)
	}
}

// An item that names a retain strategy extracts under that strategy's
// overrides, layered on top of the bank's own settings, rather than under the
// bank default. This is what makes a conversation strategy able to substitute
// its own prompt for exactly the writes that ask for it.
func TestApplyExtractionConfigAppliesNamedStrategy(t *testing.T) {
	write := storage.RetainRequest{Items: []storage.RetainItem{{Strategy: "conversation"}}}
	bank := storage.Bank{Config: map[string]any{
		"retain_extraction_mode":     "standard",
		"retain_custom_instructions": "bank level",
		"retain_strategies": map[string]any{
			"conversation": map[string]any{
				"retain_extraction_mode":     "custom",
				"retain_custom_instructions": "record only what the speaker said",
				"retain_chunk_size":          float64(700),
			},
		},
	}}
	applyExtractionConfig(&write, bank, extractionDefaults{mode: config.DefaultRetainExtractionMode})
	if write.ExtractionMode != "custom" {
		t.Fatalf("mode = %q, want the strategy's custom mode", write.ExtractionMode)
	}
	if write.ExtractionInstructions != "record only what the speaker said" {
		t.Fatalf("instructions = %q, want the strategy's instructions", write.ExtractionInstructions)
	}
	if write.ChunkSize != 700 {
		t.Fatalf("chunk size = %d, want the strategy's 700", write.ChunkSize)
	}
}

// A name the bank never defined leaves the bank-level configuration in force,
// so an unknown strategy behaves exactly as the default.
func TestApplyExtractionConfigIgnoresUnknownStrategy(t *testing.T) {
	write := storage.RetainRequest{Items: []storage.RetainItem{{Strategy: "missing"}}}
	bank := storage.Bank{Config: map[string]any{
		"retain_extraction_mode": "custom",
		"retain_strategies":      map[string]any{"conversation": map[string]any{"retain_extraction_mode": "standard"}},
	}}
	applyExtractionConfig(&write, bank, extractionDefaults{})
	if write.ExtractionMode != "custom" {
		t.Fatalf("mode = %q, want the bank's own custom mode", write.ExtractionMode)
	}
}
