package bankapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/banktemplate"
	"github.com/studyzy/wenyuan/internal/storage"
)

func templateStore(t *testing.T) *storage.MemoryStore {
	t.Helper()
	return storage.NewMemoryStore("public")
}

// The store's create hook is installed the way the composition root installs
// it: once, before any request.
func withTemplate(t *testing.T, store *storage.MemoryStore, manifest map[string]any) {
	t.Helper()
	store.SetBankCreateHook(banktemplate.CreateHook(store, manifest))
}

// The handler-set builder installs the template as the store's create hook, so
// the composition root has one call while the hook stays the single apply path.
func TestWithDefaultBankTemplateInstallsStoreHook(t *testing.T) {
	store := templateStore(t)
	h := NewHandlers(store).WithDefaultBankTemplate(map[string]any{
		"version":    "1",
		"directives": []any{map[string]any{"name": "tone", "content": "be nice"}},
	})
	mux := newMux(h)

	if rec := doJSON(t, mux, http.MethodPut, "/v1/default/banks/x", `{"name":"X"}`); rec.Code != http.StatusOK {
		t.Fatalf("create = %d, body = %s", rec.Code, rec.Body.String())
	}
	directives, err := store.ListDirectives(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if len(directives) != 1 {
		t.Fatalf("directives = %+v, want the template's tone directive", directives)
	}
}

// A configured default template provisions every freshly-created bank: its bank
// settings land on the bank row and its mental models and directives are
// created.
func TestDefaultBankTemplateAppliedOnCreate(t *testing.T) {
	store := templateStore(t)
	withTemplate(t, store, map[string]any{
		"version":       "1",
		"bank":          map[string]any{"retain_chunk_size": 512, "disposition_skepticism": 5, "reflect_mission": "help"},
		"mental_models": []any{map[string]any{"id": "goals", "name": "Goals", "source_query": "what goals?"}},
		"directives":    []any{map[string]any{"name": "tone", "content": "be nice"}},
	})
	mux := newMux(NewHandlers(store))

	rec := doJSON(t, mux, http.MethodPut, "/v1/default/banks/fresh", `{"name":"Fresh"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d, body = %s", rec.Code, rec.Body.String())
	}

	bank, err := store.GetBank(context.Background(), "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if got := bank.Config["retain_chunk_size"]; got != float64(512) && got != 512 {
		t.Fatalf("bank config retain_chunk_size = %#v, want 512", got)
	}
	if got := bank.Trait(storage.DispositionKeySkepticism); got != 5 {
		t.Fatalf("disposition skepticism = %d, want 5", got)
	}
	if bank.Mission != "help" {
		t.Fatalf("mission = %q, want help", bank.Mission)
	}

	models, err := store.ListMentalModels(context.Background(), "fresh", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "goals" {
		t.Fatalf("mental models = %+v, want the template's goals model", models)
	}
	directives, err := store.ListDirectives(context.Background(), "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if len(directives) != 1 || directives[0].Name != "tone" {
		t.Fatalf("directives = %+v, want the template's tone directive", directives)
	}
}

// The hook is a provisioning step, not a sync: replacing an existing bank must
// not re-create resources the bank no longer has.
func TestDefaultBankTemplateNotReappliedOnUpdate(t *testing.T) {
	store := templateStore(t)
	withTemplate(t, store, map[string]any{
		"version":       "1",
		"mental_models": []any{map[string]any{"id": "goals", "name": "Goals", "source_query": "what goals?"}},
	})
	mux := newMux(NewHandlers(store))

	if rec := doJSON(t, mux, http.MethodPut, "/v1/default/banks/b1", `{"name":"One"}`); rec.Code != http.StatusOK {
		t.Fatalf("create = %d, body = %s", rec.Code, rec.Body.String())
	}
	if err := store.DeleteMentalModel(context.Background(), "b1", "goals"); err != nil {
		t.Fatal(err)
	}

	if rec := doJSON(t, mux, http.MethodPut, "/v1/default/banks/b1", `{"name":"Renamed"}`); rec.Code != http.StatusOK {
		t.Fatalf("replace = %d, body = %s", rec.Code, rec.Body.String())
	}

	models, err := store.ListMentalModels(context.Background(), "b1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 0 {
		t.Fatalf("update re-applied the template: models = %+v", models)
	}
}

// A malformed server-owned template is logged and ignored: it must not fail the
// bank creation it would provision.
func TestMalformedDefaultBankTemplateDoesNotFailCreate(t *testing.T) {
	store := templateStore(t)
	withTemplate(t, store, map[string]any{"version": "999"})
	mux := newMux(NewHandlers(store))

	rec := doJSON(t, mux, http.MethodPut, "/v1/default/banks/b1", `{"name":"One"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create under a bad template = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, err := store.GetBank(context.Background(), "b1"); err != nil {
		t.Fatalf("the bank was not created: %v", err)
	}
	models, err := store.ListMentalModels(context.Background(), "b1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 0 {
		t.Fatalf("a rejected template must apply nothing, got %+v", models)
	}
}

// The configuration write route creates a missing bank lazily; that fresh bank
// receives the default template too.
func TestDefaultBankTemplateAppliedOnLazyConfigCreate(t *testing.T) {
	store := templateStore(t)
	withTemplate(t, store, map[string]any{
		"version":    "1",
		"directives": []any{map[string]any{"name": "tone", "content": "be nice"}},
	})
	h := NewHandlers(store).WithConfigAPI(true)
	mux := newMux(h)

	rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/lazy/config", `{"updates":{"retain_chunk_size":300}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("config write = %d, body = %s", rec.Code, rec.Body.String())
	}
	directives, err := store.ListDirectives(context.Background(), "lazy")
	if err != nil {
		t.Fatal(err)
	}
	if len(directives) != 1 || directives[0].Name != "tone" {
		t.Fatalf("lazily-created bank directives = %+v, want the template's tone directive", directives)
	}
}

// The configuration write route must not provision an already-existing bank:
// only the fresh create gets the template.
func TestDefaultBankTemplateSkippedOnExistingConfigWrite(t *testing.T) {
	store := templateStore(t)
	mustCreate(t, store, storage.Bank{BankID: "b1", Name: "One"})
	withTemplate(t, store, map[string]any{
		"version":    "1",
		"directives": []any{map[string]any{"name": "tone", "content": "be nice"}},
	})
	h := NewHandlers(store).WithConfigAPI(true)
	mux := newMux(h)

	rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config", `{"updates":{"retain_chunk_size":300}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("config write = %d, body = %s", rec.Code, rec.Body.String())
	}
	directives, err := store.ListDirectives(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if len(directives) != 0 {
		t.Fatalf("an existing bank was re-provisioned: directives = %+v", directives)
	}
}
