package provider

import "testing"

// test-dimension-is-fixed-constant: the boundary reports the declared
// dimension as the single fixed constant, and the constant is 384.
func TestDeclaredDimensionIsFixedConstant(t *testing.T) {
	if EmbeddingDimensions != 384 {
		t.Fatalf("EmbeddingDimensions = %d, want 384", EmbeddingDimensions)
	}
	if got := DeclaredDimensions(); got != EmbeddingDimensions {
		t.Fatalf("DeclaredDimensions() = %d, want %d", got, EmbeddingDimensions)
	}
}

// The dimension is a compile-time constant, not a configuration key. This
// asserts the type is an untyped integer constant usable in a const block,
// which a variable would not be.
func TestDimensionIsCompileTimeConstant(t *testing.T) {
	const doubled = EmbeddingDimensions * 2
	if doubled != 768 {
		t.Fatalf("doubled = %d, want 768", doubled)
	}
}
