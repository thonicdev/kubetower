package registry_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/extras/argocd"
	"github.com/thonicdev/kubetower/extras/kubecron"
	"github.com/thonicdev/kubetower/extras/redis"
	"github.com/thonicdev/kubetower/internal/extras"
	"github.com/thonicdev/kubetower/internal/registry"
)

// page is an extension whose key, old keys and routes a test chooses.
type page struct {
	key    string
	legacy []string
	routes []extension.Route
	// listing runs inside Routes, before it lists, with the Host it was given.
	listing func(extension.Host)
}

func (p page) Key() string      { return p.key }
func (p page) Label() string    { return "Page " + p.key }
func (p page) Summary() string  { return "A page for the registry's tests." }
func (p page) Needs() string    { return "nothing" }
func (p page) Legacy() []string { return p.legacy }

func (p page) Routes(h extension.Host) []extension.Route {
	if p.listing != nil {
		p.listing(h)
	}
	if p.routes != nil {
		return p.routes
	}
	return []extension.Route{
		{Method: http.MethodGet, Pattern: "/{$}", Handler: http.NotFoundHandler()},
		{Method: http.MethodGet, Pattern: "/items/{namespace}/{name}", Handler: http.NotFoundHandler()},
		// No method: the shape a WebSocket upgrade needs.
		{Pattern: "/socket", Handler: http.NotFoundHandler()},
	}
}

func get(pattern string) extension.Route {
	return extension.Route{Method: http.MethodGet, Pattern: pattern, Handler: http.NotFoundHandler()}
}

// register calls Register and turns a panic into a test failure, so that every
// refusal below is also a check that nothing panicked.
func register(t *testing.T, exts ...extension.Extension) (err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Register panicked: %v", r)
		}
	}()
	return registry.Register(exts...)
}

// refused asserts that Register refuses exts with an error naming want, and
// that the refused call left the registry as it was.
func refused(t *testing.T, want string, exts ...extension.Extension) {
	t.Helper()
	before := registry.Keys()
	err := register(t, exts...)
	if err == nil {
		t.Fatalf("Register accepted it; registered now: %v", registry.Keys())
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal does not say %q: %v", want, err)
	}
	if after := registry.Keys(); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Errorf("a refused call registered something: %v, then %v", before, after)
	}
}

func fresh(t *testing.T) {
	t.Helper()
	registry.Reset()
	t.Cleanup(registry.Reset)
}

