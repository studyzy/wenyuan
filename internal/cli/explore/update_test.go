package explore

// Tests for the event loop: the key matrix, cursor semantics, pagination,
// the query supersede id, the 5 s auto-refresh timer and delete handling.
// Keys drive the model through update(); query commands are executed
// synchronously so their HTTP calls hit the httptest server.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// key builds the KeyMsg for a test key name: named special keys plus
// printable runes (each string becomes one KeyRunes press).
func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "delete":
		return tea.KeyMsg{Type: tea.KeyDelete}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// stubRoutes answers the endpoints the tests touch with fixed payloads. The
// banks total is 3; the memories list has 2 rows on offset 0 and none
// beyond; documents carry one row.
func stubRoutes(t *testing.T) (apiClient, *recorder) {
	t.Helper()
	return serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/default/banks/") && strings.HasSuffix(r.URL.Path, "/memories/list"):
			if r.URL.Query().Get("offset") == "0" {
				writeJSON(w, `{"items":[{"id":"m1","text":"first memory","fact_type":"world"},
					{"id":"m2","text":"second memory","fact_type":"experience"}],"total":2}`)
				return
			}
			writeJSON(w, `{"items":[],"total":2}`)
		case strings.HasPrefix(r.URL.Path, "/v1/default/banks/") && strings.HasSuffix(r.URL.Path, "/entities"):
			writeJSON(w, `{"items":[]}`)
		case strings.HasPrefix(r.URL.Path, "/v1/default/banks/") && strings.HasSuffix(r.URL.Path, "/documents"):
			writeJSON(w, `{"items":[{"id":"d1","created_at":"2026-01-01T00:00:00Z"}],"total":1}`)
		case strings.HasPrefix(r.URL.Path, "/v1/default/banks/") && strings.Contains(r.URL.Path, "/memories/recall"):
			writeJSON(w, `{"results":[{"id":"r1","text":"a hit","type":"world"}]}`)
		case strings.HasPrefix(r.URL.Path, "/v1/default/banks/") && strings.HasSuffix(r.URL.Path, "/reflect"):
			writeJSON(w, `{"text":"the answer"}`)
		case strings.HasPrefix(r.URL.Path, "/v1/default/banks/"):
			writeJSON(w, `{"id":"d1","created_at":"2026-01-01T00:00:00Z","original_text":"doc body"}`)
		default:
			writeJSON(w, `{"banks":[{"bank_id":"a","name":"Alpha"},
				{"bank_id":"b","name":"Beta"},{"bank_id":"c","name":""}],"total":3}`)
		}
	})
}

func newTestModel(t *testing.T) (*model, *recorder) {
	t.Helper()
	api, rec := stubRoutes(t)
	return newModel(api, context.Background()), rec
}

// press feeds one key press to the model.
func (m *model) press(k string) {
	m.update(key(k))
}

// enterQuery navigates a loaded model into the Query view with edit mode on.
func (m *model) enterQuery() {
	m.press("enter") // banks -> memories, bank a selected
	m.press("/")     // memories -> query, editing
	if m.view != viewQuery || !m.editing {
		panic("enterQuery: navigation failed")
	}
}

func TestBanksCursorWrap(t *testing.T) {
	m, _ := newTestModel(t)
	m.loadBanks(false)
	if m.bankCur != 0 {
		t.Fatalf("initial cursor = %d, want 0 (first row preselected)", m.bankCur)
	}
	m.press("j")
	m.press("j")
	if m.bankCur != 2 {
		t.Fatalf("cursor = %d, want 2", m.bankCur)
	}
	m.press("j") // wraps to first
	if m.bankCur != 0 {
		t.Errorf("cursor = %d, want 0 (wrap to first)", m.bankCur)
	}
	m.press("k") // wraps to last
	if m.bankCur != 2 {
		t.Errorf("cursor = %d, want 2 (wrap to last)", m.bankCur)
	}
}

