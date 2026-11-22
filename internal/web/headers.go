package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"html/template"
	"io/fs"
	"net/http"
	"sort"
	"strings"
)

// The security response headers. The rendering code escapes what it draws, but
// that is a property of code anyone can edit, and this process serves
// cluster-supplied strings (labels, event messages, sync messages, Redis keys)
// to a browser holding a session good for every cluster. The policy turns an
// escaping bug into a rendering bug rather than a stolen session.
//
// `script-src` is derived, not written down: a SHA-256 of every inline
// `<script>` actually served (the compiled shell's and the login page's),
// computed at startup from the bytes the browser receives, so it cannot go stale.
// An inline script whose body differs per response has no static hash and would
// be refused silently.

const (
	// no-referrer rather than same-origin: this console's URLs carry the cluster
	// name, and on EKS that is an ARN holding an AWS account id.
	referrerPolicy = "no-referrer"

	// Redundant with `frame-ancestors 'none'` wherever the CSP is read, and kept
	// because a clickjacked Drain costs a node.
	frameOptions = "DENY"

	contentTypeOptions = "nosniff"

	// One year, this host only, no `preload` - see StrictTransport.
	strictTransportSecurity = "max-age=31536000"
)

// StrictTransport wraps h so that every response carries
// `Strict-Transport-Security`. internal/launch applies it under `--incluster`
// only. The binary sends it, so it holds whatever ingress is in front.
//
// The server profile is reached through TLS and its session cookie is `Secure`;
// without the header a first `http://` request (a typed address, an old
// bookmark) could be answered by someone on the path with a sign-in form in the
// clear. Not on the desktop: plain HTTP over loopback, where browsers discard it
// (RFC 6797 §8.1). The request's scheme is not checked: behind the ingress this
// process cannot see it, and a browser discards the header over a non-secure
// transport anyway.
//
// Conservative parameters: no `includeSubDomains` (the parent domain may host
// other services), no `preload` (a decision about a whole domain), and a year,
// longer than a password session's cap. An administrator who wants more sets it
// at the ingress and checks that its header replaces this one (a browser
// processes only the first).
func StrictTransport(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", strictTransportSecurity)
		h.ServeHTTP(w, r)
	})
}

// SecurityHeaders wraps h so that every response this process makes carries the
// headers above. internal/launch puts it on the outer router, above the session
// guard, so no route registered later can be outside it. They apply uniformly:
// `nosniff` is right on every response (CSV, SSE, plain text), and the others
// are inert on machine-facing ones.
//
// extraInlineScriptSources are HTML documents served from outside the bundle,
// so their inline scripts can be hashed: the login page, a string constant in
// internal/auth, served before anything is trusted.
func SecurityHeaders(h http.Handler, extraInlineScriptSources ...string) http.Handler {
	return SecurityHeadersFor(nil, h, extraInlineScriptSources...)
}

// SecurityHeadersFor is SecurityHeaders for a console that serves bundle as its
// front end, the value `(*Server).Bundle` answers; nil is the embedded bundle.
// The policy must be hashed from the document it is served with, or it blocks
// that document's inline script, so the bundle is a parameter both share. It is
// already resolved and used as it is.
func SecurityHeadersFor(bundle fs.FS, h http.Handler, extraInlineScriptSources ...string) http.Handler {
	if bundle == nil {
		bundle = bundleFS(nil)
	}
	base := contentSecurityPolicy(bundle, extraInlineScriptSources...)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		head := w.Header()
		head.Set("X-Content-Type-Options", contentTypeOptions)
		head.Set("X-Frame-Options", frameOptions)
		head.Set("Referrer-Policy", referrerPolicy)
		head.Set("Content-Security-Policy", base+connectSrc(r.Host))
		h.ServeHTTP(w, r)
	})
}

