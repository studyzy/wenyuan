package fileparse

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// The Office Open XML formats (.docx, .pptx, .xlsx) are ZIP archives of XML
// parts. These converters read the parts that carry the visible text with the
// standard library's archive/zip and encoding/xml, so no rendering library and
// no new module dependency is needed. Layout that XML does not describe (page
// geometry, floating shapes) is out of scope; the goal is the text.

// xmlEvent distinguishes an element's start, its end, and the character data
// inside it, so a caller can tell `<w:p>` (start a paragraph) from whitespace
// between paragraphs.
type xmlEvent int

const (
	xmlStart xmlEvent = iota
	xmlEnd
	xmlChar
)

// zipMember returns the bytes of one archive member, or false when the member
// is absent.
func zipMember(zr *zip.Reader, name string) ([]byte, bool) {
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, false
			}
			defer func() { _ = rc.Close() }()
			data, err := io.ReadAll(rc)
			if err != nil {
				return nil, false
			}
			return data, true
		}
	}
	return nil, false
}

// zipMembersMatching returns the members whose name matches the prefix/suffix
// pair, sorted by the integer between them, so slide1 precedes slide2.
func zipMembersMatching(zr *zip.Reader, prefix, suffix string) []namedMember {
	var members []namedMember
	for _, f := range zr.File {
		name := f.Name
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
			continue
		}
		mid := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
		order, _ := strconv.Atoi(mid)
		members = append(members, namedMember{name: name, order: order})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].order < members[j].order })
	return members
}

type namedMember struct {
	name  string
	order int
}

// xmlDecoder builds a tolerant decoder: OOXML parts are UTF-8 but may carry a
// declaration or entities the strict decoder rejects, and a single unreadable
// byte should not fail the whole conversion.
func xmlDecoder(data []byte) *xml.Decoder {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	return dec
}

// xmlText walks a part and calls visit for each event, giving the element's
// local name; for character data the local name is the innermost open element.
func xmlText(data []byte, visit func(ev xmlEvent, local string, attrs []xml.Attr, text string)) error {
	dec := xmlDecoder(data)
	var stack []string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
			visit(xmlStart, t.Name.Local, t.Attr, "")
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			visit(xmlEnd, t.Name.Local, nil, "")
		case xml.CharData:
			if len(stack) > 0 {
				visit(xmlChar, stack[len(stack)-1], nil, string(t))
			}
		}
	}
}

// docxToMarkdown extracts the paragraphs of a Word document.
func docxToMarkdown(data []byte, filename string) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("read '%s': %w", filename, err)
	}
	part, ok := zipMember(zr, "word/document.xml")
	if !ok {
		return "", fmt.Errorf("'%s' has no word/document.xml part", filename)
	}
	var out strings.Builder
	var para strings.Builder
	flush := func() {
		line := strings.TrimRight(para.String(), " \t")
		para.Reset()
		if line != "" {
			out.WriteString(line)
		}
		out.WriteString("\n")
	}
	err = xmlText(part, func(ev xmlEvent, local string, _ []xml.Attr, text string) {
		switch {
		case ev == xmlStart && local == "p":
			para.Reset()
		case ev == xmlEnd && local == "p":
			flush()
		case ev == xmlChar && local == "t":
			para.WriteString(text)
		case ev == xmlStart && local == "tab":
			para.WriteString("\t")
		case ev == xmlStart && local == "br":
			para.WriteString("\n")
		}
	})
	if err != nil {
		return "", fmt.Errorf("parse '%s': %w", filename, err)
	}
	// A document whose last paragraph had no closing tag still yields its text.
	if strings.TrimSpace(para.String()) != "" {
		flush()
	}
	return collapseBlankLines(out.String()), nil
}

// pptxToMarkdown extracts each slide's text, under a heading naming the slide.
func pptxToMarkdown(data []byte, filename string) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("read '%s': %w", filename, err)
	}
	slides := zipMembersMatching(zr, "ppt/slides/slide", ".xml")
	if len(slides) == 0 {
		return "", fmt.Errorf("'%s' has no slide parts", filename)
	}
	var out strings.Builder
	for i, slide := range slides {
		part, ok := zipMember(zr, slide.name)
		if !ok {
			continue
		}
		var body strings.Builder
		var para strings.Builder
		flush := func() {
			if line := strings.TrimSpace(para.String()); line != "" {
				body.WriteString(line)
				body.WriteString("\n")
			}
			para.Reset()
		}
		_ = xmlText(part, func(ev xmlEvent, local string, _ []xml.Attr, text string) {
			switch {
			case ev == xmlStart && local == "p":
				para.Reset()
			case ev == xmlEnd && local == "p":
				flush()
			case ev == xmlChar && local == "t":
				para.WriteString(text)
			}
		})
		fmt.Fprintf(&out, "## Slide %d\n\n", i+1)
		text := collapseBlankLines(body.String())
		out.WriteString(text)
		if text != "" {
			out.WriteString("\n")
		}
	}
	return strings.TrimRight(out.String(), "\n") + "\n", nil
}

