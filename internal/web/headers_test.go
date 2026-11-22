package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/auth"
)

// Tests of the security headers and the Content-Security-Policy.
//
// TestTheHashesCome… reads the policy independently of asRendered: a test that
// reads the policy through asRendered is circular by construction, and a test
// that can only agree with the code it tests is not a guard.

func through(t *testing.T, host string) http.Header {
	t.Helper()
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}), auth.LoginPageHTML())

	// The target is fixed and the Host is assigned afterwards, because these
	// tests deliberately present hosts httptest.NewRequest would refuse to parse
	// — and a real client can send exactly those.
	req := httptest.NewRequest(http.MethodGet, "/c/example/pods", nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result().Header
}

func TestEveryResponseCarriesTheSecurityHeaders(t *testing.T) {
	head := through(t, "127.0.0.1:5823")

	for _, want := range []struct{ key, value string }{
		{"X-Content-Type-Options", "nosniff"},
		{"X-Frame-Options", "DENY"},
		{"Referrer-Policy", "no-referrer"},
	} {
		if got := head.Get(want.key); got != want.value {
			t.Errorf("%s = %q, want %q", want.key, got, want.value)
		}
	}
	if head.Get("Content-Security-Policy") == "" {
		t.Error("no Content-Security-Policy on the response")
	}
}

// The one that says what the policy is *for*. A CSP whose script-src carries
// 'unsafe-inline' is a header that costs bytes and stops nothing: the injection
// this exists to survive is an inline <script> in a cluster-supplied string.
func TestTheScriptSourceAllowsNothingUnsafe(t *testing.T) {
	policy := through(t, "127.0.0.1:5823").Get("Content-Security-Policy")
	script := directive(t, policy, "script-src")

	for _, forbidden := range []string{"'unsafe-inline'", "'unsafe-eval'", "*", "http:", "https:", "data:"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("script-src contains %s: %q", forbidden, script)
		}
	}
	if !strings.Contains(script, "'sha256-") {
		t.Errorf("script-src names no inline script by hash: %q", script)
	}
}

func TestTheContentSecurityPolicyCarriesTheDirectivesTheConsoleNeeds(t *testing.T) {
	policy := through(t, "127.0.0.1:5823").Get("Content-Security-Policy")

	for _, want := range []struct{ name, value string }{
		{"default-src", "'self'"},
		// Inline style attributes cannot carry a nonce, and the gauge fills, the
		// nameplate colors and the table colgroups are all inline style
		// attributes.
		{"style-src", "'self' 'unsafe-inline'"},
		// /kt-pwa/sw.js. Forbidding it removes the install offer with nothing on
		// screen saying why.
		{"worker-src", "'self'"},
		{"object-src", "'none'"},
		{"base-uri", "'none'"},
		{"form-action", "'self'"},
		// Clickjacking on a console whose destructive verbs are one click plus a
		// typed name.
		{"frame-ancestors", "'none'"},
	} {
		if got := directive(t, policy, want.name); got != want.value {
			t.Errorf("%s = %q, want %q", want.name, got, want.value)
		}
	}
}

// A WebSocket refused by CSP surfaces in the browser as a terminal that dies
// with no status at all. Every
// shell in the dock, pod exec, the node shell and the Claude session are on that
// transport, and dock.js builds the URL from `location` (dock.js ~line 962), so
// the origin in the policy has to be the one the page was served on.
func TestTheDocksWebSocketOriginIsAllowed(t *testing.T) {
	got := directive(t, through(t, "127.0.0.1:5823").Get("Content-Security-Policy"), "connect-src")
	for _, want := range []string{"'self'", "ws://127.0.0.1:5823", "wss://127.0.0.1:5823"} {
		if !strings.Contains(got, want) {
			t.Errorf("connect-src = %q, missing %s", got, want)
		}
	}
}

// r.Host is whatever the client sent. A header assembled out of it without a
// check is a header a client writes half of.
func TestAnImplausibleHostContributesNothingToThePolicy(t *testing.T) {
	for _, host := range []string{"evil.example.com; script-src *", "a b", "héllo", ""} {
		got := directive(t, through(t, host).Get("Content-Security-Policy"), "connect-src")
		if got != "'self'" {
			t.Errorf("Host %q gave connect-src %q, want 'self'", host, got)
		}
	}
}

// The end-to-end one: every inline script this process actually serves is named
// by the policy it serves beside it.
//
// It reads the **compiled console's shell** and the login page rather than a
// list, so a script added by somebody who never opens this file is still
// covered - and one the policy does not name fails here rather than in a
// browser, where the only symptom is a control that silently does nothing.
//
// The shell is where the console's one inline script lives - the service-worker
// registration, which is what makes the console installable.
func TestThePolicyNamesEveryInlineScriptThisProcessServes(t *testing.T) {
	policy := through(t, "127.0.0.1:5823").Get("Content-Security-Policy")

	// Served as a file, so the hash is of the bytes as written.
	for _, body := range bundleInlineScripts(bundleFS(nil)) {
		if h := scriptHash(body); !strings.Contains(policy, h) {
			t.Errorf("an inline script in the bundle is not in the policy (%s):\n%s", h, headSnippet(body, 120))
		}
	}
	// Rendered by html/template, so the hash is of what the escaper emits.
	login := inlineScriptsIn(auth.LoginPageHTML())
	if len(login) == 0 {
		t.Fatal("the login page has no inline script; the scanner has stopped working")
	}
	for _, body := range login {
		if h := scriptHash(asRendered(body)); !strings.Contains(policy, h) {
			t.Errorf("the login page's inline script is not in the policy (%s):\n%s", h, headSnippet(body, 120))
		}
	}
}

