package terminal

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/identity"
)

// A shell inside a pod is reviewed for the person who asked, before anything is
// opened: `create pods/exec` on that pod, as a review naming them. Refused, the
// pane says so - and says it was the review, not the cluster being down.
func TestAnExecThePersonMayNotOpenIsRefusedOverTheSocket(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")
	kubeconfig := execKubeconfig(t, "unreachable")
	reviews := &accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) { return false, nil }}
	term, err := New(managerFor(t, kubeconfig), Options{
		Kubeconfig: cluster.Kubeconfig{kubeconfig},
		Access:     access.New(access.Options{Reviews: reviews.For, InCluster: true}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var lines []string
	term.logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	guarded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := identity.Who{Name: "erin@example.com", Method: identity.SSO, Groups: []string{"oidc:dev"}}
		term.ExecHandler().ServeHTTP(w, r.WithContext(identity.With(r.Context(), who)))
	})
	srv := httptest.NewServer(guarded)
	t.Cleanup(srv.Close)

	got := dialAndRead(t, "ws"+strings.TrimPrefix(srv.URL, "http")+
		"/kt-api/exec?cluster=unreachable&namespace=shop&pod=api-1")
	if !strings.Contains(got, "may not create pods/exec api-1 in namespace shop") {
		t.Errorf("the pane was not told the review refused it: %q", got)
	}
	if strings.Contains(got, "exec ended") {
		t.Errorf("the exec was attempted after the refusal: %q", got)
	}
	asked := reviews.Asked()
	want := access.Attributes{Verb: "create", Version: "v1", Resource: "pods", Subresource: "exec", Namespace: "shop", Name: "api-1"}
	if len(asked) != 1 || asked[0].Attributes != want || asked[0].User != "erin@example.com" || asked[0].Self {
		t.Errorf("the review asked %+v, want %+v for erin", asked, want)
	}
	mu.Lock()
	defer mu.Unlock()
	var refused bool
	for _, line := range lines {
		if strings.Contains(line, "exec refused") && strings.Contains(line, "decision=denied") &&
			strings.Contains(line, `who="erin@example.com" via=sso`) {
			refused = true
		}
	}
	if !refused {
		t.Errorf("no attributed decision=denied line: %v", lines)
	}
}
