// Package cluster is everything KubeTower knows about reaching a Kubernetes API.
//
// It keeps one client per cluster alive rather than shelling out to kubectl,
// so an EKS exec credential is paid once per token lifetime, not per request.
package cluster

import (
	gocontext "context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/util/flowcontrol"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/ekstoken"
)

// Manager holds one lazily-created client per context in the kubeconfig.
type Manager struct {
	// kubeconfig is the operator's kubeconfig files, in precedence order.
	kubeconfig Kubeconfig
	// inCluster is the server profile's source (incluster.go); kubeconfig is
	// then empty.
	inCluster bool
	// awsHome is where a context's exec plugin gets its own AWS CLI credential
	// cache (awscache.go).
	awsHome awsHome

	// newLimiter builds the rate limiter a context's clients share; nil means
	// the real one. A field so a test can recognize what the clients got.
	newLimiter func() flowcontrol.RateLimiter

	// dial opens the TCP connection under every cluster request, and dialBound
	// is how long it is given; nil and zero mean the real dialer and
	// dialTimeout.
	dial      func(ctx gocontext.Context, network, address string) (net.Conn, error)
	dialBound time.Duration

	mu      sync.RWMutex
	raw     *clientcmdapi.Config
	clients map[clientKey]*Client
	// limiters is one token bucket per context, kept across a Reload
	// (ratelimit.go).
	limiters map[string]flowcontrol.RateLimiter
	// announced is which corrections to a context's connection have been
	// logged, once per context and subject rather than per client.
	announced map[string]bool
}

// clientKey is what a cached client is filed under: the context, and whether it
// is the streaming one. rest.HTTPClientFor copies Timeout into the http.Client,
// so a client built for lists can never be borrowed for a stream.
type clientKey struct {
	context   string
	streaming bool
}

// EKSSigning describes a context whose EKS token this process signs itself, and
// is nil on every other context.
//
// It answers **"as whom?"**: a bare 401 from EKS means the identity was
// refused. An empty Profile is the SDK's default resolution (this process's
// `AWS_PROFILE`, then `[default]`), which a GUI-launched console may not share
// with the shell where `kubectl` works.
type EKSSigning struct {
	Cluster string
	Region  string
	// Profile is what the kubeconfig named: `--profile` in the exec block's
	// arguments, or `AWS_PROFILE` in its env. Empty means the SDK's default
	// resolution decided.
	Profile string
	// Identity is the ARN the tokens actually carry, once something has
	// established it, and "" until then: two machines can resolve one profile
	// to two roles. A function because the answer arrives later; it never
	// blocks and never reaches the network.
	Identity func() string
}

// Client is one cluster, ready to be asked things: a typed clientset for the
// built-in kinds and a dynamic client for everything else, built from one
// http.Client and sharing one rate limiter.
type Client struct {
	Context string
	Config  *rest.Config
	Clients kubernetes.Interface
	// Dynamic is the unstructured client, held for the life of the process: a
	// fresh one per call would mint **its own rate limiter** and turn the
	// per-cluster ceiling into a per-call allowance.
	Dynamic dynamic.Interface
	// httpClient is the one both clients were built from, so their sharing
	// does not depend on client-go's transport cache.
	httpClient *http.Client
	// discoveryTyped and discoveryDynamic are the two clients an extension is
	// handed (ForExtension): built over httpClient, so they share its
	// connections and its rate limiter, behind a transport that sends
	// discovery and nothing else.
	discoveryTyped   kubernetes.Interface
	discoveryDynamic dynamic.Interface

	// EKS is set when this context's token is signed in this process, and nil
	// otherwise, including for an EKS context that fell back to the CLI.
	EKS *EKSSigning
}

// NewManager builds the manager over the files that make up the operator's
// kubeconfig, in precedence order, earlier winning as kubectl merges them.
//
// **The paths are literal**: nothing here splits on `os.PathListSeparator`.
// KubeconfigFromEnv is the one place `KUBECONFIG` is split; the raw value
// handed here would name one file called `a;b`.
func NewManager(paths ...string) (*Manager, error) {
	m := &Manager{
		kubeconfig: Kubeconfig(paths),
		awsHome:    defaultAWSHome(),
		clients:    map[clientKey]*Client{},
		limiters:   map[string]flowcontrol.RateLimiter{},
	}
	if err := m.Reload(); err != nil {
		return nil, err
	}
	return m, nil
}

// Reload re-reads the kubeconfig from disk, so a change is picked up without a
// restart.
func (m *Manager) Reload() error {
	if m.inCluster {
		// No file to re-read: the one context is the pod, whose token the
		// transport re-reads as it rotates. Clients are still dropped.
		m.mu.Lock()
		defer m.mu.Unlock()
		m.clients = map[clientKey]*Client{}
		m.announced = nil
		return nil
	}
	raw, err := m.kubeconfig.Load()
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.raw = raw
	// Clients are dropped rather than kept: a context whose server or
	// credentials changed must not keep answering from the old one.
	m.clients = map[clientKey]*Client{}
	m.announced = nil
	return nil
}

