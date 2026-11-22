package redis

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
)

// TestAReadRefusedForThePersonIsAnAnswerAboutThem: the access review refused
// the person's Service list or tunnel (403 list_forbidden, a refusal) or could
// not be asked (503 list_unreviewed, a failure to ask), as Argo CD answers
// both. Never the 502 of a read that failed: the cluster was not read.
func TestAReadRefusedForThePersonIsAnAnswerAboutThem(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		status  int
		code    string
		kind    string
		mustSay string
	}{
		{"denied", fmt.Errorf("listing: %w", extension.ErrDenied),
			http.StatusForbidden, CodeListForbidden, kindRefusal, "may not"},
		{"unreviewable", fmt.Errorf("listing: %w", extension.ErrUnreviewable),
			http.StatusServiceUnavailable, CodeListUnreviewed, kindFailure, "could not ask the cluster whether you may"},
	} {
		t.Run(tc.name+"/inventory", func(t *testing.T) {
			h := newHost()
			h.listErr["services"] = tc.err
			w := serve(t, h, "/x/kind/redis/")
			p := decode[problemBody](t, w)
			if w.Code != tc.status || p.Code != tc.code || p.Kind != tc.kind || !strings.Contains(p.Message, tc.mustSay) {
				t.Errorf("answered %d %+v, want %d %s, %s, saying %q", w.Code, p, tc.status, tc.code, tc.kind, tc.mustSay)
			}
		})
		t.Run(tc.name+"/tunnel", func(t *testing.T) {
			h := newHost()
			h.redisService(t, "cache", "redis", corev1.ServicePort{Name: "redis", Port: 6379}, "redis-0")
			h.forwardErr = tc.err
			w := serve(t, h, "/x/kind/redis/instances/cache/redis/6379/probe")
			p := decode[problemBody](t, w)
			if w.Code != tc.status || p.Code != tc.code || p.Kind != tc.kind || !strings.Contains(p.Message, tc.mustSay) {
				t.Errorf("answered %d %+v, want %d %s, %s, saying %q", w.Code, p, tc.status, tc.code, tc.kind, tc.mustSay)
			}
		})
	}
}
