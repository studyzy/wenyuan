package explore

// The event loop: key handling for both input modes, the frame tick with
// its auto-refresh check, and the async query lifecycle. List refreshes run
// synchronously inside Update, matching the upstream blocking behavior;
// only query execution is asynchronous.

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// tickMsg drives the frame loop: the loading animation (wall-clock derived)
// and the 5 s auto-refresh check.
type tickMsg struct{}

// queryDoneMsg is the async outcome of one recall or reflect call. The id
// lets the model drop results of superseded queries.
type queryDoneMsg struct {
	id   int
	mode queryMode
	rows []recallRow
	text string
	err  error
}

// Init starts the 100 ms frame tick. The first bank load ran before the
// program started, so no fetch happens here.
func (m model) Init() tea.Cmd {
	return armTick()
}

// armTick schedules the next frame tick.
func armTick() tea.Cmd {
	return tea.Tick(tickInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

// Update is the bubbletea entry point; the logic lives in update so tests
// can drive it without a tea.Program.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	return next, cmd
}

func (m *model) update(msg tea.Msg) (*model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		return m.onTick()
	case queryDoneMsg:
		m.applyQueryResult(msg)
		return m, nil
	case tea.KeyMsg:
		return m.onKey(msg)
	}
	return m, nil
}

// onTick re-arms the frame tick, then checks the auto-refresh timer. Only
// the timer's own firing resets it: a manual R refresh deliberately does
// not, so an auto-refresh may follow shortly after. The Query view is
// query-driven and never auto-refreshes.
func (m *model) onTick() (*model, tea.Cmd) {
	if time.Since(m.lastRefresh) >= autoRefreshInterval {
		m.lastRefresh = time.Now()
		if m.view != viewQuery {
			m.refreshCurrent(false)
		}
	}
	return m, armTick()
}

// onKey dispatches one key press. Ctrl+C is checked first and works in both
// input modes.
func (m *model) onKey(msg tea.KeyMsg) (*model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	if m.editing {
		return m.onEditKey(msg)
	}
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "?":
		m.helpOpen = !m.helpOpen
	case "down", "j":
		m.moveCursor(1)
	case "up", "k":
		m.moveCursor(-1)
	case "left", "h":
		m.scroll = max(0, m.scroll-hScrollStep)
	case "right", "l":
		m.scroll = min(hScrollMax, m.scroll+hScrollStep)
	case "enter":
		return m.onEnter()
	case "esc":
		return m.onEsc()
	case "R":
		m.refreshCurrent(true)
	case "/":
		return m.onSlash()
	case "m":
		if m.view == viewQuery {
			if m.mode == modeRecall {
				m.mode = modeReflect
			} else {
				m.mode = modeRecall
			}
			m.setStatus(fmt.Sprintf("Switched to %s mode", m.mode))
		}
	case "b":
		if m.view == viewQuery {
			m.budget = m.budget.next()
			m.setStatus(fmt.Sprintf("Budget: %s", m.budget))
		}
	case "+", "=":
		if m.view == viewQuery {
			m.maxTokens = min(tokenMax, m.maxTokens+tokenStep)
			m.setStatus(fmt.Sprintf("Max tokens: %d", m.maxTokens))
		}
	case "-":
		if m.view == viewQuery {
			m.maxTokens = max(tokenMin, m.maxTokens-tokenStep)
			m.setStatus(fmt.Sprintf("Max tokens: %d", m.maxTokens))
		}
	case "delete":
		if m.view == viewDocuments && m.detail == detailNone {
			m.deleteSelectedDocument()
		}
	case "n":
		if m.view == viewMemories {
			m.memOffset += memoriesPageSize
			m.loadMemories(true)
		}
	case "p":
		if m.view == viewMemories {
			m.memOffset = max(0, m.memOffset-memoriesPageSize)
			m.loadMemories(true)
		}
	}
	return m, nil
}

// onEditKey handles the Query input mode: append-only text, backspace,
// enter to execute, esc to cancel keeping the typed text.
func (m *model) onEditKey(msg tea.KeyMsg) (*model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyRunes, tea.KeySpace:
		if len(msg.Runes) > 0 {
			m.queryText += string(msg.Runes)
		} else if msg.Type == tea.KeySpace {
			m.queryText += " "
		}
	case tea.KeyBackspace:
		if r := []rune(m.queryText); len(r) > 0 {
			m.queryText = string(r[:len(r)-1])
		}
	case tea.KeyEnter:
		return m.executeQuery()
	case tea.KeyEsc:
		m.editing = false
	}
	return m, nil
}

