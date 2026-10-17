package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/extraction"
	"github.com/studyzy/wenyuan/internal/storage"
)

// fakeDryRunExtractor records the options it is handed and returns canned facts,
// usage or an error, so the dry-run route's wiring can be exercised without a
// model.
type fakeDryRunExtractor struct {
	options storage.FactExtractionOptions
	facts   []storage.ExtractedFact
	usage   storage.TokenUsage
	err     error
	// refusal, when set, is returned by VisionRefusal so the handler's up-front
	// attachment gate can be observed.
	refusal error
	calls   int
}

func (f *fakeDryRunExtractor) Extract(context.Context, string) ([]storage.ExtractedFact, error) {
	return f.facts, f.err
}

func (f *fakeDryRunExtractor) ExtractWithOptions(_ context.Context, _ string, opts storage.FactExtractionOptions) ([]storage.ExtractedFact, error) {
	f.options = opts
	f.calls++
	return f.facts, f.err
}

func (f *fakeDryRunExtractor) ExtractDryRun(_ context.Context, _ string, opts storage.FactExtractionOptions) (storage.DryRunExtraction, error) {
	f.options = opts
	f.calls++
	return storage.DryRunExtraction{Facts: f.facts, Usage: f.usage}, f.err
}

func (f *fakeDryRunExtractor) VisionRefusal() error { return f.refusal }

// dryRunServer binds the reporting routes over an empty store (no bank), with the
// extractor given, so the dry-run route is exercised against an unknown bank.
func dryRunServer(t *testing.T, extractor storage.FactExtraction, bankID string) *http.ServeMux {
	t.Helper()
	handlers := NewHandlers(storage.NewMemoryStore("public"))
	handlers.Extraction = extractor
	mux := http.NewServeMux()
	handlers.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		if !strings.Contains(pattern, "{bank_id}") {
			mux.HandleFunc(method+" "+pattern, handler)
			return
		}
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", bankID)
			handler(w, r)
		})
	})
	return mux
}

// D1: an unknown bank is neither required nor created; the route answers 200.
func TestDryRunExtractUnknownBankIsAnswered(t *testing.T) {
	mux := dryRunServer(t, nil, "no-such-bank")
	rec := send(mux, http.MethodPost, "/v1/default/banks/no-such-bank/memories/dry-run-extract",
		`{"content":"Alice moved to Berlin in 2021."}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeObject(t, rec)
	for _, key := range []string{"facts", "chunks", "usage"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("body missing %q: %v", key, body)
		}
	}
}

// D2: the three previously dropped overrides reach the extractor's options.
func TestDryRunExtractMapsBodyOptions(t *testing.T) {
	extractor := &fakeDryRunExtractor{}
	mux := dryRunServer(t, extractor, bank)
	body := `{"content":"Alice moved to Berlin.","entities_allow_free_form":false,` +
		`"llm_output_language":"French",` +
		`"entity_labels":[{"key":"pedagogy","type":"value","values":[{"value":"scaffolding"}]}]}`
	rec := send(mux, http.MethodPost, "/v1/default/banks/"+bank+"/memories/dry-run-extract", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if extractor.options.Vocabulary == nil || extractor.options.Vocabulary.Empty() {
		t.Fatalf("entity_labels override did not reach the options: %+v", extractor.options.Vocabulary)
	}
	if extractor.options.AllowFreeFormEntities == nil || *extractor.options.AllowFreeFormEntities {
		t.Fatalf("entities_allow_free_form override did not reach the options: %v", extractor.options.AllowFreeFormEntities)
	}
	if extractor.options.OutputLanguage != "French" {
		t.Fatalf("llm_output_language override did not reach the options: %q", extractor.options.OutputLanguage)
	}
}

// D3: an attachment-bearing request against a model that cannot read images is
// refused 422 before any extraction call.
func TestDryRunExtractVisionGateRefusesAttachments(t *testing.T) {
	extractor := &fakeDryRunExtractor{refusal: &extraction.VisionNotSupportedError{
		Detail: "The model configured to read attachments (gateway/x) cannot read images, but this request carries inline image content.",
	}}
	mux := dryRunServer(t, extractor, bank)
	body := `{"content":[{"type":"text","text":"Here is an invoice:"},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"YWJj"}}]}`
	rec := send(mux, http.MethodPost, "/v1/default/banks/"+bank+"/memories/dry-run-extract", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "cannot read images") {
		t.Fatalf("refusal does not name the vision failure: %s", rec.Body.String())
	}
	if extractor.calls != 0 {
		t.Fatalf("extraction ran despite the vision refusal: %d calls", extractor.calls)
	}
}

// D4: a blank content value is the framework's value_error, carrying the exact
// value the caller sent.
func TestDryRunExtractBlankContentIsValueError(t *testing.T) {
	mux := dryRunServer(t, nil, bank)
	for _, content := range []string{`""`, `"   "`, `[]`, `[{"type":"text","text":"   "}]`} {
		rec := send(mux, http.MethodPost, "/v1/default/banks/"+bank+"/memories/dry-run-extract",
			`{"content":`+content+`}`)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("content %s status = %d, want 422: %s", content, rec.Code, rec.Body.String())
		}
		var failure struct {
			Detail []map[string]any `json:"detail"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &failure); err != nil {
			t.Fatal(err)
		}
		if len(failure.Detail) != 1 {
			t.Fatalf("content %s detail = %v", content, failure.Detail)
		}
		item := failure.Detail[0]
		if item["type"] != "value_error" || item["msg"] != "Value error, content cannot be empty" {
			t.Fatalf("content %s item = %v", content, item)
		}
	}
}

