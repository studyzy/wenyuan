package provider

// EmbeddingDimensions is the vector width the destination stores, fixed at
// 384.
//
// The value is the reference implementation's column width — both its own
// migration and a live reference database declare `vector(384)`, and the
// reference resolves the width from HINDSIGHT_API_EMBEDDING_DIMENSION, whose
// default is 384 — so this service can read and write an existing database's
// vectors rather than only its own. It is deliberately a compile-time constant
// rather than a configuration key: the vector column must agree with the width
// the provider boundary declares, and a configurable width would let the two
// drift apart. A configuration that resolves another width is refused at
// composition time rather than silently truncating or failing to store.
//
// Every path that needs the width imports this constant; it is never restated.
const EmbeddingDimensions = 384

// DeclaredDimensions reports the embedding dimension the boundary guarantees
// for every vector it returns.
func DeclaredDimensions() int { return EmbeddingDimensions }
