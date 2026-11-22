package terminal

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// twoClusters is a kubeconfig with two contexts, each with its own token. The
// second one is the one that must not travel.
func twoClusters() *clientcmdapi.Config {
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["prod"] = &clientcmdapi.Cluster{Server: "https://prod.example:6443"}
	cfg.Clusters["staging"] = &clientcmdapi.Cluster{Server: "https://staging.example:6443"}
	cfg.AuthInfos["prod-user"] = &clientcmdapi.AuthInfo{Token: "PROD-TOKEN"}
	cfg.AuthInfos["staging-user"] = &clientcmdapi.AuthInfo{Token: "STAGING-TOKEN"}
	cfg.Contexts["prod"] = &clientcmdapi.Context{Cluster: "prod", AuthInfo: "prod-user"}
	cfg.Contexts["staging"] = &clientcmdapi.Context{Cluster: "staging", AuthInfo: "staging-user"}
	cfg.CurrentContext = "staging"
	return cfg
}

// The trap this pins: `--minify` keeps the requested context but leaves
// current-context alone, so the session needs current-context set to be
// usable. Without it the shell opens on whatever the operator
// last selected — that is, on the wrong cluster — and nothing on screen says so.
func TestSessionKubeconfigSelectsTheRequestedContext(t *testing.T) {
	content, err := minifyKubeconfig(twoClusters(), "prod", sessionAWSHome{}, nil)
	if err != nil {
		t.Fatalf("minifying: %v", err)
	}

	got, err := clientcmd.Load(content)
	if err != nil {
		t.Fatalf("the result does not parse as a kubeconfig: %v", err)
	}
	if got.CurrentContext != "prod" {
		t.Errorf("current-context is %q, expected %q", got.CurrentContext, "prod")
	}
	if len(got.Contexts) != 1 {
		t.Errorf("the session kubeconfig kept %d contexts, expected 1", len(got.Contexts))
	}
}

// A shell opened on one cluster must not be handed the credentials of another.
// Two tabs on two clusters is the normal way this is used.
func TestSessionKubeconfigCarriesNoOtherClusterCredentials(t *testing.T) {
	content, err := minifyKubeconfig(twoClusters(), "prod", sessionAWSHome{}, nil)
	if err != nil {
		t.Fatalf("minifying: %v", err)
	}
	if !strings.Contains(string(content), "PROD-TOKEN") {
		t.Error("the session kubeconfig lost the credential it needs")
	}
	if strings.Contains(string(content), "STAGING-TOKEN") {
		t.Error("the session kubeconfig carries another cluster's token")
	}
	if strings.Contains(string(content), "staging.example") {
		t.Error("the session kubeconfig carries another cluster's server")
	}
}

func TestSessionKubeconfigRefusesAnUnknownContext(t *testing.T) {
	if _, err := minifyKubeconfig(twoClusters(), "does-not-exist", sessionAWSHome{}, nil); err == nil {
		t.Fatal("an unknown context was accepted")
	}
}

// The file holds a live token, in a container where everything runs as the same
// user. 0600 is the whole protection it has.
func TestSessionKubeconfigIsWrittenPrivate(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "kubeconfig")
	if err := clientcmd.WriteToFile(*twoClusters(), source); err != nil {
		t.Fatalf("writing the source kubeconfig: %v", err)
	}

	dest := filepath.Join(dir, "session", "kubeconfig")
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		t.Fatalf("creating the session directory: %v", err)
	}
	if err := writeSessionKubeconfig(cluster.Kubeconfig{source}, "prod", dest, sessionAWSHome{}, nil); err != nil {
		t.Fatalf("writing the session kubeconfig: %v", err)
	}

	loaded, err := clientcmd.LoadFromFile(dest)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	if loaded.CurrentContext != "prod" {
		t.Errorf("current-context is %q on disk", loaded.CurrentContext)
	}

	if runtime.GOOS == "windows" {
		// Windows has no POSIX mode bits; the container this ships in is Linux.
		t.Skip("file modes are meaningless here")
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the session kubeconfig is mode %o, expected 600", perm)
	}
}

// O_EXCL, because a path that already exists means somebody else got there
// first and stomping it would hand two sessions the same file.
func TestSessionKubeconfigRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "kubeconfig")
	if err := clientcmd.WriteToFile(*twoClusters(), source); err != nil {
		t.Fatalf("writing the source kubeconfig: %v", err)
	}

	dest := filepath.Join(dir, "already-there")
	if err := os.WriteFile(dest, []byte("someone else's"), 0o600); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if err := writeSessionKubeconfig(cluster.Kubeconfig{source}, "prod", dest, sessionAWSHome{}, nil); err == nil {
		t.Fatal("an existing session kubeconfig was overwritten")
	}
}

// --- the loopback correction, on both paths out of minifyKubeconfig ----------