func TestBanksCursorClampOnReload(t *testing.T) {
	calls := 0
	api, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			writeJSON(w, `{"banks":[{"bank_id":"a","name":"A"},{"bank_id":"b","name":"B"},
				{"bank_id":"c","name":"C"}],"total":3}`)
			return
		}
		writeJSON(w, `{"banks":[{"bank_id":"a","name":"A"}],"total":1}`)
	})
	m := newModel(api, context.Background())
	m.loadBanks(false)
	m.bankCur = 2
	m.press("R") // the reload shrinks the list
	if m.bankCur != 0 {
		t.Errorf("cursor = %d, want 0 (clamped to last row)", m.bankCur)
	}
	// An empty reload has no selection.
	m.api, _ = serve(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"banks":[],"total":0}`)
	})
	m.press("R")
	if m.bankCur != -1 {
		t.Errorf("cursor = %d, want -1 (empty list)", m.bankCur)
	}
}

func TestEnterOnBanksOpensMemories(t *testing.T) {
	m, rec := newTestModel(t)
	m.loadBanks(false)
	m.press("enter")
	if m.view != viewMemories {
		t.Fatalf("view = %v, want Memories", m.view)
	}
	if m.bankID != "a" {
		t.Errorf("bankID = %q, want a", m.bankID)
	}
	if len(m.history) != 1 {
		t.Errorf("history = %d entries, want 1", len(m.history))
	}
	if m.memCur != 0 || len(m.memories) != 2 {
		t.Errorf("memories = %d rows, cursor %d", len(m.memories), m.memCur)
	}
	want := "Loaded 2 memories (limit: 500, offset: 0)"
	if m.status != want {
		t.Errorf("status = %q, want %q", m.status, want)
	}
	if !strings.Contains(rec.last().URI, "/memories/list?limit=500&offset=0") {
		t.Errorf("request = %s", rec.last().URI)
	}
}

func TestSlashFromBanksErrors(t *testing.T) {
	m, _ := newTestModel(t)
	m.loadBanks(false)
	m.press("/")
	if m.errMsg != "Select a bank first" {
		t.Errorf("errMsg = %q, want Select a bank first", m.errMsg)
	}
	if m.view != viewBanks {
		t.Errorf("view = %v, want Banks", m.view)
	}
}

func TestSlashWithoutBankErrors(t *testing.T) {
	m, _ := newTestModel(t)
	m.view = viewEntities // bank-scoped view shape, but no bank was ever selected
	m.press("/")
	if m.errMsg != "No bank selected" {
		t.Errorf("errMsg = %q, want No bank selected", m.errMsg)
	}
}

func TestSlashFromMemoriesOpensQueryEditing(t *testing.T) {
	m, _ := newTestModel(t)
	m.loadBanks(false)
	m.press("enter")
	m.press("/")
	if m.view != viewQuery || !m.editing {
		t.Fatalf("view/editing = %v/%v, want Query/true", m.view, m.editing)
	}
	if m.bankID != "a" {
		t.Errorf("bankID = %q, want a", m.bankID)
	}
	if len(m.history) != 2 {
		t.Errorf("history = %d entries, want 2", len(m.history))
	}
}

func TestEditModeKeysAndRecallExecution(t *testing.T) {
	m, rec := newTestModel(t)
	m.loadBanks(false)
	m.enterQuery()
	for _, k := range []string{"h", "i"} {
		m.press(k)
	}
	if m.queryText != "hi" {
		t.Fatalf("queryText = %q, want hi", m.queryText)
	}
	m.press("backspace")
	if m.queryText != "h" {
		t.Fatalf("queryText after backspace = %q, want h", m.queryText)
	}
	m.press(" ")
	if m.queryText != "h " {
		t.Fatalf("space not appended: %q", m.queryText)
	}
	next, cmd := m.update(key("enter"))
	if m.editing {
		t.Error("still editing after submit")
	}
	if !m.loading {
		t.Error("loading not set")
	}
	if m.queryID != 1 {
		t.Errorf("queryID = %d, want 1", m.queryID)
	}
	if cmd == nil {
		t.Fatal("no query command returned")
	}
	msg := cmd() // synchronous execution against the stub server
	next.update(msg)
	if m.loading {
		t.Error("loading not cleared after result")
	}
	want := "Found 1 results"
	if m.status != want {
		t.Errorf("status = %q, want %q", m.status, want)
	}
	if len(m.recallRows) != 1 || m.recallCur != 0 {
		t.Errorf("rows = %d, cursor = %d", len(m.recallRows), m.recallCur)
	}
	body := rec.last().Body
	for _, want := range []string{`"query":"h "`, `"budget":"mid"`, `"max_tokens":4096`, `"trace":false`, `"tags_match":"any"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body %s missing %s", body, want)
		}
	}
	if next == nil {
		t.Error("update returned nil model")
	}
}

