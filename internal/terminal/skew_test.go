package terminal

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"

	"github.com/thonicdev/kubetower/internal/cluster"
)

func TestMinorVersionHandlesManagedDistributions(t *testing.T) {
	cases := map[string]struct {
		want int
		ok   bool
	}{
		"34":  {34, true},
		"31+": {31, true}, // EKS and other managed distributions append a plus
		" 24": {24, true},
		"":    {0, false},
		"abc": {0, false},
	}
	for raw, expect := range cases {
		got, ok := minorVersion(raw)
		if ok != expect.ok || got != expect.want {
			t.Errorf("minorVersion(%q) = %d, %t — expected %d, %t", raw, got, ok, expect.want, expect.ok)
		}
	}
}

func TestParseClientMinorReadsKubectlsOwnOutput(t *testing.T) {
	// The shape `kubectl version --client -o json` actually prints.
	out := []byte(`{"clientVersion":{"major":"1","minor":"34","gitVersion":"v1.34.1","platform":"linux/amd64"},"kustomizeVersion":"v5.7.1"}`)
	got, ok := parseClientMinor(out)
	if !ok || got != 34 {
		t.Errorf("parseClientMinor = %d, %t — expected 34, true", got, ok)
	}
}

// Anything unreadable means "no opinion", not a warning. A wrong version
// warning sends the operator after a version problem that is not there.
func TestParseClientMinorSaysNothingOnRubbish(t *testing.T) {
	for _, out := range [][]byte{
		nil,
		[]byte(``),
		[]byte(`not json`),
		[]byte(`{"clientVersion":{}}`),
		[]byte(`{}`),
	} {
		if _, ok := parseClientMinor(out); ok {
			t.Errorf("a client version was claimed from %q", out)
		}
	}
}

// --- a failure is not remembered, and the call can be canceled ---

// fakeDiscovery answers the version question and counts how it was asked.
//
// The embedded interface is nil on purpose: only the two version methods are
// reached from this file, and a nil call would be a test discovering that the
// production path grew a second discovery call rather than a test failing
// obscurely.
type fakeDiscovery struct {
	discovery.DiscoveryInterfaces

	mu          sync.Mutex
	plain       int // ServerVersion(), which takes no context
	contextual  int // ServerVersionWithContext(), which does
	failFirst   int
	serverMinor string
}

func (f *fakeDiscovery) answer() (*version.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failFirst > 0 {
		f.failFirst--
		return nil, errors.New("the API server did not answer")
	}
	return &version.Info{
		Major:      "1",
		Minor:      f.serverMinor,
		GitVersion: "v1." + f.serverMinor + ".0",
	}, nil
}

func (f *fakeDiscovery) ServerVersion() (*version.Info, error) {
	f.mu.Lock()
	f.plain++
	f.mu.Unlock()
	return f.answer()
}

func (f *fakeDiscovery) ServerVersionWithContext(context.Context) (*version.Info, error) {
	f.mu.Lock()
	f.contextual++
	f.mu.Unlock()
	return f.answer()
}

func (f *fakeDiscovery) counts() (plain, contextual int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.plain, f.contextual
}

type fakeClientset struct {
	kubernetes.Interface
	discovery discovery.DiscoveryInterfaces
}

func (f fakeClientset) Discovery() discovery.DiscoveryInterfaces { return f.discovery }

// fakeManager is the seam for "the first attempt fails and the second
// succeeds", which a concrete *cluster.Manager could not express.
type fakeManager struct {
	mu        sync.Mutex
	calls     int
	failFirst int
	client    *cluster.Client
}

func (m *fakeManager) Client(string) (*cluster.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.failFirst > 0 {
		m.failFirst--
		return nil, errors.New("no client for that context")
	}
	return m.client, nil
}

// skewFor builds the cache with kubectl's half already answered, so no test in
// this file execs anything. The Once is consumed rather than the fields simply
// written: leaving it unfired would run kubectl on the first miss.
func skewFor(t *testing.T, clusters clusterClients, clientMinor int) *skewCache {
	t.Helper()
	s := newSkewCache(clusters, cluster.Kubeconfig{})
	s.clientOnce.Do(func() {})
	s.clientMinor, s.clientKnown = clientMinor, true
	return s
}

func clusterWith(d discovery.DiscoveryInterfaces) *cluster.Client {
	return &cluster.Client{Context: "prod", Clients: fakeClientset{discovery: d}}
}

