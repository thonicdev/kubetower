package cluster

// The cluster this process runs in, reached as the pod's own ServiceAccount.
//
// `rest.InClusterConfig()` reads what every pod is given (the service
// environment, the mounted CA and token), so a pod needs no kubeconfig written
// for it. It sets `BearerTokenFile`, so the transport re-reads the token as the
// kubelet rotates it.
//
// The server profile manages that cluster and no other, as one context named
// `in-cluster`. It is a synthesized `clientcmdapi.Config`, so everything that
// reads the context list reads it as it reads a kubeconfig; only `client`
// differs, in where the `rest.Config` comes from.

import (
	"fmt"

	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/util/flowcontrol"
)

// InClusterContext is the name the server profile gives the cluster it runs in.
const InClusterContext = "in-cluster"

// inClusterConfig is `rest.InClusterConfig`, a variable so that a test can hand
// the manager a config pointing at a server of its own.
var inClusterConfig = rest.InClusterConfig

// NewInClusterManager builds the manager for the server profile: one cluster,
// the one this pod runs in, reached with the pod's ServiceAccount.
//
// **It refuses outside a pod**, with client-go's own sentence: `--incluster`
// on a laptop is a mistake worth a non-zero exit, not a tile that never
// answers.
func NewInClusterManager() (*Manager, error) {
	cfg, err := inClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("--incluster: this process is not running in a pod it can read its "+
			"ServiceAccount from: %w", err)
	}
	m := &Manager{
		inCluster: true,
		awsHome:   defaultAWSHome(),
		clients:   map[clientKey]*Client{},
		limiters:  map[string]flowcontrol.RateLimiter{},
		raw:       inClusterRaw(cfg.Host),
	}
	return m, nil
}

// inClusterRaw is the one-context configuration the rest of this package reads
// its context list from. The server is carried so that `Loopback` answers from
// the real address.
func inClusterRaw(host string) *clientcmdapi.Config {
	raw := clientcmdapi.NewConfig()
	raw.Clusters[InClusterContext] = &clientcmdapi.Cluster{Server: host}
	raw.AuthInfos[InClusterContext] = &clientcmdapi.AuthInfo{}
	raw.Contexts[InClusterContext] = &clientcmdapi.Context{
		Cluster: InClusterContext, AuthInfo: InClusterContext,
	}
	raw.CurrentContext = InClusterContext
	return raw
}

// InCluster reports whether this manager reads its cluster from the pod rather
// than from a kubeconfig.
func (m *Manager) InCluster() bool { return m.inCluster }

// inClusterRestConfig is a fresh `rest.Config` for the one context, read on
// every call because the caller mutates it: a shared value would carry the
// streaming client's missing timeout into the ordinary one.
func (m *Manager) inClusterRestConfig(context string) (*rest.Config, error) {
	if context != InClusterContext {
		return nil, fmt.Errorf("unknown cluster %q", context)
	}
	return inClusterConfig()
}
