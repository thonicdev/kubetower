package kubeadjust

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/extension"
)

// TestAReadRefusedForThePersonIsAnAnswerAboutThem: on the page and on a
// workload's tab, the access review refused the person's read (403
// list_forbidden) or could not be asked (503 list_unreviewed), as Argo CD
// answers both. Never the 502 of a read that failed: the cluster was not read.
func TestAReadRefusedForThePersonIsAnAnswerAboutThem(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		status  int
		code    string
		mustSay string
	}{
		{"denied", fmt.Errorf("listing: %w", extension.ErrDenied),
			http.StatusForbidden, CodeListForbidden, "may not read"},
		{"unreviewable", fmt.Errorf("listing: %w", extension.ErrUnreviewable),
			http.StatusServiceUnavailable, CodeListUnreviewed, "could not ask the cluster whether you may"},
	} {
		for _, route := range []struct {
			name  string
			serve func(*fakeHost) *httptest.ResponseRecorder
			fail  func(*fakeHost)
		}{
			{"page", func(h *fakeHost) *httptest.ResponseRecorder { return get(t, h, url.Values{}) },
				func(h *fakeHost) { h.listErr = map[string]error{"pods": tc.err} }},
			{"workload", func(h *fakeHost) *httptest.ResponseRecorder {
				return getWorkload(t, h, "deployments", "shop", "api")
			}, func(h *fakeHost) { h.listErr = map[string]error{"deployments": tc.err} }},
		} {
			t.Run(tc.name+"/"+route.name, func(t *testing.T) {
				h := shopWithDeployment(t)
				route.fail(h)
				rec := route.serve(h)
				var p problem
				if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
					t.Fatalf("decoding: %v, body %s", err, rec.Body)
				}
				if rec.Code != tc.status || p.Code != tc.code || !strings.Contains(p.Message, tc.mustSay) {
					t.Errorf("answered %d %+v, want %d %s saying %q", rec.Code, p, tc.status, tc.code, tc.mustSay)
				}
			})
		}
	}
}

// The node tab, for a person who may not list nodes: a 403 naming it, never the
// 502 of a read that failed; a node that is not there is a 404 when they may
// list nodes.
func TestTheNodeTabSaysWhenThePersonMayNotListNodes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		node    string
		status  int
		code    string
		mustSay string
	}{
		{"denied", fmt.Errorf("listing: %w", extension.ErrDenied), "worker-1",
			http.StatusForbidden, CodeListForbidden, "you may not list nodes"},
		{"unreviewable", fmt.Errorf("listing: %w", extension.ErrUnreviewable), "worker-1",
			http.StatusServiceUnavailable, CodeListUnreviewed, "could not ask the cluster whether you may"},
		{"failed", fmt.Errorf("connection reset"), "worker-1",
			http.StatusBadGateway, CodeListFailed, "could not be read"},
		{"absent node", nil, "worker-9", http.StatusNotFound, CodeUnknownNode, "No node"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := shop(t)
			if tc.err != nil {
				h.listErr = map[string]error{"nodes": tc.err}
			}
			rec := getNode(t, h, tc.node)
			var p problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatalf("decoding: %v, body %s", err, rec.Body)
			}
			if rec.Code != tc.status || p.Code != tc.code || !strings.Contains(p.Message, tc.mustSay) {
				t.Errorf("answered %d %+v, want %d %s saying %q", rec.Code, p, tc.status, tc.code, tc.mustSay)
			}
		})
	}
}
