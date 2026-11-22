package launch

// The Host allowlist must not refuse the probes: the image's CI step asks
// `/kt-healthz` at the container's bridge address, and a refusal reads a
// serving console as dead. The probes answer any Host, with their status and a
// bare word off loopback, and everything else is still refused. See
// hostguard.go, "The probes".

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/auth"
	"github.com/thonicdev/kubetower/internal/health"
)

// The address the CI step asks at: a container on Docker's default bridge.
const bridgeHost = "172.17.0.3:5823"

// fixedReadiness is health.Health's one method, answering what it is told.
type fixedReadiness health.Readiness

func (f fixedReadiness) Readiness(context.Context) health.Readiness { return health.Readiness(f) }

// What the readiness reason can carry, and must not leave this machine: the
// kubeconfig's path, the first line of what broke it, and how many contexts
// there are.
var (
	readyAnswer = fixedReadiness{OK: true, Reason: "3 of 14 contexts answered", Total: 14, Probed: 14, Answered: 3}
	notReady    = fixedReadiness{Reason: "the kubeconfig no longer reads: /home/kubetower/.kube/config: " +
		"error loading config file: yaml: line 7: could not find expected ':'"}
	leaks = []string{"context", ".kube", "kubeconfig", "yaml", "14", "detail"}
)

// probedConsole is what Run serves, through frontHandler, with the three
// probes registered as Run registers them and a catch-all standing in for the
// single-page shell and the API.
//
// **Behind the real session guard, as Run mounts it** (mountOuter around
// front.Middleware): the probes answer a caller with no cookie only because
// internal/auth lists them as public, and a bare mux would keep these tests
// green the day it stopped. The front door is the
// loopback desktop's, with no password, so nothing here signs in.
func probedConsole(t *testing.T, probe fixedReadiness, listen string, inContainer bool) (http.Handler, *int) {
	t.Helper()
	front, err := auth.New(auth.Config{User: "admin", WithoutPassword: true,
		SessionKey: []byte("a-thirty-two-byte-key-for-tests!")})
	if err != nil {
		t.Fatal(err)
	}
	ready := newReadiness(probe, nil, false)
	mux := http.NewServeMux()
	api.New(api.Deps{Probe: ready}).Mount(mux)
	mux.HandleFunc("GET "+auth.HealthPath, healthz(ready))
	others := 0
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		others++
		w.WriteHeader(http.StatusOK)
	})
	outer := http.NewServeMux()
	mountOuter(outer, front.Middleware(mux), http.NotFoundHandler(), false)
	handler, _ := frontHandler(nil, outer, listen, false, inContainer)
	return handler, &others
}

var desktops = map[string]struct {
	listen      string
	inContainer bool
}{
	"the loopback desktop":      {"127.0.0.1:5823", false},
	"the containerized desktop": {"0.0.0.0:5823", true},
}