// Contexts lists the kubeconfig context names, sorted, exactly as they are
// written there.
func (m *Manager) Contexts() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.raw.Contexts))
	for name := range m.raw.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Known reports whether a name is a context in the kubeconfig. Everything
// arriving from a browser goes through this before it reaches a client.
func (m *Manager) Known(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.raw.Contexts[name]
	return ok
}

// Resolve maps the name a URL carries back to a real context.
//
// URLs percent-encode the name, but the DNS-friendly form (`/` as `--`) is
// still accepted, because saved links carry it.
func (m *Manager) Resolve(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	if m.Known(name) {
		return name, true
	}
	for _, candidate := range m.Contexts() {
		if MakeDNSFriendly(candidate) == name {
			return candidate, true
		}
	}
	return "", false
}

// MakeDNSFriendly reproduces the transformation Headlamp applies to context
// names, so that a URL written in that form still resolves.
func MakeDNSFriendly(name string) string {
	name = strings.ReplaceAll(name, "/", "--")
	name = strings.ReplaceAll(name, " ", "__")
	return name
}

// Client returns the client for a context, building it on first use.
//
// The build is cheap: no network until the first request. This one carries
// the per-request timeout, wrong for anything that holds a body open (see
// StreamingClient).
func (m *Manager) Client(context string) (*Client, error) {
	return m.client(context, false)
}

// StreamingClient returns the client for a context with **no** per-request
// timeout, for callers that read a response body for as long as the operator
// keeps it open.
//
// rest.Config.Timeout is not a time to first byte: rest.HTTPClientFor copies
// it into http.Client.Timeout, which covers every read from the body, so a
// followed log stream on the ordinary client is cut at 30 seconds.
// A caller that asks for this owes its own ceiling on the context it passes.
func (m *Manager) StreamingClient(context string) (*Client, error) {
	return m.client(context, true)
}

// clientsFor builds the two clients one cluster needs, from one http.Client.
//
// The http.Client is a **parameter**: NewForConfig would build one per client,
// and the sharing would rest on client-go's transport cache keying as it does
// today, which is not ours to promise.
func clientsFor(cfg *rest.Config, httpClient *http.Client) (kubernetes.Interface, dynamic.Interface, error) {
	clientset, err := kubernetes.NewForConfigAndClient(cfg, httpClient)
	if err != nil {
		return nil, nil, err
	}
	dyn, err := dynamic.NewForConfigAndClient(cfg, httpClient)
	if err != nil {
		return nil, nil, err
	}
	return clientset, dyn, nil
}

// discoveryClientsFor builds the two clients an extension is handed, over
// httpClient's own transport behind access.DiscoveryOnly: one http.Client
// more, the same connections, the same rate limiter (cfg's).
func discoveryClientsFor(cfg *rest.Config, context string, httpClient *http.Client) (kubernetes.Interface, dynamic.Interface, error) {
	narrowed := *httpClient
	narrowed.Transport = access.DiscoveryOnly(cfg, context)(httpClient.Transport)
	return clientsFor(cfg, &narrowed)
}

// ForExtension is this cluster's two clients as an extension is handed them
// (extension.Host.Client): they answer **which kinds the cluster serves and
// nothing else**. A request for an object, a list, a watch or a change is
// refused before it leaves the process (access.DiscoveryOnly), so an
// extension reads objects only through the console's reviewed doors.
//
// A Client built by the manager carries them already; one built by hand (a
// test's) has them built here from its Config.
func (c *Client) ForExtension() (kubernetes.Interface, dynamic.Interface, error) {
	if c.discoveryTyped != nil && c.discoveryDynamic != nil {
		return c.discoveryTyped, c.discoveryDynamic, nil
	}
	if c.Config == nil {
		return nil, nil, fmt.Errorf("no configuration for %q, so no client can be handed to an extension", c.Context)
	}
	httpClient := c.httpClient
	if httpClient == nil {
		built, err := rest.HTTPClientFor(c.Config)
		if err != nil {
			return nil, nil, fmt.Errorf("building a client for %q: %w", c.Context, err)
		}
		httpClient = built
	}
	return discoveryClientsFor(c.Config, c.Context, httpClient)
}

// EnvAWSExec, set to anything, makes every EKS context authenticate by running
// `aws eks get-token` instead of having its token signed in this process: a
// way back to the subprocess that needs no new build.
const EnvAWSExec = "KT_AWS_EXEC"