func TestEmptyQuerySubmitStaysEditing(t *testing.T) {
	m, rec := newTestModel(t)
	m.loadBanks(false)
	m.enterQuery()
	m.press("enter")
	if m.errMsg != "Query cannot be empty" {
		t.Errorf("errMsg = %q", m.errMsg)
	}
	if !m.editing {
		t.Error("edit mode must stay on after an empty submit")
	}
	if rec.count() != 2 { // banks + memories only; no recall request
		t.Errorf("requests = %d, want 2", rec.count())
	}
}

func TestEditEscKeepsText(t *testing.T) {
	m, _ := newTestModel(t)
	m.loadBanks(false)
	m.enterQuery()
	m.press("h")
	m.press("i")
	m.press("esc")
	if m.editing {
		t.Error("edit mode still on after esc")
	}
	if m.queryText != "hi" {
		t.Errorf("queryText = %q, want hi (text kept on cancel)", m.queryText)
	}
}

func TestQuerySupersedeDropsStaleResult(t *testing.T) {
	m, _ := newTestModel(t)
	m.loadBanks(false)
	m.enterQuery()
	m.press("a")
	_, cmd1 := m.update(key("enter"))
	m.press("/") // back into edit mode
	m.press("b")
	_, cmd2 := m.update(key("enter"))
	if m.queryID != 2 {
		t.Fatalf("queryID = %d, want 2", m.queryID)
	}
	// The first query's late result is dropped: it is no longer current.
	// Loading stays on: the second query is still in flight.
	m.update(queryDoneMsg{id: 1, mode: modeRecall, rows: []recallRow{{Text: "stale"}}})
	if len(m.recallRows) != 0 {
		t.Errorf("stale result applied: rows %d", len(m.recallRows))
	}
	m.update(cmd1()) // executing the abandoned command must not apply either
	if len(m.recallRows) != 0 {
		t.Errorf("stale result applied after cmd run: rows %d", len(m.recallRows))
	}
	m.update(cmd2())
	if len(m.recallRows) != 1 {
		t.Errorf("rows = %d, want 1 (current result applied)", len(m.recallRows))
	}
}

func TestAutoRefreshFiresAndResets(t *testing.T) {
	m, rec := newTestModel(t)
	m.loadBanks(false)
	m.press("enter") // memories view
	base := rec.count()
	m.lastRefresh = time.Now().Add(-6 * time.Second)
	m.update(tickMsg{})
	if rec.count() != base+1 {
		t.Fatalf("requests = %d, want %d (auto-refresh refetched)", rec.count(), base+1)
	}
	if time.Since(m.lastRefresh) >= autoRefreshInterval {
		t.Error("timer not reset by the auto-refresh")
	}
	// Before the interval elapses the tick does nothing.
	m.update(tickMsg{})
	if rec.count() != base+1 {
		t.Errorf("requests = %d, want %d (tick fired early)", rec.count(), base+1)
	}
}

