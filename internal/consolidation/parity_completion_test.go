package consolidation

import "testing"

// Exact-duplicate matching preserves case: "TLS" and "tls" can carry different
// information, so case-folding them into one row would drop a create.
func TestNormalizeObservationTextPreservesCase(t *testing.T) {
	if got := normalizeObservationText("  TLS   handshake  "); got != "TLS handshake" {
		t.Fatalf("normalized = %q, want %q", got, "TLS handshake")
	}
	if normalizeObservationText("TLS") == normalizeObservationText("tls") {
		t.Fatal("case-only variants must stay distinct")
	}
}