// StreamingConfig is a copy of cfg with no per-request timeout, for a client
// or executor that holds a connection open; see StreamingClient.
func StreamingConfig(cfg *rest.Config) *rest.Config {
	out := rest.CopyConfig(cfg)
	out.Timeout = 0
	return out
}

// boundDial is the dial every cluster client uses: the real one, given
// dialTimeout to complete the connect.
//
// The bound is a deadline on the dialer's context rather than
// net.Dialer.Timeout so a test can put its own dialer under it; both fail as
// `dial tcp …: i/o timeout`. It bounds the connect alone: once connected, the
// context's expiry does not touch the connection, so a log stream, an exec or
// a watch runs as long as its own caller allows.
func (m *Manager) boundDial(ctx gocontext.Context, network, address string) (net.Conn, error) {
	bound := m.dialBound
	if bound <= 0 {
		bound = dialTimeout
	}
	ctx, cancel := gocontext.WithTimeout(ctx, bound)
	defer cancel()

	dial := m.dial
	if dial == nil {
		// KeepAlive as client-go's own dialer sets it, so taking the dial over
		// changes nothing but the bound.
		dial = (&net.Dialer{KeepAlive: 30 * time.Second}).DialContext
	}
	return dial(ctx, network, address)
}

// firstMention reports whether something about this context has yet to be
// announced, and records that it now has been.
//
// Keyed by subject as well as context, so one correction cannot silence
// another on the same context.
func (m *Manager) firstMention(subject, context string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.announced == nil {
		m.announced = map[string]bool{}
	}
	key := subject + "\x00" + context
	if m.announced[key] {
		return false
	}
	m.announced[key] = true
	return true
}

func (m *Manager) client(context string, streaming bool) (*Client, error) {
	key := clientKey{context: context, streaming: streaming}

	m.mu.RLock()
	existing, ok := m.clients[key]
	m.mu.RUnlock()
	if ok {
		return existing, nil
	}

	if !m.Known(context) {
		return nil, fmt.Errorf("unknown cluster %q", context)
	}

	// The same loading rules Reload uses, so a context on the rail cannot
	// resolve to another file's entry when opened. Under the server profile
	// there is no merge: the one context is the pod. Everything after this block
	// applies to both.
	var cfg *rest.Config
	var err error
	if m.inCluster {
		cfg, err = m.inClusterRestConfig(context)
	} else {
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			m.kubeconfig.loadingRules(),
			&clientcmd.ConfigOverrides{CurrentContext: context},
		).ClientConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("building a client for %q: %w", context, err)
	}

	// A cluster that does not answer must fail this request, not hold the page.
	cfg.Timeout = requestTimeout
	// A cluster that does not answer the connect fails within dialTimeout. On
	// the config, so everything built from it dials the same way. The cost:
	// client-go keys its transport cache on the dial function, so the ordinary
	// and streaming clients of a context each get their own transport.
	cfg.Dial = m.boundDial
	cfg.QPS, cfg.Burst = clientQPS, clientBurst
	// The bucket made once per context: QPS and Burst alone are per client
	// (ratelimit.go). Set before StreamingConfig, which copies the field.
	cfg.RateLimiter = m.limiterFor(context)
	if streaming {
		cfg = StreamingConfig(cfg)
	}

	// A local cluster names the loopback, which inside a container is this
	// container (loopback.go). Said once, so the next failure stays readable.
	if to, changed := redirectLoopback(cfg, containerised()); changed && m.firstMention("loopback", context) {
		log.Printf("[kubetower] %s is on the loopback; reaching it at %s", context, to)
	}

	// Sign the EKS token in this process when possible (internal/ekstoken): no
	// child process, and no `~/.aws/cli/cache` entry for two contexts to
	// corrupt. Otherwise the CLI's cache is separated per context
	// (awscache.go). **Both before rest.HTTPClientFor**, which hands the exec
	// configuration to the credential plugin: an environment added afterwards
	// would never reach the `aws` process.
	var signing *EKSSigning
	if minter, spec, ok := m.mintInProcess(cfg, context); ok {
		// The exec block goes, or client-go would still start the subprocess.
		cfg.ExecProvider = nil
		cfg.Wrap(func(base http.RoundTripper) http.RoundTripper {
			return &ekstoken.RoundTripper{Provider: minter, Base: base}
		})
		signing = &EKSSigning{
			Cluster: spec.Cluster, Region: spec.Region, Profile: spec.Profile,
			Identity: minter.KnownIdentity,
		}
		// Logged once, the first time this cluster refuses a token: the log
		// needs no browser or session, and an operator can paste it.
		minter.OnRefusal(func(note string) {
			log.Printf("[kubetower] %s: %s", context, note)
		})
		if m.firstMention("aws-token", context) {
			// The profile is in this line: a bare 401 from EKS is an identity it
			// could not map, so the profile is the diagnosis.
			log.Printf("[kubetower] %s: EKS tokens are signed in this process, no `aws` is run "+
				"(cluster %s, region %s, %s)",
				context, spec.Cluster, spec.Region, profileNote(spec.Profile))
		}
	} else {
		// Said once for the process when the separation is off on this host,
		// not once per cluster.
		if m.awsHome.Off != "" && m.firstMention("aws-cache", "") {
			log.Printf("[kubetower] AWS credential caches are shared: %s", m.awsHome.Off)
		}

		if dir, changed, err := m.awsHome.separate(cfg, context); err != nil {
			// Not fatal: the shared cache is the CLI's own behavior, and refusing
			// a client would make the cluster unreachable.
			if m.firstMention("aws-cache", context) {
				log.Printf("[kubetower] %s keeps the shared AWS credential cache: %v", context, err)
			}
		} else if changed && m.firstMention("aws-cache", context) {
			log.Printf("[kubetower] %s has its own AWS credential cache at %s", context, dir)
		}
	}

	// **After the loopback rewrite**: both constructors copy the config.
	//
	// **And the access guard, on the config, on both profiles**: a request that
	// would change this cluster leaves only when internal/access's Gate.Perform
	// reviewed it, which also covers an extension handed these clients. On the
	// config so everything built from Client.Config carries it, and last
	// because it reads the base path from cfg.Host.
	access.Guard(cfg, context, func(format string, args ...any) {
		log.Printf("[kubetower] "+format, args...)
	})
	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("building a client for %q: %w", context, err)
	}
	clientset, dyn, err := clientsFor(cfg, httpClient)
	if err != nil {
		return nil, fmt.Errorf("building a client for %q: %w", context, err)
	}

	discoveryTyped, discoveryDynamic, err := discoveryClientsFor(cfg, context, httpClient)
	if err != nil {
		return nil, fmt.Errorf("building a client for %q: %w", context, err)
	}

	client := &Client{
		Context: context, Config: cfg,
		Clients: clientset, Dynamic: dyn, httpClient: httpClient,
		discoveryTyped: discoveryTyped, discoveryDynamic: discoveryDynamic,
		EKS: signing,
	}

	m.mu.Lock()
	// Another request may have built one meanwhile; keep whichever came first.
	if raced, ok := m.clients[key]; ok {
		m.mu.Unlock()
		return raced, nil
	}
	m.clients[key] = client
	m.mu.Unlock()

	return client, nil
}

