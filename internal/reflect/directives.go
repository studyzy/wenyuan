package reflect

import (
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
)

// Directives are the bank's standing instructions. They are hard rules rather
// than evidence: unlike a retrieved fact they are not weighed against the
// question, they are followed. They therefore appear twice in the reasoning
// loop — once as a mandatory section when the conversation opens, and once as a
// numbered reminder beside the answer — because a rule stated only at the top
// is out-ranked by the later turns of a long tool loop.

// directivesSection renders the opening mandatory section, or "" when there are
// none.
func directivesSection(directives []string) string {
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
	b.WriteString("IMPORTANT: Do NOT explain or justify how you handled directives in your answer. Just follow them silently.")
	return b.String()
}

// directivesReminder renders the closing numbered reminder, or "" when there
// are none.
func directivesReminder(directives []string) string {
	rules := cleanDirectives(directives)
	if len(rules) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## REMINDER: MANDATORY DIRECTIVES\n")
	b.WriteString("Before responding, ensure your answer complies with ALL of these directives:\n\n")
	for i, rule := range rules {
		b.WriteString(strings.TrimSpace(strings.Join([]string{itoa(i + 1), ". ", rule}, "")))
		b.WriteByte('\n')
	}
	b.WriteString("\nYour response will be REJECTED if it violates any directive above.\n")
	b.WriteString("Do NOT include any commentary about how you handled directives - just follow them.")
	return b.String()
}

// cleanDirectives trims, drops blanks and sorts, so the same set of directives
// always renders the same prompt.
func cleanDirectives(directives []string) []string {
	out := make([]string, 0, len(directives))
	for _, directive := range directives {
		if trimmed := strings.TrimSpace(directive); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return sortStrings(out)
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
func PreviewMessages(directives []string) []provider.Message {
	question := PlaceholderQuestion
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: buildSystemPromptForTools(systemPromptOptions{
			Directives:          directives,
			HasMentalModels:     true,
			IncludeObservations: true,
		})},
		{Role: provider.RoleUser, Content: question},
		{Role: provider.RoleSystem, Content: buildFinalSystemPrompt(nil, bankProfile{})},
		{Role: provider.RoleUser, Content: buildFinalPrompt(question, "", 0)},
	}
	if reminder := directivesReminder(directives); reminder != "" {
		messages = append(messages, provider.Message{Role: provider.RoleSystem, Content: reminder})
	}
	return messages
}

// PreviewTools names the tools the loop exposes, so a preview can report the
// retrieval surface the model is given.
func PreviewTools() []string { return ToolNames() }
