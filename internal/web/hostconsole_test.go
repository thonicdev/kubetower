package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/metrics"
)

// Host.ForwardAsConsole is Host.Forward dug as the console: reviewed as the
// console's own `create pods/portforward`, recorded as the console's act on
// the person's behalf, refused for a request with nobody on it, counted with
// Forward's tunnels against the person's share, and closed when its request
// ends.

// reviewedTunnels digs as terminal.Forwarder does - inside Gate.Perform, as
// `create pods/portforward` on the pod it lands on - and opens nothing real,
// so what is asserted is whose review the context the host hands it makes.
type reviewedTunnels struct {
	gate *access.Gate

	mu  sync.Mutex
	dug int
}

func (f *reviewedTunnels) Open(ctx context.Context, cluster, namespace, service string, _ int) (metrics.Tunnel, error) {
	var tunnel *fakeTunnel
	_, err := f.gate.Perform(ctx, cluster, access.Attributes{Verb: "create", Version: "v1", Resource: "pods",
		Subresource: "portforward", Namespace: namespace, Name: service + "-0"}, func(context.Context) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.dug++
		tunnel = &fakeTunnel{addr: fmt.Sprintf("127.0.0.1:%d", 41000+f.dug), done: make(chan struct{})}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tunnel, nil
}

func (f *reviewedTunnels) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dug
}

// consoleTunnelServer is a console under the server profile whose tunneller
// reviews through its gate, whose reviews answer with answer, and whose audit
// log is sink.
func consoleTunnelServer(t *testing.T, answer func(accesstest.Question) (bool, error)) (*Server, *reviewedTunnels, *accesstest.Reviews, *revealSink) {
	t.Helper()
	reviews := &accesstest.Reviews{Answer: answer}
	sink := &revealSink{}
	gate := access.New(access.Options{Reviews: reviews.For, InCluster: true, Audit: audit.New(sink)})
	dig := &reviewedTunnels{gate: gate}
	s, err := New(Options{Clusters: navFixture(t, "prod").clusters, Tunnels: dig, InCluster: true, Access: gate})
	if err != nil {
		t.Fatal(err)
	}
	return s, dig, reviews, sink
}

// noPortForwardForPeople is a cluster where no person may port-forward, and
// the console's ServiceAccount may: what the chart grants it in Argo CD's and
// the collector's namespaces.
func noPortForwardForPeople(q accesstest.Question) (bool, error) {
	return q.Self || q.Attributes.Subresource != "portforward", nil
}

// requestAs is a request carrying ctx, as the session guard hands it on.
func requestAs(ctx context.Context) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/x/prod/argocd/", nil).WithContext(ctx)
}

func portForwardQuestions(asked []accesstest.Question) []accesstest.Question {
	var out []accesstest.Question
	for _, q := range asked {
		if q.Attributes.Subresource == "portforward" {
			out = append(out, q)
		}
	}
	return out
}

// A person who may not port-forward anywhere still gets the console's tunnel:
// the review is the console's own, never theirs, and the audit names the
// console with the person as on whose behalf. The same person's Forward is
// refused, which is the difference the door exists for.
func TestAConsoleTunnelIsTheConsolesReviewRecordedForThePerson(t *testing.T) {
	s, dig, reviews, sink := consoleTunnelServer(t, noPortForwardForPeople)
	h := s.hostFor("argocd")

	tunnel, err := h.ForwardAsConsole(requestAs(grace()), "prod", "argocd", "argocd-server", 443)
	if err != nil {
		t.Fatalf("the console's tunnel for a person with no port-forward was refused: %v", err)
	}
	defer tunnel.Stop()
	if tunnel.Addr == "" || dig.count() != 1 {
		t.Fatalf("the console's tunnel answered %+v with %d dug, want one tunnel", tunnel, dig.count())
	}
	asked := portForwardQuestions(reviews.Asked())
	if len(asked) != 1 || !asked[0].Self || asked[0].User != "" {
		t.Fatalf("the console's tunnel was reviewed as %+v, want one review of the console's own rights", asked)
	}

	records := sink.records(t)
	if len(records) != 2 || records[0].Phase != audit.Attempt || records[1].Phase != audit.Outcome {
		t.Fatalf("the console's tunnel wrote %+v, want an attempt and its outcome", records)
	}
	for _, r := range records {
		if r.Act != audit.PortForward || r.Via != audit.Console || r.Who != "" ||
			r.OnBehalfOf != "grace@example.com" || r.Namespace != "argocd" || r.Name != "argocd-server-0" {
			t.Errorf("the %s record reads %+v, want the console's port-forward on grace's behalf", r.Phase, r)
		}
	}

	// The person's own door, for the same backend, is reviewed as them - and
	// refused here.
	if _, err := h.Forward(grace(), "prod", "argocd", "argocd-server", 443); err == nil {
		t.Fatal("grace's own tunnel was opened although she may not port-forward")
	}
	asked = portForwardQuestions(reviews.Asked())
	if len(asked) != 2 || asked[1].Self || asked[1].User != "grace@example.com" {
		t.Errorf("Forward was reviewed as %+v, want grace's own review", asked)
	}
	if dig.count() != 1 {
		t.Errorf("%d tunnels dug, want only the console's", dig.count())
	}
}

