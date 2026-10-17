package consolidation

import (
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
)

// DefaultLanguageRule is the consolidation pipeline's preserve-the-source-
// language rule, reported as the llm_output_language block when the deployment
// configures no language.
const DefaultLanguageRule = consolidationLanguageRule

// MissionSection renders the MISSION section, heading included, as the user
// message writes it. Shared with the prompt preview, which reports this section
// as the block the observations_mission setting produces.
func MissionSection(observationsMission string) string {
	mission := observationsMission
	if strings.TrimSpace(mission) == "" {
		mission = DefaultMission
	}
	return "## MISSION\n\n" + escapeForPrompt(mission)
}

// PreviewSystemPrompt renders the consolidation system prompt for the preview,
// with the deployment's output-language handling applied the way the reference
// assembles it: an explicit language drops the preserve-the-source-language rule
// and is appended as the directive at the end.
func PreviewSystemPrompt(outputLanguage string) string {
	if strings.TrimSpace(outputLanguage) == "" {
		return systemPrompt
	}
	text := strings.Replace(systemPrompt, consolidationLanguageRule+"\n\n", "", 1)
	return text + provider.OutputLanguageDirective(outputLanguage)
}

// PreviewInput renders the per-batch user message the preview reports: the
// MISSION section (the bank's observations mission, or the shipped default) and
// the fixed placeholder facts and observations — never the JSON array a real
// pass carries, because a preview answers "what does this bank send" rather than
// showing a caller-supplied sample.
func PreviewInput(observationsMission string) string {
	return MissionSection(observationsMission) + "\n\n" +
		"## INPUT\n\n### New facts\n\n" + PlaceholderFacts +
		"\n\n### Existing observations\n\n" + PlaceholderObservations
}

// escapeForPrompt doubles lone braces so caller text survives the reference's
// str.format pass untouched. A brace adjacent to another of the same kind is
// already a literal pair and is left as-is.
func escapeForPrompt(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	runes := []rune(text)
	for i, r := range runes {
		lone := (i == 0 || runes[i-1] != r) && (i == len(runes)-1 || runes[i+1] != r)
		if lone && r == '{' {
			b.WriteString("{{")
			continue
		}
		if lone && r == '}' {
			b.WriteString("}}")
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
