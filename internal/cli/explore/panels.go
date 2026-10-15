package explore

// The main-content renderers for the Query view and the detail overlays.
// None of the panes scroll internally: content longer than the pane is
// clipped.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// renderQuery draws the 3-row input box on top and the results area below.
func (m model) renderQuery(width, height int) string {
	inputH := 3
	restH := height - inputH
	if restH < 1 {
		restH = 1
	}
	title := fmt.Sprintf("%s Query (press / to edit, m to toggle mode)", m.mode)
	input := titledBox(title, m.inputText(), width, inputH)
	return input + "\n" + m.renderQueryResults(width, restH)
}

// inputText styles the query text: teal while editing, default otherwise.
func (m model) inputText() string {
	if m.editing {
		return tealStyle.Render(m.queryText)
	}
	return m.queryText
}

// renderQueryResults picks the results area: loading panel while a query
// runs, the reflect panel, or the recall results table.
func (m model) renderQueryResults(width, height int) string {
	if m.loading {
		return m.renderLoading(width, height)
	}
	if m.mode == modeReflect {
		return m.renderReflect(width, height)
	}
	return m.renderRecallResults(width, height)
}

// renderLoading draws the in-progress panel: two blank lines, the animated
// line, a blank line, and the dim wait hint.
func (m model) renderLoading(width, height int) string {
	verb := "    Searching memories..."
	if m.mode == modeReflect {
		verb = "    Reflecting on memories..."
	}
	body := strings.Join([]string{
		"",
		"",
		verb + loadingDots(),
		"",
		dimStyle.Render("    Please wait while we process your query..."),
	}, "\n")
	return titledBox(fmt.Sprintf("%s in progress", m.mode), body, width, height)
}

// renderRecallResults draws the recall results table.
func (m model) renderRecallResults(width, height int) string {
	head := tableHeadStyle.Render(fmt.Sprintf("%-10s %-18s %-18s %s", "TYPE", "OCCURRED START", "OCCURRED END", "TEXT"))
	lines := []string{head}
	for i, r := range m.recallRows {
		row := fmt.Sprintf("%-10s %-18s %-18s %s",
			r.Type, datePart(r.OccStart, "-"), datePart(r.OccEnd, "-"),
			window(r.Text, textWindow, m.scroll))
		if i == m.recallCur {
			lines = append(lines, selectedStyle.Render(">> "+row))
		} else {
			lines = append(lines, rowStyle.Render("   "+row))
		}
	}
	title := fmt.Sprintf("Recall Results (%d) - Press Enter to view full text", len(m.recallRows))
	return titledBox(title, strings.Join(lines, "\n"), width, height)
}

// renderReflect draws the reflection panel with its empty state. Mode
// toggles keep prior artifacts, so the last response stays until replaced.
func (m model) renderReflect(width, height int) string {
	text := m.reflectOut
	if text == "" {
		text = "No response yet. Enter a query and press Enter to get a reflection."
	}
	return titledBox("Reflect Response", text, width, height)
}

// renderDetail dispatches to the open overlay's renderer.
func (m model) renderDetail(width, height int) string {
	switch m.detail {
	case detailMemory:
		return m.renderMemoryDetail(width, height)
	case detailEntity:
		return m.renderEntityDetail(width, height)
	case detailDocument:
		return m.renderDocumentDetail(width, height)
	case detailRecall:
		return m.renderRecallDetail(width, height)
	}
	return ""
}

// renderMemoryDetail stacks the 7-row metadata pane, the 45% full-text pane
// and the 55% JSON pane. The JSON pane pretty-prints the entire list-row
// object the endpoint returned.
func (m model) renderMemoryDetail(width, height int) string {
	row := m.detailMemory
	meta := strings.Join([]string{
		"Type: " + orUnknown(row.FactType),
		"Mentioned At: " + orDash(row.MentionedAt),
		"Occurred: " + occurredRange(row.OccStart, row.OccEnd),
	}, "\n")
	metaPane := titledBox("Memory Metadata", meta, width, 7)
	rest := height - 7
	if rest < 1 {
		rest = 1
	}
	textH := rest * 45 / 100
	if textH < 1 {
		textH = 1
	}
	jsonH := rest - textH
	if jsonH < 1 {
		jsonH = 1
	}
	text := row.Text
	if text == "" {
		text = "No text available"
	}
	pretty, err := indentJSON(row.Raw)
	if err != nil {
		pretty = string(row.Raw)
	}
	textPane := titledBox("Full Text (Esc to close)", text, width, textH)
	jsonPane := titledBox("Details JSON", pretty, width, jsonH)
	return strings.Join([]string{metaPane, textPane, jsonPane}, "\n")
}

// renderEntityDetail draws the single entity pane.
func (m model) renderEntityDetail(width, height int) string {
	r := m.detailEntity
	body := strings.Join([]string{
		"Name: " + r.Name,
		"Type: " + r.Type,
		fmt.Sprintf("Mentions: %d", r.Mentions),
		"First Seen: " + orUnknown(r.First),
		"Last Seen: " + orUnknown(r.Last),
	}, "\n")
	return titledBox("Entity Details (Esc to close)", body, width, height)
}

// renderDocumentDetail stacks the 5-row metadata pane and the content pane
// holding the document's stored text.
func (m model) renderDocumentDetail(width, height int) string {
	d := m.detailDoc
	meta := strings.Join([]string{
		"ID: " + d.ID,
		"Type: " + d.Type,
		"Created: " + orUnknown(datePart(d.Created, "")),
	}, "\n")
	metaPane := titledBox("Document Metadata", meta, width, 5)
	rest := height - 5
	if rest < 1 {
		rest = 1
	}
	content := d.Content
	if content == "" {
		content = "No content available"
	}
	return metaPane + "\n" + titledBox("Content (Esc to close)", content, width, rest)
}

// renderRecallDetail stacks the 7-row metadata pane and the full-text pane.
func (m model) renderRecallDetail(width, height int) string {
	r := m.detailRecall
	meta := strings.Join([]string{
		"Type: " + orUnknown(r.Type),
		"Mentioned At: " + orDash(r.MentionedAt),
		"Occurred: " + occurredRange(r.OccStart, r.OccEnd),
	}, "\n")
	metaPane := titledBox("Recall Result Metadata", meta, width, 7)
	rest := height - 7
	if rest < 1 {
		rest = 1
	}
	return metaPane + "\n" + titledBox("Full Text (Esc to close)", r.Text, width, rest)
}

// occurredRange renders "Occurred: {start} to {end}" with date-only bounds;
// both absent renders as unknown.
func occurredRange(start, end string) string {
	if start == "" && end == "" {
		return "unknown"
	}
	return datePart(start, "-") + " to " + datePart(end, "-")
}

// indentJSON pretty-prints a raw JSON value.
func indentJSON(raw []byte) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return "", err
	}
	return buf.String(), nil
}
