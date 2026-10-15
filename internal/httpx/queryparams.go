package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
)

type ignoredContextKey struct{}

// ignoredCollector accumulates unknown parameter and body-field names for one
// request in the order they are encountered and without duplicates. It is
// installed on the request context before the handler runs, so a handler that
// records a body field mutates the same collector the middleware reads after
// the handler returns.
type ignoredCollector struct {
	order []string
	seen  map[string]struct{}

	// declared is the set of query parameters the matched route accepted. A
	// name read by the handler that is absent here is a declaration drift.
	declared map[string]struct{}
	// reads records, in order, every query parameter the handler read through
	// Query or QueryValues, and undeclared the subset it read without
	// declaring. They exist so the route declaration test can tell a handler
	// that reads a parameter it never declared from one that does not.
	reads      []string
	readSeen   map[string]struct{}
	undeclared []string
}

func newIgnoredCollector() *ignoredCollector {
	return &ignoredCollector{seen: make(map[string]struct{}), readSeen: make(map[string]struct{})}
}

func (c *ignoredCollector) add(name string) {
	if c == nil || name == "" {
		return
	}
	if _, ok := c.seen[name]; ok {
		return
	}
	c.seen[name] = struct{}{}
	c.order = append(c.order, name)
}

// declare records the query parameter names the matched route accepts.
func (c *ignoredCollector) declare(names []string) {
	if c == nil {
		return
	}
	if c.declared == nil {
		c.declared = make(map[string]struct{}, len(names))
	}
	for _, name := range names {
		c.declared[name] = struct{}{}
	}
}

// read records a query parameter the handler read and flags it when the route
// never declared it.
func (c *ignoredCollector) read(name string) {
	if c == nil || name == "" {
		return
	}
	if _, ok := c.readSeen[name]; ok {
		return
	}
	c.readSeen[name] = struct{}{}
	c.reads = append(c.reads, name)
	if _, ok := c.declared[name]; !ok {
		c.undeclared = append(c.undeclared, name)
	}
}

func withIgnoredCollector(r *http.Request, c *ignoredCollector) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ignoredContextKey{}, c))
}

func collectorFrom(ctx context.Context) *ignoredCollector {
	c, _ := ctx.Value(ignoredContextKey{}).(*ignoredCollector)
	return c
}

// QueryParams declares the query-parameter names a route accepts and returns a
// handler that honours them. Registering a route through QueryParams is what
// makes the ignored-parameter report route-specific: a name accepted here is
// still reported on a route that does not declare it, and a handler that reads
// a name it did not declare is recorded as a drift the declaration test fails
// on. The parameter names are the route's own set, not a shared allowlist, so
// two routes cannot silently inherit each other's parameters.
func QueryParams(handler http.HandlerFunc, names ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r = ensureIgnoredCollector(r)
		DeclareQuery(r, names...)
		handler(w, r)
	}
}

// DeclareQuery records the query parameters the current route accepts. It is
// the inline counterpart of QueryParams, for a handler that dispatches several
// concrete routes under one registered pattern and must declare each sub-route
// its own set rather than a union of them.
func DeclareQuery(r *http.Request, names ...string) {
	if c := collectorFrom(r.Context()); c != nil {
		c.declare(names)
	}
}

// ensureIgnoredCollector returns a request that carries an ignored-parameter
// collector, installing one when the reporting middleware did not. The handler
// holds the returned request, so a declaration or a read it makes is recorded.
func ensureIgnoredCollector(r *http.Request) *http.Request {
	if collectorFrom(r.Context()) != nil {
		return r
	}
	return withIgnoredCollector(r, newIgnoredCollector())
}

// Query reads a query parameter and records it as accepted for the route. A
// handler reads parameters through Query or QueryValues rather than the raw URL
// so the declaration and the read cannot drift: a name read without being
// declared is reported by QueryAudit and fails the declaration test.
func Query(r *http.Request, name string) string {
	recordQueryRead(r, name)
	return r.URL.Query().Get(name)
}

// QueryValues reads a repeatable query parameter and records it as accepted,
// the multi-value counterpart of Query.
func QueryValues(r *http.Request, name string) []string {
	recordQueryRead(r, name)
	return r.URL.Query()[name]
}

// QueryPresent reads a repeatable query parameter and reports whether it was
// present at all, so a caller can tell an absent parameter from an empty one.
func QueryPresent(r *http.Request, name string) (values []string, present bool) {
	recordQueryRead(r, name)
	values, present = r.URL.Query()[name]
	return values, present
}

func recordQueryRead(r *http.Request, name string) {
	if c := collectorFrom(r.Context()); c != nil {
		c.read(name)
	}
}

// IgnoredParamTrace installs a fresh audit collector on the request so a caller
// that drives a handler directly can inspect what it declared and read
// afterwards. The reporting middleware reuses an installed collector instead of
// creating its own, so a traced request is reported exactly like an untraced
// one. It exists for the route declaration test.
func IgnoredParamTrace(r *http.Request) *http.Request {
	return withIgnoredCollector(r, newIgnoredCollector())
}

