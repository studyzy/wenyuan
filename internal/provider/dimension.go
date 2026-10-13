package provider

// EmbeddingDimensions is the vector width the destination stores, fixed at 384.
//
// It is deliberately a compile-time constant rather than a configuration key:
// the vector column a later unit adds must agree with the width the provider
// boundary declares, and a configurable width would let the two drift apart.
// Every path that needs the width imports this constant; it is never restated.
const EmbeddingDimensions = 384

// DeclaredDimensions reports the embedding dimension the boundary guarantees
// for every vector it returns.
func DeclaredDimensions() int { return EmbeddingDimensions }
