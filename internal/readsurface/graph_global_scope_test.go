package readsurface

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// seedGlobalScopeGraph stores two observations: one untagged (the "global"
// scope) and one tagged.
func seedGlobalScopeGraph(t *testing.T) *storage.MemoryStore {
	t.Helper()
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	for _, unit := range []storage.MemoryUnit{
		{ID: "obs-global", BankID: bank, Text: "obs global", FactType: storage.TypeObservation},
		{ID: "obs-tagged", BankID: bank, Text: "obs tagged", FactType: storage.TypeObservation, Tags: []string{"work"}},
	} {
		if _, err := store.UpsertUnit(ctx, unit); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// graphTexts returns the texts the served graph reports as table rows.
func graphTexts(t *testing.T, mux *http.ServeMux, query string) map[string]bool {
	t.Helper()
	rec := send(mux, http.MethodGet, "/v1/default/banks/"+bank+"/graph"+query, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d: %s", query, rec.Code, rec.Body.String())
	}
	var payload struct {
		TableRows []struct {
			Text string `json:"text"`
		} `json:"table_rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	texts := make(map[string]bool, len(payload.TableRows))
	for _, row := range payload.TableRows {
		texts[row.Text] = true
	}
	return texts
}

// `tags_match=exact` with no tags is the "global" scope: the rows carrying no
// tags at all, not "no filter" (engine/memories/pg/graph.py:219-236, pinned by
// tests/test_graph_filtering.py::test_graph_exact_global_scope_filter). Every
// other mode treats empty tags as no filter, so the graph stays the whole bank.
func TestGraphExactWithNoTagsSelectsTheGlobalScope(t *testing.T) {
	mux := familyRoutes(seedGlobalScopeGraph(t))

	exact := graphTexts(t, mux, "?type=observation&tags_match=exact")
	if len(exact) != 1 || !exact["obs global"] {
		t.Fatalf("exact with no tags = %v, want only the untagged observation", exact)
	}

	for _, mode := range []string{"any", "all", "any_strict", "all_strict"} {
		got := graphTexts(t, mux, "?type=observation&tags_match="+mode)
		if len(got) != 2 {
			t.Fatalf("tags_match=%s with no tags = %v, want the whole bank", mode, got)
		}
	}
	// The unstated mode is `all_strict`, which also means no filter.
	if got := graphTexts(t, mux, "?type=observation"); len(got) != 2 {
		t.Fatalf("default mode = %v, want the whole bank", got)
	}

	// A tagged request still narrows, in every mode that filters on tags.
	tagged := graphTexts(t, mux, "?type=observation&tags=work&tags_match=exact")
	if len(tagged) != 1 || !tagged["obs tagged"] {
		t.Fatalf("exact with tags = %v, want only the tagged observation", tagged)
	}
}
