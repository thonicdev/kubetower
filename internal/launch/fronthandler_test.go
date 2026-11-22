package launch

// The Host allowlist stands in for the password against DNS rebinding on the
// loopback desktop, so a Run that stopped wiring it must fail a test. These
// build what Run serves, through the same
// function, and read Run to check it serves that.

import (
	"go/ast"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWhatRunServesRefusesAnotherHostBeforeAnything(t *testing.T) {
	reached := 0
	outer := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusOK)
	})
	for name, profile := range map[string]struct {
		listen      string
		inContainer bool
	}{
		"the loopback desktop":      {"127.0.0.1:5823", false},
		"the containerized desktop": {"0.0.0.0:5823", true},
	} {
		handler, _ := frontHandler(nil, outer, profile.listen, false, profile.inContainer)
		for _, req := range []*http.Request{
			httptest.NewRequest(http.MethodGet, "/api/session", nil),
			httptest.NewRequest(http.MethodGet, "/assets/index.js", nil),
			httptest.NewRequest(http.MethodPost, "/auth/handoff", nil),
			func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/api/terminal/ws", nil)
				r.Header.Set("Connection", "Upgrade")
				r.Header.Set("Upgrade", "websocket")
				r.Header.Set("Sec-WebSocket-Version", "13")
				r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
				return r
			}(),
		} {
			req.Host = "rebound.example:5823"
			reached = 0
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusMisdirectedRequest || reached != 0 {
				t.Errorf("%s: %s %s for Host %s answered %d and reached the console %d times; want 421, never",
					name, req.Method, req.URL.Path, req.Host, rec.Code, reached)
			}
			if rec.Header().Get("Content-Security-Policy") == "" {
				t.Errorf("%s: the 421 carries no security headers", name)
			}
		}

		req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
		req.Host = "localhost:5823"
		reached = 0
		handler.ServeHTTP(httptest.NewRecorder(), req)
		if reached != 1 {
			t.Errorf("%s: a request for localhost did not reach the console", name)
		}
	}

	// Under --incluster the name is the ingress's, and is not checked.
	handler, _ := frontHandler(nil, outer, "0.0.0.0:8080", true, true)
	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	req.Host = "kubetower.example"
	reached = 0
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if reached != 1 {
		t.Error("under --incluster a request for the ingress's name was refused")
	}
}

// Read from run.go, Run and its phases alike: Run serves the handler
// frontHandler built, and no function in run.go but frontHandler wraps the
// security headers itself - in Run, in mountRoutes or in serve, a second
// SecurityHeadersFor is a handler built around frontHandler's Host check.
func TestRunServesWhatFrontHandlerBuilds(t *testing.T) {
	fset, funcs := runSource(t)

	for name, fn := range funcs {
		if name == "frontHandler" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "SecurityHeadersFor" {
					t.Errorf("%s builds a handler itself at %s, around frontHandler's Host check",
						name, fset.Position(call.Pos()))
				}
			}
			return true
		})
	}
	for _, phase := range []string{"mountRoutes", "serve"} {
		if funcs[phase] == nil {
			t.Errorf("%s is gone from run.go, so this test no longer reads where the routes and the server are built", phase)
		}
	}

	// The handler serve is handed is the one frontHandler returned.
	var built string
	var served *ast.CallExpr
	ast.Inspect(funcs["Run"].Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if call, ok := n.Rhs[0].(*ast.CallExpr); ok && calledName(call) == "frontHandler" {
				if id, ok := n.Lhs[0].(*ast.Ident); ok {
					built = id.Name
				}
			}
		case *ast.CallExpr:
			if calledName(n) == "serve" && served == nil {
				served = n
			}
		}
		return true
	})
	if built == "" {
		t.Fatal("Run does not serve what frontHandler builds, so nothing says it checks the Host")
	}
	if served == nil {
		t.Fatal("Run never calls serve")
	}
	// serve(ln, bound, listen, handler, served, stop): the handler is the
	// fourth argument.
	if len(served.Args) < 4 {
		t.Fatalf("serve is called with %d arguments, so this test no longer knows which is the handler", len(served.Args))
	}
	if id, ok := served.Args[3].(*ast.Ident); !ok || id.Name != built {
		t.Errorf("Run hands serve something other than what frontHandler built (%s), "+
			"so nothing says it checks the Host", fset.Position(served.Pos()))
	}
}

// The server profile sends Strict-Transport-Security on every response, the
// desktop on none - the binary owns the header, not the chart.
// Built through frontHandler, the function Run serves (see the test below), so a
// Run that stopped wiring it would fail here rather than in a browser.
func TestTheServerProfileSendsHSTSAndTheDesktopDoesNot(t *testing.T) {
	const want = "max-age=31536000"
	outer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	requests := func(host string) []*http.Request {
		var out []*http.Request
		for _, path := range []string{"/", "/api/session", "/auth/login", "/missing"} {
			r := httptest.NewRequest(http.MethodGet, path, nil)
			r.Host = host
			out = append(out, r)
		}
		return out
	}

	server, _ := frontHandler(nil, outer, "0.0.0.0:5823", true, true)
	for _, req := range requests("kubetower.example") {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if got := rec.Header().Values("Strict-Transport-Security"); len(got) != 1 || got[0] != want {
			t.Errorf("server profile: GET %s answered %d with Strict-Transport-Security %q, want exactly [%q]",
				req.URL.Path, rec.Code, got, want)
		}
	}

	for name, profile := range map[string]struct {
		listen      string
		inContainer bool
	}{
		"the loopback desktop":      {"127.0.0.1:5823", false},
		"the containerized desktop": {"0.0.0.0:5823", true},
	} {
		desktop, _ := frontHandler(nil, outer, profile.listen, false, profile.inContainer)
		// localhost reaches the console; rebound.example is refused with a 421,
		// and the refusal must not carry it either.
		for _, req := range append(requests("localhost:5823"), requests("rebound.example:5823")...) {
			rec := httptest.NewRecorder()
			desktop.ServeHTTP(rec, req)
			if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
				t.Errorf("%s: GET %s for %s answered %d with Strict-Transport-Security %q, want none",
					name, req.URL.Path, req.Host, rec.Code, got)
			}
			if rec.Header().Get("Content-Security-Policy") == "" {
				t.Errorf("%s: GET %s for %s carries no security headers at all, so the absence above proves nothing",
					name, req.URL.Path, req.Host)
			}
		}
	}
}
