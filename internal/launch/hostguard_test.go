package launch

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A request with a Host outside the allowlist is
// refused.

func TestOnlyThisMachinesNamesAreAnswered(t *testing.T) {
	for host, want := range map[string]bool{
		"127.0.0.1:5823":  true,
		"localhost:5823":  true,
		"[::1]:5823":      true,
		"localhost":       true,
		"[::1]":           true,
		"LOCALHOST:5823":  true,
		"localhost.:5823": true,
		// The development loop's Vite proxy keeps the browser's Host, and a
		// container is published on whatever port the operator chose.
		"localhost:5173":          true,
		"127.0.0.1:8080":          true,
		"127.0.0.2:5823":          true,
		"[::ffff:127.0.0.1]:5823": true,

		// What a rebound page sends: its own name, on the console's port.
		"attacker.example:5823":           false,
		"attacker.example":                false,
		"a.localhost:5823":                false,
		"localhost.attacker.example:5823": false,
		"192.0.2.20:5823":                 false,
		"0.0.0.0:5823":                    false,
		"":                                false,
	} {
		if got := loopbackHost(host); got != want {
			t.Errorf("loopbackHost(%q) = %v; want %v", host, got, want)
		}
	}
}

func TestARequestForAnotherNameIsRefusedBeforeAnythingElse(t *testing.T) {
	reached := false
	h := guardHost(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))

	req := httptest.NewRequest(http.MethodGet, "/api/clusters", nil)
	req.Host = "attacker.example:5823"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if reached {
		t.Error("a request for attacker.example reached the console")
	}
	if rec.Code != http.StatusMisdirectedRequest {
		t.Errorf("answered %d; want 421", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/clusters", nil)
	req.Host = "127.0.0.1:5823"
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !reached {
		t.Error("a request for 127.0.0.1 did not reach the console")
	}
}

func TestTheHostIsCheckedWhereTheConsoleIsLoopback(t *testing.T) {
	for _, c := range []struct {
		name                   string
		listen                 string
		inCluster, inContainer bool
		want                   bool
	}{
		{"the desktop", "127.0.0.1:5823", false, false, true},
		{"the containerized desktop", ":5823", false, true, true},
		{"a desktop told to listen on the LAN", "0.0.0.0:5823", false, false, false},
		{"a desktop on a non-loopback address", "192.0.2.20:5823", false, false, false},
		{"the server profile", ":5823", true, true, false},
	} {
		if got, why := hostPolicy(c.listen, c.inCluster, c.inContainer); got != c.want || why == "" {
			t.Errorf("%s: hostPolicy = %v (%q); want %v and a reason", c.name, got, why, c.want)
		}
	}
}
