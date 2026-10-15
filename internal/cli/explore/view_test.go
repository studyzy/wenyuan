package explore

// Snapshot-style assertions over the rendered frame: the four-layer layout,
// the footer priority, the table headers and selection marker, the help
// screen, the query panes and the detail overlays. Styles are stripped so
// the assertions run over plain text.

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestMain pins the color profile so style rendering is deterministic in
// every environment.
func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	os.Exit(m.Run())
}

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

// plain strips ANSI SGR sequences from rendered output.
func plain(s string) string { return ansiRe.ReplaceAllString(s, "") }

// sizedModel returns a model with a fixed frame size and loaded banks.
func sizedModel(t *testing.T) *model {
	t.Helper()
	m, _ := newTestModel(t)
	m.width, m.height = 100, 30
	m.loadBanks(false)
	return m
}

func TestFrameLayoutBanks(t *testing.T) {
	m := sizedModel(t)
	frame := plain(m.View())
	for _, want := range []string{
		"Wenyuan Explorer - Banks",
		"Context: Banks List",
		"<Enter> Select",
		"<R> Refresh",
		"<q> Quit",
		"Banks",
		"a - Alpha",
		"b - Beta",
		"c - Unnamed",
		"Select a bank to start. Press ? for help",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame missing %q\n%s", want, frame)
		}
	}
	// The startup status, not a Loaded status: the initial load is silent.
	if strings.Contains(frame, "Loaded 3 banks") {
		t.Error("initial bank load must not overwrite the startup status")
	}
}

func TestHeaderShowsBankScope(t *testing.T) {
	m := sizedModel(t)
	m.press("enter")
	frame := plain(m.View())
	if !strings.Contains(frame, "Wenyuan Explorer - Memories - [a]") {
		t.Errorf("header missing bank scope\n%s", frame)
	}
	if !strings.Contains(frame, "Context: Memories") || !strings.Contains(frame, "Bank: a") {
		t.Errorf("context box missing bank lines\n%s", frame)
	}
	if !strings.Contains(frame, "Memories (2) - Press Enter to view full text") {
		t.Errorf("panel title missing\n%s", frame)
	}
}

func TestFooterPriority(t *testing.T) {
	m := sizedModel(t)
	m.press("enter")
	m.setError("boom happened")
	m.loading = true
	frame := plain(m.View())
	if !strings.Contains(frame, "Error: boom happened") {
		t.Errorf("error must win the footer\n%s", frame)
	}
	if strings.Contains(frame, "Loading...") || strings.Contains(frame, "Loaded") {
		t.Errorf("footer priority broken\n%s", frame)
	}
	m.errMsg = ""
	frame = plain(m.View())
	if !strings.Contains(frame, "Loading...") {
		t.Errorf("loading must beat status\n%s", frame)
	}
	m.loading = false
	frame = plain(m.View())
	if !strings.Contains(frame, "Loaded 2 memories (limit: 500, offset: 0)") {
		t.Errorf("status footer missing\n%s", frame)
	}
}

func TestMemoriesTableRendersHeaderAndSelection(t *testing.T) {
	m := sizedModel(t)
	m.press("enter")
	frame := plain(m.View())
	for _, want := range []string{
		"TYPE",
		"MENTIONED AT",
		"OCCURRED AT",
		"TEXT",
		">> world",
		"experience",
		"first memory",
		"second memory",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("table missing %q\n%s", want, frame)
		}
	}
}

func TestTableDateAndWindow(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 100, 30
	m.bankID = "a"
	m.view = viewMemories
	m.memories = []memoryRow{{
		ID:          "m1",
		FactType:    "world",
		MentionedAt: "2026-01-02T03:04:05Z",
		Text:        strings.Repeat("x", 120),
	}}
	m.memCur = 0
	frame := plain(m.View())
	// Date-only rendering with the "-" fallback for absent values.
	if !strings.Contains(frame, "2026-01-02") {
		t.Errorf("date-only mentioned_at missing\n%s", frame)
	}
	if !strings.Contains(frame, "-") {
		t.Errorf("missing-date fallback missing\n%s", frame)
	}
	// The 80-rune text window starting at the shared scroll offset.
	if !strings.Contains(frame, strings.Repeat("x", 80)) {
		t.Errorf("80-char window missing\n%s", frame)
	}
	if strings.Contains(frame, strings.Repeat("x", 81)) {
		t.Errorf("window exceeds 80 runes\n%s", frame)
	}
	// A 100-rune offset leaves only a 20-rune tail in the window.
	m.scroll = 100
	frame = plain(m.View())
	if strings.Contains(frame, strings.Repeat("x", 80)) {
		t.Errorf("window did not shift with the scroll offset\n%s", frame)
	}
}

