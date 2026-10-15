package explore

// The frame renderer: vertical composition of control bar, header, main
// content and footer, plus the list tables, the help screen and the shared
// titled-box primitive.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Brand gradient constants and the few other colors the frame uses.
var (
	brandBlue   = lipgloss.Color("#0074d9")
	brandMid    = lipgloss.Color("#0083b7")
	brandTeal   = lipgloss.Color("#009296")
	errRed      = lipgloss.Color("1")
	selectionBG = lipgloss.Color("237")
	dimGray     = lipgloss.Color("241")
	plainWhite  = lipgloss.Color("255")
)

// Precomputed styles.
var (
	headerStyle    = lipgloss.NewStyle().Bold(true).Foreground(brandBlue)
	tableHeadStyle = lipgloss.NewStyle().Bold(true).Foreground(brandBlue)
	selectedStyle  = lipgloss.NewStyle().Bold(true).Background(selectionBG)
	rowStyle       = lipgloss.NewStyle().Foreground(plainWhite)
	tealBold       = lipgloss.NewStyle().Bold(true).Foreground(brandTeal)
	tealStyle      = lipgloss.NewStyle().Foreground(brandTeal)
	statusStyle    = lipgloss.NewStyle().Foreground(brandMid)
	errLabelStyle  = lipgloss.NewStyle().Bold(true).Foreground(errRed)
	loadingStyle   = lipgloss.NewStyle().Bold(true).Foreground(brandTeal)
	dimStyle       = lipgloss.NewStyle().Foreground(dimGray)
	boldStyle      = lipgloss.NewStyle().Bold(true)
	blueShortcut   = lipgloss.NewStyle().Bold(true).Foreground(brandBlue)
	redShortcut    = lipgloss.NewStyle().Bold(true).Foreground(errRed)
	contextBorder  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(brandBlue)
	plainBorder    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder())
)

// Rows of chrome around the flex main area: control bar (5), header (3),
// footer (1).
const (
	controlBarRows = 5
	headerRows     = 3
	footerRows     = 1
)

// View renders one frame: control bar, header, main content, footer. Sizes
// fall back to 80x24 until the first WindowSizeMsg arrives; no minimum
// terminal size is enforced.
func (m model) View() string {
	width, height := m.width, m.height
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	mainHeight := height - controlBarRows - headerRows - footerRows
	if mainHeight < 1 {
		mainHeight = 1
	}
	return strings.Join([]string{
		m.renderControlBar(width),
		m.renderHeader(width),
		m.renderMain(width, mainHeight),
		m.renderFooter(),
	}, "\n")
}

// renderHeader draws the 3-row bordered, centered title. The bank id is
// appended whenever a bank is in scope.
func (m model) renderHeader(width int) string {
	title := fmt.Sprintf("Wenyuan Explorer - %s", m.view)
	if m.view != viewBanks {
		title += fmt.Sprintf(" - [%s]", m.bankID)
	}
	return plainBorder.
		Width(width - 2).
		Align(lipgloss.Center).
		Render(headerStyle.Render(title))
}

