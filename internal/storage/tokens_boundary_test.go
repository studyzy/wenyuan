package storage

import (
	"strings"
	"testing"
)

// A non-positive budget clamps to zero and yields an empty string rather than
// the original text.
func TestTruncateToTokensNonPositiveBudgetIsEmpty(t *testing.T) {
	for _, budget := range []int{0, -3} {
		if got := truncateToTokens("hello world", budget); got != "" {
			t.Fatalf("budget %d = %q, want empty", budget, got)
		}
	}
}

// Truncation drops only incomplete characters; it does not trim the whitespace
// that survives the cut.
func TestTruncateToTokensKeepsSurroundingWhitespace(t *testing.T) {
	text := "  " + strings.Repeat("word ", 200)
	got := truncateToTokens(text, 20)
	if got == "" {
		t.Fatal("got an empty result")
	}
	if !strings.HasPrefix(got, "  ") {
		t.Fatalf("leading whitespace was trimmed: %q", got)
	}
	for _, r := range got {
		if r == '�' {
			t.Fatal("got a broken rune")
		}
	}
}