// The assumption the rendered half rests on.
//
// The obvious implementation hashes the `<script>` body as it stands in the
// source. html/template does not serve that: its escaper walks the text of a
// script element and deletes the JavaScript comments, and this project's inline
// blocks are mostly comment. A source-derived hash matches nothing a browser
// ever receives, so every inline script on a rendered page would be refused,
// silently.
//
// **And the other half is the opposite mistake**:
// the bundle's shell is a static file, the escaper never touches it, and hashing
// what the escaper *would* emit for it names bytes nobody sends. The two
// documents are hashed differently on purpose and this asserts both.
func TestTheHashesMatchHowEachDocumentIsServed(t *testing.T) {
	policy := through(t, "127.0.0.1:5823").Get("Content-Security-Policy")

	rewritten := 0
	for _, body := range inlineScriptsIn(auth.LoginPageHTML()) {
		out := asRendered(body)
		if out != body {
			rewritten++
			if strings.Contains(policy, scriptHash(body)) {
				t.Errorf("the policy names the *source* of a rendered script, which no browser will see:\n%s",
					headSnippet(body, 120))
			}
		}
		if !strings.Contains(policy, scriptHash(out)) {
			t.Errorf("the policy does not name what html/template emits for:\n%s", headSnippet(body, 120))
		}
	}
	// **The escaper's rewrite is proved on a synthetic body, not on a served
	// one**: the login page's script has no comments, so nothing served
	// demonstrates the rewrite. The property the code depends on is still
	// the escaper's, and this is it.
	if out := asRendered("// a comment" + "\n" + "console.log(1)"); strings.Contains(out, "a comment") {
		t.Error("html/template no longer strips comments from an inline script; " +
			"asRendered exists for that and the hashes it produces are now the source's")
	}

	for _, body := range bundleInlineScripts(bundleFS(nil)) {
		if asRendered(body) == body {
			// Nothing to tell apart for this one: the two hashes agree.
			continue
		}
		if strings.Contains(policy, scriptHash(asRendered(body))) {
			t.Errorf("the policy names the *escaped* form of a script served as a file, which no browser will see:\n%s",
				headSnippet(body, 120))
		}
	}
}

// The scanner's own edges, each of which would put a wrong hash in the header or
// leave a real script out of it.
func TestTheInlineScriptScannerReadsOnlyWhatABrowserWouldRun(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want []string
	}{
		{"a plain inline block", "<script>a()</script>", []string{"a()"}},
		{"one with attributes", "<script defer>b()</script>", []string{"b()"}},
		{"an external file is not inline", `<script src="/static/htmx.min.js" defer></script>`, nil},
		// chartzoom.html: a data block is never prepared as a script, so CSP has
		// no opinion about it — which is what lets its body be {{.Zoom.Data}}.
		{"a JSON data block", `<script type="application/json">{"a":1}</script>`, nil},
		{"a module", `<script type="module">c()</script>`, []string{"c()"}},
		{"text/javascript spelled out", `<script type="text/javascript">d()</script>`, []string{"d()"}},
		{"a tag that merely starts with script", "<scriptish>e()</scriptish>", nil},
		{"two in one document", "<script>f()</script><p>x</p><script>g()</script>", []string{"f()", "g()"}},
		{"an unterminated element", "<script>h()", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := inlineScriptsIn(c.doc)
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

// directive pulls one directive's value out of a policy, so a test asserts what
// a browser will read rather than that a substring is present somewhere.
func directive(t *testing.T, policy, name string) string {
	t.Helper()
	for _, part := range strings.Split(policy, ";") {
		part = strings.TrimSpace(part)
		if part == name || strings.HasPrefix(part, name+" ") {
			return strings.TrimSpace(strings.TrimPrefix(part, name))
		}
	}
	t.Fatalf("no %s in policy %q", name, policy)
	return ""
}

func headSnippet(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// The Strict-Transport-Security value. Which profile sends it is
// internal/launch's test; this one holds the value: one host, no
// includeSubDomains, no preload, a year.
func TestStrictTransportIsThisHostOnlyForAYear(t *testing.T) {
	h := StrictTransport(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	got := rec.Result().Header.Values("Strict-Transport-Security")
	if len(got) != 1 || got[0] != "max-age=31536000" {
		t.Fatalf("Strict-Transport-Security = %q, want exactly [\"max-age=31536000\"]", got)
	}
	if low := strings.ToLower(got[0]); strings.Contains(low, "includesubdomains") || strings.Contains(low, "preload") {
		t.Errorf("Strict-Transport-Security %q reaches beyond this host", got[0])
	}
	if rec.Code != http.StatusTeapot {
		t.Errorf("the wrapped handler's status was lost: %d", rec.Code)
	}
}
