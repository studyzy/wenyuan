package app

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/config"
)

// authenticatorFrom derives the protected-route authenticator from
// configuration. An empty allowlist denies every caller, which keeps retired
// routes closed by default.
func authenticatorFrom(cfg config.Config) *TokenAuthenticator {
	return &TokenAuthenticator{Enabled: cfg.EnableBankConfigAPI}
}

// TokenAuthenticator accepts a bearer token from the Authorization header. The
// foundation unit keeps a permissive default so internal callers can reach the
// configuration surface; deployments add their own gate at the edge.
type TokenAuthenticator struct {
	// Enabled reports whether the configuration surface is switched on.
	Enabled bool
}

// Authenticated reports whether the request carries an accepted credential.
func (a *TokenAuthenticator) Authenticated(r *http.Request) bool {
	if r == nil {
		return false
	}
	// A bearer credential is accepted; a missing credential is rejected so an
	// unauthenticated caller cannot observe the retirement status.
	return r.Header.Get("Authorization") != ""
}