func TestHorizontalWindowOnEntities(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 100, 30
	m.bankID = "a"
	m.view = viewEntities
	m.entities = []entityRow{{Name: "MARK" + strings.Repeat("n", 60), Type: "person", Mentions: 3}}
	m.entCur = 0
	frame := plain(m.View())
	if !strings.Contains(frame, "MARK") {
		t.Errorf("name start missing at offset 0\n%s", frame)
	}
	if !strings.Contains(frame, "NAME") || !strings.Contains(frame, "MENTIONS") {
		t.Errorf("entity header missing\n%s", frame)
	}
	m.scroll = 20 // the marker scrolls out of the 40-rune window
	frame = plain(m.View())
	if strings.Contains(frame, "MARK") {
		t.Errorf("window did not shift with the scroll offset\n%s", frame)
	}
}

func TestHelpScreen(t *testing.T) {
	m := sizedModel(t)
	m.height = 42 // the help panel is taller than a 30-row frame; no min size exists
	m.press("?")
	frame := plain(m.View())
	for _, want := range []string{
		"╭─ Help ",
		"Wenyuan Explorer - Keyboard Shortcuts",
		"Navigation Flow",
		"press / from any view to query (recall/reflect)",
		"Basic Navigation",
		"↑/↓, j/k navigate",
		"Query View",
		"m toggle Recall↔Reflect",
		"b cycle budget Low→Mid→High",
		"General",
		"Press ? to close help",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("help missing %q\n%s", want, frame)
		}
	}
	// Help replaces only the main area: the chrome stays.
	if !strings.Contains(frame, "Context: Banks List") {
		t.Errorf("control bar must stay visible under help\n%s", frame)
	}
}

func TestQueryViewLayout(t *testing.T) {
	m := sizedModel(t)
	m.press("enter")
	m.press("/")
	frame := plain(m.View())
	for _, want := range []string{
		"Recall Query (press / to edit, m to toggle mode)",
		"<Enter> Execute",
		"<Esc> Cancel",
		"Mode: Recall",
		"Budget: Mid | Tokens: 4096",
		"Wenyuan Explorer - Query - [a]",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("query frame missing %q\n%s", want, frame)
		}
	}
}

func TestQueryLoadingPanel(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 100, 30
	m.bankID = "a"
	m.view = viewQuery
	m.loading = true
	frame := plain(m.View())
	for _, want := range []string{
		"Recall in progress",
		"Searching memories...",
		"Please wait while we process your query...",
		"Loading...",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("loading panel missing %q\n%s", want, frame)
		}
	}
	m.mode = modeReflect
	frame = plain(m.View())
	if !strings.Contains(frame, "Reflect in progress") || !strings.Contains(frame, "Reflecting on memories...") {
		t.Errorf("reflect loading panel wrong\n%s", frame)
	}
}

func TestRecallResultsPanel(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 100, 30
	m.bankID = "a"
	m.view = viewQuery
	m.recallRows = []recallRow{
		{ID: "r1", Type: "world", OccStart: "2026-01-01T00:00:00Z", Text: "the hit"},
		{ID: "r2", Type: "experience", Text: "another"},
	}
	m.recallCur = 0
	frame := plain(m.View())
	for _, want := range []string{
		"Recall Results (2) - Press Enter to view full text",
		"OCCURRED START",
		"OCCURRED END",
		">> world",
		"the hit",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("recall table missing %q\n%s", want, frame)
		}
	}
}