// D5: a block without a discriminator is the framework's union_tag_not_found,
// and a bad attachment payload is a 400.
func TestDryRunExtractMalformedBlocks(t *testing.T) {
	mux := dryRunServer(t, nil, bank)
	rec := send(mux, http.MethodPost, "/v1/default/banks/"+bank+"/memories/dry-run-extract",
		`{"content":[{"text":"hello"}]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing discriminator status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "union_tag_not_found") {
		t.Fatalf("missing discriminator body = %s", rec.Body.String())
	}

	rec = send(mux, http.MethodPost, "/v1/default/banks/"+bank+"/memories/dry-run-extract",
		`{"content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"not-valid-base64!!!"}}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid base64 status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "attachment source data is not valid base64") {
		t.Fatalf("invalid base64 body = %s", rec.Body.String())
	}
}

// D6: an unexpected extractor failure is a 500, not a 400.
func TestDryRunExtractExtractorFailureIsInternal(t *testing.T) {
	extractor := &fakeDryRunExtractor{err: errors.New("provider exploded")}
	mux := dryRunServer(t, extractor, bank)
	rec := send(mux, http.MethodPost, "/v1/default/banks/"+bank+"/memories/dry-run-extract",
		`{"content":"Alice moved to Berlin."}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
}

// D7: a placeholder whose occurrence is exhausted is left intact, not dropped.
func TestRenderChunkPlaceholdersKeepsUnmatchedToken(t *testing.T) {
	token := AttachmentPlaceholderForTest("ab12")
	second := AttachmentPlaceholderForTest("cd34")
	text := "before " + token + " and " + second + " after"
	rendered := renderChunkPlaceholders(text, []dryRunOccurrence{{blockIndex: 0, kind: "image", mediaType: "image/png"}})
	if !strings.Contains(rendered, second) {
		t.Fatalf("exhausted placeholder was dropped: %q", rendered)
	}
	if !strings.Contains(rendered, "[Block #0: image (image/png)]") {
		t.Fatalf("first placeholder was not rendered: %q", rendered)
	}
}

// AttachmentPlaceholderForTest builds a placeholder token for the test above
// without reaching into the memoryops package's unexported helper.
func AttachmentPlaceholderForTest(shortID string) string {
	return "\u27e6hs-att:" + shortID + "\u27e7"
}