// moveCursor moves the active list's cursor with wraparound. The cursor
// lives over data rows only; the header can never hold it.
func (m *model) moveCursor(delta int) {
	switch m.view {
	case viewBanks:
		m.bankCur = wrapCursor(len(m.banks), m.bankCur, delta)
	case viewMemories:
		m.memCur = wrapCursor(len(m.memories), m.memCur, delta)
	case viewEntities:
		m.entCur = wrapCursor(len(m.entities), m.entCur, delta)
	case viewDocuments:
		m.docCur = wrapCursor(len(m.documents), m.docCur, delta)
	case viewQuery:
		m.recallCur = wrapCursor(len(m.recallRows), m.recallCur, delta)
	}
}

// onEnter handles Enter: reset the horizontal scroll, then act per view.
func (m *model) onEnter() (*model, tea.Cmd) {
	m.scroll = 0
	switch m.view {
	case viewBanks:
		if m.bankCur < 0 || m.bankCur >= len(m.banks) {
			return m, nil
		}
		m.history = append(m.history, historyEntry{kind: m.view, bankID: m.bankID})
		m.bankID = m.banks[m.bankCur].ID
		m.view = viewMemories
		m.loadMemories(true)
	case viewMemories:
		if cur := m.memCur; cur >= 0 && cur < len(m.memories) {
			m.detail = detailMemory
			m.detailMemory = m.memories[cur]
			m.setStatus("Viewing memory details (Esc to close)")
		}
	case viewEntities:
		if cur := m.entCur; cur >= 0 && cur < len(m.entities) {
			m.detail = detailEntity
			m.detailEntity = m.entities[cur]
			m.setStatus("Viewing entity details (Esc to close)")
		}
	case viewDocuments:
		if cur := m.docCur; cur >= 0 && cur < len(m.documents) {
			m.openDocument()
		}
	case viewQuery:
		if m.mode == modeRecall {
			if cur := m.recallCur; cur >= 0 && cur < len(m.recallRows) {
				m.detail = detailRecall
				m.detailRecall = m.recallRows[cur]
				m.setStatus("Viewing recall result (Esc to close)")
			}
		}
		// Reflect mode: Enter does nothing.
	}
	return m, nil
}

// openDocument fetches the full document for the selected row and opens the
// overlay. A failure shows the error and opens nothing.
func (m *model) openDocument() {
	row := m.documents[m.docCur]
	detail, err := m.api.getDocument(m.ctx, m.bankID, row.ID)
	if err != nil {
		m.setError(fmt.Sprintf("Failed to load document: %s", err))
		return
	}
	m.detail = detailDocument
	m.detailDoc = detail
	m.setStatus(fmt.Sprintf("Viewing document: %s", row.ID))
}

// onEsc closes an open overlay first; only when none is open does it pop
// the view history (if non-empty) and refresh the restored view.
func (m *model) onEsc() (*model, tea.Cmd) {
	m.scroll = 0
	if m.detail != detailNone {
		switch m.detail {
		case detailMemory:
			m.setStatus("Closed memory view")
		case detailEntity:
			m.setStatus("Closed entity view")
		case detailDocument:
			m.setStatus("Closed document view")
		case detailRecall:
			m.setStatus("Closed recall result view")
		}
		m.detail = detailNone
		return m, nil
	}
	if len(m.history) == 0 {
		return m, nil
	}
	entry := m.history[len(m.history)-1]
	m.history = m.history[:len(m.history)-1]
	m.bankID = entry.bankID
	m.view = entry.kind
	// The restored view refreshes silently so the Closed status stays
	// visible in the footer.
	m.refreshCurrent(false)
	return m, nil
}

// onSlash handles /: an error on Banks, edit mode when already in Query,
// and a switch to the Query view of the selected bank from any bank-scoped
// list. Switching to the active view would be a no-op, which only arises in
// Query itself.
func (m *model) onSlash() (*model, tea.Cmd) {
	switch m.view {
	case viewBanks:
		m.setError("Select a bank first")
	case viewQuery:
		m.editing = true
	default:
		if m.bankID == "" {
			m.setError("No bank selected")
			return m, nil
		}
		m.history = append(m.history, historyEntry{kind: m.view, bankID: m.bankID})
		m.detail = detailNone
		m.view = viewQuery
		m.editing = true
	}
	return m, nil
}

// refreshCurrent reloads the active view's data synchronously. Manual loads
// report the Loaded status; the initial load, the auto-refresh and the
// post-delete reload stay silent so they do not clobber the footer.
func (m *model) refreshCurrent(report bool) {
	switch m.view {
	case viewBanks:
		m.loadBanks(report)
	case viewMemories:
		m.loadMemories(report)
	case viewEntities:
		m.loadEntities(report)
	case viewDocuments:
		m.loadDocuments(report)
	case viewQuery:
		// Query content is purely query-driven; the timer resets above but
		// nothing loads.
	}
}

