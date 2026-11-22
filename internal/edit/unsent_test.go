package edit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

// TestARequestTheGuardHeldBackIsTheConsolesDefectNotTheClusters: when the
// access guard refuses a request the door sent - here because the client is
// another cluster's than the one the review was asked of, the shape of a
// closure that sends somewhere it was not reviewed for - what reaches the
// caller and the record is RefusedUnsent and `reason=unsent`. Never
// RefusedByAPI, which internal/web answers as a 403 "the cluster refused the
// change" about a request the cluster never saw.
//
// Through a real dynamic client on a guarded config, because the guard's error
// arrives wrapped by client-go and the classification has to see through that.
func TestARequestTheGuardHeldBackIsTheConsolesDefectNotTheClusters(t *testing.T) {
	var mu sync.Mutex
	var reached []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reached = append(reached, r.Method+" "+r.URL.Path)
		mu.Unlock()
		http.Error(w, "nothing here", http.StatusNotFound)
	}))
	defer srv.Close()

	cfg := &rest.Config{Host: srv.URL}
	access.Guard(cfg, "elsewhere", nil)
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var logged []string
	w := New(Options{
		Resolve: fakeResolver{dyn: dyn, cluster: "prod"},
		Access:  accesstest.Allow(),
		Logf:    func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	})
	target := Target{Cluster: "prod", Kind: "configmaps", Namespace: "shop", Name: "settings"}
	where, err := w.Locate(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}

	// A patch: classified inside the closure by apiRefusal.
	_, _, err = Send(context.Background(), where, target, []byte(`{"data":{"a":"b"}}`), false)
	unsent(t, "a patch", err)

	// A delete: its error comes back raw, as client-go sent it, and the
	// actions layer classifies it through APIRefusal.
	_, err = Delete(context.Background(), where, target, metav1.DeleteOptions{})
	if !errors.Is(err, access.ErrNotReviewed) {
		t.Fatalf("a delete the guard held back answered %v", err)
	}
	unsent(t, "a delete, through APIRefusal", APIRefusal(err))

	if len(logged) != 2 {
		t.Fatalf("want one failure line per held-back commit, got %q", logged)
	}
	for _, line := range logged {
		if !strings.Contains(line, "outcome=failed reason=unsent") {
			t.Errorf("the record reads %q, want reason=unsent: the cluster did not fail, the console held it back", line)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reached) != 0 {
		t.Errorf("the cluster received %v; a held-back request must not leave the process", reached)
	}
}

func unsent(t *testing.T, what string, err error) {
	t.Helper()
	refusal, ok := AsRefusal(err)
	if !ok || refusal.Kind != RefusedUnsent {
		t.Fatalf("%s the guard held back answered %v (refusal %+v), want RefusedUnsent", what, err, refusal)
	}
	if strings.Contains(refusal.Reason, "the cluster refused") || strings.Contains(refusal.Reason, "https://") ||
		!strings.Contains(refusal.Reason, "the console did not send") {
		t.Errorf("%s: the sentence is %q, want the guard's own words and no address", what, refusal.Reason)
	}
}
