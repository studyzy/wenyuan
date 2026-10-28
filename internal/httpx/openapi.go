package httpx

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// ReferenceAPIVersion is the release of the reference service whose HTTP
// surface this service serves. It is what the version endpoint reports,
// because a client that gates behavior on the version expects the version of
// the API it is speaking to, not the version of this build.
const ReferenceAPIVersion = "1.0.0"

// The descriptor surface the reference serves by virtue of being a FastAPI
// application, none of which is declared with a route decorator:
//
//	GET /openapi.json           the contract document
//	GET /docs                   Swagger UI
//	GET /docs/oauth2-redirect   the OAuth2 redirect page the UI opens
//	GET /redoc                  ReDoc
const (
	PathOpenAPI     = "/openapi.json"
	PathDocs        = "/docs"
	PathDocsOAuth   = "/docs/oauth2-redirect"
	PathRedoc       = "/redoc"
	contentTypeJSON = "application/json"
	contentTypeHTML = "text/html; charset=utf-8"
)

// openAPIDocument is the reference's contract document, captured by
// scripts/reference_openapi.py. The embedded copy is what a client generated
// from /openapi.json generates against.
//
//go:embed openapi.json
var openAPIDocument string

// The documentation pages, captured from the reference's own FastAPI build.
// They load their assets from a CDN and read the contract from /openapi.json,
// exactly as the reference's pages do; only the two URLs they carry differ, and
// those are rewritten for the configured base path.
//
//go:embed docs/swagger.html
var swaggerPage string

//go:embed docs/redoc.html
var redocPage string

//go:embed docs/oauth2-redirect.html
var oauth2RedirectPage string

// documentTitle is the served contract's info.title. The documentation pages
// take their title from it, as the reference's FastAPI does — a page whose
// title named this build rather than the API it documents would contradict the
// document a client generates from.
var documentTitle = func() string {
	var document struct {
		Info struct {
			Title string `json:"title"`
		} `json:"info"`
	}
	if err := json.Unmarshal([]byte(openAPIDocument), &document); err != nil || document.Info.Title == "" {
		return "WenYuan HTTP API"
	}
	return document.Info.Title
}()

// OpenAPIDocument returns the contract document as served at a base path.
//
// The reference adds a `servers` entry per response when its root path is
// configured, and none when it is not, so the same is done here rather than
// baked into the captured document.
func OpenAPIDocument(basePath string) string {
	if basePath == "" {
		return openAPIDocument
	}
	server := `{"servers":[{"url":"` + basePath + `"}],`
	return server + strings.TrimPrefix(openAPIDocument, "{")
}

// docsPage rewrites the two URLs a documentation page carries so they point at
// the configured base path, and titles the page after the served document's
// info.title. The alternative, templating the pages, would keep a second copy
// of the reference's markup in the source.
func docsPage(page, basePath, title string) string {
	page = withTitle(page, title)
	if basePath == "" {
		return page
	}
	page = strings.ReplaceAll(page, `"/openapi.json"`, `"`+basePath+`/openapi.json"`)
	page = strings.ReplaceAll(page, `'/openapi.json'`, `'`+basePath+`/openapi.json'`)
	page = strings.ReplaceAll(page, `'/docs/oauth2-redirect'`, `'`+basePath+`/docs/oauth2-redirect'`)
	return page
}

// withTitle replaces the document's <title> element with the given text. The
// reference renders each page's title from the application title, so the pages
// must carry the same title the served document reports.
func withTitle(page, title string) string {
	const open, close = "<title>", "</title>"
	start := strings.Index(page, open)
	if start < 0 {
		return page
	}
	start += len(open)
	end := strings.Index(page[start:], close)
	if end < 0 {
		return page
	}
	return page[:start] + title + page[start+end:]
}

// mountDescriptorNamespace registers the descriptor surface. The reference
// serves it unconditionally: it is how a client discovers the contract.
func (r *Router) mountDescriptorNamespace() {
	basePath := r.options.BasePath
	writeBody := func(w http.ResponseWriter, contentType, body string) {
		w.Header().Set("Content-Type", contentType)
		// The reference's descriptor routes are ordinary Starlette responses, so
		// a body of any size is framed with its length rather than chunked.
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}
	r.handleDescriptor(PathOpenAPI, suppressIgnored(func(w http.ResponseWriter, _ *http.Request) {
		writeBody(w, contentTypeJSON, OpenAPIDocument(basePath))
	}))
	r.handleDescriptor(PathDocs, suppressIgnored(func(w http.ResponseWriter, _ *http.Request) {
		writeBody(w, contentTypeHTML, docsPage(swaggerPage, basePath, documentTitle+" - Swagger UI"))
	}))
	r.handleDescriptor(PathDocsOAuth, suppressIgnored(func(w http.ResponseWriter, _ *http.Request) {
		writeBody(w, contentTypeHTML, oauth2RedirectPage)
	}))
	r.handleDescriptor(PathRedoc, suppressIgnored(func(w http.ResponseWriter, _ *http.Request) {
		writeBody(w, contentTypeHTML, docsPage(redocPage, basePath, documentTitle+" - ReDoc"))
	}))
	// The trailing-slash spelling of any route is answered by the router's
	// redirect (see methodSemantics), not by a registered route: the reference's
	// router strips the slash and redirects to the route it finds.
}

// suppressIgnored wraps a descriptor handler so it never reports ignored
// parameters, matching the reference's plain descriptor routes.
func suppressIgnored(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		SuppressIgnoredParams(req)
		handler(w, req)
	}
}

// redirect answers a request with the reference's redirect: a 307 to the
// absolute URL built from the request's scheme and host, an empty body, and the
// target's query string preserved.
func redirect(w http.ResponseWriter, req *http.Request, target string) {
	scheme := "http"
	if req.TLS != nil {
		scheme = "https"
	}
	w.Header().Set("Location", scheme+"://"+req.Host+target)
	w.WriteHeader(http.StatusTemporaryRedirect)
}

// BasePathMiddleware serves the whole surface under a configured prefix, in
// addition to the root paths.
//
// This mirrors the reference, whose router strips a configured root path from
// the request before matching and leaves a request that does not carry it
// alone. Both spellings therefore answer, which is what lets a deployment move
// behind a path-prefixing proxy without every client having to change.
func BasePathMiddleware(basePath string) Middleware {
	if basePath == "" {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			if path == basePath || strings.HasPrefix(path, basePath+"/") {
				stripped := strings.TrimPrefix(path, basePath)
				if stripped == "" {
					stripped = "/"
				}
				clone := r.Clone(r.Context())
				clone.URL.Path = stripped
				if r.URL.RawPath != "" {
					clone.URL.RawPath = strings.TrimPrefix(r.URL.RawPath, basePath)
				}
				r = clone
			}
			next.ServeHTTP(w, r)
		})
	}
}
