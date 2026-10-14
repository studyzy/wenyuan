package storage

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// EmbeddingDimensions is the vector width the stored column uses. It is fixed
// at 1024 to match the reference implementation's column and the provider
// boundary's declared dimension, and it is defined here once so the column and
// the boundary cannot drift apart.
const EmbeddingDimensions = 1024

// ErrEmbeddingDimensions reports an embedding whose width does not match the
// stored column. Such a value is rejected rather than truncated, because a
// silently truncated vector would rank against every other row incorrectly.
var ErrEmbeddingDimensions = fmt.Errorf("embedding must have %d dimensions", EmbeddingDimensions)

// ValidateEmbedding reports whether a vector can be stored.
func ValidateEmbedding(vec []float32) error {
	if len(vec) != EmbeddingDimensions {
		return ErrEmbeddingDimensions
	}
	for _, v := range vec {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return fmt.Errorf("embedding must not contain NaN or infinite values")
		}
	}
	return nil
}

// EncodeEmbedding renders a vector as the literal form the pgvector column
// accepts. A nil vector encodes as empty, which is stored as a SQL NULL so an
// unembedded row is distinguishable from a zero vector.
func EncodeEmbedding(vec []float32) string {
	if len(vec) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range vec {
		if i > 0 {
			b.WriteByte(',')
		}
		// Shortest representation that round-trips through float32.
		b.WriteString(strconv.FormatFloat(float64(v), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// NullableEmbedding returns the encoder's value or nil, so the caller can bind
// a SQL NULL for an unembedded row.
func NullableEmbedding(vec []float32) any {
	if len(vec) == 0 {
		return nil
	}
	return EncodeEmbedding(vec)
}

// DecodeEmbedding parses the pgvector literal form back into a vector. A blank
// value yields nil, which is how an unembedded row reads back.
func DecodeEmbedding(raw string) ([]float32, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "[]" {
		return nil, nil
	}
	trimmed = strings.TrimPrefix(trimmed, "[")
	trimmed = strings.TrimSuffix(trimmed, "]")
	parts := strings.Split(trimmed, ",")
	out := make([]float32, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 32)
		if err != nil {
			return nil, fmt.Errorf("malformed embedding component %q: %w", part, err)
		}
		out = append(out, float32(value))
	}
	return out, nil
}

// CosineSimilarity converts a cosine distance into a similarity in [0,1], so a
// stored distance ranks the same way the other arms' scores do. A distance of
// zero is an exact match and a distance of two is opposite.
func CosineSimilarity(distance float64) float64 {
	similarity := 1 - distance
	if similarity < 0 {
		return 0
	}
	if similarity > 1 {
		return 1
	}
	return similarity
}
