package registry

import (
	"context"
	"errors"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	"net/http"
	"regexp"
	"strings"
	"unicode"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/extras"
)

// Prefix is the one place an extension's routes may live: `/x/{cluster}/`,
// then the key, then the route's own pattern. Why it is a top-level segment and
// not under `/c/{cluster}/` is told where the console mounts it
// (internal/web/mountextensions.go); it is here so that Register and the router
// build one pattern from one recipe.
const Prefix = "/x/{cluster}/"

// Pattern builds the ServeMux registration of one route of the extension key,
// or says why it cannot.
//
// The key is held to extras.KeyPattern, which keeps it one literal path
// segment. The pattern an extension gives is relative and must begin with a
// slash, so an extension cannot climb out of its own prefix, and carries no
// whitespace, which ServeMux would read as the end of a method or of the path.
//
// **The raw pattern, not a trimmed one.** Trimming first accepts " /panel",
// which is a pattern ServeMux reads as a method of "" followed by a path —
// close enough to work and not the thing the extension asked for.
//
// That a pattern is well formed is checked here; that ServeMux will take it —
// a repeated wildcard name, a malformed `{…}`, a clash with another route — is
// only known by asking ServeMux, which is what Handle does.
func Pattern(key string, route extension.Route) (string, error) {
	if !extras.KeyPattern.MatchString(key) {
		return "", fmt.Errorf("the key %q is not lower case, digits and inner hyphens (2 to 40 characters), and it is a segment of every URL the extension answers on", key)
	}
	pattern := route.Pattern
	if !strings.HasPrefix(pattern, "/") {
		return "", fmt.Errorf("the route %q does not begin with a slash, and a route is relative to the extension's own prefix", pattern)
	}
	if strings.IndexFunc(pattern, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("the route %q holds whitespace, which the router would read as the end of the path", pattern)
	}
	full := Prefix + key + pattern
	method := strings.TrimSpace(route.Method)
	if method == "" {
		// No method, deliberately supported: a WebSocket upgrade is not a verb,
		// and the console's own terminal routes are registered the same way.
		return full, nil
	}
	// **The method is one upper-case word.** It is written in front of the
	// path, so a method holding a space and a path ("GET /kt-pwa/") would mount
	// the route under that path instead of the prefix, and ServeMux matches a
	// method case-sensitively, so "get" would mount a route that never answers.
	if !methodPattern.MatchString(method) {
		return "", fmt.Errorf("the method %q is not one upper-case word such as GET or POST", route.Method)
	}
	return method + " " + full, nil
}

// methodPattern is what Pattern accepts as a route's method.
var methodPattern = regexp.MustCompile(`^[A-Z]+$`)

// Handle registers handler on mux under pattern, and returns as an error what
// ServeMux would otherwise panic with: an invalid pattern (a repeated `{cluster}`
// is the likeliest, since the prefix already holds one) or one that conflicts
// with a pattern already there.
//
// ServeMux checks a pattern in full before it records it, so a refused one
// leaves the mux as it was.
func Handle(mux *http.ServeMux, pattern string, handler http.Handler) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("the router refuses %q: %v", pattern, r)
		}
	}()
	mux.Handle(pattern, handler)
	return nil
}

// checkRoutes mounts every route of e on a mux of its own, so that a route the
// console's router would refuse is refused here, at Register, as an error.
//
// The routes are asked of an unmountedHost: Register runs before the console
// that will serve them exists. extension.Extension.Routes documents that it may
// be called more than once and must not use the Host while listing. A panic
// out of Routes is a refusal, with the panic's own value in it.
//
// It runs without the registry's lock held, so a Routes that reads the registry
// (app.Extensions, app.Keys) does not deadlock the start-up.
//
// Extensions cannot clash with each other: each lives under its own literal key,
// which extras.KeyPattern keeps literal, and the keys are unique.
func checkRoutes(key string, e extension.Extension) (err error) {
	var routes []extension.Route
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("extension %q panicked while listing its routes: %v", key, r)
			}
		}()
		routes = e.Routes(unmountedHost{})
	}()
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	placeholder := http.NotFoundHandler()
	for _, route := range routes {
		pattern, err := Pattern(key, route)
		if err != nil {
			return fmt.Errorf("extension %q: %w", key, err)
		}
		if err := Handle(mux, pattern, placeholder); err != nil {
			return fmt.Errorf("extension %q asks for a route the console cannot mount (%q %q): %w",
				key, route.Method, route.Pattern, err)
		}
	}
	return nil
}

// errUnmounted is what every method of unmountedHost answers.
var errUnmounted = errors.New("the console is not built yet: an extension's Host answers only once its routes are mounted")

// unmountedHost is the Host Routes is asked with at Register. Every method
// that returns an error answers errUnmounted, and ConfirmToken, which has no
// error to return, answers an empty token; none panics, so a handler built
// with it, or a goroutine an extension started with it, fails a request and
// never the process. The real Host is handed over when the console mounts the
// routes.
//
// It spells every method out, so a method added to extension.Host fails this
// file at compile time rather than becoming a nil call.
type unmountedHost struct{}

var _ extension.Host = unmountedHost{}

func (unmountedHost) Client(*http.Request, string) (extension.Clients, error) {
	return extension.Clients{}, errUnmounted
}

func (unmountedHost) Definitions(*http.Request, string) (extension.CRDRegistry, error) {
	return nil, errUnmounted
}

func (unmountedHost) List(*http.Request, string, extension.ListQuery) (extension.Page, error) {
	return extension.Page{}, errUnmounted
}

func (unmountedHost) Reveal(*http.Request, string, string, string, string) (string, error) {
	return "", errUnmounted
}

func (unmountedHost) ConfiguredSecret(*http.Request, string, string, string, string) (string, error) {
	return "", errUnmounted
}

func (unmountedHost) ConfiguredService(*http.Request, string, string, string) (corev1.Service, error) {
	return corev1.Service{}, errUnmounted
}

func (unmountedHost) Exec(*http.Request, string, extension.ExecQuery) (extension.ExecResult, error) {
	return extension.ExecResult{}, errUnmounted
}

func (unmountedHost) Usage(*http.Request, string, []string) (extension.Usage, error) {
	return extension.Usage{}, errUnmounted
}

func (unmountedHost) ContainerUsage(*http.Request, string, string) (extension.LiveUsage, error) {
	return extension.LiveUsage{}, errUnmounted
}

func (unmountedHost) UsageWindow(*http.Request, string, string) (extension.Window, error) {
	return extension.Window{}, errUnmounted
}

func (unmountedHost) DNSWindow(*http.Request, string, extension.DNSSpan) (extension.DNS, error) {
	return extension.DNS{}, errUnmounted
}

func (unmountedHost) VolumeFill(*http.Request, string, []extension.ClaimRef) (extension.VolumeFill, error) {
	return extension.VolumeFill{}, errUnmounted
}

func (unmountedHost) Forward(context.Context, string, string, string, int) (extension.Tunnel, error) {
	return extension.Tunnel{}, errUnmounted
}

func (unmountedHost) ForwardAsConsole(*http.Request, string, string, string, int) (extension.Tunnel, error) {
	return extension.Tunnel{}, errUnmounted
}

func (unmountedHost) ConfirmToken(*http.Request) string { return "" }

func (unmountedHost) RefreshApplication(*http.Request, string, string, string) error {
	return errUnmounted
}

func (unmountedHost) Setting(*http.Request, string, string) string { return "" }

func (unmountedHost) MayReadLogs(*http.Request, string, string) error { return errUnmounted }