func TestReflectPanelAndEmptyState(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 100, 30
	m.bankID = "a"
	m.view = viewQuery
	m.mode = modeReflect
	frame := plain(m.View())
	for _, want := range []string{
		"Reflect Response",
		"No response yet. Enter a query and press Enter to get a reflection.",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("reflect panel missing %q\n%s", want, frame)
		}
	}
	m.reflectOut = "here is the answer"
	frame = plain(m.View())
	if !strings.Contains(frame, "here is the answer") {
		t.Errorf("reflection text missing\n%s", frame)
	}
}

func TestMemoryDetailOverlay(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 100, 34
	m.bankID = "a"
	m.view = viewMemories
	m.memories = []memoryRow{{ID: "m1", FactType: "world", MentionedAt: "2026-01-02T03:04:05Z", OccStart: "2026-01-01T00:00:00Z", OccEnd: "2026-01-02T00:00:00Z", Text: "full text here", Raw: []byte(`{"id":"m1","text":"full text here","proof_count":2}`)}}
	m.memCur = 0
	m.detail = detailMemory
	m.detailMemory = m.memories[0]
	frame := plain(m.View())
	for _, want := range []string{
		"Memory Metadata",
		"Type: world",
		"Mentioned At: 2026-01-02",
		"Occurred: 2026-01-01 to 2026-01-02",
		"Full Text (Esc to close)",
		"full text here",
		"Details JSON",
		`"proof_count": 2`,
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("memory overlay missing %q\n%s", want, frame)
		}
	}
}

func TestEntityDetailOverlay(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 100, 30
	m.bankID = "a"
	m.view = viewEntities
	m.entities = []entityRow{{Name: "Alice", Type: "person", Mentions: 7, First: "2026-01-01T00:00:00Z"}}
	m.entCur = 0
	m.detail = detailEntity
	m.detailEntity = m.entities[0]
	frame := plain(m.View())
	for _, want := range []string{
		"Entity Details (Esc to close)",
		"Name: Alice",
		"Type: person",
		"Mentions: 7",
		"First Seen: 2026-01-01",
		"Last Seen: unknown",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("entity overlay missing %q\n%s", want, frame)
		}
	}
}

func TestDocumentDetailOverlay(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 100, 30
	m.bankID = "a"
	m.view = viewDocuments
	m.detail = detailDocument
	m.detailDoc = documentDetail{ID: "d1", Type: "note", Created: "2026-02-03T04:05:06Z"}
	frame := plain(m.View())
	for _, want := range []string{
		"Document Metadata",
		"ID: d1",
		"Content (Esc to close)",
		"No content available",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("document overlay missing %q\n%s", want, frame)
		}
	}
}

func TestRecallResultDetailOverlay(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 100, 32
	m.bankID = "a"
	m.view = viewQuery
	m.detail = detailRecall
	m.detailRecall = recallRow{ID: "r1", Type: "world", MentionedAt: "2026-01-02T00:00:00Z", Text: "result body"}
	frame := plain(m.View())
	for _, want := range []string{
		"Recall Result Metadata",
		"Type: world",
		"Occurred: unknown",
		"Full Text (Esc to close)",
		"result body",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("recall overlay missing %q\n%s", want, frame)
		}
	}
}

func TestSmallTerminalDoesNotPanic(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 20, 5
	m.loadBanks(false)
	m.press("enter")
	if frame := m.View(); frame == "" {
		t.Error("empty frame on a tiny terminal")
	}
}

func TestQueryShortcutsReflectMode(t *testing.T) {
	m := sizedModel(t)
	m.press("enter")
	m.press("/")
	m.press("esc") // normal mode
	frame := plain(m.View())
	for _, want := range []string{"<m> Mode", "<b> Budget", "<+/-> Tokens", "<←→> Scroll", "<q> Quit"} {
		if !strings.Contains(frame, want) {
			t.Errorf("query shortcuts missing %q\n%s", want, frame)
		}
	}
	m.mode = modeReflect
	frame = plain(m.View())
	if strings.Contains(frame, "<←→> Scroll") {
		t.Errorf("scroll shortcut must disappear in reflect mode\n%s", frame)
	}
}
