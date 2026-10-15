// Package tenant resolves which PostgreSQL schema a request or a background
// job operates on.
//
// Every query the storage layer issues is fully qualified with a schema name,
// so tenant isolation is a matter of agreeing on that name. This package is the
// single place that answers it, which is what lets a background loop discover
// the schemas it should sweep without knowing how a deployment decides them.
//
// The default extension reproduces the single-tenant behaviour the service has
// always had: one schema, taken from the configuration, with no authentication.
// A deployment that needs real isolation supplies its own extension and the
// callers do not change.
package tenant

import (
	"context"
	"errors"
	"strings"
)

// ErrAuthentication is returned when a request's credentials do not resolve to
// a tenant. It is a sentinel so a caller can map it onto a response status
// without inspecting the message.
var ErrAuthentication = errors.New("tenant authentication failed")

// AuthenticationError reports why a request could not be attributed to a
// tenant, and carries any headers the response should include.
type AuthenticationError struct {
	// Reason describes the failure for the operator's log.
	Reason string
	// Headers are extra response headers the extension wants sent, such as a
	// challenge for a scheme the client should retry with.
	Headers map[string]string
}

func (e *AuthenticationError) Error() string {
	return ErrAuthentication.Error() + ": " + e.Reason
}

// Unwrap exposes the sentinel so errors.Is works for the whole class.
func (e *AuthenticationError) Unwrap() error { return ErrAuthentication }

// Context is the resolved tenant for one operation: the schema every statement
// in that operation is qualified with.
type Context struct {
	// Schema is the PostgreSQL schema name.
	Schema string
	// Overrides are the tenant's configuration overrides, keyed by the public
	// config names. They sit between the process configuration they layer over
	// and the bank's own overrides, which win per key; a deployment that does
	// not key configuration by tenant leaves this nil.
	Overrides map[string]any
}

// Tenant is one schema a background worker should process. It is the discovery
// counterpart to Context: a request arrives with credentials and resolves to a
// Context, while a worker has no request and asks for the whole list.
type Tenant struct {
	// Schema is the PostgreSQL schema name.
	Schema string
	// ID is an optional tenant identifier. When provided, a background job can
	// carry it so configuration resolution honours tenant-level overrides; it
	// is empty for a deployment that does not key configuration by tenant.
	ID string
	// Overrides are the tenant's configuration overrides, keyed by the public
	// config names. Background work that resolves configuration for a tenant
	// applies them beneath the bank's own overrides; a deployment that does not
	// key configuration by tenant leaves this nil.
	Overrides map[string]any
}

// Extension resolves the schema for a request and enumerates the schemas a
// worker should process.
type Extension interface {
	// Authenticate attributes a request to a tenant, or returns an
	// *AuthenticationError.
	Authenticate(ctx context.Context, request Request) (Context, error)
	// ListTenants returns every schema background work should cover. A
	// single-tenant deployment returns the one configured schema.
	ListTenants(ctx context.Context) ([]Tenant, error)
}

// Request is the request data an extension may authenticate on.
//
// Only the credential is populated by default. A deployment that authenticates
// on something else — an identity assertion a proxy attaches beside a shared
// bearer token, say — opts those headers in through configuration, so an
// extension sees no header data unless an operator asked for it.
type Request struct {
	// Credential is the bearer credential the request carried, empty when
	// none was supplied.
	Credential string
	// Headers holds any headers the deployment opted into, keyed by
	// lower-cased name.
	Headers map[string]string
}

// Default is the single-tenant extension: no authentication, and one schema
// taken from the configuration.
//
// It is what a deployment with no tenant extension configured gets, so the
// service keeps serving exactly as before the abstraction existed.
type Default struct {
	schema string
}

// NewDefault builds the single-tenant extension over a resolved schema name. An
// empty name means the default schema the storage layer would have used.
func NewDefault(schema string) *Default {
	trimmed := strings.TrimSpace(schema)
	if trimmed == "" {
		trimmed = "public"
	}
	return &Default{schema: trimmed}
}

// Authenticate reports the configured schema without inspecting the request.
func (d *Default) Authenticate(context.Context, Request) (Context, error) {
	return Context{Schema: d.schema}, nil
}

// ListTenants returns the one configured schema.
func (d *Default) ListTenants(context.Context) ([]Tenant, error) {
	return []Tenant{{Schema: d.schema}}, nil
}

// Schema reports the schema this extension resolves to. It is exposed so a
// caller that needs the name directly — building a report, say — does not have
// to go through Authenticate.
func (d *Default) Schema() string { return d.schema }
