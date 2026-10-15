package httpx

import (
	"net/http"
)

// TenantOverrideSource is the optional capability an Authenticator may
// implement to supply the caller's tenant-level configuration overrides.
//
// An authenticator that resolved a tenant for the request — because it
// consulted the tenant extension, say — implements it and returns that tenant's
// overrides. One that does not know a tenant omits the method, and the
// middleware below becomes a pass-through, so a deployment whose authenticator
// has no tenant layer is unaffected. It mirrors how a tag scope is carried: the
// middleware records what the extension resolved so a downstream handler can
// read it without reaching back into the auth path.
type TenantOverrideSource interface {
	// TenantOverrides returns the caller's tenant configuration overrides, or
	// nil when the authenticator cannot attribute the request to a tenant.
	TenantOverrides(r *http.Request) map[string]any
}

// tenantOverridesKey keys the caller's resolved tenant overrides on the request
// context. A distinct struct type keeps it from colliding with any other value.
type tenantOverridesKey struct{}

// WithTenantOverrides records the caller's tenant configuration overrides on
// the request, for a downstream handler to layer under the bank's own.
func WithTenantOverrides(r *http.Request, overrides map[string]any) *http.Request {
	return r.WithContext(contextWithValue(r.Context(), tenantOverridesKey{}, overrides))
}

// TenantOverridesFrom returns the caller's tenant configuration overrides, or
// nil when the request carries none.
func TenantOverridesFrom(r *http.Request) map[string]any {
	overrides, _ := r.Context().Value(tenantOverridesKey{}).(map[string]any)
	return overrides
}

// TenantOverridesMiddleware records the caller's tenant configuration overrides
// on the request context when the authenticator can supply them. An
// authenticator that does not implement TenantOverrideSource yields a
// pass-through, so a deployment whose authenticator knows no tenant behaves
// exactly as before the capability existed.
func TenantOverridesMiddleware(auth Authenticator) Middleware {
	source, ok := auth.(TenantOverrideSource)
	if !ok {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if overrides := source.TenantOverrides(r); len(overrides) > 0 {
				r = WithTenantOverrides(r, overrides)
			}
			next.ServeHTTP(w, r)
		})
	}
}
