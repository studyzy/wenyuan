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
