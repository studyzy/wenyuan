package cli

import (
	"fmt"
	"io"
	"strings"
)

// Table renders aligned text columns for pretty output.
type Table struct {
	headers []string
	rows    [][]string
}

// NewTable starts a table with the given header cells.
func NewTable(headers ...string) *Table {
	return &Table{headers: append([]string(nil), headers...)}
}

// AddRow appends one row. Rows wider than the header are padded out so
// column indices stay aligned.
func (t *Table) AddRow(cells ...string) {
	row := append([]string(nil), cells...)
	for len(row) < len(t.headers) {
		row = append(row, "")
	}
	t.rows = append(t.rows, row)
}

// Render writes the header row followed by the body rows, each column padded
// to its widest cell and separated by two spaces.
func (t *Table) Render(w io.Writer) error {
	widths := make([]int, len(t.headers))
	for i, h := range t.headers {
		widths[i] = len([]rune(h))
	}
	for _, row := range t.rows {
		for i, cell := range row {
			if i < len(widths) {
				if n := len([]rune(cell)); n > widths[i] {
					widths[i] = n
				}
			}
		}
	}
	lines := make([][]string, 0, len(t.rows)+1)
	lines = append(lines, t.headers)
	lines = append(lines, t.rows...)
	for _, line := range lines {
		var b strings.Builder
		for i, cell := range line {
			if i > 0 {
				b.WriteString("  ")
			}
			b.WriteString(cell)
			if i < len(widths) {
				b.WriteString(strings.Repeat(" ", widths[i]-len([]rune(cell))))
			}
		}
		if _, err := fmt.Fprintln(w, strings.TrimRight(b.String(), " ")); err != nil {
			return err
		}
	}
	return nil
}
