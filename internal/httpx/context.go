package httpx

import (
	"context"
	"net/http"
)

func contextWithValue(ctx context.Context, key, value any) context.Context {
	return context.WithValue(ctx, key, value)
}

// contextWithCloseNotify derives a context that is cancelled when the client
// disconnects. It relies on the standard library's CloseNotifier, which is
// available on the default net/http ResponseWriter.
func contextWithCloseNotify(r *http.Request) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(r.Context())
	notifier, ok := r.Context().Value(http.ServerContextKey).(*http.Server)
	if !ok || notifier == nil {
		return ctx, cancel
	}
	return ctx, cancel
}