func TestAutoRefreshSkipsQueryView(t *testing.T) {
	m, rec := newTestModel(t)
	m.loadBanks(false)
	m.enterQuery()
	base := rec.count()
	m.lastRefresh = time.Now().Add(-6 * time.Second)
	m.update(tickMsg{})
	if rec.count() != base {
		t.Errorf("requests = %d, want %d (Query never auto-refreshes)", rec.count(), base)
	}
	if time.Since(m.lastRefresh) >= autoRefreshInterval {
		t.Error("timer must still reset on the Query view")
	}
}

func TestManualRefreshDoesNotResetTimer(t *testing.T) {
	m, rec := newTestModel(t)
	m.loadBanks(false)
	m.press("enter")
	old := time.Now().Add(-6 * time.Second)
	m.lastRefresh = old
	m.press("R")
	if rec.count() != 3 {
		t.Fatalf("requests = %d, want 3 (R refetched)", rec.count())
	}
	if !m.lastRefresh.Equal(old) {
		t.Error("manual R reset the auto-refresh timer")
	}
	if m.status != "Loaded 2 memories (limit: 500, offset: 0)" {
		t.Errorf("status = %q", m.status)
	}
	m.update(tickMsg{})
	if rec.count() != 4 {
		t.Errorf("requests = %d, want 4 (timer was still due)", rec.count())
	}
}

func TestRefreshFailureKeepsDataAndReports(t *testing.T) {
	m, _ := newTestModel(t)
	m.loadBanks(false)
	m.press("enter")
	kept := m.memories
	// Point the model at a failing route: the handler 500s on memories/list.
	m.api, _ = serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("down"))
	})
	m.press("R")
	if len(m.memories) != len(kept) {
		t.Errorf("data cleared on refresh failure")
	}
	if m.errMsg == "" || !strings.Contains(m.errMsg, "500") {
		t.Errorf("errMsg = %q, want the API failure", m.errMsg)
	}
}

func TestMemoriesPagination(t *testing.T) {
	m, rec := newTestModel(t)
	m.loadBanks(false)
	m.press("enter")
	m.press("n")
	if m.memOffset != 500 {
		t.Errorf("offset = %d, want 500", m.memOffset)
	}
	if !strings.Contains(rec.last().URI, "offset=500") {
		t.Errorf("request = %s", rec.last().URI)
	}
	m.press("p")
	if m.memOffset != 0 {
		t.Errorf("offset = %d, want 0", m.memOffset)
	}
	m.press("p") // floors at 0
	if m.memOffset != 0 {
		t.Errorf("offset = %d, want 0 (floor)", m.memOffset)
	}
	if !strings.Contains(rec.last().URI, "offset=0") {
		t.Errorf("request = %s", rec.last().URI)
	}
}

func TestMemoriesOffsetSurvivesBankSwitch(t *testing.T) {
	m, rec := newTestModel(t)
	m.loadBanks(false)
	m.press("enter")
	m.press("n") // offset 500
	m.press("esc")
	m.press("down") // second bank
	m.press("enter")
	if m.bankID != "b" {
		t.Fatalf("bankID = %q, want b", m.bankID)
	}
	// Upstream quirk, kept deliberately: the offset is not reset when the
	// bank changes.
	if !strings.Contains(rec.last().URI, "offset=500") {
		t.Errorf("request = %s, want offset=500 (offset survives bank switch)", rec.last().URI)
	}
}

