package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/metrics"
)

// Host.ForwardPod: a tunnel to one port of one pod, reviewed for the person as
// `create pods/portforward` on that pod and recorded, counted with Forward's
// tunnels, and closed when its request ends.

// reviewedPodTunnels digs as terminal.Forwarder does for a pod: inside
// Gate.Perform, as `create pods/portforward` on the pod named.
type reviewedPodTunnels struct {
	reviewedTunnels
}

func (f *reviewedPodTunnels) OpenPod(ctx context.Context, cluster, namespace, pod string, _ int) (metrics.Tunnel, error) {
	var tunnel *fakeTunnel
	_, err := f.gate.Perform(ctx, cluster, access.Attributes{Verb: "create", Version: "v1", Resource: "pods",
		Subresource: "portforward", Namespace: namespace, Name: pod}, func(context.Context) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.dug++
		tunnel = &fakeTunnel{addr: fmt.Sprintf("127.0.0.1:%d", 42000+f.dug), done: make(chan struct{})}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tunnel, nil
}

func podTunnelServer(t *testing.T, answer func(accesstest.Question) (bool, error)) (extension.PodForwarder, *reviewedPodTunnels, *accesstest.Reviews, *revealSink) {
	t.Helper()
	reviews := &accesstest.Reviews{Answer: answer}
	sink := &revealSink{}
	gate := access.New(access.Options{Reviews: reviews.For, InCluster: true, Audit: audit.New(sink)})
	dig := &reviewedPodTunnels{reviewedTunnels{gate: gate}}
	s, err := New(Options{Clusters: navFixture(t, "prod").clusters, Tunnels: dig, InCluster: true, Access: gate})
	if err != nil {
		t.Fatal(err)
	}
	pods, ok := s.hostFor("runtime").(extension.PodForwarder)
	if !ok {
		t.Fatal("the Host handed to an extension opens no tunnel to a pod")
	}
	return pods, dig, reviews, sink
}

func podRequest(ctx context.Context) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/x/prod/runtime/pod/shop/api-0/app/goroutines", nil).WithContext(ctx)
}

// The tunnel is the person's: reviewed as them on the pod they named, and
// recorded with the pod. It names that pod.
func TestAPodTunnelIsReviewedAndRecordedForThePerson(t *testing.T) {
	h, dig, reviews, sink := podTunnelServer(t, func(accesstest.Question) (bool, error) { return true, nil })
	tunnel, err := h.ForwardPod(podRequest(grace()), "prod", "shop", "api-0", 6060)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Stop()
	if tunnel.Addr == "" || tunnel.Pod != "api-0" || !tunnel.PodKnown || dig.count() != 1 {
		t.Fatalf("tunnel = %+v, %d dug", tunnel, dig.count())
	}
	asked := portForwardQuestions(reviews.Asked())
	if len(asked) != 1 || asked[0].Self || asked[0].User != "grace@example.com" || asked[0].Attributes.Name != "api-0" ||
		asked[0].Attributes.Namespace != "shop" {
		t.Fatalf("reviewed as %+v, want grace on shop/api-0", asked)
	}
	records := sink.records(t)
	if len(records) != 2 {
		t.Fatalf("records = %+v, want an attempt and its outcome", records)
	}
	for _, r := range records {
		if r.Act != audit.PortForward || r.Who != "grace@example.com" || r.Namespace != "shop" || r.Name != "api-0" {
			t.Errorf("the %s record reads %+v", r.Phase, r)
		}
	}
}

// A person who may not port-forward gets no tunnel, nothing is dug, and the
// refusal is the gate's, classifiable.
func TestAPodTunnelThePersonMayNotOpenIsNotDug(t *testing.T) {
	h, dig, _, sink := podTunnelServer(t, func(q accesstest.Question) (bool, error) {
		return q.Attributes.Subresource != "portforward", nil
	})
	_, err := h.ForwardPod(podRequest(grace()), "prod", "shop", "api-0", 6060)
	var refused *access.Refused
	if !errors.As(err, &refused) {
		t.Fatalf("answered %v, want the refusal", err)
	}
	if dig.count() != 0 {
		t.Errorf("%d dug", dig.count())
	}
	if records := sink.records(t); len(records) == 0 || records[0].Decision != string(access.Denied) {
		t.Errorf("the refusal was recorded as %+v", records)
	}
}

