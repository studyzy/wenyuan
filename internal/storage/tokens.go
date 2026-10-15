package storage

import (
	"strings"
	"sync"

	"github.com/tiktoken-go/tokenizer"
)

// DefaultTokenEncoding is the vocabulary this build counts with unless the
// operator selects another. It matches the reference's default (o200k_base,
// the vocabulary current OpenAI models tokenize with) rather than cl100k_base:
// the two agree to within a fraction of a percent on English, but o200k_base is
// far closer on non-Latin scripts, and these counts drive the budgets that
// stand in for a model's context window.
const DefaultTokenEncoding = tokenizer.O200kBase

// Token counting is one operation with two possible backends per vocabulary:
// the real BPE tokenizer, and a character-based approximation used only when
// the tokenizer cannot be initialised. The approximation keeps the service
// serving rather than failing a write because a vocabulary could not be loaded.
var (
	tokenCounterMu     sync.RWMutex
	tokenCounterName   = string(DefaultTokenEncoding)
	tokenCounterByName = map[string]tokenCounterFunc{}
)

// tokenCounterFunc reports how many tokens text costs.
type tokenCounterFunc func(text string) int

// SetTokenEncoding selects the vocabulary CountTokens measures with. An empty
// or unknown name leaves the selection unchanged, so a misconfigured value
// degrades to the default rather than disabling token counting.
func SetTokenEncoding(name string) {
	if name == "" || !tokenizerSupported(name) {
		return
	}
	tokenCounterMu.Lock()
	tokenCounterName = name
	tokenCounterMu.Unlock()
}

// TokenEncoding reports the vocabulary CountTokens currently measures with.
func TokenEncoding() string {
	tokenCounterMu.RLock()
	defer tokenCounterMu.RUnlock()
	return tokenCounterName
}

// tokenizerSupported reports whether the named vocabulary can be loaded.
func tokenizerSupported(name string) bool {
	_, err := tokenizer.Get(tokenizer.Encoding(name))
	return err == nil
}

// counterFor returns the cached counter for a vocabulary, loading it once. A
// vocabulary that cannot be loaded falls back to the character approximation.
func counterFor(name string) tokenCounterFunc {
	if name == "" {
		name = string(DefaultTokenEncoding)
	}
	tokenCounterMu.RLock()
	cached, ok := tokenCounterByName[name]
	tokenCounterMu.RUnlock()
	if ok {
		return cached
	}
	counter := approximateTokenCount
	if codec, err := tokenizer.Get(tokenizer.Encoding(name)); err == nil {
		counter = func(text string) int {
			count, err := codec.Count(text)
			if err != nil {
				return approximateTokenCount(text)
			}
			return count
		}
	}
	tokenCounterMu.Lock()
	tokenCounterByName[name] = counter
	tokenCounterMu.Unlock()
	return counter
}

// CountTokens reports the number of BPE tokens in text under the selected
// vocabulary.
//
// The count is what the response's processed-content figure and the chunk
// budget are measured in, so it must be the same measure every time: the
// tokenizer is initialised once per vocabulary and reused.
func CountTokens(text string) int {
	return CountTokensWith(text, TokenEncoding())
}

// CountTokensWith counts text under an explicitly named vocabulary. A caller
// that knows the vocabulary it is budgeting against (the embedding model's own
// tokenizer, say) passes it here so its count matches what the provider
// enforces, rather than the general-purpose selection.
func CountTokensWith(text, encoding string) int {
	if text == "" {
		return 0
	}
	return counterFor(encoding)(text)
}

// approximateTokenCount estimates tokens from characters alone. It is the
// fallback for a tokenizer that could not be loaded, and it is deliberately
// the same rough ratio the rest of the codebase uses so a degraded count is at
// least consistent.
func approximateTokenCount(text string) int {
	return len(text)/4 + 1
}

// countTokensFor is the counter the package uses internally, so the seam is
// swappable in tests without touching every call site.
var countTokensFor = CountTokens

// RetainAccounting derives the token accounting for a write whose items were
// submitted. The content tokens are counted from the submitted text, so the
// figure reports what was processed rather than how many units the write
// happened to produce.
func RetainAccounting(items []RetainItem) (Usage, int) {
	total := 0
	for _, item := range items {
		total += countTokensFor(item.Content)
	}
	return Usage{
		InputTokens:  total,
		OutputTokens: total,
		TotalTokens:  total * 2,
	}, total
}

// truncateToTokens returns the longest prefix of text whose token count does
// not exceed maxTokens. It is used where a caller supplies a token budget, and
// it never splits a multi-byte rune.
func truncateToTokens(text string, maxTokens int) string {
	if maxTokens <= 0 {
		return text
	}
	if countTokensFor(text) <= maxTokens {
		return text
	}
	runes := []rune(text)
	// Binary search the largest rune prefix within budget; a linear scan of a
	// large document would be needlessly slow.
	low, high := 0, len(runes)
	for low < high {
		mid := (low + high + 1) / 2
		if countTokensFor(string(runes[:mid])) <= maxTokens {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return strings.TrimSpace(string(runes[:low]))
}
