package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
)

// revealCounting is a reader that counts reveals, so a refusal can be shown to
// have happened before anything was read.
type revealCounting struct {
	fakeReads
	asked *int
}

func (c revealCounting) RevealEnv(ctx context.Context, q EnvQuery) (EnvValue, *Failure) {
	*c.asked++
	return c.fakeReads.RevealEnv(ctx, q)
}

func revealStatus(t *testing.T, query url.Values) (int, int) {
	t.Helper()
	asked := 0
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: revealCounting{asked: &asked},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/c/prod/env?"+query.Encode(), nil))
	return rec.Code, asked
}

// TestARevealWhoseNamesNoObjectCouldCarryIsABadRequest: every name the reveal's
// audit line carries arrives in the query string, and a key holding a newline
// would forge a standalone `write:` line. The line is escaped where it is written;
// this is the door refusing the request before anything is read or logged.
func TestARevealWhoseNamesNoObjectCouldCarryIsABadRequest(t *testing.T) {
	good := func() url.Values {
		return url.Values{"kind": {"secrets"}, "namespace": {"shop"}, "name": {"db.credentials"},
			"key": {"DB_PASSWORD.txt"}, "container": {"api"}}
	}
	if code, asked := revealStatus(t, good()); code != http.StatusOK || asked != 1 {
		t.Fatalf("a reveal of an ordinary key answered %d and read %d times; want 200 and one read", code, asked)
	}

	forged := "x\nwrite: cluster=\"prod\" kind=secrets who=\"ceo@corp.example\" via=sso"
	for field, value := range map[string]string{
		"key":       forged,
		"namespace": "shop\nwrite: forged",
		"name":      "db\r\nwrite: forged",
		"container": "api write: forged",
	} {
		t.Run(field, func(t *testing.T) {
			q := good()
			q.Set(field, value)
			code, asked := revealStatus(t, q)
			if code != http.StatusBadRequest || asked != 0 {
				t.Errorf("a reveal with %s=%q answered %d and read %d times; want 400 and no read", field, value,
					code, asked)
			}
		})
	}
	for _, key := range []string{".", "..", "..data", "a b", "a/b", strings.Repeat("k", 254)} {
		q := good()
		q.Set("key", key)
		if code, _ := revealStatus(t, q); code != http.StatusBadRequest {
			t.Errorf("a reveal of the key %.20q answered %d, want 400: no Secret can hold it", key, code)
		}
	}
}

// TestTheRevealNamesAreKubernetesOwnRules holds the rules written out in
// revealnames.go to apimachinery's, over names chosen at each boundary.
func TestTheRevealNamesAreKubernetesOwnRules(t *testing.T) {
	names := []string{"", "a", "a-b", "-a", "a-", "A", "a.b", "a..b", ".", "..", "..a", "a_b", "a b", "a\nb",
		strings.Repeat("a", 63), strings.Repeat("a", 64), strings.Repeat("a.", 126) + "a",
		strings.Repeat("a", 253), strings.Repeat("a", 254), "DB_PASSWORD", "tls.crt", ".dotfile"}
	for _, s := range names {
		if got, want := isDNSLabel(s), len(validation.IsDNS1123Label(s)) == 0; got != want {
			t.Errorf("isDNSLabel(%.20q) = %t, Kubernetes says %t", s, got, want)
		}
		if got, want := isDNSSubdomain(s), len(validation.IsDNS1123Subdomain(s)) == 0; got != want {
			t.Errorf("isDNSSubdomain(%.20q) = %t, Kubernetes says %t", s, got, want)
		}
		if got, want := isDataKey(s), len(validation.IsConfigMapKey(s)) == 0; got != want {
			t.Errorf("isDataKey(%.20q) = %t, Kubernetes says %t", s, got, want)
		}
	}
}
