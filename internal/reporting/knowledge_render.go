package reporting

import (
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// Knowledge-page markdown constants. They mirror the reference's rendering
// module so the read surface and the export bundle carry the same bytes.
const (
	// knowledgeDefaultPageType is the `type` a page reports when it declares
	// none through a `type:<x>` tag.
	knowledgeDefaultPageType = "knowledge-page"
	// knowledgeTypeTagPrefix marks the tag that lifts a page's type.
	knowledgeTypeTagPrefix = "type:"
	// knowledgeEmptyNotice is what a reader is told when a page has no body yet.
	// It is produced on the way out and never stored, because a stored marker
	// would make every unwritten page searchable as its own marker.
	knowledgeEmptyNotice = "No content yet."
	// knowledgeIndexFilename is the reserved navigation file of an export.
	knowledgeIndexFilename = "index.md"
	// knowledgeLogSuffix is the reserved per-page history filename suffix, so a
	// bundle carries the refreshes a page went through, not only its last body.
	knowledgeLogSuffix = ".log.md"
	// knowledgeSnippetChars bounds a search snippet to the page's opening text.
	knowledgeSnippetChars = 280
	// knowledgePageDefaultMaxTokens is the refresh budget a knowledge page gets
	// when its create names none, matching the reference's page default.
	knowledgePageDefaultMaxTokens = 4096
)

// knowledgePageTypeInfo is a page's `type` plus the tags left after the type
// tag is split off from them.
type knowledgePageTypeInfo struct {
	Type        string
	DisplayTags []string
}

// knowledgePageType splits a `type:<x>` tag out of the tag list. The first such
// tag wins and every `type:` tag is removed from the display tags, so a page's
// type never leaks into its label list.
func knowledgePageType(tags []string) knowledgePageTypeInfo {
	resolved := knowledgeDefaultPageType
	display := make([]string, 0, len(tags))
	for _, tag := range tags {
		if strings.HasPrefix(tag, knowledgeTypeTagPrefix) {
			if suffix := strings.TrimSpace(strings.TrimPrefix(tag, knowledgeTypeTagPrefix)); suffix != "" &&
				resolved == knowledgeDefaultPageType {
				resolved = suffix
			}
			continue
		}
		display = append(display, tag)
	}
	return knowledgePageTypeInfo{Type: resolved, DisplayTags: display}
}

// knowledgeSnippet is the opening text a search result shows. The reference cuts
// the stored body to its first 280 characters in SQL and strips that window, so
// leading whitespace consumes the budget rather than being removed before it. A
// page whose body is still empty says so in words rather than coming back blank,
// so a caller can tell an unwritten page from one whose snippet did not render.
func knowledgeSnippet(content string) string {
	runes := []rune(content)
	if len(runes) > knowledgeSnippetChars {
		runes = runes[:knowledgeSnippetChars]
	}
	trimmed := strings.TrimSpace(string(runes))
	if trimmed == "" {
		return knowledgeEmptyNotice
	}
	return trimmed
}

// knowledgeField is one ordered frontmatter entry: either a scalar or a list.
type knowledgeField struct {
	key    string
	value  string
	list   []string
	isList bool
}

// renderKnowledgeFrontmatter renders an ordered frontmatter mapping into a
// `---` fenced YAML block. Empty scalars and empty lists are dropped, matching
// the reference, so a folder or an unwritten page does not carry a blank key.
func renderKnowledgeFrontmatter(fields []knowledgeField) string {
	lines := []string{"---"}
	for _, field := range fields {
		if field.isList {
			if len(field.list) == 0 {
				continue
			}
			lines = append(lines, field.key+":")
			for _, item := range field.list {
				lines = append(lines, "  - "+knowledgeScalar(item))
			}
			continue
		}
		if field.value == "" {
			continue
		}
		lines = append(lines, field.key+": "+knowledgeScalar(field.value))
	}
	lines = append(lines, "---")
	return strings.Join(lines, "\n")
}

// knowledgeScalar emits a YAML-safe double-quoted scalar, so arbitrary page
// names and source queries cannot be misread as YAML special forms.
func knowledgeScalar(value string) string {
	escaped := strings.ReplaceAll(value, "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "\"", "\\\"")
	escaped = strings.ReplaceAll(escaped, "\n", "\\n")
	escaped = strings.ReplaceAll(escaped, "\r", "")
	return "\"" + escaped + "\""
}

// knowledgeDocumentFields builds the ordered frontmatter of a page document:
// id, type, title, description, tags, timestamp.
func knowledgeDocumentFields(id, pageType, title, description, timestamp string, tags []string) []knowledgeField {
	return []knowledgeField{
		{key: "id", value: id},
		{key: "type", value: pageType},
		{key: "title", value: title},
		{key: "description", value: description},
		{key: "tags", list: tags, isList: true},
		{key: "timestamp", value: timestamp},
	}
}

// renderKnowledgeDocument renders a page as a markdown document: frontmatter
// block plus body. `noticeWhenEmpty` puts the empty-page notice in place of a
// missing body for the surfaces a reader sees; the export bundle leaves it off
// so a notice cannot be mistaken for stored content on the way back in.
func renderKnowledgeDocument(fields []knowledgeField, body string, noticeWhenEmpty bool) string {
	frontmatter := renderKnowledgeFrontmatter(fields)
	trimmed := strings.TrimSpace(body)
	if trimmed == "" && noticeWhenEmpty {
		trimmed = knowledgeEmptyNotice
	}
	if trimmed != "" {
		return frontmatter + "\n\n" + trimmed + "\n"
	}
	return frontmatter + "\n"
}

// renderKnowledgeIndex renders the reserved `index.md` as nested markdown
// navigation over the tree: folders nest their children, pages link to their
// document and name the question they answer.
func renderKnowledgeIndex(roots []KnowledgeNode) string {
	frontmatter := renderKnowledgeFrontmatter([]knowledgeField{
		{key: "type", value: "index"},
		{key: "title", value: "Knowledge base"},
	})
	lines := []string{frontmatter, "", "# Knowledge base", ""}
	var walk func(nodes []KnowledgeNode, depth int)
	walk = func(nodes []KnowledgeNode, depth int) {
		for _, node := range nodes {
			indent := strings.Repeat("  ", depth)
			if node.Kind == string(knowledgeKindFolder) {
				lines = append(lines, indent+"- **"+node.Name+"/**")
				walk(node.Children, depth+1)
				continue
			}
			link := indent + "- [" + node.Name + "](./" + node.ID + ".md)"
			if description := derefString(node.Description); description != "" {
				lines = append(lines, link+" \u2014 "+description)
			} else {
				lines = append(lines, link)
			}
		}
	}
	walk(roots, 0)
	if len(lines) == 4 {
		lines = append(lines, "_No knowledge pages yet._")
	}
	return strings.Join(lines, "\n") + "\n"
}

// renderKnowledgeLog renders the reserved per-page `<id>.log.md` from a page's
// refresh history. Each entry captures the content a refresh replaced, newest
// first; the file exists only when there is at least one entry, so a bundle
// never carries an empty history.
func renderKnowledgeLog(title string, entries []storage.MentalModelHistoryEntry) string {
	frontmatter := renderKnowledgeFrontmatter([]knowledgeField{
		{key: "type", value: "log"},
		{key: "title", value: title + " \u2014 history"},
	})
	lines := []string{frontmatter, "", "# " + title + " \u2014 history", ""}
	if len(entries) == 0 {
		lines = append(lines, "_No refresh history._")
		return strings.Join(lines, "\n") + "\n"
	}
	for _, entry := range entries {
		changedAt := httpx.FormatTimestamp(entry.ChangedAt)
		if changedAt == "" {
			changedAt = "unknown"
		}
		previous := ""
		if entry.PreviousContent != nil {
			previous = strings.TrimSpace(*entry.PreviousContent)
		}
		if previous == "" {
			previous = "_(empty)_"
		}
		lines = append(lines, "## "+changedAt, "", previous, "")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), " \n\t") + "\n"
}
