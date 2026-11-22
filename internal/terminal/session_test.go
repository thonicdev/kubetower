package terminal

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/cluster"
)

func envValue(env []string, key string) (string, bool) {
	// Last wins, as it does in exec: the caller's own environment is the base
	// and these overrides are appended.
	value, found := "", false
	for _, entry := range env {
		if after, ok := strings.CutPrefix(entry, key+"="); ok {
			value, found = after, true
		}
	}
	return value, found
}

// The shims are written whether or not any cluster is read-only. This is the
// only thing that decides whether the operator's shell ever resolves kubectl to
// one of them, and a correct shim on a PATH that does not include it protects
// nothing.
func TestSessionEnvPutsTheOverridesOnPathOnlyForAReadOnlyCluster(t *testing.T) {
	base := []string{"PATH=/usr/bin"}
	roBin := filepath.Join("/tmp", "kubetower", "ro-bin")

	writable := sessionEnv(base, sessionSpec{
		cluster: "docker-desktop", kubeconfig: "/tmp/k", roBinDir: roBin, path: "/usr/bin",
	})
	path, _ := envValue(writable, "PATH")
	if strings.Contains(path, roBin) {
		t.Errorf("a writable cluster got the read-only overrides on PATH: %q", path)
	}
	if _, set := envValue(writable, "KT_READONLY"); set {
		t.Error("KT_READONLY was set on a writable cluster")
	}

	guarded := sessionEnv(base, sessionSpec{
		cluster: "prod", kubeconfig: "/tmp/k", readOnly: true, roBinDir: roBin, path: "/usr/bin",
	})
	path, _ = envValue(guarded, "PATH")
	// Prepended, not appended: appended, the real kubectl comes first and the
	// shim never runs.
	if !strings.HasPrefix(path, roBin+string(os.PathListSeparator)) {
		t.Errorf("the overrides are not first on PATH: %q", path)
	}
	if v, _ := envValue(guarded, "KT_READONLY"); v != "1" {
		t.Errorf("KT_READONLY is %q on a read-only cluster", v)
	}
}

// Two tabs on two clusters is the normal way this is used, and the whole point
// of a per-session kubeconfig is that neither can move the other's context.
func TestSessionEnvPointsAtTheSessionKubeconfig(t *testing.T) {
	env := sessionEnv(
		[]string{"KUBECONFIG=/home/kt/.kube/config", "PATH=/usr/bin"},
		sessionSpec{cluster: "prod", kubeconfig: "/tmp/kubetower/term-abc/kubeconfig", path: "/usr/bin"},
	)

	if got, _ := envValue(env, "KUBECONFIG"); got != "/tmp/kubetower/term-abc/kubeconfig" {
		t.Errorf("KUBECONFIG is %q, expected the session copy", got)
	}
	if got, _ := envValue(env, "KT_CLUSTER"); got != "prod" {
		t.Errorf("KT_CLUSTER is %q", got)
	}
	// xterm-256color and not the inherited TERM: whatever the container was
	// started with has nothing to do with what xterm.js can render.
	if got, _ := envValue(env, "TERM"); got != "xterm-256color" {
		t.Errorf("TERM is %q", got)
	}
}

// A terminal with nothing to connect to is a startup mistake, not a per-request
// one: failing here is a log line the operator reads, failing later is a button
// that does nothing.
func TestNewRefusesANilClusterManager(t *testing.T) {
	if _, err := New(nil, Options{WorkRoot: t.TempDir()}); err == nil {
		t.Fatal("New accepted a nil cluster manager")
	}
}