func TestAValidExtensionRegisters(t *testing.T) {
	fresh(t)
	if err := register(t, page{key: "alfa"}, page{key: "bravo-2", legacy: []string{"old-bravo"}}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(registry.Keys(), ","); got != "alfa,bravo-2" {
		t.Errorf("registered %q", got)
	}
}

// The second extension of one call is checked against the first as well as
// against the registry, which the first is not in yet.
func TestADuplicateKeyInOneCallIsRefused(t *testing.T) {
	fresh(t)
	refused(t, `two extensions claim the key "alfa"`, page{key: "alfa"}, page{key: "alfa"})
}

func TestADuplicateKeyAcrossCallsIsRefused(t *testing.T) {
	fresh(t)
	if err := register(t, page{key: "alfa"}); err != nil {
		t.Fatal(err)
	}
	refused(t, `two extensions claim the key "alfa"`, page{key: "alfa"})
}

func TestALegacyKeyCollisionIsRefused(t *testing.T) {
	t.Run("an old key naming a live key of the same call", func(t *testing.T) {
		fresh(t)
		refused(t, `lists "alfa" as an old key`, page{key: "alfa"}, page{key: "bravo", legacy: []string{"alfa"}})
	})
	t.Run("a live key naming an old key of the same call", func(t *testing.T) {
		fresh(t)
		refused(t, `which Page bravo lists as an old key`, page{key: "bravo", legacy: []string{"alfa"}}, page{key: "alfa"})
	})
	t.Run("an old key naming a key registered earlier", func(t *testing.T) {
		fresh(t)
		if err := register(t, page{key: "alfa"}); err != nil {
			t.Fatal(err)
		}
		refused(t, `lists "alfa" as an old key`, page{key: "bravo", legacy: []string{"alfa"}})
	})
	t.Run("a key naming an old key registered earlier", func(t *testing.T) {
		fresh(t)
		if err := register(t, page{key: "bravo", legacy: []string{"alfa"}}); err != nil {
			t.Fatal(err)
		}
		refused(t, `which Page bravo lists as an old key`, page{key: "alfa"})
	})
	for _, old := range []string{"Scheduled", "old key", "old/key", "{old}"} {
		t.Run("an old key outside the key's shape: "+old, func(t *testing.T) {
			fresh(t)
			refused(t, "held to the key's own shape", page{key: "alfa", legacy: []string{old}})
		})
	}
	t.Run("an old key two pages of the same call share", func(t *testing.T) {
		fresh(t)
		refused(t, `lists "older" as an old key, and so does Page alfa`,
			page{key: "alfa", legacy: []string{"older"}}, page{key: "bravo", legacy: []string{" older"}})
	})
	t.Run("an old key a page registered earlier lists", func(t *testing.T) {
		fresh(t)
		if err := register(t, page{key: "alfa", legacy: []string{"older"}}); err != nil {
			t.Fatal(err)
		}
		refused(t, `lists "older" as an old key, and so does Page alfa`, page{key: "bravo", legacy: []string{"older"}})
	})
}

// A key goes into the ServeMux pattern verbatim, so one that is not a path
// segment is refused before it gets there.
func TestAKeyThatIsNotAPathSegmentIsRefused(t *testing.T) {
	for _, key := range []string{
		"al fa", " alfa", "alfa ", "al/fa", "{alfa}", "al{fa", "alfa}", "{cluster}",
		"Alfa", "al_fa", "al.fa", "-alfa", "alfa-", "a", strings.Repeat("a", 41),
	} {
		t.Run(key, func(t *testing.T) {
			fresh(t)
			refused(t, "lower case, digits and inner hyphens", page{key: key})
		})
	}
	t.Run("empty", func(t *testing.T) {
		fresh(t)
		refused(t, "has no key", page{key: ""})
	})
	t.Run("blank", func(t *testing.T) {
		fresh(t)
		refused(t, "has no key", page{key: "  "})
	})
}

// A route ServeMux refuses panics inside the router; Mount turns that panic
// into a refusal, an error the caller can print.
func TestARouteTheRouterCannotMountIsRefused(t *testing.T) {
	for _, c := range []struct {
		name   string
		routes []extension.Route
		want   string
	}{
		{"a repeated {cluster}", []extension.Route{get("/{cluster}")}, "cannot mount"},
		{"a repeated wildcard of its own", []extension.Route{get("/{name}/{name}")}, "cannot mount"},
		{"a malformed wildcard", []extension.Route{get("/{name")}, "cannot mount"},
		{"two identical routes", []extension.Route{get("/panel"), get("/panel")}, "cannot mount"},
		{"two routes with overlapping wildcards", []extension.Route{get("/{a}"), get("/{b}")}, "cannot mount"},
		{"two identical method-less routes", []extension.Route{get("/socket"), {Pattern: "/socket"}, {Pattern: "/socket"}}, "cannot mount"},
		{"no leading slash", []extension.Route{get("panel")}, "does not begin with a slash"},
		{"whitespace in the pattern", []extension.Route{get("/pa nel")}, "holds whitespace"},
		{"a method the router cannot read", []extension.Route{{Method: "GE{T", Pattern: "/panel"}}, "not one upper-case word"},
		{"a method that holds a path", []extension.Route{{Method: "GET /kt-pwa/", Pattern: "/p"}}, "not one upper-case word"},
		{"a lower-case method", []extension.Route{{Method: "get", Pattern: "/panel"}}, "not one upper-case word"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			refused(t, c.want, page{key: "alfa", routes: c.routes})
		})
	}
}

// A GET route and a method-less one on one path do not conflict in ServeMux,
// and an extension may well want both (a page and its socket).
func TestAGetAndAMethodLessRouteOnOnePathRegister(t *testing.T) {
	fresh(t)
	if err := register(t, page{key: "alfa", routes: []extension.Route{get("/socket"), {Pattern: "/socket"}}}); err != nil {
		t.Fatal(err)
	}
}

