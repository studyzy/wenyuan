package storage

import (
	"strings"
	"sync"

	"github.com/tiktoken-go/tokenizer"
)

// Token counting is one operation with two possible backends: the real BPE
// tokenizer, and a character-based approximation used only when the tokenizer
// cannot be initialised. The approximation keeps the service serving rather
// than failing a write because a vocabulary could not be loaded.
var (
	tokenCounterOnce sync.Once
	tokenCounter     tokenCounterFunc
)

// tokenCounterFunc reports how many tokens text costs.
type tokenCounterFunc func(text string) int

// CountTokens reports the number of BPE tokens in text.
//
// The count is what the response's processed-content figure and the chunk
// budget are measured in, so it must be the same measure every time: the
// tokenizer is initialised once and reused.
func CountTokens(text string) int {
	if text == "" {
		return 0
	}
	tokenCounterOnce.Do(func() {
		codec, err := tokenizer.Get(tokenizer.Cl100kBase)
		if err != nil {
			tokenCounter = approximateTokenCount
			return
		}
		tokenCounter = func(text string) int {
			count, err := codec.Count(text)
			if err != nil {
				return approximateTokenCount(text)
			}
			return count
		}
	})
	return tokenCounter(text)
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
