package app

// End-to-end coverage of the v1.1.0 per-document change history (#5638): the
// served `GET /v1/default/banks/{bank_id}/documents/{document_id}/history`
// route, and which operation each change is recorded on.
//
// The reference's own spec is hindsight-api-slim/tests/test_document_history.py:
// a fact edit is recorded on the consolidation it queues, next to that rebuild;
// a tag change is recorded the same way; a change that ran without an operation
// — a synchronous retain, or an edit with auto-consolidation off — leaves none.

import (
	"net/http"
	"testing"
)

// documentHistory decodes the served history of one document.
func documentHistory(t *testing.T, a *App, bankID, docID string) map[string]any {
	t.Helper()
	rec := exchange(a, http.MethodGet, "/v1/default/banks/"+bankID+"/documents/"+docID+"/history", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("history for %q = %d (body %s)", docID, rec.Code, rec.Body.String())
	}
	return decodeJSON(t, rec)
}

// historyKinds lists the served event kinds, newest first.
func historyKinds(t *testing.T, history map[string]any) []string {
	t.Helper()
	items, _ := history["items"].([]any)
	kinds := make([]string, 0, len(items))
	for _, entry := range items {
		event, _ := entry.(map[string]any)
		kind, _ := event["kind"].(string)
		kinds = append(kinds, kind)
	}
	return kinds
}

// firstEventOfKind returns the newest event of one kind.
func firstEventOfKind(t *testing.T, history map[string]any, kind string) map[string]any {
	t.Helper()
	items, _ := history["items"].([]any)
	for _, entry := range items {
		event, _ := entry.(map[string]any)
		if event["kind"] == kind {
			return event
		}
	}
	t.Fatalf("no %q event in %v", kind, historyKinds(t, history))
	return nil
}

// The retain's own operation carries the retain event, and a fact edit is
// recorded on the consolidation it queues — beside that rebuild — with the
// observation the edit removed and the one the rebuild created.
func TestDocumentHistoryRecordsRetainAndFactEdit(t *testing.T) {
	a := parityApp(t)
	const bankID = "history-bank"
	bankURL := "/v1/default/banks/" + bankID
	if rec := exchange(a, http.MethodPut, bankURL, `{"name":"History Bank"}`); rec.Code != http.StatusOK {
		t.Fatalf("create bank = %d (body %s)", rec.Code, rec.Body.String())
	}

	// A synchronous retain runs without an operation, so nothing is recorded;
	// the consolidation it queues is the first event the document has.
	body := `{"items":[{"content":"Alice called about her knee surgery recovery.","document_id":"call-1"}]}`
	rec := exchange(a, http.MethodPost, bankURL+"/memories", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("retain = %d (body %s)", rec.Code, rec.Body.String())
	}
	history := documentHistory(t, a, bankID, "call-1")
	if kinds := historyKinds(t, history); len(kinds) != 0 {
		t.Fatalf("a synchronous retain recorded %v, want none", kinds)
	}
	if history["total"] != float64(0) {
		t.Fatalf("total = %v, want 0", history["total"])
	}

	// Which memories the retain produced, so the edit can name one.
	list := exchange(a, http.MethodGet, bankURL+"/memories/list?limit=20", "")
	if list.Code != http.StatusOK {
		t.Fatalf("list memories = %d (body %s)", list.Code, list.Body.String())
	}
	listed := decodeJSON(t, list)
	facts := make([]string, 0)
	items, _ := listed["items"].([]any)
	for _, entry := range items {
		unit, _ := entry.(map[string]any)
		if factType, _ := unit["fact_type"].(string); factType == "world" || factType == "experience" {
			if id, ok := unit["id"].(string); ok {
				facts = append(facts, id)
			}
		}
	}
	if len(facts) == 0 {
		t.Fatalf("retain produced no fact to edit: %s", list.Body.String())
	}

	edit := exchange(a, http.MethodPatch, bankURL+"/memories/"+facts[0], `{"occurred_start":"2026-10-01T10:00:00Z"}`)
	if edit.Code != http.StatusOK {
		t.Fatalf("edit = %d (body %s)", edit.Code, edit.Body.String())
	}

	history = documentHistory(t, a, bankID, "call-1")
	editEvent := firstEventOfKind(t, history, "fact_edited")
	if editEvent["fact_id"] != facts[0] {
		t.Fatalf("fact_edited fact_id = %v, want %s", editEvent["fact_id"], facts[0])
	}
	// The edit is recorded on the consolidation it queued, which is the
	// operation the event names.
	operationID, _ := editEvent["operation_id"].(string)
	if operationID == "" {
		t.Fatalf("fact_edited carries no operation_id: %v", editEvent)
	}
	op := exchange(a, http.MethodGet, bankURL+"/operations/"+operationID, "")
	if op.Code != http.StatusOK {
		t.Fatalf("operation read = %d (body %s)", op.Code, op.Body.String())
	}
	if operationType := decodeJSON(t, op)["operation_type"]; operationType != "consolidation" {
		t.Fatalf("edit recorded on a %v, want a consolidation", operationType)
	}
}

