package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/overview"
)

// boardCluster is the docker-desktop node and its pods, with a metrics-server
// that answers `status` and `answer` on both paths the Overview reads: the
// node metrics the rows and the tile come from, and the pod metrics the
// Hottest pods board comes from.
func boardCluster(t *testing.T, status int, answer string) *cluster.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apis/metrics.k8s.io/v1beta1/nodes", "/apis/metrics.k8s.io/v1beta1/pods":
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(server.Close)
	client, err := rest.UnversionedRESTClientFor(&rest.Config{
		Host:          server.URL,
		ContentConfig: rest.ContentConfig{NegotiatedSerializer: scheme.Codecs.WithoutConversion()},
	})
	if err != nil {
		t.Fatalf("a REST client for the fake metrics-server: %v", err)
	}
	clientset := fake.NewSimpleClientset([]runtime.Object{
		desktopNode(),
		requesting("kube-apiserver", "3", "3000Mi"),
		requesting("coredns", "405m", "112Mi"),
	}...)
	return &cluster.Client{Context: "docker-desktop", Clients: withMetricsServer{Clientset: clientset, rest: client}}
}

// metricsStatus is a Status as the API server sends it, in JSON.
func metricsStatus(code int, reason, message string) string {
	return `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"` + reason +
		`","code":` + strconv.Itoa(code) + `,"message":"` + message + `"}`
}

// The Hottest pods board and the Capacity tile say one thing about one
// metrics-server.
//
// The board must not choose between `absent` and `unreachable` from whether Prometheus
// was configured. With metrics-server present and no Prometheus - docker-
// desktop's shape - a 403 or a 503 would otherwise read `absent` / `not_installed`,
// "no metrics backend was found on this cluster", while on the same page the
// tile quotes the API server.
//
// Through the real path: overview.Cluster and overview.TopPods against one fake
// metrics-server answering both reads, then overviewOf.
func TestTheHottestPodsBoardAgreesWithTheCapacityTile(t *testing.T) {
	const forbidden = `metrics.k8s.io is forbidden: User \"jane\" cannot list resource \"pods\" in API group \"metrics.k8s.io\"`
	for _, c := range []struct {
		name       string
		status     int
		answer     string
		prometheus overview.Metrics
		state      api.State
		code       api.ReasonCode
		retryable  bool
		says       string
		// resource: what the failure's scope names - the read that failed.
		resource string
		// gate: the access review TopPods reads under; Allow when nil.
		gate *access.Gate
	}{
		{name: "403, no Prometheus", status: http.StatusForbidden,
			answer: metricsStatus(403, "Forbidden", forbidden),
			state:  api.StateForbidden, code: api.CodeVerbDenied,
			says: `User "jane" cannot list resource`, resource: "pods.metrics.k8s.io"},
		{name: "503, no Prometheus", status: http.StatusServiceUnavailable,
			answer: metricsStatus(503, "ServiceUnavailable", "the server is currently unable to handle the request"),
			state:  api.StateUnreachable, code: api.CodeClusterUnreachable, retryable: true,
			says: "unable to handle the request", resource: "pods.metrics.k8s.io"},
		{name: "401, no Prometheus", status: http.StatusUnauthorized,
			answer: metricsStatus(401, "Unauthorized", "Unauthorized"),
			state:  api.StateUnreadable, code: api.CodeCredentialsExpired, retryable: true,
			resource: "pods.metrics.k8s.io"},
		{
			// Prometheus asked too, and failing: still metrics-server's failure
			// that classifies the board, with both
			// backends' words in the detail.
			name: "403, and a Prometheus that did not answer", status: http.StatusForbidden,
			answer:     metricsStatus(403, "Forbidden", forbidden),
			prometheus: promSource{err: errors.New("dial tcp: connection refused")},
			state:      api.StateForbidden, code: api.CodeVerbDenied,
			says: `User "jane" cannot list resource`, resource: "pods.metrics.k8s.io",
		},
		{
			// Prometheus ranked the pods and the access review of the ranking
			// - may this person list core pods - could not be made: that
			// failure is about `pods`, and its scope says so.
			name: "a Prometheus ranking whose access review failed", status: http.StatusNotFound,
			answer:     metricsStatus(404, "NotFound", "the server could not find the requested resource"),
			prometheus: promSource{cpu: vector("search", "indexer-0", "0.5"), memory: vector("search", "indexer-0", "1073741824")},
			// The server profile and a person: a review that could not be
			// made is let through only for the console, on the desktop.
			gate: access.New(access.Options{InCluster: true, Reviews: (&accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
				if q.Attributes.Group == "" && q.Attributes.Resource == "pods" {
					return false, errors.New("the SubjectAccessReview timed out")
				}
				return true, nil
			}}).For}),
			state: api.StateUnreadable, code: api.CodeAccessUnreviewable, retryable: true, resource: "pods",
		},
		{
			// An empty answer is not a failure: the board is empty
			// with its reason.
			name: "an empty answer", status: http.StatusOK,
			answer: `{"kind":"PodMetricsList","apiVersion":"metrics.k8s.io/v1beta1","items":[]}`,
			state:  api.StateEmpty, code: api.CodeEmptyScope,
		},
		{
			// Not installed, and nothing else to ask: the one case the board
			// words as an absence.
			name: "404, no Prometheus", status: http.StatusNotFound,
			answer: metricsStatus(404, "NotFound", "the server could not find the requested resource"),
			state:  api.StateAbsent, code: api.CodeNotInstalled, retryable: true,
		},
		{
			// Not installed, and a Prometheus configured that did not answer:
			// the board's question is Prometheus's, and it did not answer.
			name: "404, and a Prometheus that did not answer", status: http.StatusNotFound,
			answer:     metricsStatus(404, "NotFound", "the server could not find the requested resource"),
			prometheus: promSource{err: errors.New("dial tcp: connection refused")},
			state:      api.StateUnreachable, code: api.CodeClusterUnreachable, retryable: true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			client := boardCluster(t, c.status, c.answer)
			machines := overviewMachines(t, client, accesstest.Allow(), nil)
			gate := c.gate
			if gate == nil {
				gate = accesstest.Allow()
			}
			ctx := context.Background()
			if c.gate != nil {
				ctx = identity.With(ctx, identity.Who{Name: "jane@example.com", Method: identity.SSO})
			}
			cpu, memory, usage, _ := overview.TopPods(ctx, gate, client, "", c.prometheus)
			board := overviewOf("docker-desktop", machines, cpu, memory, usage, c.prometheus != nil).Top

			reason := board.Reason()
			if board.State() != c.state || reason == nil || reason.Code != c.code {
				t.Fatalf("board = %q / %+v; want %q / %q", board.State(), reason, c.state, c.code)
			}
			if c.resource != "" {
				// The scope names the read that failed, so a refusal names the
				// permission that is missing: the board
				// names pods.metrics.k8s.io.
				if reason.Scope == nil || reason.Scope.Resource != c.resource || reason.Scope.Verb != "list" ||
					reason.Scope.Cluster != "docker-desktop" {
					t.Errorf("board scope = %+v; want docker-desktop, %s, list", reason.Scope, c.resource)
				}
			}
			retryable := reason.Retryable != nil && *reason.Retryable
			if retryable != c.retryable {
				t.Errorf("board retryable = %v, want %v", retryable, c.retryable)
			}
			if c.says != "" && !strings.Contains(reason.Message, c.says) {
				t.Errorf("board says %q; want the API server's own words, %q", reason.Message, c.says)
			}
			if c.prometheus != nil && c.state == api.StateForbidden &&
				(!strings.Contains(reason.Detail, "connection refused") || !strings.Contains(reason.Detail, "metrics-server")) {
				t.Errorf("detail = %q; want both backends' words", reason.Detail)
			}
			if board.Len() != 0 {
				t.Errorf("the board drew %d boards with nothing ranked", board.Len())
			}
			// And the tile, which carries a sentence rather than a state: the
			// API server's own words, the ones the board quotes.
			if c.says != "" {
				for _, label := range []string{"CPU", "Memory"} {
					used := cellNamed(t, capacityCells("docker-desktop", machines), label).Used
					if used == nil || !strings.Contains(used.Text, c.says) {
						t.Errorf("the %s tile's used says %+v; want %q, as the board does", label, used, c.says)
					}
				}
			}
		})
	}
}

