package storage

import (
	"strings"
	"testing"
)

// Token counting measures real BPE tokens rather than a character
// approximation, so the reported figure matches what the model sees.
func TestCountTokensCountsRealTokens(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		check func(t *testing.T, got int)
	}{
		{
			name: "empty text is zero tokens",
			text: "",
			check: func(t *testing.T, got int) {
				if got != 0 {
					t.Fatalf("count = %d, want 0", got)
				}
			},
		},
		{
			name: "a common word is one token",
			text: "hello world",
			check: func(t *testing.T, got int) {
				if got != 2 {
					t.Fatalf("count = %d, want 2", got)
				}
			},
		},
		{
			name: "repeated text costs more than once",
			text: strings.Repeat("token ", 100),
			check: func(t *testing.T, got int) {
				if got < 100 {
					t.Fatalf("count = %d, want at least 100", got)
				}
			},
		},
		{
			name: "non-Latin text is counted rather than dropped",
			text: "文渊是一个记忆服务",
			check: func(t *testing.T, got int) {
				if got == 0 {
					t.Fatal("count = 0, want a positive count")
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.check(t, CountTokens(test.text))
		})
	}
}

// A write reports the token count of the submitted content, not the number of
// units it happened to produce.
func TestRetainAccountingCountsContentTokens(t *testing.T) {
	items := []RetainItem{{Content: strings.Repeat("word ", 200)}}
	usage, processed := RetainAccounting(items)
	if want := CountTokens(items[0].Content); processed != want {
		t.Fatalf("processed tokens = %d, want %d", processed, want)
	}
	if usage.TotalTokens == 0 || usage.InputTokens != processed {
		t.Fatalf("usage = %+v, want input %d", usage, processed)
	}
}

// The content-token figure is independent of how many chunks the content
// splits into, because it measures the input rather than the output.
func TestRetainAccountingIndependentOfChunkCount(t *testing.T) {
	short := []RetainItem{{Content: "short body"}}
	long := []RetainItem{{Content: strings.Repeat("a", ChunkSize*3)}}
	_, shortTokens := RetainAccounting(short)
	_, longTokens := RetainAccounting(long)
	if longTokens <= shortTokens {
		t.Fatalf("long content reported %d tokens, not more than short content's %d", longTokens, shortTokens)
	}
	if len(Segment(long[0].Content)) < 2 {
		t.Fatal("the long fixture is expected to split into several chunks")
	}
}

// Truncation honours the token budget and never splits a multi-byte rune.
func TestTruncateToTokens(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		maxTokens int
		check     func(t *testing.T, got string)
	}{
		{
			name:      "text within budget is returned whole",
			text:      "short",
			maxTokens: 100,
			check: func(t *testing.T, got string) {
				if got != "short" {
					t.Fatalf("got %q, want the input unchanged", got)
				}
			},
		},
		{
			name:      "text over budget is cut to fit",
			text:      strings.Repeat("word ", 500),
			maxTokens: 10,
			check: func(t *testing.T, got string) {
				if CountTokens(got) > 10 {
					t.Fatalf("got %d tokens, over the 10 budget", CountTokens(got))
				}
				if got == "" {
					t.Fatal("got an empty result, want a usable prefix")
				}
			},
		},
		{
			name:      "multibyte text is not cut mid-rune",
			text:      strings.Repeat("文渊记忆服务", 100),
			maxTokens: 5,
			check: func(t *testing.T, got string) {
				for _, r := range got {
					if r == '\uFFFD' {
						t.Fatalf("got %q, which carries a broken rune", got)
					}
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.check(t, truncateToTokens(test.text, test.maxTokens))
		})
	}
}