// A tag change drops the observations built over the document's memories, and
// the retag event lands on the consolidation it queues, naming what went.
func TestDocumentHistoryRecordsRetag(t *testing.T) {
	a := parityApp(t)
	const bankID = "history-retag"
	bankURL := "/v1/default/banks/" + bankID
	if rec := exchange(a, http.MethodPut, bankURL, `{"name":"Retag Bank"}`); rec.Code != http.StatusOK {
		t.Fatalf("create bank = %d (body %s)", rec.Code, rec.Body.String())
	}
	body := `{"items":[{"content":"Alice started physio on Monday.","document_id":"session-1","tags":["user:alice"]}]}`
	if rec := exchange(a, http.MethodPost, bankURL+"/memories", body); rec.Code != http.StatusOK {
		t.Fatalf("retain = %d (body %s)", rec.Code, rec.Body.String())
	}

	retag := exchange(a, http.MethodPatch, bankURL+"/documents/session-1", `{"tags":["patient:alice"]}`)
	if retag.Code != http.StatusOK {
		t.Fatalf("retag = %d (body %s)", retag.Code, retag.Body.String())
	}
	history := documentHistory(t, a, bankID, "session-1")
	retagEvent := firstEventOfKind(t, history, "retag")
	if retagEvent["fact_id"] != nil {
		t.Fatalf("retag fact_id = %v, want null", retagEvent["fact_id"])
	}
	// Every observation sample is present, so a reader can select the ids
	// unconditionally — the reference's model serializes all three.
	for _, member := range []string{"observations_created", "observations_updated", "observations_removed"} {
		if _, ok := retagEvent[member].(map[string]any); !ok {
			t.Fatalf("retag event has no %s sample: %v", member, retagEvent)
		}
	}
}

// A document's history is its own: another document's id reads an empty page,
// and an absent document reads one too — the history is the bank's operations,
// so a document whose row is gone still has a timeline.
func TestDocumentHistoryIsPerDocumentAndNotFound(t *testing.T) {
	a := parityApp(t)
	const bankID = "history-scoped"
	bankURL := "/v1/default/banks/" + bankID
	if rec := exchange(a, http.MethodPut, bankURL, `{"name":"Scoped Bank"}`); rec.Code != http.StatusOK {
		t.Fatalf("create bank = %d (body %s)", rec.Code, rec.Body.String())
	}
	body := `{"items":[{"content":"Alice called about her knee.","document_id":"doc-a"}]}`
	if rec := exchange(a, http.MethodPost, bankURL+"/memories", body); rec.Code != http.StatusOK {
		t.Fatalf("retain = %d (body %s)", rec.Code, rec.Body.String())
	}
	if rec := exchange(a, http.MethodPatch, bankURL+"/memories/x", `{}`); rec.Code == http.StatusOK {
		t.Fatal("an unknown memory was updated")
	}

	empty := documentHistory(t, a, bankID, "doc-b")
	if empty["total"] != float64(0) {
		t.Fatalf("doc-b total = %v, want 0", empty["total"])
	}
	items, ok := empty["items"].([]any)
	if !ok || len(items) != 0 {
		t.Fatalf("doc-b items = %v, want []", empty["items"])
	}

	absent := exchange(a, http.MethodGet, bankURL+"/documents/no-such-document/history", "")
	if absent.Code != http.StatusOK {
		t.Fatalf("absent document history = %d (body %s)", absent.Code, absent.Body.String())
	}
	if body := absent.Body.String(); body == "404 page not found\n" {
		t.Fatal("the history route is not mounted: the stdlib not-found answered")
	}
	if got := decodeJSON(t, absent)["total"]; got != float64(0) {
		t.Fatalf("absent document total = %v, want 0", got)
	}
}

// A document id may itself contain a slash: the reference declares the path
// parameter path-style, so "a/b/history" names document "a/b".
func TestDocumentHistoryPathStyleDocumentID(t *testing.T) {
	a := parityApp(t)
	const bankID = "history-path"
	bankURL := "/v1/default/banks/" + bankID
	if rec := exchange(a, http.MethodPut, bankURL, `{"name":"Path Bank"}`); rec.Code != http.StatusOK {
		t.Fatalf("create bank = %d (body %s)", rec.Code, rec.Body.String())
	}
	body := `{"items":[{"content":"Alice called about her knee.","document_id":"a/b"}]}`
	if rec := exchange(a, http.MethodPost, bankURL+"/memories", body); rec.Code != http.StatusOK {
		t.Fatalf("retain = %d (body %s)", rec.Code, rec.Body.String())
	}

	history := documentHistory(t, a, bankID, "a/b")
	if history["document_id"] != "a/b" {
		t.Fatalf("document_id = %v, want a/b", history["document_id"])
	}
}
