package launch

import (
	"strings"
	"testing"
)

// A `kubetower://` link opens a read-only page
// and never acts. Every accepted shape and every refused form is here, and the
// test runs on every platform - the macOS glue only calls consolePathFromURL.

func TestALinkOpensOnlyAReadOnlyPage(t *testing.T) {
	for raw, want := range map[string]string{
		"kubetower://c/prod-eks/pods/default/web-1":                            "/c/prod-eks/pods/default/web-1",
		"kubetower:///c/prod-eks/pods/default/web-1":                           "/c/prod-eks/pods/default/web-1",
		"kubetower:c/prod-eks/pods/default/web-1":                              "/c/prod-eks/pods/default/web-1",
		"KubeTower://c/prod-eks/pods/default/web-1":                            "/c/prod-eks/pods/default/web-1",
		"kubetower://c/prod-eks/nodes/ip-10-0-0-1":                             "/c/prod-eks/nodes/ip-10-0-0-1",
		"kubetower://c/prod-eks/deployments.apps":                              "/c/prod-eks/deployments.apps",
		"kubetower://c/prod-eks":                                               "/c/prod-eks",
		"kubetower://c/prod-eks/":                                              "/c/prod-eks",
		"kubetower://c/prod-eks/helm/default/ingress":                          "/c/prod-eks/helm/default/ingress",
		"kubetower://c/kind-dev/clusterroles/system:controller:job-controller": "/c/kind-dev/clusterroles/system:controller:job-controller",
		// Re-encoded by this function, not passed through as the link spelled it.
		"kubetower://c/my%20ctx":       "/c/my%20ctx",
		"kubetower://c/caf%C3%A9/pods": "/c/caf%C3%A9/pods",
		"kubetower://":                 "/",
		"kubetower://health":           "/health",
		"kubetower://settings":         "/settings",
		"kubetower://settings/general": "/settings/general",
	} {
		got, err := consolePathFromURL(raw)
		if want == "" {
			if err == nil {
				t.Errorf("%q was accepted as %q; it must be refused", raw, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("consolePathFromURL(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
}

func TestEveryOtherFormIsRefused(t *testing.T) {
	for _, raw := range []string{
		// Not a page: the API, the front door, the sign-in page, a probe.
		"kubetower://api/c/prod/pods",
		"kubetower:///api/clusters",
		"kubetower://auth/logout",
		"kubetower://auth/launch?code=x",
		"kubetower://login",
		"kubetower://kt-healthz",
		"kubetower://kt-api/terminal",
		// An extension's page is not this core's to vouch for.
		"kubetower://c/prod/x",
		"kubetower://c/prod/x/argocd",
		"kubetower://c/prod/x/argocd/app",
		// Deeper than any object route.
		"kubetower://c/prod/pods/default/web-1/logs",
		"kubetower://health/extra",
		"kubetower://settings/general/extra",
		"kubetower://c",
		// A query or a fragment, which a page could read as an instruction.
		"kubetower://c/prod/pods?delete=1",
		"kubetower://c/prod/pods?",
		"kubetower://c/prod/pods#exec",
		"kubetower://settings/general?reset=all",
		// Traversal and smuggling.
		"kubetower://c/prod/../../api/clusters",
		"kubetower://c/prod/./pods",
		"kubetower://c/prod%2Fother/pods",
		"kubetower://c/prod/pods%2F..%2F..%2Fapi",
		"kubetower://c/prod%5C..%5Capi/pods",
		"kubetower://c/prod/pods/default%3Fx=1/web",
		"kubetower://c/prod/pods/default/web%0A1",
		"kubetower://c/prod/pods/default/web%001",
		"kubetower://c//pods",
		"kubetower://c/prod/pods/%",
		// Other schemes, and the parts a link has no business carrying.
		"javascript:alert(1)",
		"http://127.0.0.1:5823/c/prod/pods",
		"kubetowers://c/prod/pods",
		"kubetower://user:pass@c/prod/pods",
		"kubetower://c:5823/prod/pods",
		"kubetower://c/" + strings.Repeat("a", maxSegment+1),
		"kubetower://c/prod/" + strings.Repeat("a/", maxSchemeURL),
		"",
	} {
		if got, err := consolePathFromURL(raw); err == nil {
			t.Errorf("%q was accepted as %q; it must be refused", raw, got)
		}
	}
}

// FuzzALinkNeverBecomesMoreThanAPage holds the invariants whatever the input:
// an accepted address becomes a path with no query, no fragment and no dot
// segment, outside every non-page prefix, and reading it again gives itself.
func FuzzALinkNeverBecomesMoreThanAPage(f *testing.F) {
	for _, seed := range []string{
		"kubetower://c/prod/pods/default/web-1", "kubetower://c/prod%2F..%2Fapi",
		"kubetower://settings/general", "kubetower://c/prod/x/argocd", "kubetower:///api",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		path, err := consolePathFromURL(raw)
		if err != nil {
			return
		}
		if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, `?#\`) {
			t.Fatalf("%q became %q", raw, path)
		}
		for _, segment := range strings.Split(path, "/")[1:] {
			if segment == "." || segment == ".." {
				t.Fatalf("%q became %q, with a dot segment", raw, path)
			}
		}
		for _, prefix := range []string{"/api", "/auth", "/login", "/kt-"} {
			if strings.HasPrefix(path, prefix) {
				t.Fatalf("%q became %q, under %s", raw, path, prefix)
			}
		}
		again, err := consolePathFromURL("kubetower://" + strings.TrimPrefix(path, "/"))
		if err != nil || again != path {
			t.Fatalf("%q became %q, which reads back as %q, %v", raw, path, again, err)
		}
	})
}
