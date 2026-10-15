package memoryops

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// recordingDefenseNotifier captures the deliveries a Memory Defense decision
// would enqueue.
type recordingDefenseNotifier struct {
	bankIDs []string
	calls   []map[string]any
	// events records the event type of every call, so a test can assert what a
	// request raised rather than only that something was raised.
	events []string
}

// defenseCalls returns the calls raised for the memory_defense.triggered event,
// which is the only event a defense decision raises.
func (r *recordingDefenseNotifier) defenseCalls() []map[string]any {
	out := make([]map[string]any, 0, len(r.calls))
	for i, event := range r.events {
		if event == "memory_defense.triggered" {
			out = append(out, r.calls[i])
		}
	}
	return out
}

func (r *recordingDefenseNotifier) EnqueueWebhookEvent(_ context.Context, bankID, eventType string, data map[string]any) {
	r.bankIDs = append(r.bankIDs, bankID)
	r.events = append(r.events, eventType)
	r.calls = append(r.calls, data)
}

// defenseServer builds a served handler set with an optional memory_defense
// config and notifier.
func defenseServer(t *testing.T, policy map[string]any, notifier WebhookNotifier) (*http.ServeMux, *storage.MemoryStore) {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	if policy != nil {
		config := map[string]any{"memory_defense": policy}
		if _, err := store.UpdateBank(context.TODO(), "b1", storage.BankPatch{Config: &config}); err != nil {
			t.Fatal(err)
		}
	}
	h := NewHandlers(store, 5, 30000)
	h.Webhooks = notifier
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	return mux, store
}

func defensePolicy(action string) map[string]any {
	return map[string]any{
		"enabled": true,
		"rules":   []any{map[string]any{"on": "sensitive_data", "action": action}},
	}
}

