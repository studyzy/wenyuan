package provider

// EmbeddingDimensions is the vector width the destination stores, fixed at
// 1024.
//
// The value is the reference implementation's column width, so this service can
// read and write an existing database's vectors rather than only its own. It is
// deliberately a compile-time constant rather than a configuration key: the
// vector column must agree with the width the provider boundary declares, and a
// configurable width would let the two drift apart. Every path that needs the
// width imports this constant; it is never restated.
//
// The consequence for a deployment: the embedding model must emit 1024 floats.
// A model configured for another width cannot store vectors at all — the column
// rejects them rather than truncating.
const EmbeddingDimensions = 1024

// DeclaredDimensions reports the embedding dimension the boundary guarantees
// for every vector it returns.
func DeclaredDimensions() int { return EmbeddingDimensions }