// One refusal anywhere in a call leaves the whole call unregistered, so the
// catalog and the router never hold half of what a `main` handed over.
func TestARefusedCallRegistersNone(t *testing.T) {
	fresh(t)
	refused(t, "cannot mount", page{key: "alfa"}, page{key: "bravo", routes: []extension.Route{get("/x"), get("/x")}})
	if got := extras.Catalogue(); len(got) != 0 {
		t.Errorf("the catalog holds %v after a refused call", got)
	}
}

// A Routes that panics is refused, and the refusal carries the panic's own
// value rather than a guess at its cause.
func TestARoutesThatPanicsIsRefusedWithItsPanic(t *testing.T) {
	fresh(t)
	refused(t, "panicked while listing its routes: the fixture's own panic",
		page{key: "alfa", listing: func(extension.Host) { panic("the fixture's own panic") }})
}

// The Host Routes is first asked with answers every method with an error, so a
// Routes, a handler or a goroutine that uses it fails a request and never
// crashes the process. A Routes that only lists still registers.
func TestTheHostAtRegistrationAnswersWithErrorsNotPanics(t *testing.T) {
	fresh(t)
	var errs []error
	listing := func(h extension.Host) {
		if errs != nil {
			return // the first call is Register's; that is the one under test
		}
		_, err := h.Client(nil, "prod")
		errs = append(errs, err)
		_, err = h.Definitions(nil, "prod")
		errs = append(errs, err)
		_, err = h.List(nil, "prod", extension.ListQuery{})
		errs = append(errs, err)
		_, err = h.Reveal(nil, "prod", "ns", "name", "key")
		errs = append(errs, err)
		_, err = h.Usage(nil, "prod", nil)
		errs = append(errs, err)
		_, err = h.Forward(context.Background(), "prod", "ns", "svc", 80)
		errs = append(errs, err)
		_ = h.ConfirmToken(nil)
	}
	if err := register(t, page{key: "alfa", listing: listing}); err != nil {
		t.Fatal(err)
	}
	if len(errs) != 6 {
		t.Fatalf("Routes saw %d answers, want 6", len(errs))
	}
	for i, err := range errs {
		if err == nil {
			t.Errorf("Host method %d answered no error at registration", i)
		}
	}
}

// Routes runs outside the registry's lock: a Routes that reads the registry
// (app.Extensions and app.Keys are these two) once hung the start-up for good.
func TestARoutesThatReadsTheRegistryDoesNotDeadlock(t *testing.T) {
	fresh(t)
	if err := register(t, page{key: "first"}); err != nil {
		t.Fatal(err)
	}
	reads := page{key: "alfa", listing: func(extension.Host) {
		_ = registry.Extensions()
		_ = registry.Keys()
	}}
	done := make(chan error, 1)
	go func() { done <- registry.Register(reads) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Register did not return: a Routes that reads the registry deadlocked it")
	}
	if got := strings.Join(registry.Keys(), ","); got != "alfa,first" {
		t.Errorf("registered %q", got)
	}
}

// The catalog gets the old keys Register checked — trimmed, each once, with
// neither the empty ones nor the page's own key — and folds them onto the page.
func TestTheCatalogueGetsTheOldKeysTrimmed(t *testing.T) {
	fresh(t)
	if err := register(t, page{key: "bravo", legacy: []string{"  old-bravo ", "", "old-bravo", "bravo", "older"}}); err != nil {
		t.Fatal(err)
	}
	catalogue := extras.Catalogue()
	if len(catalogue) != 1 {
		t.Fatalf("the catalog holds %d entries", len(catalogue))
	}
	if got := strings.Join(catalogue[0].Legacy, ","); got != "old-bravo,older" {
		t.Errorf("the catalog holds the old keys %q, want \"old-bravo,older\"", got)
	}
	if got := extras.Canonical("old-bravo"); got != "bravo" {
		t.Errorf("old-bravo folds onto %q, want bravo", got)
	}
}

// Every extension this repository carries still registers under the stricter
// checks, together and in one call, as cmd/kubetower hands them to app.Run.
// Redis is a package of the module not yet in cmd/kubetower's list, and is
// checked here so that adding it there does not fail at start-up.
func TestEveryInTreeExtraRegisters(t *testing.T) {
	fresh(t)
	if err := register(t, argocd.New(), kubecron.New(), redis.New()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(registry.Keys(), ","); got != "argocd,kubecron,redis" {
		t.Errorf("registered %q", got)
	}
}
