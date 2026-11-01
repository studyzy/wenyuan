package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// extensionLifecycleProbe records the lifecycle calls one composed application
// made, so a test can assert the order Serve establishes them in.
type extensionLifecycleProbe struct {
	events *[]string
}

func (p *extensionLifecycleProbe) Mount(r httpx.RouteRegistrar) {
	r.Handle(http.MethodGet, "/probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}

func (p *extensionLifecycleProbe) Start() error {
	*p.events = append(*p.events, "start")
	return nil
}

func (p *extensionLifecycleProbe) Stop() { *p.events = append(*p.events, "stop") }

// rootProbeExtension answers a path the core surface leaves free, from the
// application root.
type rootProbeExtension struct{}

func (rootProbeExtension) Mount(httpx.RouteRegistrar) {}

func (rootProbeExtension) MountRoot(r httpx.RouteRegistrar) {
	r.Handle(http.MethodGet, "/extension-root-probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
}

// t-extension-root-router: a root-routing extension's route is served by the
// composed application, through the same router the core surface uses.
func TestComposedApplicationServesExtensionRootRoutes(t *testing.T) {
	host := httpx.NewExtensionHost()
	host.Register(rootProbeExtension{})
	application, err := New(context.Background(), testConfig(), Dependencies{
		Store:      storage.NewMemoryStore("public"),
		Extensions: host,
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	application.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/extension-root-probe", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("root extension route status = %d, want 418", rec.Code)
	}
}

// t-extension-selector: a selector naming a class this build cannot load
// refuses startup instead of composing an application that mounts nothing.
func TestUnloadableExtensionSelectorRefusesStartup(t *testing.T) {
	cfg := testConfig()
	cfg.TenantExtension = "acme.extensions:CustomRouter"
	_, err := New(context.Background(), cfg, Dependencies{Store: storage.NewMemoryStore("public")})
	if err == nil {
		t.Fatal("an extension class this build cannot load must refuse startup")
	}
	if !strings.Contains(err.Error(), config.EnvTenantExtension) {
		t.Fatalf("error must name the key the operator set, got %v", err)
	}
}

// A missing selector composes normally: no extension, not an error.
func TestMissingExtensionSelectorComposes(t *testing.T) {
	application, err := New(context.Background(), testConfig(), Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatalf("a deployment with no extension selector must compose: %v", err)
	}
	if application.Extensions.SelectedClass() != "" {
		t.Fatalf("SelectedClass() = %q, want empty", application.Extensions.SelectedClass())
	}
}

// t-extension-lifecycle: the application's startup hook runs before it is
// served and the shutdown hook on the graceful path, in that order.
func TestAppRunsExtensionLifecycle(t *testing.T) {
	var events []string
	application, err := New(context.Background(), testConfig(), Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	application.Extensions.Register(&extensionLifecycleProbe{events: &events})
	stop, err := application.startExtensions()
	if err != nil {
		t.Fatalf("startExtensions() error = %v", err)
	}
	if len(events) != 1 || events[0] != "start" {
		t.Fatalf("events before shutdown = %v, want [start]", events)
	}
	stop()
	if len(events) != 2 || events[1] != "stop" {
		t.Fatalf("events after shutdown = %v, want [start stop]", events)
	}
	// The graceful path runs the stop once, even if it is reached twice.
	stop()
	if len(events) != 2 {
		t.Fatalf("a second stop must be a no-op, got %v", events)
	}
}

// Serve returns that stop because the listener it was given failed to bind, so
// a shutdown hook is never wired to a process that never served. The happy path
// is covered by the entrypoint's own tests; what matters here is that no hook
// runs when composition refused.
func TestServeDoesNotStartExtensionsWhenBindingFails(t *testing.T) {
	var events []string
	host := httpx.NewExtensionHost()
	host.Register(&extensionLifecycleProbe{events: &events})
	err := Serve(context.Background(), testConfig(), Dependencies{Store: storage.NewMemoryStore("public"), Extensions: host},
		ServeOptions{Listen: func(string, string) (net.Listener, error) {
			return nil, errors.New("address in use")
		}})
	if err == nil {
		t.Fatal("Serve() must report a bind failure")
	}
	if len(events) != 0 {
		t.Fatalf("a refused Serve must not run lifecycle hooks, got %v", events)
	}
}

// t-extension-lifecycle-through-serve: Serve itself runs the startup hook
// before it answers and the shutdown hook when its context is cancelled. The
// wiring is what the claim is about, so this goes through Serve rather than
// calling the hook runner directly.
func TestServeRunsExtensionLifecycle(t *testing.T) {
	var mu sync.Mutex
	var events []string
	record := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}
	host := httpx.NewExtensionHost()
	host.Register(&serveLifecycleProbe{record: record})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	listeners := make(chan net.Listener, 1)
	go func() {
		served <- Serve(ctx, testConfig(), Dependencies{Store: storage.NewMemoryStore("public"), Extensions: host},
			ServeOptions{Listen: captureListener(listeners, net.Listen)})
	}()

	var listener net.Listener
	select {
	case listener = <-listeners:
	case err := <-served:
		t.Fatalf("Serve() returned before it bound a listener: %v", err)
	}
	// Binding is the last thing Serve does before its hooks, so waiting for the
	// first response is what makes the startup hook observable here.
	base := "http://" + listener.Addr().String()
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get(base + "/version")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Serve() never answered /version: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	before := append([]string(nil), events...)
	mu.Unlock()
	if len(before) != 1 || before[0] != "start" {
		t.Fatalf("events once Serve answered = %v, want [start]", before)
	}

	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve() after cancellation = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve() did not return after its context was cancelled")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 || events[1] != "stop" {
		t.Fatalf("events after shutdown = %v, want [start stop]", events)
	}
}

// serveLifecycleProbe records the hooks Serve runs without mounting anything,
// so the probe cannot change which routes the process answers.
type serveLifecycleProbe struct {
	record func(string)
}

func (p *serveLifecycleProbe) Mount(httpx.RouteRegistrar) {}

func (p *serveLifecycleProbe) Start() error { p.record("start"); return nil }

func (p *serveLifecycleProbe) Stop() { p.record("stop") }

// captureListener wraps a listener factory so the test learns the address Serve
// bound. Handing back the same listener keeps Serve's single bind intact.
func captureListener(out chan<- net.Listener, listen func(string, string) (net.Listener, error)) func(string, string) (net.Listener, error) {
	return func(network, addr string) (net.Listener, error) {
		l, err := listen(network, addr)
		if err != nil {
			return nil, err
		}
		out <- l
		return l, nil
	}
}
