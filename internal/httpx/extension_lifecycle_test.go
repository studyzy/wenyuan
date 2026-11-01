package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// lifecycleExtension records what the host asked of it, in order.
type lifecycleExtension struct {
	events   *[]string
	startErr error
	class    string
	mounted  bool
}

func (e *lifecycleExtension) Mount(r RouteRegistrar) {
	e.mounted = true
	r.Handle(http.MethodGet, "/life", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}

func (e *lifecycleExtension) Start() error {
	*e.events = append(*e.events, "start")
	return e.startErr
}

func (e *lifecycleExtension) Stop() {
	*e.events = append(*e.events, "stop")
}

func (e *lifecycleExtension) SelectedAs(class string) { e.class = class }

// rootExtension registers one route at the application root.
type rootExtension struct {
	extensionFunc
	pattern string
}

func (e rootExtension) MountRoot(r RouteRegistrar) {
	r.Handle(http.MethodGet, e.pattern, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
}

// t-extension-lifecycle: the shutdown hooks run in reverse registration order,
// and exactly once even if the graceful path runs twice.
func TestStartExtensionsRunsLifecycleInRegistrationOrder(t *testing.T) {
	var events []string
	first := &lifecycleExtension{events: &events}
	second := &lifecycleExtension{events: &events}
	host := NewExtensionHost()
	host.Register(first)
	host.Register(second)

	stop, err := StartExtensions(host)
	if err != nil {
		t.Fatalf("StartExtensions() error = %v", err)
	}
	stop()
	stop()

	want := []string{"start", "start", "stop", "stop"}
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events = %v, want %v", events, want)
		}
	}
}

// t-extension-lifecycle: a failing startup hook refuses startup and stops only
// what already started, so a refused composition leaves nothing initialized.
func TestStartExtensionsFailsStartupAndUnwinds(t *testing.T) {
	var events []string
	first := &lifecycleExtension{events: &events}
	failing := &lifecycleExtension{events: &events, startErr: errors.New("model unavailable")}
	third := &lifecycleExtension{events: &events}
	host := NewExtensionHost()
	host.Register(first)
	host.Register(failing)
	host.Register(third)

	stop, err := StartExtensions(host)
	if err == nil {
		t.Fatal("StartExtensions() must report the failing hook")
	}
	stop()
	want := []string{"start", "start", "stop"}
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v (the unwound first extension, and nothing else)", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events = %v, want %v", events, want)
		}
	}
}

// An extension without a lifecycle is started and stopped by neither, which is
// the unextended behaviour.
func TestStartExtensionsIgnoresExtensionsWithoutLifecycle(t *testing.T) {
	host := NewExtensionHost()
	mounted := false
	host.Register(extensionFunc(func(RouteRegistrar) { mounted = true }))
	stop, err := StartExtensions(host)
	if err != nil {
		t.Fatalf("StartExtensions() error = %v", err)
	}
	stop()
	if mounted {
		t.Fatal("StartExtensions must not mount; mounting is MountExtensions' job")
	}
}

// t-extension-selector: the selector reaches an extension registered before or
// after it is recorded, so registration order does not decide the outcome.
func TestSelectClassReachesExtensionsRegisteredBeforeOrAfter(t *testing.T) {
	var events []string
	early := &lifecycleExtension{events: &events}
	host := NewExtensionHost()
	host.Register(early)
	host.SelectClass("builtin.tenant:ApiKeyTenantExtension")
	if early.class != "builtin.tenant:ApiKeyTenantExtension" {
		t.Fatalf("early extension selector = %q", early.class)
	}
	late := &lifecycleExtension{events: &events}
	host.Register(late)
	if late.class != "builtin.tenant:ApiKeyTenantExtension" {
		t.Fatalf("late extension must be told the selector at registration, got %q", late.class)
	}
	if host.SelectedClass() != "builtin.tenant:ApiKeyTenantExtension" {
		t.Fatalf("SelectedClass() = %q", host.SelectedClass())
	}
}

// t-extension-settings: extension-only settings live on the host, are copied on
// the way in and out, and a host given none reports nil.
func TestExtensionSettingsAreCopiedInAndOut(t *testing.T) {
	host := NewExtensionHost()
	if host.Settings() != nil {
		t.Fatalf("a host with no settings must report nil, got %v", host.Settings())
	}
	given := map[string]string{"region": "eu"}
	host.SetSettings(given)
	given["region"] = "mutated"
	if host.Settings()["region"] != "eu" {
		t.Fatalf("the host must keep its own copy, got %v", host.Settings())
	}
	read := host.Settings()
	read["region"] = "mutated"
	if host.Settings()["region"] != "eu" {
		t.Fatalf("a reader must not be able to mutate the host's copy, got %v", host.Settings())
	}
}

// t-extension-root-router: a root-routing extension is offered to the
// composition root, and one without the capability is not.
func TestRootMountsReturnsOnlyRootRouters(t *testing.T) {
	host := NewExtensionHost()
	host.Register(extensionFunc(func(RouteRegistrar) {}))
	host.Register(rootExtension{pattern: "/ext-root-probe"})
	mounts := host.RootMounts()
	if len(mounts) != 1 {
		t.Fatalf("RootMounts() = %d, want 1 (the extension implementing ExtensionRootRouter)", len(mounts))
	}

	mux := http.NewServeMux()
	mounts[0].Router.MountRoot(rootRegistrarFunc(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ext-root-probe", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("root extension route status = %d, want 418", rec.Code)
	}
}

// t-empty-extension-answers-json-404: an unmatched path under the extension
// namespace answers the shared JSON envelope, not the mux's plain text.
func TestEmptyExtensionNamespaceAnswersJSONNotFound(t *testing.T) {
	router := NewRouter(RouterOptions{})
	rec := httptest.NewRecorder()
	router.Service().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, NamespaceExt+"/nothing-here", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, rec.Body.String())
	}
	if body["detail"] != "Not Found" {
		t.Fatalf("body = %v, want {\"detail\":\"Not Found\"}", body)
	}
}

// rootRegistrarFunc adapts a function to the route registrar.
type rootRegistrarFunc func(method, pattern string, handler http.HandlerFunc)

func (f rootRegistrarFunc) Handle(method, pattern string, handler http.HandlerFunc) {
	f(method, pattern, handler)
}