func storedTexts(t *testing.T, store *storage.MemoryStore) []string {
	t.Helper()
	page, err := store.ListMemories(context.TODO(), storage.MemoryQuery{BankID: "b1", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	texts := make([]string, 0, len(page.Memories))
	for _, unit := range page.Memories {
		texts = append(texts, unit.Text)
	}
	return texts
}

// A secret-shaped string in a redacted bank is stored as its placeholder, not
// verbatim, and the response carries no defense_findings field.
func TestRedactRuleStoresPlaceholder(t *testing.T) {
	secret := "ghp_" + strings.Repeat("a", 36)
	notifier := &recordingDefenseNotifier{}
	mux, store := defenseServer(t, defensePolicy("redact"), notifier)

	rec := post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"content":"my token is `+secret+`"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "defense_findings") {
		t.Fatalf("the retain response must not carry defense_findings: %s", rec.Body.String())
	}
	texts := storedTexts(t, store)
	if len(texts) != 1 {
		t.Fatalf("stored units = %v, want one", texts)
	}
	if strings.Contains(texts[0], secret) {
		t.Fatalf("the raw secret was stored: %q", texts[0])
	}
	if !strings.Contains(texts[0], "[REDACTED:github_token]") {
		t.Fatalf("the stored memory must carry the placeholder: %q", texts[0])
	}

	if calls := notifier.defenseCalls(); len(calls) != 1 {
		t.Fatalf("defense events = %d, want exactly one", len(calls))
	}
	assertDefenseEvent(t, notifier.defenseCalls()[0], notifier.bankIDs[0], "redact", secret)
}

// A batch the policy blocks in full is refused: the caller asked for something
// to be remembered and nothing was, so it is a client error about their content
// rather than a successful write of nothing. The reference answers 422 with the
// violations under `detail` — an object rather than its usual sentence — so a
// caller can see which item matched what.
func TestBlockRuleRefusesABatchItBlocksInFull(t *testing.T) {
	secret := "ghp_" + strings.Repeat("b", 36)
	notifier := &recordingDefenseNotifier{}
	mux, store := defenseServer(t, defensePolicy("block"), notifier)

	rec := post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"content":"my token is `+secret+`"}]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Detail struct {
			Violations []struct {
				Index    int    `json:"index"`
				Detector string `json:"detector"`
				Message  string `json:"message"`
			} `json:"violations"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode the refusal: %v (%s)", err, rec.Body.String())
	}
	if len(body.Detail.Violations) != 1 {
		t.Fatalf("violations = %+v, want exactly the one blocked item", body.Detail.Violations)
	}
	violation := body.Detail.Violations[0]
	if violation.Index != 0 || violation.Detector != "sensitive_data" || violation.Message == "" {
		t.Fatalf("violation = %+v, want index 0, the detector that matched, and its message", violation)
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Error("a refusal must not echo the blocked content")
	}
	if texts := storedTexts(t, store); len(texts) != 0 {
		t.Fatalf("a blocked item must not be stored: %v", texts)
	}
	if calls := notifier.defenseCalls(); len(calls) != 1 {
		t.Fatalf("defense events = %d, want exactly one", len(calls))
	}
	assertDefenseEvent(t, notifier.defenseCalls()[0], notifier.bankIDs[0], "block", secret)
}

// A partial block is a smaller write, not a refusal: the surviving items are
// stored and the caller sees a result for them.
func TestBlockRuleDropsOnlyTheBlockedItems(t *testing.T) {
	secret := "ghp_" + strings.Repeat("c", 36)
	notifier := &recordingDefenseNotifier{}
	mux, store := defenseServer(t, defensePolicy("block"), notifier)

	rec := post(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"my token is `+secret+`"},{"content":"Alice moved to Berlin"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 because one item survives, body = %s", rec.Code, rec.Body.String())
	}
	texts := storedTexts(t, store)
	if len(texts) != 1 || !strings.Contains(texts[0], "Alice moved to Berlin") {
		t.Fatalf("stored = %v, want only the surviving item", texts)
	}
	if strings.Contains(strings.Join(texts, " "), secret) {
		t.Fatal("the blocked item must not reach storage")
	}
	if calls := notifier.defenseCalls(); len(calls) != 1 {
		t.Fatalf("defense events = %d, want exactly one", len(calls))
	}
}

// A disabled policy stores the secret untouched.
func TestDisabledPolicyStoresUntouched(t *testing.T) {
	secret := "ghp_" + strings.Repeat("c", 36)
	mux, store := defenseServer(t, map[string]any{"enabled": false, "rules": []any{map[string]any{"on": "sensitive_data", "action": "redact"}}}, nil)

	rec := post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"content":"my token is `+secret+`"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	texts := storedTexts(t, store)
	if len(texts) != 1 || !strings.Contains(texts[0], secret) {
		t.Fatalf("a disabled policy must store the text verbatim: %v", texts)
	}
}

// assertDefenseEvent checks the webhook payload names the decision and never
// leaks the raw secret.
func assertDefenseEvent(t *testing.T, data map[string]any, bankID, action, secret string) {
	t.Helper()
	if bankID != "b1" {
		t.Fatalf("event bank = %q", bankID)
	}
	if data["action"] != action {
		t.Fatalf("event action = %v, want %v", data["action"], action)
	}
	if data["detector"] != "sensitive_data" {
		t.Fatalf("event detector = %v", data["detector"])
	}
	types, _ := data["matched_types"].([]string)
	if len(types) != 1 || types[0] != "github_token" {
		t.Fatalf("event matched_types = %v", data["matched_types"])
	}
	hits, _ := data["hits"].([]map[string]any)
	if len(hits) == 0 {
		t.Fatalf("event hits = %v, want at least one", data["hits"])
	}
	for _, hit := range hits {
		if hit["detector"] != "github_token" {
			t.Fatalf("hit detector = %v", hit["detector"])
		}
		preview, _ := hit["preview"].(string)
		if preview == "" || preview == secret || strings.Contains(preview, secret) {
			t.Fatalf("hit preview leaks the raw secret: %q", preview)
		}
	}
}
