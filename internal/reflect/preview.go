package reflect

import (
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
)

// PreviewProfile is the bank identity the previewed reflect prompt shows: the
// name, the mission (reflect_mission overlaying the legacy column) and the
// graded disposition traits.
type PreviewProfile struct {
	Name        string
	Mission     string
	Disposition map[string]int
}

// ToolsLanguageRule is the reasoning loop's default language rule, reported as
// the llm_output_language block when the deployment configures no language.
const ToolsLanguageRule = toolsLanguageRule

// BankNameLine renders the bank's name as the prompt writes it. Shared with the
// prompt preview so the wording has one home.
func BankNameLine(profile PreviewProfile) string {
	name := profile.Name
	if name == "" {
		name = "Assistant"
	}
	return "## Memory Bank: " + name
}

// BankDispositionLine renders the bank's disposition traits as the prompt writes
// them, or "" when it has none.
func BankDispositionLine(profile PreviewProfile) string {
	return dispositionLine(profile.Disposition)
}

// DirectivesSection renders the opening mandatory directives section, or "" when
// there are none.
func DirectivesSection(directives []Directive) string {
	return directivesSection(directives)
}

// PreviewSystemPrompt renders the tool-loop system prompt for a bank profile,
// with output-language handling applied the way the reference assembles it: an
// explicit language drops the default language rule outright (the directive
// itself rides on the user message).
func PreviewSystemPrompt(profile PreviewProfile, directives []Directive, outputLanguage string) string {
	text := buildSystemPromptForTools(systemPromptOptions{
		Profile:             bankProfile(profile),
		Directives:          directives,
		HasMentalModels:     true,
		IncludeObservations: true,
	})
	if strings.TrimSpace(outputLanguage) != "" {
		text = strings.Replace(text, toolsLanguageRule+"\n\n", "", 1)
	}
	return text
}

// AgentUserPrompt renders the reasoning loop's opening user message: the
// question, closed by the output-language directive when one is configured.
func AgentUserPrompt(query, outputLanguage string) string {
	return query + provider.OutputLanguageDirective(outputLanguage)
}
