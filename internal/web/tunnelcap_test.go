package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/metrics"
)

// fakeTunnels digs tunnels that do nothing, and can stop one as if its pod
// had gone.
type fakeTunnels struct {
	mu   sync.Mutex
	dug  int
	last *fakeTunnel
	pods []string
	// fail, when set, is what every Open answers: a tunnel the cluster refused.
	fail error
}

func (f *fakeTunnels) Open(context.Context, string, string, string, int) (metrics.Tunnel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	f.dug++
	f.last = &fakeTunnel{addr: fmt.Sprintf("127.0.0.1:%d", 40000+f.dug), done: make(chan struct{})}
	return f.last, nil
}

// OpenPod is Open to a pod's port, recorded as the pod it was asked for.
func (f *fakeTunnels) OpenPod(ctx context.Context, cluster, namespace, pod string, port int) (metrics.Tunnel, error) {
	f.mu.Lock()
	f.pods = append(f.pods, fmt.Sprintf("%s/%s/%s:%d", cluster, namespace, pod, port))
	f.mu.Unlock()
	return f.Open(ctx, cluster, namespace, pod, port)
}

type fakeTunnel struct {
	addr string
	done chan struct{}
	once sync.Once
}

func (t *fakeTunnel) Addr() string          { return t.addr }
func (t *fakeTunnel) Close()                { t.once.Do(func() { close(t.done) }) }
func (t *fakeTunnel) Done() <-chan struct{} { return t.done }

// TestTheTunnelsAnExtensionOpensAreCounted: Host.Forward has a cap,
// a counter and a registry, one shared process-wide. N+1 tunnels through it: the last is refused with the limit
// in the sentence, before anything is dug; closing one frees a slot, and so
// does a tunnel that stops on its own.
func TestTheTunnelsAnExtensionOpensAreCounted(t *testing.T) {
	const limit = 3
	dig := &fakeTunnels{}
	counted := countTunnels(dig, limit).(*countedTunnels)
	h := host{s: &Server{tunnels: counted}}
	ctx := context.Background()

	var stops []func()
	for i := 0; i < limit; i++ {
		tunnel, err := h.Forward(ctx, "prod", "cache", "redis", 6379)
		if err != nil || tunnel.Addr == "" {
			t.Fatalf("tunnel %d of %d refused: %v", i+1, limit, err)
		}
		stops = append(stops, tunnel.Stop)
	}
	_, err := h.Forward(ctx, "prod", "cache", "redis", 6379)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%d tunnels", limit)) {
		t.Fatalf("tunnel %d was not refused with the limit in the sentence: %v", limit+1, err)
	}
	if dig.dug != limit {
		t.Errorf("the refused tunnel was dug anyway: %d dug, want %d", dig.dug, limit)
	}

	stops[0]()
	stops[0]() // a second stop gives nothing back twice
	if got := counted.live(); got != limit-1 {
		t.Fatalf("%d tunnels counted after one was closed twice, want %d", got, limit-1)
	}
	if _, err := h.Forward(ctx, "prod", "cache", "redis", 6379); err != nil {
		t.Fatalf("closing a tunnel freed no slot: %v", err)
	}

	// One stops on its own - its pod went away - and the extension has not
	// called its stop yet.
	dig.mu.Lock()
	gone := dig.last
	dig.mu.Unlock()
	gone.Close()
	deadline := time.Now().Add(2 * time.Second)
	for counted.live() != limit-1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := counted.live(); got != limit-1 {
		t.Errorf("a tunnel that stopped on its own still holds its slot: %d counted, want %d", got, limit-1)
	}
}