// What is not a pod's port, a cluster that is not there, and a name that is
// not a name are refused before anything is asked.
func TestAPodTunnelRefusesWhatIsNotATarget(t *testing.T) {
	h, dig, reviews, _ := podTunnelServer(t, nil)
	for _, tc := range []struct {
		cluster, namespace, pod string
		port                    int
		want                    string
	}{
		{"prod", "shop", "api-0", 0, "between 1 and 65535"},
		{"prod", "shop", "api-0", 70000, "between 1 and 65535"},
		{"prod", "Shop!", "api-0", 6060, "not a namespace name"},
		{"prod", "shop", "api/0", 6060, "not a pod name"},
	} {
		_, err := h.ForwardPod(podRequest(grace()), tc.cluster, tc.namespace, tc.pod, tc.port)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v answered %v", tc, err)
		}
	}
	_, err := h.ForwardPod(podRequest(grace()), "staging", "shop", "api-0", 6060)
	if !errors.Is(err, extension.ErrUnknownCluster) {
		t.Errorf("an unknown cluster answered %v", err)
	}
	if dig.count() != 0 || len(reviews.Asked()) != 0 {
		t.Errorf("dug %d, asked %v", dig.count(), reviews.Asked())
	}
}

// Pod tunnels share Forward's ceiling and the person's share, and end with
// their request.
func TestAPodTunnelIsCountedAndEndsWithItsRequest(t *testing.T) {
	h, counted, dig := tunnelServer(t, true)
	pods := h.(extension.PodForwarder)
	alice := identity.Who{Name: "alice", Method: identity.SSO}
	asAlice := identity.With(context.Background(), alice)

	ctx, cancel := context.WithCancel(asAlice)
	tunnel, err := pods.ForwardPod(podRequest(ctx), "prod", "shop", "api-0", 6060)
	if err != nil {
		t.Fatal(err)
	}
	if counted.heldBy(alice) != 1 || len(dig.pods) != 1 || dig.pods[0] != "prod/shop/api-0:6060" {
		t.Fatalf("held %d, dug %v", counted.heldBy(alice), dig.pods)
	}
	cancel()
	select {
	case <-dig.last.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the pod tunnel outlived its request")
	}
	deadline := time.Now().Add(5 * time.Second)
	for counted.live() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	tunnel.Stop()
	if counted.live() != 0 {
		t.Fatalf("%d counted after the request ended", counted.live())
	}

	for i := 0; i < perPersonExtensionTunnels; i++ {
		if i%2 == 0 {
			_, err = pods.ForwardPod(podRequest(asAlice), "prod", "shop", "api-0", 6060)
		} else {
			_, err = h.Forward(asAlice, "prod", "cache", "redis", 6379)
		}
		if err != nil {
			t.Fatalf("alice's tunnel %d refused: %v", i+1, err)
		}
	}
	_, err = pods.ForwardPod(podRequest(asAlice), "prod", "shop", "api-0", 6060)
	if err == nil || !strings.Contains(err.Error(), "one person's share") {
		t.Fatalf("alice at her share was given a pod tunnel: %v", err)
	}
}

// A tunneller that opens Services only costs no slot and says so.
func TestAPodTunnelWithoutAPodTunnellerSaysSo(t *testing.T) {
	built, err := New(Options{Clusters: navFixture(t, "prod").clusters, Tunnels: servicesOnly{}, InCluster: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = built.hostFor("runtime").(extension.PodForwarder).ForwardPod(podRequest(grace()), "prod", "shop", "api-0", 6060)
	if !errors.Is(err, errNoPodTunnels) {
		t.Fatalf("answered %v", err)
	}
	if built.tunnels.(*countedTunnels).live() != 0 {
		t.Error("a refused pod tunnel holds a slot")
	}
}

type servicesOnly struct{}

func (servicesOnly) Open(context.Context, string, string, string, int) (metrics.Tunnel, error) {
	return nil, errors.New("not dug in this test")
}