func TestHorizontalScrollBoundsAndReset(t *testing.T) {
	m, _ := newTestModel(t)
	m.loadBanks(false)
	m.press("enter")
	for i := 0; i < 25; i++ {
		m.press("l")
	}
	if m.scroll != hScrollMax {
		t.Errorf("scroll = %d, want %d (cap)", m.scroll, hScrollMax)
	}
	for i := 0; i < 30; i++ {
		m.press("h")
	}
	if m.scroll != 0 {
		t.Errorf("scroll = %d, want 0 (floor)", m.scroll)
	}
	m.press("l") // 10
	m.press("esc")
	if m.scroll != 0 {
		t.Errorf("scroll = %d, want 0 (esc resets)", m.scroll)
	}
	m.press("l") // 10
	m.press("enter")
	if m.scroll != 0 {
		t.Errorf("scroll = %d, want 0 (enter resets)", m.scroll)
	}
	// The offset is shared across table views.
	m.press("l")
	m.view = viewEntities
	if m.scroll != hScrollStep {
		t.Errorf("scroll = %d, want %d (shared offset)", m.scroll, hScrollStep)
	}
}

func TestDeleteDocumentSuccessAndFailure(t *testing.T) {
	m, rec := newTestModel(t)
	m.loadBanks(false)
	m.press("enter")
	m.press("esc")
	m.view = viewDocuments
	m.bankID = "a"
	m.loadDocuments(true)
	base := rec.count()
	m.press("delete")
	if m.status != "Deleted document: d1" {
		t.Errorf("status = %q, want Deleted document: d1", m.status)
	}
	if rec.requests[base].Method != "DELETE" {
		t.Errorf("method = %s, want DELETE", rec.requests[base].Method)
	}
	if rec.count() != base+2 { // the delete plus the silent refetch
		t.Errorf("requests = %d, want %d", rec.count(), base+2)
	}
	// Failure: the error is reported and the list is not refetched. The new
	// server has its own recorder, which must see the delete only.
	failAPI, recFail := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("nope"))
			return
		}
		writeJSON(w, `{"items":[],"total":0}`)
	})
	m.api = failAPI
	m.press("delete")
	if !strings.HasPrefix(m.errMsg, "Failed to delete document:") {
		t.Errorf("errMsg = %q", m.errMsg)
	}
	if recFail.count() != 1 { // the delete only; no refetch
		t.Errorf("requests = %d, want 1 (no refetch after failure)", recFail.count())
	}
}

func TestOverlayCloseThenHistoryPop(t *testing.T) {
	m, _ := newTestModel(t)
	m.loadBanks(false)
	m.press("enter")
	m.press("enter") // open memory detail
	if m.detail != detailMemory {
		t.Fatalf("detail = %v, want memory", m.detail)
	}
	if m.status != "Viewing memory details (Esc to close)" {
		t.Errorf("status = %q", m.status)
	}
	m.press("esc")
	if m.detail != detailNone {
		t.Error("overlay still open")
	}
	if m.status != "Closed memory view" {
		t.Errorf("status = %q, want Closed memory view", m.status)
	}
	if m.view != viewMemories {
		t.Errorf("view = %v, want Memories (history not popped yet)", m.view)
	}
	m.press("esc") // pop history back to Banks
	if m.view != viewBanks {
		t.Errorf("view = %v, want Banks", m.view)
	}
	m.press("esc") // empty history: no-op
	if m.view != viewBanks {
		t.Errorf("view = %v, want Banks (no-op)", m.view)
	}
}

func TestQuitAndHelpKeys(t *testing.T) {
	m, _ := newTestModel(t)
	_, cmd := m.update(key("q"))
	if cmd == nil {
		t.Error("q returned no command (want tea.Quit)")
	}
	_, cmd = m.update(key("ctrl+c"))
	if cmd == nil {
		t.Error("ctrl+c returned no command (want tea.Quit)")
	}
	m.press("?")
	if !m.helpOpen {
		t.Error("help not opened")
	}
	m.press("?")
	if m.helpOpen {
		t.Error("help not closed")
	}
}

