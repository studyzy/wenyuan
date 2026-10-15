package fileparse

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
)

// The markup converters turn the text-based formats the file route accepts into
// markdown with the standard library alone.

// htmlToMarkdown converts HTML to markdown without an HTML parser dependency.
// Script and style bodies are dropped; block elements become line breaks,
// headings get their markdown prefix, list items their bullet, table cells a
// pipe, and anchors their link syntax. Character entities are decoded. The
// result is best-effort prose, which is what the retain pipeline consumes.
func htmlToMarkdown(data []byte) string {
	source := stripHTMLElements(string(data), "script")
	source = stripHTMLElements(source, "style")

	var out strings.Builder
	var href string
	for i := 0; i < len(source); {
		if source[i] != '<' {
			end := strings.IndexByte(source[i:], '<')
			if end < 0 {
				end = len(source) - i
			}
			out.WriteString(decodeHTMLEntities(source[i : i+end]))
			i += end
			continue
		}
		close := strings.IndexByte(source[i:], '>')
		if close < 0 {
			break
		}
		tag := source[i+1 : i+close]
		i += close + 1
		name, attrHref, closing := parseTag(tag)
		switch {
		case name == "a" && !closing:
			href = attrHref
			out.WriteString("[")
		case name == "a" && closing:
			if href != "" {
				out.WriteString("](" + href + ")")
			} else {
				out.WriteString("]")
			}
			href = ""
		case name == "li" && !closing:
			out.WriteString("\n- ")
		case name == "br" || name == "hr":
			out.WriteString("\n")
		case name == "p" || name == "div" || name == "tr" || name == "ul" ||
			name == "ol" || name == "table" || name == "section" || name == "article":
			out.WriteString("\n\n")
		case name == "td" || name == "th":
			out.WriteString(" | ")
		case len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6':
			if !closing {
				out.WriteString("\n\n" + strings.Repeat("#", int(name[1]-'0')) + " ")
			} else {
				out.WriteString("\n\n")
			}
		}
	}
	return collapseBlankLines(out.String())
}

// stripHTMLElements removes every <name ...>...</name> element body, including
// the tags themselves, matching the element name case-insensitively.
func stripHTMLElements(s, name string) string {
	open := "<" + name
	closeTag := "</" + name
	lower := strings.ToLower(s)
	var out strings.Builder
	for {
		start := strings.Index(lower, open)
		if start < 0 {
			out.WriteString(s)
			return out.String()
		}
		out.WriteString(s[:start])
		end := strings.Index(lower[start:], closeTag)
		if end < 0 {
			// An unclosed element swallows the remainder.
			return out.String()
		}
		end += start
		gt := strings.IndexByte(lower[end:], '>')
		if gt < 0 {
			return out.String()
		}
		cut := end + gt + 1
		s = s[cut:]
		lower = lower[cut:]
	}
}

// parseTag splits a tag body into its lowercased name, any href attribute, and
// whether it is a closing tag.
func parseTag(tag string) (name, href string, closing bool) {
	tag = strings.TrimSpace(tag)
	if strings.HasPrefix(tag, "/") {
		closing = true
		tag = strings.TrimSpace(tag[1:])
	}
	if strings.HasPrefix(tag, "!") || strings.HasPrefix(tag, "?") {
		return "", "", closing
	}
	fields := strings.Fields(tag)
	if len(fields) == 0 {
		return "", "", closing
	}
	name = strings.ToLower(fields[0])
	for _, field := range fields[1:] {
		lower := strings.ToLower(field)
		if strings.HasPrefix(lower, "href=") {
			href = strings.Trim(strings.SplitN(field, "=", 2)[1], `"'`)
		}
	}
	return name, href, closing
}

// decodeHTMLEntities resolves the common named entities and numeric character
// references.
func decodeHTMLEntities(s string) string {
	if !strings.ContainsRune(s, '&') {
		return s
	}
	replacements := []struct{ entity, rune string }{
		{"&amp;", "&"}, {"&lt;", "<"}, {"&gt;", ">"},
		{"&quot;", `"`}, {"&#39;", "'"}, {"&apos;", "'"}, {"&nbsp;", " "},
	}
	for _, r := range replacements {
		s = strings.ReplaceAll(s, r.entity, r.rune)
	}
	return s
}

// delimitedToMarkdown renders a CSV/TSV payload as a markdown table. A payload
// the CSV reader rejects is returned verbatim, so nothing is lost.
func delimitedToMarkdown(data []byte, comma rune) string {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = comma
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	rows, err := reader.ReadAll()
	if err != nil || len(rows) == 0 {
		return string(data)
	}
	return rowsToMarkdownTable(rows)
}

// jsonToMarkdown renders JSON as a fenced, indented code block. JSON Lines and
// a notebook's JSON both survive as-is; malformed input is returned verbatim.
func jsonToMarkdown(data []byte) string {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if json.Indent(&buf, trimmed, "", "  ") == nil {
		return "```json\n" + buf.String() + "\n```\n"
	}
	return "```json\n" + string(trimmed) + "\n```\n"
}
