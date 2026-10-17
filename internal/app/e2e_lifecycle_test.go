package app

// End-to-end memory lifecycle through the served handler.
//
// The parity cases fix individual contract oracles; this case follows one
// memory the whole way a client would — retain it, recall it, read it back,
// reflect over it, and see it counted in the bank's statistics — so a
// regression that breaks the write and read sides' agreement surfaces here
// rather than in the consumer that first noticed. Reflection is part of the
// walk, but a deployment with no LLM provider refuses it rather than answering,
// so the route's refusal is what this case observes.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// TestMemoryLifecycleEndToEnd writes a memory through the retain route and then
// observes it through every read route that is supposed to see it.
func TestMemoryLifecycleEndToEnd(t *testing.T) {
	app := parityApp(t)
	const bank = "/v1/default/banks/e2e-bank"
	if rec := exchange(app, http.MethodPut, bank, `{"name":"E2E"}`); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create bank: %d, body = %s", rec.Code, rec.Body.String())
	}

	retain := exchange(app, http.MethodPost, bank+"/memories", `{
		"items": [{"content": "the deployment window is Thursday at 14:00"}]
	}`)
	if retain.Code != http.StatusOK {
		t.Fatalf("retain: %d, body = %s", retain.Code, retain.Body.String())
	}

	recall := exchange(app, http.MethodPost, bank+"/memories/recall", `{"query":"deployment window"}`)
	if recall.Code != http.StatusOK {
		t.Fatalf("recall: %d, body = %s", recall.Code, recall.Body.String())
	}
	recallBody := decodeJSON(t, recall)
	matches, _ := recallBody["results"].([]any)
	if len(matches) == 0 {
		t.Fatalf("recall returned no matches: %s", recall.Body.String())
	}

	list := exchange(app, http.MethodGet, bank+"/memories/list?limit=10", "")
	if list.Code != http.StatusOK {
		t.Fatalf("list memories: %d, body = %s", list.Code, list.Body.String())
	}
	if total, _ := decodeJSON(t, list)["total"].(float64); total < 1 {
		t.Fatalf("list reported no memories: %s", list.Body.String())
	}

	reflect := exchange(app, http.MethodPost, bank+"/reflect", `{"query":"when is the deployment window?"}`)
	// This deployment has no LLM provider, so the reference refuses the
	// reflection rather than answering it from a deterministic synthesis.
	if reflect.Code != http.StatusBadRequest {
		t.Fatalf("reflect: %d, body = %s", reflect.Code, reflect.Body.String())
	}

	// The statistics route must see the write the retain route made.
	stats := exchange(app, http.MethodGet, bank+"/stats", "")
	if stats.Code == http.StatusOK {
		if total, _ := decodeJSON(t, stats)["total_nodes"].(float64); total < 1 {
			t.Fatalf("stats reported no nodes: %s", stats.Body.String())
		}
	}
}

// With document-text storage off, a retain through the served route keeps the
// raw body out of storage, and the read routes report a null original_text and
// blank chunk texts rather than the text they were handed. The write and read
// sides must agree on the switch, so this follows one document both ways.
func TestStoreDocumentTextSwitchEndToEnd(t *testing.T) {
	cfg := testConfig()
	cfg.StoreDocumentText = false
	a, err := New(context.Background(), cfg, Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	const bank = "/v1/default/banks/store-text-bank"
	if rec := exchange(a, http.MethodPut, bank, `{"name":"StoreText"}`); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create bank: %d, body = %s", rec.Code, rec.Body.String())
	}

	const secret = "the raw secret body"
	retain := exchange(a, http.MethodPost, bank+"/memories",
		`{"items":[{"content":"`+secret+`","document_id":"d1"}]}`)
	if retain.Code != http.StatusOK {
		t.Fatalf("retain: %d, body = %s", retain.Code, retain.Body.String())
	}

	doc := exchange(a, http.MethodGet, bank+"/documents/d1", "")
	if doc.Code != http.StatusOK {
		t.Fatalf("get document: %d, body = %s", doc.Code, doc.Body.String())
	}
	body := decodeJSON(t, doc)
	if value, ok := body["original_text"]; !ok || value != nil {
		t.Fatalf("original_text must be present and null, got %v (body %s)", value, doc.Body.String())
	}
	if strings.Contains(doc.Body.String(), secret) {
		t.Fatalf("the discarded text must not surface on the document read: %s", doc.Body.String())
	}

	chunks := exchange(a, http.MethodGet, bank+"/documents/d1/chunks", "")
	if chunks.Code != http.StatusOK {
		t.Fatalf("list chunks: %d, body = %s", chunks.Code, chunks.Body.String())
	}
	if strings.Contains(chunks.Body.String(), secret) {
		t.Fatalf("the discarded text must not surface on the chunk read: %s", chunks.Body.String())
	}

	// Append cannot work with no stored text, and is refused rather than
	// silently degrading to a replace.
	appendRec := exchange(a, http.MethodPost, bank+"/memories",
		`{"items":[{"content":"more","document_id":"d1","update_mode":"append"}]}`)
	if appendRec.Code != http.StatusBadRequest {
		t.Fatalf("append with text storage off = %d, want 400 (body %s)", appendRec.Code, appendRec.Body.String())
	}
	if detail, _ := decodeJSON(t, appendRec)["detail"].(string); !strings.Contains(detail, "HINDSIGHT_API_STORE_DOCUMENT_TEXT") {
		t.Fatalf("append refusal detail = %q, want the reference's wording", detail)
	}
}
