package auth

import (
	"bytes"
	"html/template"
	"log"
	"net/http"
	"net/url"

	"github.com/thonicdev/kubetower/internal/api"
)

// loginView is what the page is told.
//
// No "keep me signed in" box: the sliding session already means a person in
// use is not asked, so the box would be ticked by default, and unticked it
// would be a second session policy to explain and test. A deployment that
// wants shorter sessions sets `KT_SESSION_TTL` once.
type loginView struct {
	// Error is shown in red and means a submission was refused.
	Error string
	// Notice is shown in gray: nothing went wrong, the operator was sent here
	// on the way somewhere else.
	Notice string
	// Next travels through the form so a refused attempt keeps the
	// destination. Already through safeNext.
	Next   string
	Action string

	// PasswordNote is the path of a file where a drawn password can still be
	// read, or "" for a page that says nothing about one. The path, never the
	// contents; see Config.PasswordNote.
	PasswordNote string

	// Launch is the loopback desktop's page: no password and no single
	// sign-on, so the page names the way in that remains.
	Launch bool

	// Password is whether the form is drawn at all: false where an identity
	// provider is configured and the shared account was not kept, since the
	// form would post to a route that does not exist.
	Password bool
	// SSO is the provider's state (`api.SSO*`), and SSOStart the address the
	// button navigates to, carrying `next`. SSODetail says why the provider is
	// unavailable; SSOError is a fixed sentence for a refused sign-in, never
	// the provider's own words.
	SSO       string
	SSOStart  string
	SSODetail string
	SSOError  string
}

