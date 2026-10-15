package monitoring

import (
	"net/http"
	"net/http/pprof"
)

// BindProfileRoutes registers the pprof profiling endpoints under /debug/pprof.
// It is called only when profiling is enabled, so a default deployment exposes
// no profiling surface.
func BindProfileRoutes(handle func(method, pattern string, handler http.HandlerFunc)) {
	handle(http.MethodGet, "/debug/pprof/", pprof.Index)
	handle(http.MethodGet, "/debug/pprof/cmdline", pprof.Cmdline)
	handle(http.MethodGet, "/debug/pprof/profile", pprof.Profile)
	handle(http.MethodGet, "/debug/pprof/symbol", pprof.Symbol)
	handle(http.MethodPost, "/debug/pprof/symbol", pprof.Symbol)
	handle(http.MethodGet, "/debug/pprof/trace", pprof.Trace)
	for _, name := range []string{"goroutine", "heap", "allocs", "block", "mutex", "threadcreate"} {
		handle(http.MethodGet, "/debug/pprof/"+name, pprof.Handler(name).ServeHTTP)
	}
}
