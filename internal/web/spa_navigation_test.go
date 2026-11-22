package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A navigation that has been through a service worker is still a navigation.
//
// # Why
//
// The console registers a service worker at scope `/`
// (`/kt-pwa/sw.js`). A service worker re-issues the navigation it intercepts,
// and Chrome stamps the re-issued request `Sec-Fetch-Dest: empty` while keeping
// `Sec-Fetch-Mode: navigate`. A rule that reads only `Dest` would call
// every deep link an asset request and the shell would answer 404 "not in the
// front-end bundle".
//
// In any browser that has opened the console, `/ui/` would work and every other
// address would not. Measured against a real Chromium, signed in, with the header
// set written out below exactly as it arrives.
func TestANavigationThroughAServiceWorkerIsNotAnAssetRequest(t *testing.T) {
	for _, one := range []struct {
		name    string
		headers map[string]string
		rest    string
		want    bool
	}{
		{
			name: "a service worker's re-issued navigation",
			headers: map[string]string{
				"Sec-Fetch-Dest": "empty",
				"Sec-Fetch-Mode": "navigate",
				"Accept":         "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
			},
			rest: "c/prod/pods",
			want: false,
		},
		{
			name:    "an ordinary navigation",
			headers: map[string]string{"Sec-Fetch-Dest": "document", "Sec-Fetch-Mode": "navigate"},
			rest:    "c/prod/pods",
			want:    false,
		},
		{
			// The case the Dest rule exists for, and it must survive: a `fetch`
			// for a path under the shell is not a page and answering it with
			// HTML is how a client reports "the answer was not JSON".
			name:    "a fetch for something under the shell",
			headers: map[string]string{"Sec-Fetch-Dest": "empty", "Sec-Fetch-Mode": "cors"},
			rest:    "c/prod/pods",
			want:    true,
		},
		{
			name:    "a script, which is an asset by any reading",
			headers: map[string]string{"Sec-Fetch-Dest": "script", "Sec-Fetch-Mode": "no-cors"},
			rest:    "something.js",
			want:    true,
		},
		{
			// No Sec-Fetch headers at all: curl, an old browser, a probe. The
			// extension test is the fallback and is unchanged.
			name:    "no fetch metadata, and a path that looks like a page",
			headers: map[string]string{},
			rest:    "c/prod/pods",
			want:    false,
		},
	} {
		t.Run(one.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/ui/"+one.rest, nil)
			for key, value := range one.headers {
				r.Header.Set(key, value)
			}
			if got := spaWantsAsset(r, one.rest); got != one.want {
				t.Errorf("spaWantsAsset = %v, want %v", got, one.want)
			}
		})
	}
}

