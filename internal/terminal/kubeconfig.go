package terminal

// The kubeconfig one terminal session sees.
//
// client-go does in memory what `kubectl config view --raw --minify --context
// X` and `kubectl config use-context X` would: no process launched, no
// credential fetch on an EKS context.

import (
	"fmt"
	"os"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/thonicdev/kubetower/internal/cluster"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// writeSessionKubeconfig extracts one context out of the operator's kubeconfig
// and writes it where a single terminal session can use it.
//
// source is the whole KUBECONFIG list, never one file of it: a context from the
// second file, looked up in the first alone, is missing, and kubectl in the
// terminal would answer "no context is currently set".
//
// aws is the private AWS CLI credential cache for this session's exec plugin
// (awshome.go). The zero value leaves the exec blocks alone.
func writeSessionKubeconfig(source cluster.Kubeconfig, context, dest string, aws sessionAWSHome, logf func(string, ...any)) error {
	// cluster.Kubeconfig.Load, not clientcmd.LoadFromFile: the merge must be the
	// one the manager resolved the context against, or the rail and the terminal
	// could mean different clusters by one name.
	raw, err := source.Load()
	if err != nil {
		return err
	}

	content, err := minifyKubeconfig(raw, context, aws, logf)
	if err != nil {
		return err
	}

	// The file holds a token or a client key. O_EXCL because the directory was
	// just created and anything already there means someone else got there first;
	// 0600 because every process here runs as the same user. os.WriteFile would
	// apply the mode only to a new file.
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("writing the session kubeconfig: %w", err)
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing the session kubeconfig: %w", err)
	}
	return f.Close()
}

// minifyKubeconfig reduces a kubeconfig to one context, marked current.
// Separate from the file handling so it can be tested without a kubeconfig on
// disk or a cluster.
func minifyKubeconfig(raw *clientcmdapi.Config, context string, aws sessionAWSHome, logf func(string, ...any)) ([]byte, error) {
	cfg := raw.DeepCopy()

	if _, ok := cfg.Contexts[context]; !ok {
		return nil, fmt.Errorf("no context %q in the kubeconfig", context)
	}

	// Set before minifying: MinifyConfig keeps what current-context names and
	// refuses to run without one, and minifying alone leaves current-context
	// unchanged.
	cfg.CurrentContext = context
	if err := clientcmdapi.MinifyConfig(cfg); err != nil {
		return nil, fmt.Errorf("reducing the kubeconfig to %q: %w", context, err)
	}

	// Inline the certificate authority and any client certificate: the session's
	// working directory is the session folder, so a relative path in the original
	// would resolve to nothing and surface as a TLS error.
	if err := clientcmdapi.FlattenConfig(cfg); err != nil {
		// Not fatal: the config already worked with paths, and refusing to open the
		// terminal over a file that could not be re-read would be worse.
		if logf != nil {
			logf("could not inline the certificates for %q, keeping the file paths: %v", context, err)
		}
		cfg = raw.DeepCopy()
		cfg.CurrentContext = context
		if err := clientcmdapi.MinifyConfig(cfg); err != nil {
			return nil, fmt.Errorf("reducing the kubeconfig to %q: %w", context, err)
		}
	}

	// A loopback cluster means the host, not this container, and kubectl and helm
	// read this file without the in-process client's correction; without it, the
	// terminal would answer "connection refused" beside a UI that works.
	//
	// Last, after the flatten fallback, which rebuilds cfg from raw and would undo
	// anything applied earlier. That fallback is taken by any kubeconfig whose
	// certificate is an unreadable file, the ordinary shape of a mounted one.
	redirectLoopbackClusters(cfg, logf)

	// This session's own AWS CLI credential cache (awshome.go). Also after the
	// flatten fallback, for the same reason: both corrections sit between the last
	// rebuild and clientcmd.Write, so no path skips them.
	if changed, err := aws.separate(cfg); err != nil {
		// Not fatal: the shared cache may get corrupted, while refusing would make the
		// cluster certainly unreachable from the terminal.
		if logf != nil {
			logf("terminal: %s keeps the shared AWS credential cache: %v", context, err)
		}
	} else if changed && logf != nil {
		logf("terminal: %s has its own AWS credential cache at %s", context, aws.Dir)
	}

	content, err := clientcmd.Write(*cfg)
	if err != nil {
		return nil, fmt.Errorf("serializing the session kubeconfig: %w", err)
	}
	return content, nil
}

// redirectLoopbackClusters points every loopback cluster in a kubeconfig at the
// host, and asks for the certificate name the API server actually carries. Both
// halves together: the address alone would turn "connection refused" into a
// certificate error.
func redirectLoopbackClusters(cfg *clientcmdapi.Config, logf func(string, ...any)) {
	inContainer := cluster.InContainer()
	for name, c := range cfg.Clusters {
		if c == nil {
			continue
		}
		address, serverName, changed := cluster.RedirectLoopback(c.Server, inContainer)
		if !changed {
			continue
		}
		c.Server = address
		c.TLSServerName = serverName
		if logf != nil {
			// Logged, as the in-process rewrite is: a silent change to where commands are
			// sent is its own trap.
			logf("terminal: %s is on the loopback; reaching it at %s", name, address)
		}
	}
}