func ask(h http.Handler, method, path, host string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestTheProbesAnswerAnotherHostWithAStatusAndNoReason(t *testing.T) {
	for name, desktop := range desktops {
		for _, c := range []struct {
			probe fixedReadiness
			path  string
			code  int
			body  string // exact, or "" for a problem checked below
		}{
			{readyAnswer, api.LivenessPath, 200, "{\"ok\":true}\n"},
			{readyAnswer, api.ReadinessPath, 200, "{\"ok\":true}\n"},
			{readyAnswer, auth.HealthPath, 200, "ok\n"},
			{notReady, api.LivenessPath, 200, "{\"ok\":true}\n"},
			{notReady, api.ReadinessPath, 503, ""},
			{notReady, auth.HealthPath, 503, "degraded\n"},
		} {
			handler, _ := probedConsole(t, c.probe, desktop.listen, desktop.inContainer)
			rec := ask(handler, http.MethodGet, c.path, bridgeHost)
			body := rec.Body.String()
			what := name + ": GET " + c.path + " for Host " + bridgeHost
			if rec.Code != c.code {
				t.Errorf("%s answered %d %q; want %d", what, rec.Code, body, c.code)
				continue
			}
			for _, leak := range leaks {
				if strings.Contains(body, leak) {
					t.Errorf("%s carries %q off loopback: %q", what, leak, body)
				}
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("%s can be cached", what)
			}
			if rec.Header().Get("Content-Security-Policy") == "" {
				t.Errorf("%s carries no security headers", what)
			}
			if c.body != "" {
				if body != c.body {
					t.Errorf("%s answered %q; want %q", what, body, c.body)
				}
				if ct, want := rec.Header().Get("Content-Type"), terseType(c.path); ct != want {
					t.Errorf("%s answered Content-Type %q; want %q", what, ct, want)
				}
				continue
			}
			// `/readyz` failing: the contract's problem, with no detail.
			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("%s answered %s; want application/problem+json", what, ct)
			}
			var problem map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
				t.Errorf("%s answered a body that is not JSON: %q", what, body)
				continue
			}
			if _, ok := problem["detail"]; ok || problem["title"] != "Not ready" || problem["status"] != float64(503) {
				t.Errorf("%s answered %v; want a 503 problem titled \"Not ready\" with no detail", what, problem)
			}
		}

		// HEAD is what some probes send; it is a GET for the router - and a
		// failing one keeps its status and carries no reason either.
		for _, c := range []struct {
			probe fixedReadiness
			path  string
			code  int
		}{
			{readyAnswer, auth.HealthPath, 200},
			{readyAnswer, api.ReadinessPath, 200},
			{notReady, api.ReadinessPath, 503},
			{notReady, auth.HealthPath, 503},
		} {
			handler, _ := probedConsole(t, c.probe, desktop.listen, desktop.inContainer)
			rec := ask(handler, http.MethodHead, c.path, bridgeHost)
			what := name + ": HEAD " + c.path + " for Host " + bridgeHost
			if rec.Code != c.code {
				t.Errorf("%s answered %d; want %d", what, rec.Code, c.code)
			}
			for _, leak := range leaks {
				if strings.Contains(rec.Body.String(), leak) {
					t.Errorf("%s carries %q off loopback: %q", what, leak, rec.Body.String())
				}
			}
		}
	}
}

// terseType is the Content-Type a probe's bare word is sent with off loopback:
// each names its charset, as this process's other text answers do.
func terseType(path string) string {
	if path == auth.HealthPath {
		return "text/plain; charset=utf-8"
	}
	return "application/json; charset=utf-8"
}

func TestOnLoopbackTheProbesStillSayWhy(t *testing.T) {
	for name, desktop := range desktops {
		for _, c := range []struct {
			probe  fixedReadiness
			path   string
			code   int
			reason string
		}{
			{readyAnswer, api.LivenessPath, 200, "the console is serving"},
			{readyAnswer, api.ReadinessPath, 200, readyAnswer.Reason},
			{readyAnswer, auth.HealthPath, 200, "ok: " + readyAnswer.Reason},
			{notReady, api.ReadinessPath, 503, "/home/kubetower/.kube/config"},
			{notReady, auth.HealthPath, 503, "degraded: " + notReady.Reason},
		} {
			for _, host := range []string{"127.0.0.1:5823", "localhost:8080", "[::1]:5823"} {
				handler, _ := probedConsole(t, c.probe, desktop.listen, desktop.inContainer)
				rec := ask(handler, http.MethodGet, c.path, host)
				if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.reason) {
					t.Errorf("%s: GET %s for Host %s answered %d %q; want %d carrying %q",
						name, c.path, host, rec.Code, rec.Body.String(), c.code, c.reason)
				}
			}
		}
	}
}

