package memoryops

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// failingBankConfigStore fails every bank-configuration read, modelling the
// transient failure that must not let a write through unscreened.
type failingBankConfigStore struct {
	storage.Store
	err error
}

func (s failingBankConfigStore) BankConfig(context.Context, string) (map[string]any, error) {
	return nil, s.err
}

// defenseConfiguredStore builds a served handler set whose bank has a Memory
// Defense redact policy but whose configuration cannot be read.
func defenseConfiguredStore(t *testing.T) (*http.ServeMux, *storage.MemoryStore) {
	t.Helper()
	base := storage.NewMemoryStore("public")
	if _, err := base.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"memory_defense": defensePolicy("redact")}
	if _, err := base.UpdateBank(context.TODO(), "b1", storage.BankPatch{Config: &cfg}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(failingBankConfigStore{Store: base, err: errors.New("config read failed")}, 5, 30000)
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	return mux, base
}

func assertNothingStored(t *testing.T, store *storage.MemoryStore) {
	t.Helper()
	page, err := store.ListMemories(context.TODO(), storage.MemoryQuery{BankID: "b1", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Memories) != 0 {
		t.Fatalf("stored %d memories despite an unreadable bank configuration: %+v", len(page.Memories), page.Memories)
	}
}

// A write must not proceed unscreened when the bank's configuration cannot be
// read: the bank configured Memory Defense, so an unreadable read refuses the
// write rather than storing the secret verbatim on exactly the failing request.
func TestRetainRefusesWhenBankConfigUnreadable(t *testing.T) {
	mux, store := defenseConfiguredStore(t)
	secret := "ghp_" + strings.Repeat("a", 36)

	rec := post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"content":"token `+secret+`"}]}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s; want 503 rather than an unscreened write", rec.Code, rec.Body.String())
	}
	assertNothingStored(t, store)
}

// The file route shares the refusal: a configuration that cannot be read must
// not let an upload through without the bank's Memory Defense policy.
func TestFileRetainRefusesWhenBankConfigUnreadable(t *testing.T) {
	mux, store := defenseConfiguredStore(t)
	secret := "ghp_" + strings.Repeat("a", 36)

	rec := uploadFile(t, mux, "notes.txt", "token "+secret, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s; want 503 rather than an unscreened upload", rec.Code, rec.Body.String())
	}
	assertNothingStored(t, store)
}
