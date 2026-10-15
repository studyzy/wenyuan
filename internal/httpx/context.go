package httpx

import (
	"context"
)

func contextWithValue(ctx context.Context, key, value any) context.Context {
	return context.WithValue(ctx, key, value)
}