// loopbackConfig is a kubeconfig for a cluster on the host's loopback, which is
// what docker-desktop, kind, k3d and minikube all write.
//
// unreadableCA points the cluster's certificate authority at a file that is not
// there, which is what makes FlattenConfig fail and sends minifyKubeconfig
// down its fallback path.
func loopbackConfig(unreadableCA bool) *clientcmdapi.Config {
	cfg := clientcmdapi.NewConfig()
	cluster := &clientcmdapi.Cluster{Server: "https://127.0.0.1:6443"}
	if unreadableCA {
		cluster.CertificateAuthority = filepath.Join("does-not-exist", "ca.crt")
	}
	cfg.Clusters["docker-desktop"] = cluster
	cfg.AuthInfos["docker-desktop"] = &clientcmdapi.AuthInfo{Token: "LOCAL-TOKEN"}
	cfg.Contexts["docker-desktop"] = &clientcmdapi.Context{
		Cluster: "docker-desktop", AuthInfo: "docker-desktop"}
	cfg.CurrentContext = "docker-desktop"
	return cfg
}

// The loopback correction, on **both** ways out of minifyKubeconfig.
//
// The flatten failure path rebuilds the whole config from the original, so a
// rewrite run before FlattenConfig would give a kubeconfig whose CA is a file
// this process cannot read a session kubeconfig with 127.0.0.1 in it. Inside
// the container that is the container:
// `kubectl get ns` answers "connection refused" in a terminal opened on a
// cluster whose pods the UI is listing three inches away, and it reads as a
// broken cluster rather than as two code paths disagreeing.
//
// Both halves are asserted, not just the address. Redirecting the address alone
// turns a connection refused into a certificate error — still broken, and now it
// looks like the cluster's fault.
func TestTheSessionKubeconfigRewritesTheLoopbackOnEveryPath(t *testing.T) {
	// The rewrite is deliberately off on a host, where 127.0.0.1 means what the
	// kubeconfig says. This is the switch cluster.InContainer reads.
	t.Setenv("KT_IN_CONTAINER", "1")

	// **What makes the fallback fire, and why this fixture is load-bearing.**
	//
	// The second case below is the only cover the loopback correction has on the rebuild
	// branch, and it only covers it while `loopbackConfig(true)` still makes
	// FlattenConfig fail. It fails because that cluster's
	// `certificate-authority` names a file that is not there —
	// `does-not-exist/ca.crt` — and FlattenConfig's job is to read the
	// referenced files and inline them. Nothing else in the fixture does it: the
	// token is inline, there is no client certificate, and the address is never
	// dialled.
	//
	// So if you edit loopbackConfig — give the cluster real CA data, drop the
	// path, point it at a file that exists — the flatten stops failing, this
	// subtest quietly starts exercising the *same* branch as the one above it,
	// and both keep passing. TestAnUnreadableCertificateFallsBackRatherThanFailing
	// below is the alarm for exactly that: it asserts the fallback was taken, by
	// the log line, and fails if the flatten succeeded.
	//
	// The tell is worth knowing: putting the rewrite above the fallback fails the
	// second subtest **only**. A
	// mutation that breaks one of two cases and not the other reads, at a glance,
	// like a flaky test rather than a caught bug.
	for _, c := range []struct {
		name       string
		flattening bool
	}{
		{"the flatten succeeded", true},
		{"the flatten failed and the config was rebuilt", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			content, err := minifyKubeconfig(loopbackConfig(!c.flattening), "docker-desktop", sessionAWSHome{}, nil)
			if err != nil {
				t.Fatalf("minifying: %v", err)
			}
			got, err := clientcmd.Load(content)
			if err != nil {
				t.Fatalf("the result does not parse as a kubeconfig: %v", err)
			}
			cluster := got.Clusters["docker-desktop"]
			if cluster == nil {
				t.Fatalf("the cluster is gone from the session kubeconfig: %+v", got.Clusters)
			}
			if strings.Contains(cluster.Server, "127.0.0.1") {
				t.Errorf("the session kubeconfig points at %q. Inside the container that is the "+
					"container, so kubectl in the terminal answers \"connection refused\" for a "+
					"cluster the UI beside it is listing pods from", cluster.Server)
			}
			if !strings.Contains(cluster.Server, "host.docker.internal") {
				t.Errorf("the session kubeconfig points at %q, which is neither the loopback nor "+
					"the host", cluster.Server)
			}
			// The other half, and it is not optional: the API server's certificate
			// carries "kubernetes" and nothing else, so redirecting the address on
			// its own turns a refused connection into a certificate error.
			if cluster.TLSServerName != "kubernetes" {
				t.Errorf("tls-server-name is %q, want \"kubernetes\" — the address was redirected "+
					"and the certificate name was not, which trades connection refused for a "+
					"TLS error and reads as the cluster's fault", cluster.TLSServerName)
			}
		})
	}
}

// The fallback path has to actually be the fallback path, or the test above is
// checking one branch twice.
//
// A kubeconfig naming a certificate file that is not there is what FlattenConfig
// refuses, and the session still has to open: the file paths are kept, which is
// worse than inlining them and far better than no terminal.
func TestAnUnreadableCertificateFallsBackRatherThanFailing(t *testing.T) {
	var logged []string
	content, err := minifyKubeconfig(loopbackConfig(true), "docker-desktop", sessionAWSHome{},
		func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) })
	if err != nil {
		t.Fatalf("an unreadable certificate refused the whole session: %v", err)
	}
	if len(content) == 0 {
		t.Fatal("the fallback produced an empty kubeconfig")
	}
	var said bool
	for _, line := range logged {
		if strings.Contains(line, "could not inline the certificates") {
			said = true
		}
	}
	if !said {
		t.Fatalf("the flatten did not fail, so the test above is exercising one branch twice: %v",
			logged)
	}
}