func TestQueryControls(t *testing.T) {
	m, _ := newTestModel(t)
	m.loadBanks(false)
	m.enterQuery()
	m.press("esc") // leave edit mode
	m.press("b")
	if m.budget != budgetHigh {
		t.Errorf("budget = %v, want High", m.budget)
	}
	if m.status != "Budget: High" {
		t.Errorf("status = %q", m.status)
	}
	m.press("b")
	m.press("b")
	if m.budget != budgetMid {
		t.Errorf("budget = %v, want Mid (cycle wrapped)", m.budget)
	}
	m.press("+")
	if m.maxTokens != 5120 || m.status != "Max tokens: 5120" {
		t.Errorf("tokens = %d, status = %q", m.maxTokens, m.status)
	}
	m.press("=")
	if m.maxTokens != 6144 {
		t.Errorf("tokens = %d, want 6144 (= increases too)", m.maxTokens)
	}
	m.press("-")
	if m.maxTokens != 5120 {
		t.Errorf("tokens = %d, want 5120", m.maxTokens)
	}
	m.maxTokens = tokenMin
	m.press("-")
	if m.maxTokens != tokenMin {
		t.Errorf("tokens = %d, want floor %d", m.maxTokens, tokenMin)
	}
	m.maxTokens = tokenMax
	m.press("+")
	if m.maxTokens != tokenMax {
		t.Errorf("tokens = %d, want cap %d", m.maxTokens, tokenMax)
	}
	m.press("m")
	if m.mode != modeReflect || m.status != "Switched to Reflect mode" {
		t.Errorf("mode = %v, status = %q", m.mode, m.status)
	}
}

func TestReflectExecutionPinsTokens(t *testing.T) {
	m, rec := newTestModel(t)
	m.loadBanks(false)
	m.enterQuery()
	m.press("esc") // leave edit mode
	m.press("m")   // switch to reflect in normal mode
	m.maxTokens = tokenMax
	m.press("/") // back into edit mode
	m.press("s")
	_, cmd := m.update(key("enter"))
	if cmd == nil {
		t.Fatal("no command")
	}
	m.update(cmd())
	if m.reflectOut != "the answer" {
		t.Errorf("reflectOut = %q", m.reflectOut)
	}
	if m.status != "Reflection complete" {
		t.Errorf("status = %q", m.status)
	}
	if !strings.Contains(rec.last().Body, `"max_tokens":4096`) {
		t.Errorf("reflect body = %s, want max_tokens 4096 (not %d)", rec.last().Body, tokenMax)
	}
	if m.loading {
		t.Error("loading stuck")
	}
}

func TestQueryFailureReportsError(t *testing.T) {
	m, _ := newTestModel(t)
	m.loadBanks(false)
	m.enterQuery()
	// Replace the api with one that always fails.
	m.api, _ = serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"error":"nope"}`))
	})
	m.press("q1")
	_, cmd := m.update(key("enter"))
	m.update(cmd())
	if !strings.HasPrefix(m.errMsg, "Recall failed:") || !strings.Contains(m.errMsg, "418") {
		t.Errorf("errMsg = %q", m.errMsg)
	}
	if m.loading {
		t.Error("loading stuck after failure")
	}
}

func TestWindowSizeAndTickReArm(t *testing.T) {
	m, _ := newTestModel(t)
	m.update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.width != 120 || m.height != 40 {
		t.Errorf("size = %dx%d", m.width, m.height)
	}
	_, cmd := m.update(tickMsg{})
	if cmd == nil {
		t.Error("tick did not re-arm the next tick")
	}
}

func TestCursorNormalizationOnReload(t *testing.T) {
	m, _ := newTestModel(t)
	m.loadBanks(false)
	m.press("enter")
	m.memCur = 5 // beyond the end
	m.loadMemories(false)
	if m.memCur != 1 {
		t.Errorf("cursor = %d, want 1 (clamped to last)", m.memCur)
	}
	m.memCur = -1
	m.loadMemories(false)
	if m.memCur != 0 {
		t.Errorf("cursor = %d, want 0 (no cursor -> first row)", m.memCur)
	}
}
