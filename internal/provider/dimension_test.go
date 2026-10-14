package provider

import "testing"

// test-dimension-is-fixed-constant: the boundary reports the declared
// dimension as the single fixed constant, and the constant is the
// reference column's width.
func TestDeclaredDimensionIsFixedConstant(t *testing.T) {
	if EmbeddingDimensions != 1024 {
		t.Fatalf("EmbeddingDimensions = %d, want 1024", EmbeddingDimensions)
	}
	if got := DeclaredDimensions(); got != EmbeddingDimensions {
		t.Fatalf("DeclaredDimensions() = %d, want %d", got, EmbeddingDimensions)
	}
}

// The dimension is a compile-time constant, not a configuration key.
//
// The assertion is that it can appear in a const expression and in an array
// length, which a variable could not. A number written out here would only
// restate the other test while breaking on every deliberate width change.
func TestDimensionIsCompileTimeConstant(t *testing.T) {
	const doubled = EmbeddingDimensions * 2
	var sized [EmbeddingDimensions]float32
	if len(sized) != EmbeddingDimensions {
		t.Fatalf("array length = %d, want %d", len(sized), EmbeddingDimensions)
	}
	if doubled != 2*EmbeddingDimensions {
		t.Fatalf("const arithmetic = %d, want %d", doubled, 2*EmbeddingDimensions)
	}
}
