package app

import (
	"fmt"
	"strings"
)

// unifiedDiff renders a unified diff from current to preview, with three lines
// of context, matching the shape the reference's dry run returns (fromfile
// "current", tofile "preview"). It is empty when the two documents are equal.
func unifiedDiff(current, preview string) string {
	if current == preview {
		return ""
	}
	a := splitLines(current)
	b := splitLines(preview)
	ops := diffOps(a, b)
	if !hasChanges(ops) {
		return ""
	}
	var out strings.Builder
	out.WriteString("--- current\n")
	out.WriteString("+++ preview\n")
	for _, hunk := range buildHunks(ops, 3) {
		writeHunk(&out, hunk)
	}
	return strings.TrimRight(out.String(), "\n")
}

// diffOpKind is one step of the edit script.
type diffOpKind int

const (
	opEqual diffOpKind = iota
	opDelete
	opInsert
)

// diffOp is one line of the edit script.
type diffOp struct {
	kind diffOpKind
	line string
	// aIndex and bIndex are the 1-based positions in current and preview.
	aIndex int
	bIndex int
}

// splitLines splits a document into lines without a trailing empty element for
// a final newline.
func splitLines(doc string) []string {
	if doc == "" {
		return nil
	}
	lines := strings.Split(doc, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// diffOps computes an edit script from current to preview using a longest
// common subsequence.
func diffOps(a, b []string) []diffOp {
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{kind: opEqual, line: a[i], aIndex: i + 1, bIndex: j + 1})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{kind: opDelete, line: a[i], aIndex: i + 1, bIndex: j + 1})
			i++
		default:
			ops = append(ops, diffOp{kind: opInsert, line: b[j], aIndex: i + 1, bIndex: j + 1})
			j++
		}
	}
	for ; i < len(a); i++ {
		ops = append(ops, diffOp{kind: opDelete, line: a[i], aIndex: i + 1, bIndex: j + 1})
	}
	for ; j < len(b); j++ {
		ops = append(ops, diffOp{kind: opInsert, line: b[j], aIndex: i + 1, bIndex: j + 1})
	}
	return ops
}

// hasChanges reports whether the edit script contains any insertion or deletion.
func hasChanges(ops []diffOp) bool {
	for _, op := range ops {
		if op.kind != opEqual {
			return true
		}
	}
	return false
}

// hunk is one contiguous region of the edit script with surrounding context.
type hunk struct {
	aStart, aCount int
	bStart, bCount int
	ops            []diffOp
}

// buildHunks groups changed regions into hunks with the given context width,
// merging regions closer together than twice the context.
func buildHunks(ops []diffOp, context int) []hunk {
	var changed []int
	for index, op := range ops {
		if op.kind != opEqual {
			changed = append(changed, index)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	var hunks []hunk
	start := max(0, changed[0]-context)
	end := min(len(ops), changed[0]+context+1)
	for _, index := range changed[1:] {
		if index-context <= end {
			end = min(len(ops), index+context+1)
			continue
		}
		hunks = append(hunks, makeHunk(ops, start, end))
		start = max(0, index-context)
		end = min(len(ops), index+context+1)
	}
	hunks = append(hunks, makeHunk(ops, start, end))
	return hunks
}

// makeHunk builds a hunk from a slice of the edit script.
func makeHunk(ops []diffOp, start, end int) hunk {
	slice := ops[start:end]
	h := hunk{ops: slice}
	for _, op := range slice {
		switch op.kind {
		case opEqual:
			h.aCount++
			h.bCount++
			if h.aStart == 0 {
				h.aStart = op.aIndex
			}
			if h.bStart == 0 {
				h.bStart = op.bIndex
			}
		case opDelete:
			h.aCount++
			if h.aStart == 0 {
				h.aStart = op.aIndex
			}
		case opInsert:
			h.bCount++
			if h.bStart == 0 {
				h.bStart = op.bIndex
			}
		}
	}
	if h.aStart == 0 {
		h.aStart = 1
	}
	if h.bStart == 0 {
		h.bStart = 1
	}
	return h
}

// writeHunk renders one hunk with its @@ header and prefixed lines.
func writeHunk(out *strings.Builder, h hunk) {
	fmt.Fprintf(out, "@@ -%s +%s @@\n", rangeText(h.aStart, h.aCount), rangeText(h.bStart, h.bCount))
	for _, op := range h.ops {
		switch op.kind {
		case opEqual:
			out.WriteString(" " + op.line + "\n")
		case opDelete:
			out.WriteString("-" + op.line + "\n")
		case opInsert:
			out.WriteString("+" + op.line + "\n")
		}
	}
}

// rangeText renders an @@ header range, collapsing a single-line range to its
// bare start like the reference does.
func rangeText(start, count int) string {
	switch count {
	case 1:
		return fmt.Sprintf("%d", start)
	case 0:
		return fmt.Sprintf("%d,0", start-1)
	default:
		return fmt.Sprintf("%d,%d", start, count)
	}
}
