//go:build pgx

package storage

// nativeLanguageOrDefault resolves the dictionary a component that does not own
// the text-search backend writes its keyword vectors with: the configured one,
// or the package default when it was given none.
func nativeLanguageOrDefault(language string) string {
	if language == "" {
		return defaultNativeLanguage
	}
	return language
}
