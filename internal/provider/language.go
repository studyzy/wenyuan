package provider

import "strings"

// OutputLanguageDirective is the instruction that forces every generated
// artifact into the deployment's configured language. It is appended to the
// prompt of each pipeline (retain, consolidation, reflect) so one setting has a
// uniform effect. An empty or blank language returns an empty string, leaving
// the prompt unchanged.
//
// It is the counterpart of the preserve-the-source-language rule each pipeline
// otherwise carries: when a language is configured the rule is dropped and this
// directive takes its place.
func OutputLanguageDirective(language string) string {
	language = strings.TrimSpace(language)
	if language == "" {
		return ""
	}
	return "\n\nIMPORTANT: Respond exclusively in " + language + ". " +
		"Translate any source content into " + language + ". " +
		"All output text — including fact text, observations, entity names, and the final response — must be in " + language + "."
}