// profileNote says which AWS profile a context signs with. An empty profile
// means the SDK's default resolution decided.
func profileNote(profile string) string {
	if profile == "" {
		// AWS_ACCESS_KEY_ID is named first because it wins first: with no
		// profile named, the SDK puts the process's own static keys ahead of
		// every profile, so naming only AWS_PROFILE would mislead.
		return "no AWS profile named in the kubeconfig, so the default resolution decided " +
			"(this process's own AWS_ACCESS_KEY_ID first, then AWS_PROFILE, then `default`)"
	}
	return "AWS profile " + profile
}

// mintInProcess decides whether this context's EKS token can be signed here
// instead of by a subprocess, and builds the minter if it can.
//
// It says no when:
//
//   - **the exec block is not one ekstoken.Parse understands**: a refusal costs
//     one subprocess, a wrong acceptance authenticates as somebody else;
//   - **`KT_AWS_EXEC` is set**, the operator's way back to the subprocess;
//   - **the AWS configuration will not load**, which the CLI is more likely to
//     report usefully.
//
// An expired SSO session is not a reason: `LoadDefaultConfig` touches no
// network, so it surfaces on the first request, where health classifies it.
func (m *Manager) mintInProcess(cfg *rest.Config, context string) (*ekstoken.Provider, ekstoken.Spec, bool) {
	spec, ok := ekstoken.Parse(cfg.ExecProvider)
	if !ok {
		return nil, spec, false
	}
	if strings.TrimSpace(os.Getenv(EnvAWSExec)) != "" {
		if m.firstMention("aws-token", "") {
			log.Printf("[kubetower] %s is set: EKS tokens come from `aws eks get-token` as before",
				EnvAWSExec)
		}
		return nil, spec, false
	}

	// A background context: this provider outlives the request that built the
	// client, and a canceled request must not leave a dead one. Nothing here
	// blocks on the network.
	minter, err := ekstoken.New(gocontext.Background(), spec)
	if err != nil {
		if m.firstMention("aws-token", context) {
			log.Printf("[kubetower] %s: falling back to `aws eks get-token`: %v", context, err)
		}
		return nil, spec, false
	}
	return minter, spec, true
}
