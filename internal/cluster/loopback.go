package cluster

import (
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"

	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/ekstoken"
)

// Reaching a cluster that lives on the host's loopback.
//
// docker-desktop, kind, minikube and k3d declare their API server on
// 127.0.0.1. Inside a container that address is the container itself, so every
// request is refused on a cluster that works from a terminal on the host. The
// kubeconfig is read in-process, so the rewrite lives next to the code that
// builds the connection.
//
// Two things change together: the address, and the name the certificate is
// checked against. The API server's certificate carries "kubernetes" and the
// loopback IP, never "host.docker.internal", so redirecting the address alone
// turns a refused connection into a certificate error.

// RedirectLoopback rewrites a kubeconfig cluster's server address the way
// redirectLoopback does for an in-process rest.Config, and reports the TLS
// server name that has to go with it.
//
// Exported because every way of reaching a cluster needs it: the terminal's
// per-session kubeconfig for kubectl and helm would otherwise answer
// "connection refused" where the UI's corrected rest.Config works.
//
// Returns ("", "", false) when nothing should change.
func RedirectLoopback(server string, inContainer bool) (address, serverName string, changed bool) {
	if !inContainer {
		return "", "", false
	}
	host, ok := loopbackTarget(server)
	if !ok {
		return "", "", false
	}
	return host, certificateName, true
}

// InContainer reports whether this process is running inside a container, and so
// whether the loopback means something other than what a kubeconfig intends.
func InContainer() bool { return containerised() }

// Loopback reports whether a context's API server is on the local machine, as
// the kubeconfig spells it — before any redirect this package applies.
//
// It reads the merged configuration rather than a built client: a list cannot
// afford to connect, and a cluster that is down is still on the loopback. It
// reads the kubeconfig before any redirect, because a redirected client's host
// is the gateway, while the question is whether the API server lives on the
// operator's own machine.
//
// False for a context that is not in the kubeconfig, or whose cluster entry is
// missing.
func (m *Manager) Loopback(context string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.raw.Contexts[context]
	if !ok || entry == nil {
		return false
	}
	target, ok := m.raw.Clusters[entry.Cluster]
	if !ok || target == nil {
		return false
	}
	return serverIsLoopback(target.Server)
}

// AWS reports whether a context's user signs in through the AWS CLI, as the
// kubeconfig spells it: its exec plugin's command is `aws`. Asked of the merged
// configuration for Loopback's reason - a list cannot afford to connect - and
// false for an unknown context or one with no exec block.
func (m *Manager) AWS(context string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.raw.Contexts[context]
	if !ok || entry == nil {
		return false
	}
	user, ok := m.raw.AuthInfos[entry.AuthInfo]
	return ok && user != nil && ekstoken.UsesAWSCLI(user.Exec)
}

// serverIsLoopback is isLoopback over a kubeconfig's `server` URL, which is what
// the file holds rather than a bare host.
func serverIsLoopback(server string) bool {
	if server == "" {
		return false
	}
	parsed, err := url.Parse(server)
	if err != nil || parsed.Host == "" {
		return false
	}
	return isLoopback(parsed.Hostname())
}

// gatewayHost is the name Docker gives the host from inside a container. It is
// not automatic: compose.dev.yaml maps it with `extra_hosts: host-gateway`.
const gatewayHost = "host.docker.internal"

// certificateName is what a Kubernetes API server certificate actually carries.
const certificateName = "kubernetes"

// containerized reports whether this process is running inside a container, and
// so whether 127.0.0.1 means something other than what the kubeconfig intends.
//
// Conservative: on the host the rewrite must not happen, because there
// host.docker.internal is absent or points elsewhere, and a working local
// cluster would break.
func containerised() bool {
	return containerVerdict(os.Getenv("KT_IN_CONTAINER"), dockerenv, warnContainerSwitch)
}

// containerVerdict is containerized with its three inputs named, so a table
// test can hold every spelling without setting the machine.
//
// The variable takes the yes/no vocabulary of every other KT_ switch (`1`,
// `true`, `yes`, `on` against `0`, `false`, `no`, `off`, any case), so `no`
// and `off` do not force container mode, which would rewrite every loopback
// cluster and drop the loopback desktop's passwordless sign-in. Any other
// value is said once and left to detection: a word nobody can read has forced
// nothing.
func containerVerdict(raw string, detected func() bool, logf func(string, ...any)) bool {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "":
		return detected()
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	verdict := detected()
	logf("KT_IN_CONTAINER=%q is not a yes/no value (1, true, yes, on or 0, false, no, off); "+
		"it is ignored and /.dockerenv decides: %t", raw, verdict)
	return verdict
}

// dockerenv is the detection the variable overrides.
func dockerenv() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

// warnedContainerSwitch holds the values already said, so an unreadable value
// is said once per process.
var warnedContainerSwitch sync.Map

func warnContainerSwitch(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	if _, said := warnedContainerSwitch.LoadOrStore(line, true); said {
		return
	}
	log.Printf("[kubetower] %s", line)
}

// redirectLoopback points a config at the host when the config names the
// loopback and we are inside a container. It returns the address it settled on
// and whether it changed anything, so the caller can say so out loud.
func redirectLoopback(cfg *rest.Config, inContainer bool) (string, bool) {
	if cfg == nil || !inContainer {
		return "", false
	}
	host, ok := loopbackTarget(cfg.Host)
	if !ok {
		return "", false
	}

	cfg.Host = host
	// The certificate does not name the gateway, so ask for the name it does
	// carry. Still verified against the cluster's own CA; only the name being
	// matched changes.
	cfg.TLSClientConfig.ServerName = certificateName
	return host, true
}

// loopbackTarget rewrites a loopback URL to the gateway, preserving the scheme,
// the port and anything after it. False for an address that is not loopback.
func loopbackTarget(raw string) (string, bool) {
	if raw == "" {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", false
	}
	if !isLoopback(parsed.Hostname()) {
		return "", false
	}

	// Rebuild the authority rather than string-replacing the host, which could
	// also rewrite matching digits in the port.
	authority := gatewayHost
	if port := parsed.Port(); port != "" {
		authority = net.JoinHostPort(gatewayHost, port)
	}
	parsed.Host = authority
	return parsed.String(), true
}

// isLoopback covers 127.0.0.1, localhost and ::1, and the rest of 127/8, since
// minikube can sit on 127.0.0.2.
func isLoopback(host string) bool {
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
