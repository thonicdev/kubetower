package kubecron

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/extension"
)

// TestAReadRefusedForThePersonIsAnAnswerAboutThem: the access review refused
// the person's CronJob list (403 list_forbidden) or could not be asked (503
// list_unreviewed), as Argo CD answers both. Never the 502 of a read that
// failed: the cluster was not read.
func TestAReadRefusedForThePersonIsAnAnswerAboutThem(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		status  int
		code    string
		mustSay string
	}{
		{"denied", fmt.Errorf("listing: %w", extension.ErrDenied),
			http.StatusForbidden, CodeListForbidden, "you may not read"},
		{"unreviewable", fmt.Errorf("listing: %w", extension.ErrUnreviewable),
			http.StatusServiceUnavailable, CodeListUnreviewed, "could not ask the cluster whether you may"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := hostWithCollector(t)
			h.listErr = tc.err
			rec := serve(t, testExtra().list(h), "/x/docker-desktop/kubecron/", nil)
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