// The ceiling is in front of the tunneller web.New is handed, and a build with
// none still says so rather than counting nothing.
func TestTheCeilingIsWhatHostForwardOpensThrough(t *testing.T) {
	if countTunnels(nil, maxExtensionTunnels) != nil {
		t.Error("no tunneller became a counted one, so Host.Forward would no longer say the build has none")
	}
	s := navFixture(t, "prod")
	built, err := New(Options{Clusters: s.clusters, Tunnels: &fakeTunnels{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := built.tunnels.(*countedTunnels); !ok {
		t.Errorf("web.New hands Host.Forward an uncounted tunneller: %T", built.tunnels)
	}
}

// tunnelServer is web.New as internal/launch calls it, on one profile, with
// tunnels that do nothing: the ceiling and the share are the ones a console
// started that way puts in front of Host.Forward.
func tunnelServer(t *testing.T, inCluster bool) (extension.Host, *countedTunnels, *fakeTunnels) {
	t.Helper()
	dig := &fakeTunnels{}
	built, err := New(Options{Clusters: navFixture(t, "prod").clusters, Tunnels: dig, InCluster: inCluster})
	if err != nil {
		t.Fatal(err)
	}
	counted, ok := built.tunnels.(*countedTunnels)
	if !ok {
		t.Fatalf("web.New hands Host.Forward an uncounted tunneller: %T", built.tunnels)
	}
	return built.hostFor("tunnelshare"), counted, dig
}

// TestOnePersonCannotHoldEveryExtensionTunnel covers the per-person share.
// The sixteen are counted per process, so under --incluster one
// person - or one extra their page drove in a loop - could otherwise hold every slot and
// refuse every other person's Redis, Argo CD diff and KubeCron page. Instead A at
// their share is refused with the share in the sentence, before anything is
// dug; B can still open one; closing one of A's frees A's slot.
func TestOnePersonCannotHoldEveryExtensionTunnel(t *testing.T) {
	h, counted, dig := tunnelServer(t, true)
	alice := identity.Who{Name: "alice", Method: identity.SSO}
	bob := identity.Who{Name: "bob", Method: identity.SSO}
	asAlice := identity.With(context.Background(), alice)
	asBob := identity.With(context.Background(), bob)

	var stops []func()
	for i := 0; i < perPersonExtensionTunnels; i++ {
		tunnel, err := h.Forward(asAlice, "prod", "cache", "redis", 6379)
		if err != nil {
			t.Fatalf("alice's tunnel %d of her %d refused: %v", i+1, perPersonExtensionTunnels, err)
		}
		stops = append(stops, tunnel.Stop)
	}
	_, err := h.Forward(asAlice, "prod", "cache", "redis", 6379)
	if err == nil {
		t.Fatalf("alice held %d tunnels and was given one more; her share is %d", perPersonExtensionTunnels, perPersonExtensionTunnels)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d tunnels", perPersonExtensionTunnels)) ||
		!strings.Contains(err.Error(), "one person's share") {
		t.Errorf("alice's refusal does not name her share: %v", err)
	}
	if dig.dug != perPersonExtensionTunnels {
		t.Errorf("the refused tunnel was dug anyway: %d dug, want %d", dig.dug, perPersonExtensionTunnels)
	}

	bobs, err := h.Forward(asBob, "prod", "cache", "redis", 6379)
	if err != nil {
		t.Fatalf("alice at her share refused bob's tunnel: %v", err)
	}
	if got := counted.heldBy(bob); got != 1 {
		t.Errorf("bob holds %d against his share, want 1", got)
	}

	stops[0]()
	stops[0]() // a second stop gives nothing back twice
	if got := counted.heldBy(alice); got != perPersonExtensionTunnels-1 {
		t.Fatalf("alice holds %d after closing one twice, want %d", got, perPersonExtensionTunnels-1)
	}
	if _, err := h.Forward(asAlice, "prod", "cache", "redis", 6379); err != nil {
		t.Fatalf("closing one of alice's tunnels freed no slot for her: %v", err)
	}

	bobs.Stop()
	if got, want := counted.live(), perPersonExtensionTunnels; got != want {
		t.Errorf("%d tunnels counted, want %d (alice's share, bob's closed)", got, want)
	}
}

// The share is the server profile's only. On the desktop there is one person,
// and the ceiling they have is the sixteen.
func TestOnTheDesktopOnePersonStillHasEveryExtensionTunnel(t *testing.T) {
	h, _, dig := tunnelServer(t, false)
	asAlice := identity.With(context.Background(), identity.Who{Name: "alice", Method: identity.SSO})
	for i := 0; i < maxExtensionTunnels; i++ {
		if _, err := h.Forward(asAlice, "prod", "cache", "redis", 6379); err != nil {
			t.Fatalf("tunnel %d of %d refused on the desktop: %v", i+1, maxExtensionTunnels, err)
		}
	}
	_, err := h.Forward(asAlice, "prod", "cache", "redis", 6379)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%d tunnels", maxExtensionTunnels)) ||
		strings.Contains(err.Error(), "share") {
		t.Fatalf("tunnel %d on the desktop was not refused with the ceiling: %v", maxExtensionTunnels+1, err)
	}
	if dig.dug != maxExtensionTunnels {
		t.Errorf("%d dug, want %d", dig.dug, maxExtensionTunnels)
	}
}

// The shared password account is everybody who holds the password, so it has
// no share - as the shells' and the streams' - and its tunnels count against
// the sixteen alone.
func TestTheSharedAccountHasNoTunnelShare(t *testing.T) {
	h, counted, _ := tunnelServer(t, true)
	admin := identity.Who{Name: "admin", Method: identity.Password}
	asAdmin := identity.With(context.Background(), admin)
	for i := 0; i < perPersonExtensionTunnels+1; i++ {
		if _, err := h.Forward(asAdmin, "prod", "cache", "redis", 6379); err != nil {
			t.Fatalf("the shared account's tunnel %d refused: %v", i+1, err)
		}
	}
	if got := counted.heldBy(admin); got != 0 {
		t.Errorf("the shared account holds %d against a share it does not have", got)
	}
}

// The cap must not be asked before the name is: with every slot
// taken an unknown cluster would answer "close a page and try again" rather than
// extension.ErrUnknownCluster. The name is resolved first, and nothing is dug.
func TestAnUnknownClusterIsUnknownEvenWithEveryTunnelTaken(t *testing.T) {
	h, _, dig := tunnelServer(t, true)
	asAlice := identity.With(context.Background(), identity.Who{Name: "alice", Method: identity.SSO})
	for i := 0; i < perPersonExtensionTunnels; i++ {
		if _, err := h.Forward(asAlice, "prod", "cache", "redis", 6379); err != nil {
			t.Fatal(err)
		}
	}
	_, err := h.Forward(asAlice, "gone", "cache", "redis", 6379)
	if !errors.Is(err, extension.ErrUnknownCluster) {
		t.Fatalf("an unknown cluster with alice at her share answered %v, want ErrUnknownCluster", err)
	}
	if dig.dug != perPersonExtensionTunnels {
		t.Errorf("a tunnel was dug for an unknown cluster: %d dug", dig.dug)
	}
}

// A tunnel that did not open gives its slot back at once, Forward's and
// ForwardPod's alike. Kept, four refused tunnels would use up a person's share
// and the next one that would open is refused for it.
func TestATunnelThatDidNotOpenHoldsNoSlot(t *testing.T) {
	h, counted, dig := tunnelServer(t, true)
	pods := h.(extension.PodForwarder)
	alice := identity.Who{Name: "alice", Method: identity.SSO}
	asAlice := identity.With(context.Background(), alice)
	refused := errors.New("the API server refused the upgrade")
	dig.mu.Lock()
	dig.fail = refused
	dig.mu.Unlock()

	for i := 0; i <= perPersonExtensionTunnels; i++ {
		if _, err := h.Forward(asAlice, "prod", "cache", "redis", 6379); err == nil || !strings.Contains(err.Error(), refused.Error()) {
			t.Fatalf("failed tunnel %d answered %v, want the cluster's refusal", i+1, err)
		}
		if _, err := pods.ForwardPod(podRequest(asAlice), "prod", "shop", "api-0", 6060); err == nil || !strings.Contains(err.Error(), refused.Error()) {
			t.Fatalf("failed pod tunnel %d answered %v, want the cluster's refusal", i+1, err)
		}
	}
	if counted.live() != 0 || counted.heldBy(alice) != 0 {
		t.Fatalf("%d slots held, %d of them alice's, after tunnels that never opened", counted.live(), counted.heldBy(alice))
	}

	dig.mu.Lock()
	dig.fail = nil
	dig.mu.Unlock()
	tunnel, err := pods.ForwardPod(podRequest(asAlice), "prod", "shop", "api-0", 6060)
	if err != nil {
		t.Fatalf("alice's first tunnel that opens was refused: %v", err)
	}
	tunnel.Stop()
}
