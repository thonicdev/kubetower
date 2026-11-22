package terminal

// Warning the operator when kubectl is too far from the API server it is about
// to talk to.
//
// Kubernetes supports a skew of one minor version either way. Beyond that,
// basic verbs work while anything touching an API that moved in between fails in
// ways that look like a cluster bug. The server half comes from client-go's
// discovery client, already authenticated; the client half needs kubectl itself,
// the binary the operator is about to type at.

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// clusterClients is the one thing this cache needs of *cluster.Manager, an
// interface so a test can supply one whose first call fails and second
// succeeds: the case a failure must not be cached for.
type clusterClients interface {
	Client(context string) (*cluster.Client, error)
}

// serverVersionTimeout bounds the discovery call this cache makes, so a
// cluster behind a black-holed route does not hold a session for
// rest.Config.Timeout. A var only so a test can shrink it.
var serverVersionTimeout = 10 * time.Second

type skewCache struct {
	clusters   clusterClients
	kubeconfig cluster.Kubeconfig

	// seen caches each context's answer, since the check costs an API round trip
	// (and on EKS a token fetch). A success only: a failure's input is
	// reachability, and caching it would mean a cluster briefly unreachable when its
	// first session opened never gets its skew checked again. The cost is one
	// ServerVersion() per terminal opened on an unreachable cluster.
	mu   sync.RWMutex
	seen map[string]string // context -> warning, empty meaning "nothing to say"

	clientOnce  sync.Once
	clientMinor int
	clientKnown bool
}

func newSkewCache(clusters clusterClients, kubeconfig cluster.Kubeconfig) *skewCache {
	return &skewCache{clusters: clusters, kubeconfig: kubeconfig, seen: map[string]string{}}
}

// warning returns the line to print, or "" when the versions are close enough
// or either could not be established (an unreachable cluster says so on its
// own). A failure is returned but not cached, so the next session asks again.
func (s *skewCache) warning(name string) string {
	s.mu.RLock()
	cached, known := s.seen[name]
	s.mu.RUnlock()
	if known {
		return cached
	}

	warning, settled := s.compute(name)
	if !settled {
		// Not cached, so the next session asks again. No short-lived negative entry:
		// a second clock to get wrong, to save one round trip per terminal opened.
		return warning
	}

	s.mu.Lock()
	s.seen[name] = warning
	s.mu.Unlock()
	return warning
}

// compute returns the line to print and whether that answer is settled, worth
// remembering for the life of the process. Three empty answers are as final as a
// warning:
//
//   - kubectl's own version is unknown: clientMinorVersion is a sync.Once over
//     a binary that does not change, guarding an exec with a ten-second timeout;
//   - the server's minor field did not parse, and will not until an upgrade;
//   - the versions are fine.
//
// Not settled are the two whose input is reachability: the cluster's client
// could not be built, or the API server did not answer.
func (s *skewCache) compute(name string) (string, bool) {
	client, ok := s.clientMinorVersion()
	if !ok {
		return "", true
	}

	c, err := s.clusters.Client(name)
	if err != nil {
		return "", false
	}

	ctx, cancel := context.WithTimeout(context.Background(), serverVersionTimeout)
	defer cancel()
	info, err := serverVersion(ctx, c.Clients.Discovery())
	if err != nil || info == nil {
		return "", false
	}

	server, ok := minorVersion(info.Minor)
	if !ok {
		return "", true
	}

	gap := client - server
	if gap <= 1 && gap >= -1 {
		return "", true
	}
	return fmt.Sprintf(
		"kubetower: kubectl is 1.%d and this cluster runs %s — Kubernetes supports one minor version of difference, not %d. Basic commands work; anything touching an API that changed in between may not.",
		client, info.GitVersion, abs(gap)), true
}

// serverVersion asks the API server its version, cancelably. A bare
// Discovery().ServerVersion() takes no context, so a black-holed cluster would
// hold this goroutine, and the session's start-up notice, for the 30 s of
// rest.Config.Timeout. internal/health and internal/overview make the same call
// the same way.
//
// The fallback is for an implementation that is not client-go's (a test fake):
// probing uncancelably costs socket lifetime, refusing would cost the answer.
func serverVersion(ctx context.Context, d discovery.DiscoveryInterface) (*version.Info, error) {
	if withCtx, ok := d.(discovery.ServerVersionInterfaceWithContext); ok {
		return withCtx.ServerVersionWithContext(ctx)
	}
	return d.ServerVersion()
}

// clientMinorVersion asks kubectl what it is, once for the life of the
// process: the binary only changes with the image, which is a restart.
func (s *skewCache) clientMinorVersion() (int, bool) {
	s.clientOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// --client so this never touches the network; the server half comes from
		// discovery.
		cmd := exec.CommandContext(ctx, "kubectl", "version", "--client", "-o", "json")
		// Env, not String: kubectl reads it, so it needs the platform's path-list
		// separator. Harmless here (--client reads no kubeconfig), but correct for
		// whatever copies it.
		cmd.Env = append(cmd.Environ(), cluster.EnvKubeconfig+"="+s.kubeconfig.Env())
		out, err := cmd.Output()
		if err != nil {
			return
		}
		s.clientMinor, s.clientKnown = parseClientMinor(out)
	})
	return s.clientMinor, s.clientKnown
}

// parseClientMinor is split out so the shape of kubectl's answer can be tested
// without kubectl.
func parseClientMinor(out []byte) (int, bool) {
	var report struct {
		ClientVersion struct {
			Minor string `json:"minor"`
		} `json:"clientVersion"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		return 0, false
	}
	return minorVersion(report.ClientVersion.Minor)
}

// minorVersion reads the minor number out of the field, which carries a
// trailing "+" on managed distributions (EKS reports "31+").
func minorVersion(raw string) (int, bool) {
	digits := strings.TrimRight(strings.TrimSpace(raw), "+")
	n, err := strconv.Atoi(digits)
	if err != nil {
		return 0, false
	}
	return n, true
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