// renderControlBar draws the 5-row control bar: the Context box (30%) and
// the Shortcuts box (70%) side by side, both bordered brand blue.
func (m model) renderControlBar(width int) string {
	leftW := width * 3 / 10
	rightW := width - leftW
	left := contextBorder.
		Width(leftW - 2).
		Height(3).
		Render(tealBold.Render(m.contextText()))
	right := contextBorder.
		Width(rightW - 2).
		Height(3).
		Render(m.shortcutsText())
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

// contextText renders the Context box body for the active view.
func (m model) contextText() string {
	switch m.view {
	case viewBanks:
		return "Context: Banks List"
	case viewQuery:
		return fmt.Sprintf("Mode: %s\nBudget: %s | Tokens: %d", m.mode, m.budget, m.maxTokens)
	default:
		return fmt.Sprintf("Context: %s\nBank: %s", m.view, m.bankID)
	}
}

// shortcut is one active key/description pair. Quit and delete render red.
type shortcut struct {
	key    string
	desc   string
	danger bool
}

// shortcuts returns the active pair set in the spec's order.
func (m model) shortcuts() []shortcut {
	switch m.view {
	case viewBanks:
		return []shortcut{
			{"Enter", "Select", false}, {"R", "Refresh", false},
			{"?", "Help", false}, {"q", "Quit", true},
		}
	case viewMemories:
		return []shortcut{
			{"Enter", "View", false}, {"/", "Query", false}, {"←→", "Scroll", false},
			{"n", "Next", false}, {"p", "Prev", false}, {"Esc", "Back", false},
			{"R", "Refresh", false}, {"?", "Help", false}, {"q", "Quit", true},
		}
	case viewEntities:
		return []shortcut{
			{"Enter", "View", false}, {"/", "Query", false}, {"←→", "Scroll", false},
			{"Esc", "Back", false}, {"R", "Refresh", false},
			{"?", "Help", false}, {"q", "Quit", true},
		}
	case viewDocuments:
		return []shortcut{
			{"Enter", "View", false}, {"/", "Query", false}, {"←→", "Scroll", false},
			{"Del", "Delete", true}, {"Esc", "Back", false}, {"R", "Refresh", false},
			{"?", "Help", false}, {"q", "Quit", true},
		}
	case viewQuery:
		if m.editing {
			return []shortcut{{"Enter", "Execute", false}, {"Esc", "Cancel", false}}
		}
		pairs := []shortcut{{"/", "Query", false}, {"m", "Mode", false}}
		if m.mode == modeRecall {
			pairs = append(pairs, shortcut{"←→", "Scroll", false})
		}
		return append(pairs,
			shortcut{"b", "Budget", false}, shortcut{"+/-", "Tokens", false},
			shortcut{"Esc", "Back", false}, shortcut{"?", "Help", false},
			shortcut{"q", "Quit", true},
		)
	}
	return nil
}

// shortcutsText lays the pairs out column-major over 3 rows, each formatted
// "<key> desc" with the description padded to 10 columns.
func (m model) shortcutsText() string {
	pairs := m.shortcuts()
	cells := make([]string, len(pairs))
	for i, p := range pairs {
		style := blueShortcut
		if p.danger {
			style = redShortcut
		}
		cells[i] = style.Render(fmt.Sprintf("<%s> %-10s", p.key, p.desc))
	}
	rows := make([]string, 3)
	for r := 0; r < 3; r++ {
		var parts []string
		for c := r; c < len(cells); c += 3 {
			parts = append(parts, cells[c])
		}
		rows[r] = strings.Join(parts, " ")
	}
	return strings.Join(rows, "\n")
}

// renderFooter draws the 1-row footer; first match wins: error, loading,
// status, blank. Errors and statuses persist until replaced.
func (m model) renderFooter() string {
	switch {
	case m.errMsg != "":
		return " " + errLabelStyle.Render("Error:") + " " + m.errMsg
	case m.loading:
		return " " + loadingStyle.Render("Loading...")
	case m.status != "":
		return " " + statusStyle.Render(m.status)
	}
	return ""
}

// renderMain fills the flex area: help screen, detail overlay, query panes,
// or the view's table.
func (m model) renderMain(width, height int) string {
	switch {
	case m.helpOpen:
		return m.renderHelp(width, height)
	case m.detail != detailNone:
		return m.renderDetail(width, height)
	case m.view == viewQuery:
		return m.renderQuery(width, height)
	default:
		return m.renderTable(width, height)
	}
}

// renderTable draws the active list inside a titled panel: the header row
// (Banks has none) plus data rows, the cursor over data rows only.
func (m model) renderTable(width, height int) string {
	var head, body string
	var rows []string
	cursor := -1
	switch m.view {
	case viewBanks:
		cursor = m.bankCur
		for _, r := range m.banks {
			name := r.Name
			if name == "" {
				name = "Unnamed"
			}
			rows = append(rows, fmt.Sprintf("%s - %s", r.ID, name))
		}
	case viewMemories:
		head = tableHeadStyle.Render(fmt.Sprintf("%-10s %-18s %-18s %s", "TYPE", "MENTIONED AT", "OCCURRED AT", "TEXT"))
		cursor = m.memCur
		for _, r := range m.memories {
			occurred := datePart(r.OccStart, "-")
			if occurred == "-" {
				occurred = datePart(r.OccEnd, "-")
			}
			rows = append(rows, fmt.Sprintf("%-10s %-18s %-18s %s",
				r.FactType, datePart(r.MentionedAt, "-"), occurred,
				window(r.Text, textWindow, m.scroll)))
		}
	case viewEntities:
		head = tableHeadStyle.Render(fmt.Sprintf("%-40s %-15s %-10s", "NAME", "TYPE", "MENTIONS"))
		cursor = m.entCur
		for _, r := range m.entities {
			rows = append(rows, fmt.Sprintf("%-40s %-15s %-10d",
				window(r.Name, nameWindow, m.scroll), r.Type, r.Mentions))
		}
	case viewDocuments:
		head = tableHeadStyle.Render(fmt.Sprintf("%-40s %-20s %s", "ID", "TYPE", "CREATED"))
		cursor = m.docCur
		for _, r := range m.documents {
			rows = append(rows, fmt.Sprintf("%-40s %-20s %s",
				window(r.ID, nameWindow, m.scroll), r.Type, datePart(r.Created, "unknown")))
		}
	case viewQuery:
		return m.renderQuery(width, height)
	}

	lines := make([]string, 0, len(rows)+1)
	if head != "" {
		lines = append(lines, head)
	}
	for i, row := range rows {
		if i == cursor {
			lines = append(lines, selectedStyle.Render(">> "+row))
		} else {
			lines = append(lines, rowStyle.Render("   "+row))
		}
	}
	body = strings.Join(lines, "\n")
	return titledBox(m.panelTitle(), body, width, height)
}

// panelTitle is the list panel's title line.
func (m model) panelTitle() string {
	switch m.view {
	case viewBanks:
		return "Banks"
	case viewMemories:
		return fmt.Sprintf("Memories (%d) - Press Enter to view full text", len(m.memories))
	case viewEntities:
		return fmt.Sprintf("Entities (%d) - Press Enter to view details", len(m.entities))
	case viewDocuments:
		return fmt.Sprintf("Documents (%d) - Press Enter to view content", len(m.documents))
	}
	return ""
}

// renderHelp draws the help panel that replaces the main content. It is not
// modal: keys keep driving the view underneath.
func (m model) renderHelp(width, height int) string {
	lines := []string{
		headerStyle.Render("Wenyuan Explorer - Keyboard Shortcuts"),
		"",
		boldStyle.Render("Navigation Flow"),
		"  1. select a bank (Enter)",
		"  2. view memories, entities, or documents",
		"  3. press / from any view to query (recall/reflect)",
		"",
		boldStyle.Render("Basic Navigation"),
		"  ↑/↓, j/k navigate",
		"  ←/→, h/l scroll text in tables",
		"  Enter select/view details",
		"  Esc back/close",
		"",
		boldStyle.Render("Query View"),
		"  / start or edit query (from any non-bank view)",
		"  m toggle Recall↔Reflect",
		"  b cycle budget Low→Mid→High",
		"  +/- adjust max tokens",
		"  Enter execute",
		"",
		boldStyle.Render("General"),
		"  R refresh",
		"  ? toggle help",
		"  q quit",
		"",
		dimStyle.Render("Press ? to close help"),
	}
	return titledBox("Help", strings.Join(lines, "\n"), width, height)
}

// titledBox draws a rounded box with the title embedded in the top border.
// The content wraps to the inner width and clips to the inner height; there
// is no internal scrolling.
func titledBox(title, content string, width, height int) string {
	innerW, innerH := width-2, height-2
	if innerW < 1 {
		innerW = 1
	}
	if innerH < 1 {
		innerH = 1
	}
	wrapped := lipgloss.NewStyle().Width(innerW).Render(content)
	lines := strings.Split(wrapped, "\n")
	if len(lines) > innerH {
		lines = lines[:innerH]
	}
	for len(lines) < innerH {
		lines = append(lines, "")
	}
	for i, line := range lines {
		lines[i] = padClip(line, innerW)
	}
	dashes := innerW - lipgloss.Width(title) - 3
	if dashes < 0 {
		dashes = 0
	}
	var b strings.Builder
	fmt.Fprintf(&b, "╭─ %s %s╮\n", title, strings.Repeat("─", dashes))
	for _, line := range lines {
		b.WriteString("│" + line + "│\n")
	}
	b.WriteString("╰" + strings.Repeat("─", innerW) + "╯")
	return b.String()
}

// window returns the rune window of s starting at the shared scroll offset.
func window(s string, size, offset int) string {
	r := []rune(s)
	if offset >= len(r) {
		return ""
	}
	end := offset + size
	if end > len(r) {
		end = len(r)
	}
	return string(r[offset:end])
}

// padClip pads s with spaces to width or clips it to width runes.
func padClip(s string, width int) string {
	r := []rune(s)
	if len(r) > width {
		return string(r[:width])
	}
	return s + strings.Repeat(" ", width-len(r))
}

// orUnknown renders an absent detail value as unknown.
func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// orDash renders an absent detail value as "-".
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