// panickingREST is a raw REST client whose every request panics, which is how
// a defect in the usage read presents: gather's recover turns it into the
// read's failure rather than ending the process.
type panickingREST struct{ rest.Interface }

func (panickingREST) Get() *rest.Request { panic("a nil map in the usage read") }

// A panic in the usage read is said as one on the Capacity tile.
//
// The tile, finding no per-node answer and no error, would otherwise fall back
// to the history's sentence - with no
// Prometheus, "no metrics backend is configured", which is untrue of a cluster
// whose metrics-server was never asked.
func TestAPanicInTheUsageReadIsOneSentenceOnTheTile(t *testing.T) {
	clientset := fake.NewSimpleClientset(desktopNode(), requesting("coredns", "405m", "112Mi"))
	client := &cluster.Client{Context: "docker-desktop", Clients: withMetricsServer{Clientset: clientset, rest: panickingREST{}}}

	machines := overviewMachines(t, client, accesstest.Allow(), nil)
	if machines.NowFrom.OK || !strings.Contains(machines.NowFrom.Detail, "panicked") {
		t.Fatalf("NowFrom = %+v; this case is about the usage read panicking", machines.NowFrom)
	}
	for _, label := range []string{"CPU", "Memory"} {
		used := cellNamed(t, capacityCells("docker-desktop", machines), label).Used
		if used == nil || used.Text != machines.NowFrom.Detail {
			t.Errorf("the %s tile's used says %+v; want the read's own sentence, %q", label, used, machines.NowFrom.Detail)
		}
	}
}

// A crash is a failure the page draws, never an absence it hides. The Overview
// draws no *Most used* panel where
// the board is absent with `not_installed` - no metrics source - so a usage
// read that panicked must not arrive in that shape. Through the real path,
// with no Prometheus: overview.TopPods over a metrics-server whose every
// request panics, then overviewOf. TopPods runs on its own goroutine, so the
// panic must be confined there.
func TestAPanicInTheUsageReadIsAFailedBoardAndNotAnAbsentOne(t *testing.T) {
	clientset := fake.NewSimpleClientset(desktopNode(), requesting("coredns", "405m", "112Mi"))
	client := &cluster.Client{Context: "docker-desktop", Clients: withMetricsServer{Clientset: clientset, rest: panickingREST{}}}
	machines := overviewMachines(t, client, accesstest.Allow(), nil)

	cpu, memory, usage, _ := overview.TopPods(context.Background(), accesstest.Allow(), client, "", nil)
	board := overviewOf("docker-desktop", machines, cpu, memory, usage, false).Top

	reason := board.Reason()
	if board.State() == api.StateAbsent || board.State() == api.StateEmpty || board.State() == api.StateOK || reason == nil {
		t.Fatalf("board = %q / %+v; a panicked read is a failure", board.State(), reason)
	}
	if reason.Code == api.CodeNotInstalled {
		t.Errorf("code = %q: a crash is not a missing metrics source", reason.Code)
	}
	if !strings.Contains(reason.Message, "panicked") {
		t.Errorf("message = %q; want the read's own sentence, which names the panic", reason.Message)
	}
}