func TestNewAppliesItsDefaultsAndWritesTheOverridesAtStartup(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "kubeconfig")
	if err := os.WriteFile(source, []byte(oneContextKubeconfig), 0o600); err != nil {
		t.Fatalf("seeding a kubeconfig: %v", err)
	}

	manager, err := cluster.NewManager(source)
	if err != nil {
		t.Fatalf("building a cluster manager: %v", err)
	}

	work := filepath.Join(root, "work")
	term, err := New(manager, Options{
		Kubeconfig: cluster.Kubeconfig{source},
		WorkRoot:   work,
		Logf:       func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Without --norc the image's /etc/bash.bashrc runs and resets PS1, and the
	// cluster name — the one thing on screen saying which cluster these commands
	// will hit — disappears from the prompt. And that is not the only reason:
	// `sessionEnv` prepends the override directory to PATH and that
	// only wins because nothing later re-points it, so a shell that reads an
	// init file is a shell whose read-only guardrail a profile can undo.
	//
	// **Per platform**, because the flags are, and this asserts
	// the property rather than the words: the Windows default is
	// `pwsh -NoLogo -NoProfile`, which carries both of those reasons across, and
	// asserting `--norc` there would be asserting that the Windows shell is bash.
	// resolveDefaultShell in shell_windows.go / shell_other.go says which flag
	// stands in for which reason.
	shell := strings.Join(term.opts.Shell, " ")
	noInitFlags := []string{"--norc", "--noprofile", "-i"}
	if runtime.GOOS == "windows" {
		noInitFlags = []string{"-NoProfile"}
	}
	for _, flag := range noInitFlags {
		if !strings.Contains(shell, flag) {
			t.Errorf("the default shell command line %q is missing %s, so a profile or an rc file "+
				"can re-point PATH and take the read-only overrides off it", shell, flag)
		}
	}

	// The overrides are rendered at startup, not per session: a session that had
	// to write them could fail to open for a reason the operator cannot act on.
	if _, err := os.Stat(filepath.Join(work, "ro-bin")); err != nil {
		t.Errorf("the override directory was not created: %v", err)
	}

	// No cluster is read-only unless somebody says so, and the default must not
	// be "everything", which would refuse every write with no way to tell why.
	if term.opts.ReadOnly("anything") {
		t.Error("a cluster was read-only by default")
	}
}

// A kubeconfig the cluster manager will load without reaching a network.
const oneContextKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: docker-desktop
  cluster:
    server: https://127.0.0.1:6443
users:
- name: docker-desktop
  user:
    token: not-a-real-token
contexts:
- name: docker-desktop
  context:
    cluster: docker-desktop
    user: docker-desktop
current-context: docker-desktop
`

// Both callers of sessionEnv pass os.Environ(), and this process's environment
// holds the key every session cookie is signed with, which `env` in any
// terminal would print. The operator's own shell owning the container
// makes that survivable; the Claude session, whose transcript leaves the machine,
// does not.
//
// Asserted on the prefix rather than on the three names, because a denylist of
// names is right until the fourth secret is added and then silently wrong.
func TestSessionEnvHandsNoKubeTowerVariableToTheShell(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"HOME=/home/kubetower",
		"AWS_PROFILE=prod",
		"KT_SESSION_SECRET=0123456789abcdef0123456789abcdef",
		"KT_PASSWORD_HASH=$2a$14$saltsaltsaltsaltsaltsa",
		// Deliberately a variable this process does not set. The property being
		// asserted is the *prefix*, not a list of names, and a fixture built only
		// from variables that exist today cannot show that.
		"KT_FUTURE_SECRET=deadbeef",
		"KT_STATE_DIR=/var/lib/kubetower",
	}

	env := sessionEnv(base, sessionSpec{
		cluster: "docker-desktop", kubeconfig: "/tmp/k", path: "/usr/bin",
	})

	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "KT_CLUSTER", "KT_READONLY":
			// Set by sessionEnv itself, deliberately and visibly.
			continue
		}
		if strings.HasPrefix(name, "KT_") {
			t.Errorf("an inherited KubeTower variable reached the shell: %q", entry)
		}
	}

	// The secrets must be gone by value too, not merely shadowed by a later
	// assignment: `env` prints every entry, and shells differ on which wins.
	for _, leaked := range []string{"0123456789abcdef0123456789abcdef", "deadbeef", "$2a$14$"} {
		for _, entry := range env {
			if strings.Contains(entry, leaked) {
				t.Errorf("a secret survived as %q", entry)
			}
		}
	}

	// What the terminal is for must survive. An EKS context authenticates by
	// running `aws eks get-token`, so dropping AWS_* would break every EKS tab.
	for _, needed := range []string{"AWS_PROFILE", "HOME", "PATH"} {
		if _, set := envValue(env, needed); !set {
			t.Errorf("%s was dropped, and the terminal needs it", needed)
		}
	}
}
