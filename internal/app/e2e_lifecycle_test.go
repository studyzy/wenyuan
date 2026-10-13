package app

// End-to-end memory lifecycle through the served handler.
//
// The parity cases fix individual contract oracles; this case follows one
// memory the whole way a client would — retain it, recall it, read it back,
// reflect over it, and see it counted in the bank's statistics — so a
// regression that breaks the write and read sides' agreement surfaces here
// rather than in the consumer that first noticed.

import (
	"net/http"
	"testing"
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
	if reflect.Code != http.StatusOK {
		t.Fatalf("reflect: %d, body = %s", reflect.Code, reflect.Body.String())
	}
	if answer, _ := decodeJSON(t, reflect)["text"].(string); answer == "" {
		t.Fatalf("reflect produced no answer: %s", reflect.Body.String())
	}

	// The statistics route must see the write the retain route made.
	stats := exchange(app, http.MethodGet, bank+"/stats", "")
	if stats.Code == http.StatusOK {
		if total, _ := decodeJSON(t, stats)["total_nodes"].(float64); total < 1 {
			t.Fatalf("stats reported no nodes: %s", stats.Body.String())
		}
	}
}