// A navigation Firefox re-issues through a service worker says neither
// `navigate` nor `document`, and it is still a page load.
//
// # Why
//
// The sign-in page registers `/kt-pwa/sw.js` at scope `/`, and that worker
// answers every fetch with `fetch(event.request)`. Re-issued that way, Firefox
// stamps a navigation `Sec-Fetch-Mode: same-origin` and `Sec-Fetch-Dest: empty`
// - the shape of a script's own `fetch()` - so a rule that trusts `Mode` because
// Chrome keeps `navigate` would answer every reload, pasted link and bookmark
// with a 404 "not in the front-end bundle". A fresh profile signed in through
// the API never registers the worker, which is why it does not reproduce there.
//
// Measured against Playwright's Firefox, through the real
// worker, with the header set below exactly as it arrives. The signal that
// survives in every engine is `Accept`: only a document asks for `text/html`.
func TestAFirefoxNavigationThroughAServiceWorkerIsNotAnAssetRequest(t *testing.T) {
	const (
		firefoxNavigation = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
		chromeImage       = "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8"
	)
	for _, one := range []struct {
		name    string
		headers map[string]string
		rest    string
		want    bool
	}{
		{
			name: "Firefox's re-issued navigation",
			headers: map[string]string{
				"Sec-Fetch-Dest": "empty",
				"Sec-Fetch-Mode": "same-origin",
				"Accept":         firefoxNavigation,
			},
			rest: "c/in-cluster/pods",
			want: false,
		},
		{
			// An object whose name ends in an asset extension, reloaded in the
			// same browser: the page asked for HTML, so the extension is a name.
			name: "Firefox's re-issued navigation to a ConfigMap called x.json",
			headers: map[string]string{
				"Sec-Fetch-Dest": "empty",
				"Sec-Fetch-Mode": "same-origin",
				"Accept":         firefoxNavigation,
			},
			rest: "c/in-cluster/configmaps/default/x.json",
			want: false,
		},
		{
			// No fetch metadata at all, as an engine without it sends a
			// navigation: Accept is the only thing saying it is a page.
			name:    "a navigation with no fetch metadata to a name ending in .json",
			headers: map[string]string{"Accept": firefoxNavigation},
			rest:    "c/in-cluster/configmaps/default/x.json",
			want:    false,
		},
		{
			// The two asset requests the rule exists for must keep their 404.
			name:    "a fetch() from the page through the same worker",
			headers: map[string]string{"Sec-Fetch-Dest": "empty", "Sec-Fetch-Mode": "same-origin", "Accept": "*/*"},
			rest:    "c/in-cluster/pods",
			want:    true,
		},
		{
			name:    "an image the worker re-issued",
			headers: map[string]string{"Sec-Fetch-Dest": "empty", "Sec-Fetch-Mode": "no-cors", "Accept": chromeImage},
			rest:    "logo.png",
			want:    true,
		},
		{
			name:    "a script tag, with no fetch metadata",
			headers: map[string]string{"Accept": "*/*"},
			rest:    "vendor/thing.js",
			want:    true,
		},
		{
			// `text/html;q=0` is a refusal of HTML, not a request for it.
			name:    "an Accept that refuses HTML",
			headers: map[string]string{"Sec-Fetch-Dest": "empty", "Sec-Fetch-Mode": "cors", "Accept": "application/json, text/html;q=0"},
			rest:    "c/in-cluster/pods",
			want:    true,
		},
	} {
		t.Run(one.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/"+one.rest, nil)
			for key, value := range one.headers {
				r.Header.Set(key, value)
			}
			if got := spaWantsAsset(r, one.rest); got != one.want {
				t.Errorf("spaWantsAsset = %v, want %v", got, one.want)
			}
		})
	}
}

// Through the handler, end to end: the reload a person makes gets the shell,
// a missing chunk asked for the same way keeps its 404, and the answer names
// every header it was decided on, so no cache in front can hand one answer to
// the other request.
func TestAReloadThroughFirefoxsServiceWorkerGetsTheShell(t *testing.T) {
	mux := muxFor(newSPAFiles(fakeBundle()))
	firefox := []string{
		"Sec-Fetch-Dest", "empty",
		"Sec-Fetch-Mode", "same-origin",
		"Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
	}

	for _, target := range []string{"/c/in-cluster/pods", "/settings/general", "/c/in-cluster/configmaps/default/x.json"} {
		rec := get(t, mux, target, firefox...)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="root"`) {
			t.Errorf("reload of %s answered %d %q, want 200 with the shell", target, rec.Code, firstLine(rec.Body.String()))
		}
		vary := strings.Join(rec.Header().Values("Vary"), ",")
		for _, h := range []string{"Sec-Fetch-Dest", "Sec-Fetch-Mode", "Accept"} {
			if !strings.Contains(vary, h) {
				t.Errorf("reload of %s: Vary %q does not name %s, which the answer was decided on", target, vary, h)
			}
		}
	}

	// A file under /assets/ is never a page, whatever the request says: a
	// missing chunk is a 404 the SPA's chunk recovery can classify.
	if rec := get(t, mux, "/assets/index-Chmc5KSy.js", firefox...); rec.Code != http.StatusNotFound {
		t.Errorf("a missing chunk asked for with Accept text/html answered %d, want 404", rec.Code)
	}
}
