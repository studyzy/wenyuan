package explore

// The TUI state: one bubbletea model over five views, a view-history stack,
// per-view rows and cursors, a shared horizontal scroll offset, the query
// state, the open detail overlay, and the footer state.

import (
	"context"
	"time"
)

// viewKind identifies the active view. Four views carry the identifier of
// the bank they inspect; Banks does not.
type viewKind int

const (
	viewBanks viewKind = iota
	viewMemories
	viewEntities
	viewDocuments
	viewQuery
)

// String returns the view title used in the header and context box.
func (v viewKind) String() string {
	switch v {
	case viewBanks:
		return "Banks"
	case viewMemories:
		return "Memories"
	case viewEntities:
		return "Entities"
	case viewDocuments:
		return "Documents"
	case viewQuery:
		return "Query"
	}
	return "unknown"
}

// queryMode is the Query view's active operation.
type queryMode int

const (
	modeRecall queryMode = iota
	modeReflect
)

// String returns the capitalized mode name used in titles and statuses.
func (m queryMode) String() string {
	if m == modeReflect {
		return "Reflect"
	}
	return "Recall"
}

// budgetLevel is the recall token budget, cycled by the b key.
type budgetLevel int

const (
	budgetLow budgetLevel = iota
	budgetMid
	budgetHigh
)

// String returns the capitalized level used in statuses and the context box.
func (b budgetLevel) String() string {
	switch b {
	case budgetLow:
		return "Low"
	case budgetHigh:
		return "High"
	}
	return "Mid"
}

// wire returns the budget string the request body carries.
func (b budgetLevel) wire() string {
	switch b {
	case budgetLow:
		return "low"
	case budgetHigh:
		return "high"
	}
	return "mid"
}

// next cycles Low -> Mid -> High -> Low.
func (b budgetLevel) next() budgetLevel {
	return (b + 1) % 3
}

// detailKind is the type of open detail overlay, if any.
type detailKind int

const (
	detailNone detailKind = iota
	detailMemory
	detailEntity
	detailDocument
	detailRecall
)

// historyEntry is one saved view on the navigation stack.
type historyEntry struct {
	kind   viewKind
	bankID string
}

// Timing, scrolling and token constants from the behavior spec.
const (
	tickInterval        = 100 * time.Millisecond
	autoRefreshInterval = 5 * time.Second
	hScrollStep         = 10
	hScrollMax          = 200
	textWindow          = 80
	nameWindow          = 40
	recallDefaultTokens = 4096
	tokenStep           = 1024
	tokenMin            = 512
	tokenMax            = 16384
)

// model is the whole TUI state.
type model struct {
	api apiClient
	ctx context.Context

	width  int
	height int

	view    viewKind
	bankID  string // the selected bank, remembered across views
	history []historyEntry

	banks     []bankRow
	bankCur   int
	memories  []memoryRow
	memCur    int
	memOffset int // persists across bank switches (upstream quirk, kept deliberately)
	entities  []entityRow
	entCur    int
	documents []docRow
	docCur    int

	scroll int // shared horizontal scroll; reset by Enter/Esc

	mode      queryMode
	budget    budgetLevel
	maxTokens int
	queryText string
	editing   bool

	recallRows []recallRow
	recallCur  int
	reflectOut string

	detail       detailKind
	detailMemory memoryRow
	detailEntity entityRow
	detailDoc    documentDetail
	detailRecall recallRow
	helpOpen     bool

	status      string
	errMsg      string
	loading     bool
	queryID     int // monotonic; results of superseded queries are dropped
	lastRefresh time.Time
}

// newModel builds the startup state: Banks view, no bank selected, recall
// mode, budget mid, 4096 tokens, the startup status, and the auto-refresh
// timer armed.
func newModel(api apiClient, ctx context.Context) *model {
	return &model{
		api:         api,
		ctx:         ctx,
		view:        viewBanks,
		mode:        modeRecall,
		budget:      budgetMid,
		maxTokens:   recallDefaultTokens,
		status:      "Select a bank to start. Press ? for help",
		lastRefresh: time.Now(),
	}
}

// normalizeCursor clamps a list cursor after a reload: empty list means no
// selection (-1), no cursor moves to the first row, and a cursor beyond the
// end clamps to the last row.
func normalizeCursor(n, cur int) int {
	switch {
	case n == 0:
		return -1
	case cur < 0:
		return 0
	case cur >= n:
		return n - 1
	}
	return cur
}

// wrapCursor moves a cursor by delta with wraparound at both ends.
func wrapCursor(n, cur, delta int) int {
	if n == 0 {
		return -1
	}
	if cur < 0 {
		cur = 0
	}
	return ((cur+delta)%n + n) % n
}

// setStatus records a footer status, replacing any error: the footer has a
// single message slot and the newest message wins.
func (m *model) setStatus(s string) {
	m.status = s
	m.errMsg = ""
}

// setError records a footer error. The app keeps running on the previous
// data.
func (m *model) setError(s string) {
	m.errMsg = s
}
