package forward

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/identity"
)

// A forward the review refuses is refused before a port is taken or a tunnel
// dug, and the review asked is the one a forward needs.
func TestAForwardTheReviewRefusesIsNeverDug(t *testing.T) {
	reviews := &accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) { return false, nil }}
	d := &fakeDigger{}
	m := testManagerOn(t, d, Options{Access: reviews.Gate()})

	_, err := m.Open(t.Context(), dnsRequest())
	if !errors.Is(err, access.ErrDenied) {
		t.Fatalf("a denied forward answered %v", err)
	}
	if d.calls != 0 {
		t.Errorf("the tunnel was dug %d time(s) despite the refusal", d.calls)
	}
	asked := reviews.Asked()
	want := access.Attributes{Verb: "create", Version: "v1", Resource: "pods", Subresource: "portforward", Namespace: "kube-system"}
	if len(asked) != 1 || asked[0].Attributes != want {
		t.Errorf("the review asked %+v, want %+v", asked, want)
	}
}

// With no gate at all, nothing is dug: the fail-closed default.
func TestAForwardWithNoAccessReviewIsNeverDug(t *testing.T) {
	d := &fakeDigger{}
	m := testManager(t, d)
	m.access = nil
	if _, err := m.Open(t.Context(), dnsRequest()); !errors.Is(err, access.ErrUnreviewable) {
		t.Fatalf("a forward with no review answered %v", err)
	}
	if d.calls != 0 {
		t.Errorf("the tunnel was dug with no review")
	}
}

// The desktop's one exception: a review of the kubeconfig's own identity that
// could not be made lets the forward be dug - the API server checks that same
// identity - and the open line says decision=unreviewable. Under the server
// profile the same answer refuses.
func TestADesktopForwardGoesThroughAnUnreviewableReviewAndTheServerProfileRefuses(t *testing.T) {
	failing := func(accesstest.Question) (bool, error) {
		return false, errors.New("the authorization API is not served")
	}
	shared := identity.With(context.Background(), identity.Who{Name: "admin", Method: identity.Password})
	for _, tc := range []struct {
		name      string
		inCluster bool
		digs      bool
	}{
		{"desktop", false, true},
		{"server profile", true, false},
	} {
		var mu sync.Mutex
		var lines []string
		d := &fakeDigger{}
		m := testManagerOn(t, d, Options{Access: access.New(access.Options{Reviews: (&accesstest.Reviews{Answer: failing}).For, InCluster: tc.inCluster})})
		m.logf = func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, fmt.Sprintf(format, args...))
		}
		_, err := m.Open(shared, dnsRequest())
		if tc.digs {
			if err != nil || d.calls != 1 {
				t.Errorf("%s: the forward answered %v after %d digs, want dug", tc.name, err, d.calls)
			}
			mu.Lock()
			found := false
			for _, line := range lines {
				found = found || (strings.Contains(line, "open:") && strings.Contains(line, "decision=unreviewable"))
			}
			mu.Unlock()
			if !found {
				t.Errorf("%s: no open line with decision=unreviewable: %v", tc.name, lines)
			}
			m.Shutdown()
		} else if !errors.Is(err, access.ErrUnreviewable) || d.calls != 0 {
			t.Errorf("%s: answered %v after %d digs, want refused and nothing dug", tc.name, err, d.calls)
		}
	}
}