// A review the console's own ServiceAccount is refused - a chart that grants
// no port-forward there - opens nothing, and the refusal is recorded.
func TestAConsoleTunnelTheConsoleMayNotDigIsNotDug(t *testing.T) {
	s, dig, _, sink := consoleTunnelServer(t, func(q accesstest.Question) (bool, error) {
		return !(q.Self && q.Attributes.Subresource == "portforward"), nil
	})
	_, err := s.hostFor("argocd").ForwardAsConsole(requestAs(grace()), "prod", "argocd", "argocd-server", 443)
	var refused *access.Refused
	if !errors.As(err, &refused) {
		t.Fatalf("a tunnel the console may not dig answered %v, want the refusal", err)
	}
	if dig.count() != 0 {
		t.Errorf("a refused tunnel was dug: %d", dig.count())
	}
	records := sink.records(t)
	if len(records) == 0 || records[0].Decision != string(access.Denied) || records[0].OnBehalfOf != "grace@example.com" {
		t.Errorf("the refusal was recorded as %+v, want a denial on grace's behalf", records)
	}
}

// Under the server profile a request with nobody signed in is refused before
// anything is asked: as the console, it would be reviewed as the console and
// recorded as nobody's.
func TestAConsoleTunnelForNobodyIsRefused(t *testing.T) {
	s, dig, reviews, sink := consoleTunnelServer(t, nil)
	_, err := s.hostFor("argocd").ForwardAsConsole(requestAs(context.Background()), "prod", "argocd", "argocd-server", 443)
	if !errors.Is(err, errNobodyForTheConsole) {
		t.Fatalf("a console tunnel for nobody answered %v, want it refused", err)
	}
	if dig.count() != 0 || len(reviews.Asked()) != 0 || len(sink.records(t)) != 0 {
		t.Errorf("a console tunnel for nobody dug %d, asked %v, recorded %d", dig.count(), reviews.Asked(), len(sink.records(t)))
	}
}

// The console's tunnel ends with the request it was dug for, even when the
// page forgets to stop it, and its slot comes back; a Stop after that, or a
// second one, gives nothing back twice.
func TestAConsoleTunnelEndsWithItsRequest(t *testing.T) {
	h, counted, dig := tunnelServer(t, true)
	ctx, cancel := context.WithCancel(grace())
	tunnel, err := h.ForwardAsConsole(requestAs(ctx), "prod", "argocd", "argocd-server", 443)
	if err != nil {
		t.Fatal(err)
	}
	if counted.live() != 1 {
		t.Fatalf("%d tunnels counted, want the console's", counted.live())
	}
	cancel()
	select {
	case <-dig.last.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the console's tunnel outlived its request")
	}
	deadline := time.Now().Add(5 * time.Second)
	for counted.live() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if counted.live() != 0 {
		t.Fatalf("%d tunnels counted after the request ended, want none", counted.live())
	}
	tunnel.Stop()
	tunnel.Stop()
	if counted.live() != 0 {
		t.Errorf("%d tunnels counted after Stop, want none", counted.live())
	}

	// Stopped first, the request ending later closes nothing twice.
	ctx, cancel = context.WithCancel(grace())
	tunnel, err = h.ForwardAsConsole(requestAs(ctx), "prod", "argocd", "argocd-server", 443)
	if err != nil {
		t.Fatal(err)
	}
	tunnel.Stop()
	cancel()
	if counted.live() != 0 || counted.heldBy(identity.Who{Name: "grace@example.com", Method: identity.SSO}) != 0 {
		t.Errorf("a stopped console tunnel is still counted: %d", counted.live())
	}
}

// The console's tunnels come out of the same ceiling and the same per-person
// share as the person's own: a person cannot hold more by asking for the
// console's door, and another person still gets theirs.
func TestConsoleTunnelsCountAgainstThePersonsShare(t *testing.T) {
	h, counted, dig := tunnelServer(t, true)
	alice := identity.Who{Name: "alice", Method: identity.SSO}
	asAlice := identity.With(context.Background(), alice)
	asBob := identity.With(context.Background(), identity.Who{Name: "bob", Method: identity.SSO})

	for i := 0; i < perPersonExtensionTunnels; i++ {
		var err error
		if i%2 == 0 {
			_, err = h.ForwardAsConsole(requestAs(asAlice), "prod", "kubecron", "kubecron", 8080)
		} else {
			_, err = h.Forward(asAlice, "prod", "cache", "redis", 6379)
		}
		if err != nil {
			t.Fatalf("alice's tunnel %d of her %d refused: %v", i+1, perPersonExtensionTunnels, err)
		}
	}
	if got := counted.heldBy(alice); got != perPersonExtensionTunnels {
		t.Fatalf("alice holds %d against her share, want %d: the console's tunnels are hers too", got, perPersonExtensionTunnels)
	}
	_, err := h.ForwardAsConsole(requestAs(asAlice), "prod", "kubecron", "kubecron", 8080)
	if err == nil || !strings.Contains(err.Error(), "one person's share") {
		t.Fatalf("alice at her share was given a console tunnel: %v", err)
	}
	if dig.dug != perPersonExtensionTunnels {
		t.Errorf("the refused tunnel was dug anyway: %d dug", dig.dug)
	}
	if _, err := h.ForwardAsConsole(requestAs(asBob), "prod", "kubecron", "kubecron", 8080); err != nil {
		t.Errorf("alice at her share refused bob's console tunnel: %v", err)
	}
}