// A cluster that could not be reached when its first session opened has its
// skew computed again later.
//
// Caching that failure as "" would make the key `known` for the life of the
// process, and an operator on a two-minor-version-old kubectl would get no
// warning for the rest of the run - because of one unlucky moment at the first
// terminal. An empty answer and a failed one must not be the same entry.
func TestAClusterThatFailedOnceIsAskedAgain(t *testing.T) {
	d := &fakeDiscovery{serverMinor: "28"}
	clusters := &fakeManager{failFirst: 1, client: clusterWith(d)}
	s := skewFor(t, clusters, 34)

	if got := s.warning("prod"); got != "" {
		t.Fatalf("a cluster whose client could not be built produced %q; the cluster "+
			"is about to say so on its own", got)
	}

	s.mu.RLock()
	_, cached := s.seen["prod"]
	s.mu.RUnlock()
	if cached {
		t.Error("the failure was written into the cache; it is the absence of an entry " +
			"that lets the next session ask again")
	}

	got := s.warning("prod")
	if !strings.Contains(got, "kubectl is 1.34") || !strings.Contains(got, "v1.28.0") {
		t.Fatalf("the second attempt said %q; a six-minor-version gap is exactly what "+
			"this line exists to announce", got)
	}
}

// The same, one layer down: the client built and the API server did not answer.
func TestAnApiServerThatDidNotAnswerIsAskedAgain(t *testing.T) {
	d := &fakeDiscovery{serverMinor: "28", failFirst: 1}
	clusters := &fakeManager{client: clusterWith(d)}
	s := skewFor(t, clusters, 34)

	if got := s.warning("prod"); got != "" {
		t.Fatalf("an unreachable API server produced %q", got)
	}
	if got := s.warning("prod"); !strings.Contains(got, "kubectl is 1.34") {
		t.Fatalf("the second attempt said %q rather than the skew line", got)
	}
}

// And the cache is still a cache: the reason it exists is that the check costs
// an API round trip, and on an EKS cluster that means a token fetch. Un-caching
// a success would trade one defect for a per-terminal cost.
func TestASuccessIsComputedOnce(t *testing.T) {
	d := &fakeDiscovery{serverMinor: "28"}
	clusters := &fakeManager{client: clusterWith(d)}
	s := skewFor(t, clusters, 34)

	first := s.warning("prod")
	second := s.warning("prod")
	if first == "" || first != second {
		t.Fatalf("the two answers are %q and %q", first, second)
	}
	if _, contextual := d.counts(); contextual != 1 {
		t.Errorf("the API server was asked %d times for an answer that only changes "+
			"when a cluster is upgraded", contextual)
	}
}

// A settled empty answer is cached too, and that is the case that is easy to
// lose while fixing the one above: "the versions are fine" is the ordinary
// outcome, and re-asking it every session is the cost the cache was built to
// avoid.
func TestVersionsThatAreFineAreRememberedAsWell(t *testing.T) {
	d := &fakeDiscovery{serverMinor: "34"}
	clusters := &fakeManager{client: clusterWith(d)}
	s := skewFor(t, clusters, 34)

	if got := s.warning("prod"); got != "" {
		t.Fatalf("a cluster one minor version away produced %q", got)
	}
	if got := s.warning("prod"); got != "" {
		t.Fatalf("second call produced %q", got)
	}
	if _, contextual := d.counts(); contextual != 1 {
		t.Errorf("the API server was asked %d times to repeat that nothing is wrong", contextual)
	}
}

// The discovery call takes a context: a bare one would hold the goroutine for
// the 30 s of rest.Config.Timeout on a cluster behind a black-holed route.
//
// Asserted by counting which method was reached rather than by timing anything:
// a timing test here would pass against a client that ignored the deadline and
// happened to answer.
func TestTheSkewCheckPassesAContextToDiscovery(t *testing.T) {
	d := &fakeDiscovery{serverMinor: "28"}
	clusters := &fakeManager{client: clusterWith(d)}
	s := skewFor(t, clusters, 34)

	s.warning("prod")

	plain, contextual := d.counts()
	if contextual != 1 {
		t.Errorf("ServerVersionWithContext was called %d times; without it the deadline "+
			"bounds this caller and nothing else", contextual)
	}
	if plain != 0 {
		t.Errorf("ServerVersion() - the uncancelable one - was called %d times", plain)
	}
}

// And the fallback is still there, for a discovery implementation that is not
// client-go's. It probes uncancelably rather than refusing: losing the context
// costs socket lifetime, and refusing would cost the answer.
func TestADiscoveryWithoutTheContextualMethodStillAnswers(t *testing.T) {
	info, err := serverVersion(context.Background(), plainOnlyDiscovery{})
	if err != nil {
		t.Fatalf("the fallback refused instead of probing: %v", err)
	}
	if info.GitVersion != "v1.28.0" {
		t.Errorf("GitVersion = %q", info.GitVersion)
	}
}

type plainOnlyDiscovery struct{ discovery.DiscoveryInterface }

func (plainOnlyDiscovery) ServerVersion() (*version.Info, error) {
	return &version.Info{Major: "1", Minor: "28", GitVersion: "v1.28.0"}, nil
}