func TestEverythingButTheProbesIsStillRefusedAnotherHost(t *testing.T) {
	for name, desktop := range desktops {
		handler, others := probedConsole(t, readyAnswer, desktop.listen, desktop.inContainer)
		for _, c := range []struct{ method, path string }{
			{http.MethodGet, "/"},
			{http.MethodGet, "/api/clusters"},
			{http.MethodGet, "/api/session"},
			{http.MethodPost, "/api/auth/login"},
			{http.MethodGet, "/auth/launch"},
			{http.MethodPost, "/auth/handoff"},
			{http.MethodGet, "/assets/index.js"},
			{http.MethodGet, "/c/prod/overview"},
			// A probe's neighbors and a probe under another method.
			{http.MethodGet, "/kt-healthz/"},
			{http.MethodGet, "/kt-healthz/x"},
			{http.MethodGet, "/healthzz"},
			{http.MethodGet, "/api/healthz"},
			{http.MethodPost, "/kt-healthz"},
			{http.MethodPost, "/readyz"},
			{http.MethodDelete, "/healthz"},
			// A probe's path spelt another way. The guard compares the path
			// whole and before any cleaning, so each is refused here rather
			// than reaching the router's redirect to the clean path.
			{http.MethodGet, "//healthz"},
			{http.MethodGet, "/./healthz"},
			{http.MethodGet, "/a/../healthz"},
			{http.MethodGet, "/HEALTHZ"},
			{http.MethodHead, "//readyz"},
			{http.MethodGet, "/KT-HEALTHZ"},
		} {
			*others = 0
			rec := ask(handler, c.method, c.path, bridgeHost)
			if rec.Code != http.StatusMisdirectedRequest || *others != 0 {
				t.Errorf("%s: %s %s for Host %s answered %d and reached the console %d times; want 421, never",
					name, c.method, c.path, bridgeHost, rec.Code, *others)
			}
		}
	}
}

// A 103 Early Hints before a 503 must not keep 103 as the probe's status: the
// bare word would then go out under net/http's implied 200 - a failing console
// read as healthy off loopback.
func TestAnInformationalAnswerIsNotTheProbesStatus(t *testing.T) {
	for _, path := range []string{api.LivenessPath, api.ReadinessPath, auth.HealthPath} {
		for _, c := range []struct {
			name   string
			answer func(w http.ResponseWriter)
			code   int
		}{
			{"a 103, then a 503", func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusEarlyHints)
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, "degraded: the kubeconfig no longer reads")
			}, 503},
			{"a 100 and a 103, then a write", func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusContinue)
				w.WriteHeader(http.StatusEarlyHints)
				_, _ = io.WriteString(w, "ok")
			}, 200},
		} {
			h := guardHost(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { c.answer(w) }))
			if rec := ask(h, http.MethodGet, path, bridgeHost); rec.Code != c.code {
				t.Errorf("GET %s for Host %s, whose handler answered %s: answered %d; want %d",
					path, bridgeHost, c.name, rec.Code, c.code)
			}
		}
	}
}

// Copying the handler's headers through statusOnly would break no other test,
// because no probe sets a header worth stealing today. This handler sets one of
// every kind, and none of
// them may reach a foreign Host: only the two the guard sets itself.
func TestNoHeaderAProbeSetsLeavesThisMachine(t *testing.T) {
	canaries := map[string]string{
		"X-Kubeconfig":  "/home/kubetower/.kube/config",
		"Set-Cookie":    "kt_session=secret; Path=/; HttpOnly",
		"Location":      "/c/prod/overview",
		"Link":          "</assets/index.js>; rel=preload",
		"Content-Type":  "text/html; charset=utf-8",
		"Cache-Control": "max-age=3600",
		"Retry-After":   "14",
	}
	for _, path := range []string{api.LivenessPath, api.ReadinessPath, auth.HealthPath} {
		for _, code := range []int{http.StatusOK, http.StatusServiceUnavailable} {
			h := guardHost(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range canaries {
					w.Header().Set(k, v)
				}
				w.WriteHeader(code)
				_, _ = io.WriteString(w, "the reason")
			}))
			rec := ask(h, http.MethodGet, path, bridgeHost)
			what := "GET " + path + " for Host " + bridgeHost
			if rec.Code != code {
				t.Errorf("%s answered %d; want %d", what, rec.Code, code)
			}
			for k, v := range canaries {
				for _, got := range rec.Header().Values(k) {
					if got == v {
						t.Errorf("%s carries the handler's %s: %q off loopback", what, k, got)
					}
				}
			}
			for k := range rec.Header() {
				if k != "Cache-Control" && k != "Content-Type" {
					t.Errorf("%s carries %s: %q, which the guard does not set", what, k, rec.Header().Get(k))
				}
			}
		}
	}
}