// render writes the login page.
//
// html/template, not string replacement: fmt would read the stylesheet's "%"
// as verbs, and a hand-written HTML escaper would be more security-relevant
// code. html/template escapes by context (attribute, text, URL).
func (a *Auth) render(w http.ResponseWriter, r *http.Request, errMsg string, status int) {
	// The destination survives both the GET and a failed POST, or a wrong
	// password would drop the operator back at the home page.
	next := safeNext(r.URL.Query().Get("next"))
	if v := r.PostFormValue("next"); v != "" {
		next = safeNext(v)
	}

	view := loginView{Error: errMsg, Next: next, Action: LoginPath, Password: a.OffersPassword()}
	// The drawn password's file is named only where a password is offered: a
	// hint at a credential that opens nothing is worse than none.
	if view.Password {
		view.PasswordNote = a.passwordNote()
	}
	methods := a.Methods()
	view.SSO = methods.SSO.State
	if view.SSO != api.SSOOff {
		view.SSOStart = StartPath + "?" + url.Values{"next": {next}}.Encode()
		if view.SSO != api.SSOReady {
			view.SSODetail = methods.SSO.Detail
		}
	}
	// A refused sign-in is said in this page's own sentence; an unknown code
	// says nothing rather than echoing the query string.
	if code := r.URL.Query().Get("sso"); code != "" && errMsg == "" {
		view.SSOError = ssoSentences[code]
	}
	view.Launch = !view.Password && view.SSO == api.SSOOff
	if errMsg == "" && view.SSOError == "" && !view.Launch {
		switch {
		case a.loggedOut(r):
			// This browser chose to sign out; saying its session ran out would
			// describe something that did not happen.
			view.Notice = "You are signed out."
			if next != "/" {
				view.Notice += " Sign in to open that page."
			}
		case next != "/":
			// A destination in hand means a session ran out mid-task; saying so
			// keeps the login page from looking like a fault.
			view.Notice = "Your session has expired. Sign in to carry on where you left off."
		}
	}

	// Rendered to a buffer first: a template failure after WriteHeader would
	// be a 200 carrying half a page.
	var buf bytes.Buffer
	if err := loginTpl.Execute(&buf, view); err != nil {
		log.Printf("[auth] rendering the login page: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// no-store, not no-cache: the page reflects session state, and a cached
	// copy after signing in would be a form the operator cannot get past.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// passwordNote asks the caller where a drawn password is still readable, and
// answers "" for every way of not knowing.
//
// Asked on every render: the file exists only until somebody logs in, so a
// value captured at start-up would be wrong when read.
//
// The recover matters: a panic in a caller's predicate would abort the login
// page for every visitor, locking the operator out of their console over a
// hint.
func (a *Auth) passwordNote() (path string) {
	if a.cfg.PasswordNote == nil {
		return ""
	}
	defer func() {
		if p := recover(); p != nil {
			log.Printf("[auth] the password-note hook panicked (%v). The login page stands, "+
				"without the hint", p)
			path = ""
		}
	}()
	return a.cfg.PasswordNote()
}

var loginTpl = template.Must(template.New("login").Parse(loginHTML))

// LoginPageHTML is this page's own source, for internal/web's
// Content-Security-Policy, which hashes every inline `<script>` from the
// bytes actually sent. The page's script cannot move into `/static/`: that
// prefix is behind the session guard, and this page is what a browser sees
// with no session.
func LoginPageHTML() string { return loginHTML }

// The login page: self-contained by requirement. It is served before
// anything is trusted, so it fetches nothing from another origin; its few
// same-origin references (`/kt-pwa/`) are served without a session.
const loginHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>KubeTower</title>
<!-- The same application manifest and the same worker as the console behind it.
     An expired session lands on this page, and it has to stay the installed
     application rather than break out into a browser tab. The middleware serves
     /kt-pwa/ without a session precisely so this page can reference it. -->
<link rel="manifest" href="/kt-pwa/manifest.webmanifest">
<link rel="icon" type="image/svg+xml" href="/kt-pwa/icon.svg">
<meta name="theme-color" content="#326ce5">
<style>
  :root {
    /* color-scheme rather than a toggle: form controls, scrollbars and the
       canvas behind the page are painted by the browser, and this is the only
       thing that tells it which way round. */
    color-scheme: light dark;
    --bg: #f4f5f7; --card: #ffffff; --fg: #1b1f24; --muted: #5c6470;
    --border: #d8dce2; --accent: #326ce5; --accent-fg: #ffffff; --error: #b3261e;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #12151a; --card: #1b1f27; --fg: #e6e9ef; --muted: #9aa4b2;
      --border: #2c323d; --accent: #5b8def; --accent-fg: #0d1117; --error: #f2857c;
    }
  }
  * { box-sizing: border-box; }
  body {
    margin: 0; min-height: 100vh; display: flex; align-items: center;
    justify-content: center; background: var(--bg); color: var(--fg);
    font: 15px/1.5 system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
    padding: 1.5rem;
  }
  .card {
    background: var(--card); border: 1px solid var(--border); border-radius: 12px;
    padding: 2rem; width: 100%; max-width: 22rem;
    box-shadow: 0 1px 3px rgba(0,0,0,.08), 0 8px 24px rgba(0,0,0,.06);
  }
  h1 { margin: 0 0 .25rem; font-size: 1.3rem; letter-spacing: -.01em; }
  .sub { margin: 0 0 1.5rem; color: var(--muted); font-size: .875rem; }
  label { display: block; font-size: .8rem; font-weight: 600; margin-bottom: .35rem; }
  input {
    width: 100%; padding: .6rem .7rem; margin-bottom: 1rem; font: inherit;
    color: var(--fg); background: var(--bg);
    border: 1px solid var(--border); border-radius: 7px;
  }
  input:focus { outline: 2px solid var(--accent); outline-offset: 1px; border-color: transparent; }
  button {
    width: 100%; padding: .65rem; font: inherit; font-weight: 600; cursor: pointer;
    color: var(--accent-fg); background: var(--accent);
    border: 0; border-radius: 7px;
  }
  button:hover { filter: brightness(1.08); }
  .banner {
    margin: 0 0 1rem; padding: .55rem .7rem; font-size: .85rem;
    border: 1px solid var(--border); border-radius: 7px; color: var(--muted);
  }
  /* The path is the one thing on this page meant to be taken somewhere else —
     typed into a terminal or an editor — so it gets a line of its own and
     user-select: all, the same bargain .setup-command makes on the console
     behind this page. break-all because a Windows profile path has no spaces in
     it and would otherwise decide the width of the card.

     No backtick anywhere in this block, and that is not a style choice: this
     whole page is one Go raw string literal, so a backtick in a CSS comment
     closes it and the file stops compiling nine lines further down, naming
     something that reads like CSS. */
  .banner code {
    display: block; margin: .4rem 0 .1rem; padding: .3rem .4rem;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    font-size: .78rem; word-break: break-all; user-select: all;
    background: var(--bg); border: 1px solid var(--border); border-radius: 5px;
  }
  .banner.error {
    color: var(--error); border-color: var(--error);
    background: color-mix(in srgb, var(--error) 8%, transparent);
  }
  /* Single sign-on is a link, not a form: it is a navigation to the identity
     provider, and a navigation needs no script and carries no credential. */
  a.sso {
    display: block; width: 100%; padding: .65rem; font-weight: 600; text-align: center;
    text-decoration: none; color: var(--accent-fg); background: var(--accent);
    border-radius: 7px; margin-bottom: 1rem;
  }
  a.sso:hover { filter: brightness(1.08); }
  a.sso:focus { outline: 2px solid var(--accent); outline-offset: 2px; }
  a.sso.secondary { color: var(--fg); background: var(--bg); border: 1px solid var(--border); }
  .or { text-align: center; color: var(--muted); font-size: .8rem; margin: 0 0 1rem; }
</style>
</head>
<body>
  <main class="card">
    <h1>KubeTower</h1>
    <p class="sub">Kubernetes console</p>
{{if .Error}}    <p class="banner error" role="alert">{{.Error}}</p>
{{else if .SSOError}}    <p class="banner error" role="alert">{{.SSOError}}</p>
{{else if .Notice}}    <p class="banner">{{.Notice}}</p>
{{end}}{{if eq .SSO "ready"}}    <a class="sso" href="{{.SSOStart}}">Sign in with SSO</a>
{{else if eq .SSO "pending"}}    <p class="banner">{{.SSODetail}}</p>
    <a class="sso secondary" href="{{.SSOStart}}">Sign in with SSO</a>
{{else if eq .SSO "unavailable"}}    <p class="banner error" role="status"><strong>Single sign-on is unavailable.</strong> {{.SSODetail}}</p>
    <a class="sso secondary" href="{{.SSOStart}}">Try single sign-on again</a>
{{end}}{{if and .Password (ne .SSO "off")}}    <p class="or">or with the shared account</p>
{{end}}{{if .PasswordNote}}    <p class="banner">
      <strong>First run.</strong> KubeTower drew a password and wrote it here, on
      the machine this console is running on:
      <code>{{.PasswordNote}}</code>
      Sign in with what that file says and KubeTower deletes it.
    </p>
{{end}}{{if .Launch}}    <p class="banner"><strong>Open KubeTower again, or run <code>kubetower open</code>.</strong>
      On this machine the console opens signed in from the application itself, and nothing here
      asks for a password.
    </p>
{{end}}{{if .Password}}    <form method="post" action="{{.Action}}">
      <input type="hidden" name="next" value="{{.Next}}">
      <label for="user">User</label>
      <input id="user" name="user" autocomplete="username" autocapitalize="off"
             autocorrect="off" spellcheck="false" autofocus required>
      <label for="password">Password</label>
      <input id="password" name="password" type="password" autocomplete="current-password" required>
      <button type="submit">Sign in</button>
    </form>
{{end}}  </main>
<script>if("serviceWorker" in navigator){addEventListener("load",function(){navigator.serviceWorker.register("/kt-pwa/sw.js",{scope:"/"}).catch(function(){})})}</script>
</body>
</html>
`
