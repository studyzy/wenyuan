package provider

import "context"

// TraceContext carries the attribution a model call is recorded under. It is
// optional: a call with no context set is still recorded, with empty
// attribution, because the call itself is the fact worth keeping.
type TraceContext struct {
	// BankID is the bank the call was made for.
	BankID string
	// Operation is the operation label, such as retain or reflect.
	Operation string
	// Metadata is the row's own `metadata` object, as decoded values. The
	// reference always writes one — `_safe_json(record.metadata) or "{}"`
	// (engine/llm_trace.py:899) — so a call that bound nothing stores `{}`
	// rather than NULL, and the `?document_id=` / `?memory_id=` filters read
	// one shape whichever call wrote the row.
	Metadata map[string]any
}

// IsEmpty reports whether the attribution names nothing at all. A call whose
// context carries an empty attribution is not part of a known run: it still
// gets its own trace, but no bank, operation or metadata.
func (t TraceContext) IsEmpty() bool {
	return t.BankID == "" && t.Operation == "" && len(t.Metadata) == 0
}

// WithResource returns a copy attributed to the given bank and operation, with
// no metadata. It is what binds a new run. The metadata set is left alone, so a
// caller that already bound per-call values keeps them.
func (t TraceContext) WithResource(bankID, operation string) TraceContext {
	t.BankID = bankID
	t.Operation = operation
	return t
}

// WithMetadata returns a copy whose metadata has the given values merged over
// the current ones. A later binding wins, which is what lets a per-call value
// refine the run's.
//
// Both methods are value-based rather than context-based so a component outside
// this package can shape a trace context it was handed without importing this
// one — the boundary the extraction pipeline sits on.
func (t TraceContext) WithMetadata(metadata map[string]any) TraceContext {
	if len(metadata) == 0 {
		return t
	}
	merged := make(map[string]any, len(t.Metadata)+len(metadata))
	for key, value := range t.Metadata {
		merged[key] = value
	}
	for key, value := range metadata {
		merged[key] = value
	}
	t.Metadata = merged
	return t
}

type traceContextKey struct{}

// WithTraceContext attaches call attribution to a context.
func WithTraceContext(ctx context.Context, tc TraceContext) context.Context {
	return context.WithValue(ctx, traceContextKey{}, tc)
}

// TraceContextFrom reads the attribution attached to a context.
func TraceContextFrom(ctx context.Context) (TraceContext, bool) {
	tc, ok := ctx.Value(traceContextKey{}).(TraceContext)
	return tc, ok
}
