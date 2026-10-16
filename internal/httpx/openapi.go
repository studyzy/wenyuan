package httpx

import (
	_ "embed"
	"net/http"
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
// the configured base path. The alternative, templating the pages, would keep a
// second copy of the reference's markup in the source.
func docsPage(page, basePath string) string {
	if basePath == "" {
		return page
	}
	page = strings.ReplaceAll(page, `"/openapi.json"`, `"`+basePath+`/openapi.json"`)
	page = strings.ReplaceAll(page, `'/openapi.json'`, `'`+basePath+`/openapi.json'`)
	page = strings.ReplaceAll(page, `'/docs/oauth2-redirect'`, `'`+basePath+`/docs/oauth2-redirect'`)
	return page
}

// mountDescriptorNamespace registers the descriptor surface. The reference
// serves it unconditionally: it is how a client discovers the contract.
func (r *Router) mountDescriptorNamespace() {
	basePath := r.options.BasePath
	writeBody := func(w http.ResponseWriter, contentType, body string) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}
	r.Handle(http.MethodGet, PathOpenAPI, func(w http.ResponseWriter, _ *http.Request) {
		writeBody(w, contentTypeJSON, OpenAPIDocument(basePath))
	})
	r.Handle(http.MethodGet, PathDocs, func(w http.ResponseWriter, _ *http.Request) {
		writeBody(w, contentTypeHTML, docsPage(swaggerPage, basePath))
	})
	r.Handle(http.MethodGet, PathDocsOAuth, func(w http.ResponseWriter, _ *http.Request) {
		writeBody(w, contentTypeHTML, oauth2RedirectPage)
	})
	r.Handle(http.MethodGet, PathRedoc, func(w http.ResponseWriter, _ *http.Request) {
		writeBody(w, contentTypeHTML, docsPage(redocPage, basePath))
	})
	// The reference's router answers the trailing-slash spelling of a page with
	// a redirect to the page itself, because the page is not a subtree.
	for _, page := range []string{PathDocs, PathRedoc} {
		target := basePath + page
		r.Handle(http.MethodGet, page+"/", func(w http.ResponseWriter, req *http.Request) {
			http.Redirect(w, req, target, http.StatusTemporaryRedirect)
		})
	}
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
