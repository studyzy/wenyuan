package config

import "testing"

// The reranker candidate cap defaults to the reference's
// DEFAULT_RERANKER_MAX_CANDIDATES (config.py:1416): 300, with the per-level
// overrides unset so each level falls back to the flat cap.
func TestRerankerCandidateDepthDefaults(t *testing.T) {
	r := resolveReranker(func(string) (string, bool) { return "", false })
	if r.CandidateDepth != 300 {
		t.Fatalf("CandidateDepth = %d, want the reference default 300", r.CandidateDepth)
	}
	if r.CandidateDepthLow != 0 || r.CandidateDepthMid != 0 || r.CandidateDepthHigh != 0 {
		t.Fatalf("per-level depths = %+v, want unset so they fall back to the flat cap", r)
	}
}
