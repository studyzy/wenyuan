package reflect

import (
	"sort"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
)

// Directives are the bank's standing instructions. They are hard rules rather
// than evidence: unlike a retrieved fact they are not weighed against the
// question, they are followed. They therefore appear twice in the reasoning
// loop — once as a mandatory section when the conversation opens, and once as a
// numbered reminder at the end of the system prompt — because a rule stated
// only at the top is out-ranked by the later turns of a long tool loop.

// Directive is a standing instruction as the prompt renders it: the name the
// bank gave it, and its content. The reference bolds the name ahead of the
// content when the bank set one, and renders bare content otherwise.
type Directive struct {
	Name    string
	Content string
}

// directivesSection renders the opening mandatory section, or "" when there are
// none. Its text is the reference's, byte for byte, including the trailing
// newline it emits.
func directivesSection(directives []Directive) string {
	rules := cleanDirectives(directives)
	if len(rules) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## DIRECTIVES (MANDATORY)\n")
	b.WriteString("These are hard rules you MUST follow in ALL responses:\n\n")
	for _, rule := range rules {
		b.WriteString("- ")
		b.WriteString(rule)
		b.WriteByte('\n')
	}
	b.WriteString("\nNEVER violate these directives, even if other context suggests otherwise.\n")
	b.WriteString("IMPORTANT: Do NOT explain or justify how you handled directives in your answer. Just follow them silently.\n")
	return b.String()
}

// directivesReminder renders the closing numbered reminder, or "" when there
// are none. Its text is the reference's, byte for byte, including the leading
// newline that separates it from whatever precedes it.
func directivesReminder(directives []Directive) string {
	rules := cleanDirectives(directives)
	if len(rules) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## REMINDER: MANDATORY DIRECTIVES\n")
	b.WriteString("Before responding, ensure your answer complies with ALL of these directives:\n\n")
	for i, rule := range rules {
		b.WriteString(itoa(i + 1))
		b.WriteString(". ")
		b.WriteString(rule)
		b.WriteByte('\n')
	}
	b.WriteString("\nYour response will be REJECTED if it violates any directive above.\n")
	b.WriteString("Do NOT include any commentary about how you handled directives - just follow them.")
	return b.String()
}

// directiveRule renders one directive as the reference does: the name in bold
// ahead of the content when the bank set one, bare content otherwise.
func directiveRule(directive Directive) string {
	if directive.Name != "" {
		return "**" + directive.Name + "**: " + directive.Content
	}
	return directive.Content
}

// cleanDirectives trims the content of each directive, drops those left empty
// and sorts the rest, so the same set of directives always renders the same
// prompt.
func cleanDirectives(directives []Directive) []string {
	out := make([]string, 0, len(directives))
	for _, directive := range directives {
		content := strings.TrimSpace(directive.Content)
		if content == "" {
			continue
		}
		out = append(out, directiveRule(Directive{Name: directive.Name, Content: content}))
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

// itoa renders a small positive integer without pulling in strconv at the call
// site.
func itoa(value int) string {
	if value <= 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// PlaceholderQuestion stands in for the question being reflected on.
const PlaceholderQuestion = "«the question being asked»"

// PreviewMessages returns the messages the reasoning loop and its answering call
// would send for a question, in order: the system prompt the loop sends, the
// question, and the answering call's system and user messages. It is what the
// prompt-preview route reports, so a reader sees the real wording rather than a
// second copy that can drift.
func PreviewMessages(directives []Directive) []provider.Message {
	question := PlaceholderQuestion
	return []provider.Message{
		{Role: provider.RoleSystem, Content: buildSystemPromptForTools(systemPromptOptions{
			Directives:          directives,
			HasMentalModels:     true,
			IncludeObservations: true,
		})},
		{Role: provider.RoleUser, Content: question},
		{Role: provider.RoleSystem, Content: buildFinalSystemPrompt(directives, bankProfile{})},
		{Role: provider.RoleUser, Content: buildFinalPrompt(question, "", 0)},
	}
}

// PreviewTools names the tools the loop exposes, so a preview can report the
// retrieval surface the model is given.
func PreviewTools() []string { return ToolNames() }