// contentSecurityPolicy is everything in the policy that does not depend on the
// request; `connect-src` is appended per request (connectSrc).
func contentSecurityPolicy(bundle fs.FS, extraInlineScriptSources ...string) string {
	script := append([]string{"'self'"}, inlineScriptHashes(bundle, extraInlineScriptSources...)...)

	return strings.Join([]string{
		"default-src 'self'",

		// No 'unsafe-inline' and no 'unsafe-eval': the inline blocks served are named by
		// hash, and an injected <script> is not.
		"script-src " + strings.Join(script, " "),

		// 'unsafe-inline' for styles is unavoidable and much smaller than for script:
		// the front end sets inline style attributes (widths, colors), and an attribute
		// cannot carry a nonce.
		"style-src 'self' 'unsafe-inline'",

		// The PWA's service worker, /kt-pwa/sw.js. worker-src falls back to script-src,
		// whose hash list would not match a file, so it is stated.
		"worker-src 'self'",

		"object-src 'none'",
		// Nothing writes a <base>, and an injected one would re-point every relative URL
		// on the page.
		"base-uri 'none'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
}

// connectSrc names this origin's WebSocket scheme explicitly beside 'self'. CSP3
// lets 'self' match ws:/wss:, but a browser that does not would kill every
// terminal with no status. It is this origin, not a bare `ws:` that would allow a
// socket anywhere. r.Host is client-supplied, so an implausible host contributes
// nothing and the policy falls back to 'self'.
func connectSrc(host string) string {
	if !plausibleHost(host) {
		return "; connect-src 'self'"
	}
	return "; connect-src 'self' ws://" + host + " wss://" + host
}

func plausibleHost(host string) bool {
	if host == "" || len(host) > 255 {
		return false
	}
	for _, c := range host {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '-', c == ':', c == '[', c == ']':
		default:
			return false
		}
	}
	return true
}

// inlineScriptHashes returns the sha256 source expressions for every inline
// script this process serves, deduplicated and ordered so the header is stable.
// The compiled shell is served verbatim, so its bytes are hashed; the login page
// goes through html/template, whose escaper deletes JavaScript comments, so what
// it emits is hashed.
func inlineScriptHashes(bundle fs.FS, extra ...string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		if seen[h] {
			return
		}
		seen[h] = true
		out = append(out, h)
	}
	// Served as a file: what is written is what is sent.
	for _, body := range bundleInlineScripts(bundle) {
		add(scriptHash(body))
	}
	// Rendered: what html/template emits is what is sent.
	for _, body := range inlineScriptsIn(strings.Join(extra, "\n")) {
		add(scriptHash(asRendered(body)))
	}
	sort.Strings(out)
	return out
}

// asRendered puts one inline script body through html/template as the page goes
// through it, because the escaper deletes JavaScript comments and a hash of the
// source would match nothing a browser sees. A body that fails to parse is
// hashed as it stands rather than dropped, so the failure is a refused script,
// not a silently missing entry.
func asRendered(body string) string {
	tpl, err := template.New("inline").Parse("<script>" + body + "</script>")
	if err != nil {
		return body
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, nil); err != nil {
		return body
	}
	got := inlineScriptsIn(buf.String())
	if len(got) != 1 {
		return body
	}
	return got[0]
}

func scriptHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// bundleInlineScripts reads the compiled shell's inline scripts, so one added
// anywhere is still covered. The shell carries one (the service-worker
// registration), and without its hash the install offer would silently never
// appear. The built shell, not `web/index.html`, because the browser runs what
// Vite emitted.
func bundleInlineScripts(bundle fs.FS) []string {
	raw, err := fs.ReadFile(bundle, bundleIndex)
	if err != nil {
		// No build embedded: the dev server serves its own document, with nothing of
		// ours in front.
		return nil
	}
	return inlineScriptsIn(string(raw))
}

// inlineScriptsIn returns the body of every `<script>` in doc that a browser will
// execute: no `src`, and no `type` naming something other than JavaScript. A
// data block (`type="application/json"`) is never run as a script, so CSP has no
// opinion about it.
func inlineScriptsIn(doc string) []string {
	var out []string
	rest := doc
	for {
		i := strings.Index(rest, "<script")
		if i < 0 {
			return out
		}
		rest = rest[i+len("<script"):]
		if rest == "" {
			return out
		}
		// `<scriptish>` is not a script element: the tag name has to end.
		switch rest[0] {
		case '>', '/', ' ', '\t', '\n', '\r':
		default:
			continue
		}
		j := strings.IndexByte(rest, '>')
		if j < 0 {
			return out
		}
		attrs, body := rest[:j], rest[j+1:]
		rest = body
		if !executableScript(attrs) {
			continue
		}
		k := strings.Index(body, "</script")
		if k < 0 {
			return out
		}
		out = append(out, body[:k])
		rest = body[k:]
	}
}

func executableScript(attrs string) bool {
	low := strings.ToLower(attrs)
	if strings.Contains(low, "src=") {
		return false
	}
	switch strings.TrimSpace(attributeValue(low, "type")) {
	case "", "module", "text/javascript", "application/javascript":
		return true
	}
	return false
}

// attributeValue reads one quoted attribute out of a start tag's attribute text.
// It errs towards "there is a type here": an unreadable one is not a JavaScript
// type, so the element is skipped rather than hashed wrongly.
func attributeValue(attrs, name string) string {
	i := strings.Index(attrs, name+"=")
	if i < 0 {
		return ""
	}
	v := attrs[i+len(name)+1:]
	if v == "" {
		return "?"
	}
	quote := v[0]
	if quote != '"' && quote != '\'' {
		if end := strings.IndexAny(v, " \t\r\n"); end >= 0 {
			return v[:end]
		}
		return v
	}
	end := strings.IndexByte(v[1:], quote)
	if end < 0 {
		return "?"
	}
	return v[1 : 1+end]
}