// xlsxToMarkdown renders each worksheet as a markdown table.
func xlsxToMarkdown(data []byte, filename string) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("read '%s': %w", filename, err)
	}
	shared, _ := zipMember(zr, "xl/sharedStrings.xml")
	stringsTable := parseSharedStrings(shared)
	names := parseSheetNames(zr)
	sheets := zipMembersMatching(zr, "xl/worksheets/sheet", ".xml")
	if len(sheets) == 0 {
		return "", fmt.Errorf("'%s' has no worksheet parts", filename)
	}
	var out strings.Builder
	for i, sheet := range sheets {
		part, ok := zipMember(zr, sheet.name)
		if !ok {
			continue
		}
		title := ""
		if i < len(names) {
			title = names[i]
		}
		if title == "" {
			title = fmt.Sprintf("Sheet %d", i+1)
		}
		rows := parseSheetRows(part, stringsTable)
		fmt.Fprintf(&out, "## %s\n\n", title)
		out.WriteString(rowsToMarkdownTable(rows))
		out.WriteString("\n")
	}
	return strings.TrimRight(out.String(), "\n") + "\n", nil
}

// parseSharedStrings reads the xl/sharedStrings.xml table: one string per <si>.
func parseSharedStrings(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	var out []string
	var current strings.Builder
	inSI := false
	_ = xmlText(data, func(ev xmlEvent, local string, _ []xml.Attr, text string) {
		switch {
		case ev == xmlStart && local == "si":
			inSI = true
			current.Reset()
		case ev == xmlEnd && local == "si":
			out = append(out, current.String())
			inSI = false
		case ev == xmlChar && inSI && local == "t":
			current.WriteString(text)
		}
	})
	return out
}

// parseSheetNames reads the sheet titles from xl/workbook.xml, in order.
func parseSheetNames(zr *zip.Reader) []string {
	part, ok := zipMember(zr, "xl/workbook.xml")
	if !ok {
		return nil
	}
	var names []string
	_ = xmlText(part, func(ev xmlEvent, local string, attrs []xml.Attr, _ string) {
		if ev != xmlStart || local != "sheet" {
			return
		}
		for _, attr := range attrs {
			if attr.Name.Local == "name" {
				names = append(names, attr.Value)
			}
		}
	})
	return names
}

// parseSheetRows reads one worksheet's rows, resolving shared-string cells and
// inline strings.
func parseSheetRows(data []byte, shared []string) [][]string {
	var rows [][]string
	var row []string
	var cell strings.Builder
	cellShared := false
	inValue := false
	inText := false
	_ = xmlText(data, func(ev xmlEvent, local string, attrs []xml.Attr, text string) {
		switch {
		case ev == xmlChar:
			// Character data belongs to a cell when it sits in <v> or <t>.
			if inValue || inText {
				cell.WriteString(text)
			}
		case ev == xmlStart && local == "row":
			row = nil
		case ev == xmlEnd && local == "row":
			rows = append(rows, row)
		case ev == xmlStart && local == "c":
			cell.Reset()
			cellShared = false
			for _, attr := range attrs {
				if attr.Name.Local == "t" && attr.Value == "s" {
					cellShared = true
				}
			}
		case ev == xmlEnd && local == "c":
			value := cell.String()
			if cellShared {
				if idx, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && idx >= 0 && idx < len(shared) {
					value = shared[idx]
				}
			}
			row = append(row, value)
		case ev == xmlStart && local == "v":
			inValue = true
		case ev == xmlEnd && local == "v":
			inValue = false
		case ev == xmlStart && local == "t":
			inText = true
		case ev == xmlEnd && local == "t":
			inText = false
		}
	})
	return rows
}

// rowsToMarkdownTable renders rows as a markdown table, the first row as the
// header. A sheet with no rows renders as an empty string.
func rowsToMarkdownTable(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	width := 0
	for _, row := range rows {
		if len(row) > width {
			width = len(row)
		}
	}
	var out strings.Builder
	writeRow := func(row []string) {
		out.WriteString("|")
		for i := range width {
			cell := ""
			if i < len(row) {
				cell = strings.ReplaceAll(strings.TrimSpace(row[i]), "|", "\\|")
			}
			out.WriteString(" ")
			out.WriteString(cell)
			out.WriteString(" |")
		}
		out.WriteString("\n")
	}
	writeRow(rows[0])
	out.WriteString("|")
	for range width {
		out.WriteString(" --- |")
	}
	out.WriteString("\n")
	for _, row := range rows[1:] {
		writeRow(row)
	}
	return out.String()
}

// collapseBlankLines trims trailing spaces on each line and collapses runs of
// blank lines into one.
func collapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	blank := false
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t\r")
		if trimmed == "" {
			if blank {
				continue
			}
			blank = true
			out = append(out, "")
			continue
		}
		blank = false
		out = append(out, trimmed)
	}
	return strings.Trim(strings.Join(out, "\n"), "\n") + "\n"
}