// QueryAudit returns the query parameter names the handler read during the
// request and the subset it read without declaring. A route's handler and its
// declared set have drifted when the second list is non-empty.
func QueryAudit(r *http.Request) (read, undeclared []string) {
	c := collectorFrom(r.Context())
	if c == nil {
		return nil, nil
	}
	return c.reads, c.undeclared
}

// queryParamNamesInOrder returns the distinct query parameter names present on
// the request, in the order they appear in the query string. It matches how the
// reference reports names, which is encounter order rather than map iteration.
func queryParamNamesInOrder(r *http.Request) []string {
	names := make([]string, 0)
	seen := make(map[string]struct{})
	for _, pair := range strings.Split(r.URL.RawQuery, "&") {
		if pair == "" {
			continue
		}
		name := pair
		if i := strings.IndexByte(pair, '='); i >= 0 {
			name = pair[:i]
		}
		if decoded, err := url.QueryUnescape(name); err == nil {
			name = decoded
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

// pathParamNames returns the path-parameter names of a route pattern, so a
// query parameter that merely repeats a path-parameter name is never reported.
// The reference skips a name present in the endpoint's path parameters for the
// same reason.
func pathParamNames(pattern string) map[string]struct{} {
	names := map[string]struct{}{}
	for _, segment := range strings.Split(pattern, "/") {
		if len(segment) < 2 || segment[0] != '{' || segment[len(segment)-1] != '}' {
			continue
		}
		name := segment[1 : len(segment)-1]
		name = strings.TrimSuffix(name, "...")
		if name != "" {
			names[name] = struct{}{}
		}
	}
	return names
}

// ignoredQuery returns the query parameters present on the request that the
// route neither declared nor names in its path, in encounter order.
func (c *ignoredCollector) ignoredQuery(r *http.Request) []string {
	path := pathParamNames(r.Pattern)
	var out []string
	for _, name := range queryParamNamesInOrder(r) {
		if _, ok := path[name]; ok {
			continue
		}
		if _, ok := c.declared[name]; ok {
			continue
		}
		out = append(out, name)
	}
	return out
}

// ignoredFor returns the full ignored-parameter list for the response: the
// unknown query parameters first, then the unknown body fields a handler
// recorded, without duplication. That ordering matches the reference, which
// collects the query names before the body keys.
func (c *ignoredCollector) ignoredFor(r *http.Request) []string {
	out := c.ignoredQuery(r)
	if c == nil {
		return out
	}
	seen := make(map[string]struct{}, len(out))
	for _, name := range out {
		seen[name] = struct{}{}
	}
	for _, name := range c.order {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// RecordIgnoredParams annotates the request with unknown parameter names so the
// reporting middleware can emit the header when the response is written. When
// the middleware installed a collector the names are added to it; otherwise the
// names are carried on the returned request's context.
func RecordIgnoredParams(r *http.Request, names []string) *http.Request {
	if c := collectorFrom(r.Context()); c != nil {
		for _, name := range names {
			c.add(name)
		}
		return r
	}
	if len(names) == 0 {
		return r
	}
	c := newIgnoredCollector()
	for _, name := range names {
		c.add(name)
	}
	return withIgnoredCollector(r, c)
}

// UnknownBodyFields returns the top-level object keys of a JSON body that are
// not in the allowed set, in the order they appear in the body. A body that is
// not a JSON object yields nothing, because the route's own decoder reports
// that failure. This is the body counterpart of the ignored query parameters:
// an unknown body field is never rejected, only reported.
func UnknownBodyFields(body []byte, allowed map[string]struct{}) []string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil
	}
	unknown := make([]string, 0)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return unknown
		}
		key, ok := keyTok.(string)
		if !ok {
			return unknown
		}
		if _, allowedKey := allowed[key]; !allowedKey {
			unknown = append(unknown, key)
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return unknown
		}
	}
	return unknown
}

// RecordIgnoredBodyFields annotates the request with unknown body field names,
// merging them with any query parameters already recorded so the header reports
// both. The body is read into memory and restored on the returned request so
// the handler can still decode it.
func RecordIgnoredBodyFields(r *http.Request, allowed map[string]struct{}) ([]byte, *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, r
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	names := UnknownBodyFields(body, allowed)
	if len(names) == 0 {
		return body, r
	}
	return body, RecordIgnoredParams(r, names)
}

// AllowedBodyFields returns the JSON field names a request model accepts, read
// from its tagged fields. Deriving the set from the model keeps the reported
// unknown fields in step with the model instead of a hand-maintained list that
// drifts.
func AllowedBodyFields(sample any) map[string]struct{} {
	allowed := map[string]struct{}{}
	t := reflect.TypeOf(sample)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return allowed
	}
	for i := range t.NumField() {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue
		}
		tag := field.Tag.Get("json")
		if tag == "" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" {
			continue
		}
		allowed[name] = struct{}{}
	}
	return allowed
}

// RecordUnknownBodyFields reports the top-level body fields a request carried
// that the given model does not accept. It mutates the request's ignored
// collector, so the reporting middleware emits the header.
func RecordUnknownBodyFields(r *http.Request, body []byte, sample any) {
	names := UnknownBodyFields(body, AllowedBodyFields(sample))
	if len(names) == 0 {
		return
	}
	RecordIgnoredParams(r, names)
}

func formatIgnoredParams(names []string) string {
	return strings.Join(names, ", ")
}