func (m *model) loadBanks(report bool) {
	rows, err := m.api.listBanks(m.ctx)
	if err != nil {
		m.setError(err.Error())
		return
	}
	m.banks = rows
	m.bankCur = normalizeCursor(len(rows), m.bankCur)
	if report {
		m.setStatus(fmt.Sprintf("Loaded %d banks", len(rows)))
	}
}

func (m *model) loadMemories(report bool) {
	rows, err := m.api.listMemories(m.ctx, m.bankID, m.memOffset)
	if err != nil {
		m.setError(err.Error())
		return
	}
	m.memories = rows
	m.memCur = normalizeCursor(len(rows), m.memCur)
	if report {
		m.setStatus(fmt.Sprintf("Loaded %d memories (limit: %d, offset: %d)", len(rows), memoriesPageSize, m.memOffset))
	}
}

func (m *model) loadEntities(report bool) {
	rows, err := m.api.listEntities(m.ctx, m.bankID)
	if err != nil {
		m.setError(err.Error())
		return
	}
	m.entities = rows
	m.entCur = normalizeCursor(len(rows), m.entCur)
	if report {
		m.setStatus(fmt.Sprintf("Loaded %d entities", len(rows)))
	}
}

func (m *model) loadDocuments(report bool) {
	rows, err := m.api.listDocuments(m.ctx, m.bankID)
	if err != nil {
		m.setError(err.Error())
		return
	}
	m.documents = rows
	m.docCur = normalizeCursor(len(rows), m.docCur)
	if report {
		m.setStatus(fmt.Sprintf("Loaded %d documents", len(rows)))
	}
}

// deleteSelectedDocument removes the selected document without any
// confirmation. Success reports the status and refetches the list silently;
// failure reports the error and leaves the list untouched. Delete never
// acts from a detail overlay (the caller guards that).
func (m *model) deleteSelectedDocument() {
	if m.docCur < 0 || m.docCur >= len(m.documents) {
		return
	}
	id := m.documents[m.docCur].ID
	if err := m.api.deleteDocument(m.ctx, m.bankID, id); err != nil {
		m.setError(fmt.Sprintf("Failed to delete document: %s", err))
		return
	}
	m.setStatus(fmt.Sprintf("Deleted document: %s", id))
	m.loadDocuments(false)
}

// executeQuery submits the current query text. An empty text is an error
// and stays in edit mode. Otherwise the edit mode ends, the loading state
// turns on, and the HTTP call runs asynchronously; the monotonically
// increasing query id supersedes any in-flight query.
func (m *model) executeQuery() (*model, tea.Cmd) {
	if m.queryText == "" {
		m.setError("Query cannot be empty")
		return m, nil
	}
	m.editing = false
	m.loading = true
	m.queryID++
	id := m.queryID
	mode, bank := m.mode, m.bankID
	query, budget, tokens := m.queryText, m.budget.wire(), m.maxTokens
	api, ctx := m.api, m.ctx
	cmd := func() tea.Msg {
		if mode == modeRecall {
			rows, err := api.recall(ctx, bank, query, budget, tokens)
			return queryDoneMsg{id: id, mode: mode, rows: rows, err: err}
		}
		text, err := api.reflect(ctx, bank, query, budget)
		return queryDoneMsg{id: id, mode: mode, text: text, err: err}
	}
	return m, cmd
}

// applyQueryResult applies a finished query. A result whose id is no longer
// current belongs to a superseded query and is dropped.
func (m *model) applyQueryResult(msg queryDoneMsg) {
	if msg.id != m.queryID {
		return
	}
	m.loading = false
	if msg.err != nil {
		if msg.mode == modeRecall {
			m.setError(fmt.Sprintf("Recall failed: %s", msg.err))
		} else {
			m.setError(fmt.Sprintf("Reflect failed: %s", msg.err))
		}
		return
	}
	if msg.mode == modeRecall {
		m.recallRows = msg.rows
		m.recallCur = normalizeCursor(len(m.recallRows), 0)
		m.setStatus(fmt.Sprintf("Found %d results", len(m.recallRows)))
		return
	}
	m.reflectOut = msg.text
	m.setStatus("Reflection complete")
}

// loadingDots returns the 0-3 animated trailing dots, wall-clock derived at
// a 500 ms period.
func loadingDots() string {
	return strings.Repeat(".", int(time.Now().UnixMilli()/500)%4)
}
