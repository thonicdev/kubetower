package terminal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// **The session kubeconfig, out of a KUBECONFIG with several files in it.**
//
// This is the call site most likely to be got wrong, and
// the failure has no error in it worth reading. A session opened on a context
// that came from the *second* file, built from the first file alone, either
// fails inside `MinifyConfig` for a context the manager plainly knows about, or
// goes out with no current context and `kubectl` in a terminal that opened
// perfectly answers "no context is currently set". Both read as a broken
// cluster rather than as two code paths disagreeing about what the kubeconfig
// is.

// listKubeconfig writes a kubeconfig declaring one context whose server is
// derived from its name, so a merge can be checked by the address it produced.
func listKubeconfig(t *testing.T, path, name, server string) string {
	t.Helper()
	doc := fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: %[1]s
clusters:
- name: %[1]s
  cluster:
    server: %[2]s
    insecure-skip-tls-verify: true
contexts:
- name: %[1]s
  context:
    cluster: %[1]s
    user: %[1]s
users:
- name: %[1]s
  user:
    token: TOKEN-%[1]s
`, name, server)
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func TestASessionKubeconfigIsBuiltFromEveryFileInTheList(t *testing.T) {
	dir := t.TempDir()
	first := listKubeconfig(t, filepath.Join(dir, "first"), "from-first", "https://first.example:6443")
	second := listKubeconfig(t, filepath.Join(dir, "second"), "from-second", "https://second.example:6443")
	files := cluster.Kubeconfig{first, second}

	// Every context in the list, and the second one is the case that fails when
	// only the first file is read. The first is here so a mutation that reads
	// the *last* file instead is caught too — it would otherwise pass.
	for _, c := range []struct{ context, server string }{
		{"from-first", "https://first.example:6443"},
		{"from-second", "https://second.example:6443"},
	} {
		t.Run(c.context, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "kubeconfig")
			if err := writeSessionKubeconfig(files, c.context, dest, sessionAWSHome{}, nil); err != nil {
				t.Fatalf("writing a session kubeconfig for %q: %v\nThe manager resolves this "+
					"context out of the same KUBECONFIG, so a terminal that cannot be opened "+
					"on it is the merge having been lost on this path alone", c.context, err)
			}
			got, err := clientcmd.LoadFromFile(dest)
			if err != nil {
				t.Fatalf("reading it back: %v", err)
			}
			if got.CurrentContext != c.context {
				t.Fatalf("current-context is %q. kubectl in that terminal answers \"no context "+
					"is currently set\", which reads as a broken cluster", got.CurrentContext)
			}
			if got.Contexts[c.context] == nil {
				t.Fatalf("the context is not in the session kubeconfig: %v", got.Contexts)
			}
			if server := got.Clusters[c.context].Server; server != c.server {
				t.Errorf("the session points at %s, want %s — so the session and the console "+
					"disagree about which cluster this name means", server, c.server)
			}
		})
	}
}

// **The loopback correction after the merge.**
//
// The loopback rewrite is the last thing before `clientcmd.Write`, and the
// config comes out of client-go's merge rather than out of one
// `LoadFromFile`, and the flatten fallback rebuilds it from that merge. What
// matters is what happens after the correction, not whether it is present, and
// the merge sits before it — so this drives the whole of writeSessionKubeconfig
// on a loopback cluster that lives in the **second** file, which is the
// combination neither the loopback tests (they call minifyKubeconfig with a
// config in hand) nor the list tests above cover.
func TestTheLoopbackRewriteSurvivesTheMerge(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "1")

	dir := t.TempDir()
	first := listKubeconfig(t, filepath.Join(dir, "first"), "remote", "https://remote.example:6443")
	second := listKubeconfig(t, filepath.Join(dir, "second"), "docker-desktop", "https://127.0.0.1:6443")

	dest := filepath.Join(t.TempDir(), "kubeconfig")
	if err := writeSessionKubeconfig(cluster.Kubeconfig{first, second}, "docker-desktop", dest,
		sessionAWSHome{}, nil); err != nil {
		t.Fatalf("writing the session kubeconfig: %v", err)
	}
	got, err := clientcmd.LoadFromFile(dest)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}

	c := got.Clusters["docker-desktop"]
	if c == nil {
		t.Fatalf("the cluster is gone from the session kubeconfig: %v", got.Clusters)
	}
	if strings.Contains(c.Server, "127.0.0.1") {
		t.Errorf("the session kubeconfig points at %q. Inside the container that is the "+
			"container: the loopback correction did not survive the merge", c.Server)
	}
	if !strings.Contains(c.Server, "host.docker.internal") {
		t.Errorf("the session kubeconfig points at %q, neither the loopback nor the host",
			c.Server)
	}
	// Both halves, always: redirecting the address alone trades a refused
	// connection for a certificate error, which reads as the cluster's fault.
	if c.TLSServerName != "kubernetes" {
		t.Errorf("tls-server-name is %q, want \"kubernetes\"", c.TLSServerName)
	}
}

// A KUBECONFIG entry that is not there is skipped, here as everywhere else — a
// terminal must not refuse to open because one of five paths is for another
// machine.
func TestAMissingFileDoesNotStopASessionOpening(t *testing.T) {
	dir := t.TempDir()
	good := listKubeconfig(t, filepath.Join(dir, "good"), "works", "https://works.example:6443")
	files := cluster.Kubeconfig{filepath.Join(dir, "not-here"), good}

	dest := filepath.Join(t.TempDir(), "kubeconfig")
	if err := writeSessionKubeconfig(files, "works", dest, sessionAWSHome{}, nil); err != nil {
		t.Fatalf("a missing sibling refused the whole session: %v", err)
	}
}

// **A list handed to a child process is joined for the child, not for a reader.**
//
// `Kubeconfig` has two joinings and they are not interchangeable: `Env()` uses
// the platform's path-list separator because that is what `KUBECONFIG` means to
// kubectl, and `String()` uses commas because that is what a person reads in a
// log line. Handing kubectl the comma form names a single file that does not
// exist.
//
// **This test pins the call site.** `internal/cluster` asserts what `Env()`
// produces, and swapping the *call site* here for `String()` would leave every
// one of those assertions green: a helper asserted and its callers not. There is
// no seam: the one call site sits inside a `sync.Once` around a `kubectl` this
// host need not have, and its answer is deliberately discarded on failure. So
// the source is read, the way
// `TestEverySessionKubeconfigIsWrittenWithASessionAWSHome` reads it next door.
//
// Today the skew probe is the only child handed the whole list, and it carries
// `--client` so it reads no kubeconfig at all — which makes the mutation
// harmless *there* and is precisely why it must not be left in the tree for the
// next child process to copy.
func TestAKubeconfigListHandedToAChildProcessIsJoinedForTheChild(t *testing.T) {
	entries, err := filepath.Glob(filepath.Join(".", "*.go"))
	if err != nil {
		t.Fatalf("listing the package: %v", err)
	}

	// The list is only ever held in two places in this package, so those are the
	// two spellings a `String()` can appear on. Matching the *receiver* rather
	// than the word KUBECONFIG is what keeps the session kubeconfigs out of it:
	// `"KUBECONFIG="+spec.kubeconfig` is one file by construction, which is the
	// whole point of writing it.
	forbidden := []string{"s.kubeconfig.String()", "opts.Kubeconfig.String()",
		"t.opts.Kubeconfig.String()"}
	joined := false
	for _, path := range entries {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if strings.Contains(line, "s.kubeconfig.Env()") {
				joined = true
			}
			for _, bad := range forbidden {
				if strings.Contains(line, bad) {
					t.Errorf("%s:%d joins the kubeconfig list the way a log line does:\n\t%s\n"+
						"KUBECONFIG is read with os.PathListSeparator, so the comma form "+
						"names one file that does not exist. Kubeconfig.Env() is the "+
						"joining a program reads; Kubeconfig.String() is the one a person "+
						"reads.", path, i+1, trimmed)
				}
			}
		}
	}
	if !joined {
		t.Error("nothing in this package joins the kubeconfig list for a child process any " +
			"more, so this test has stopped looking at anything. If the skew probe no " +
			"longer sets KUBECONFIG, delete this rather than leaving a guard over nothing")
	}
}

// The wiring: every builder of a session hands over the **whole** list.
//
// `Options.Kubeconfig` being a `cluster.Kubeconfig` makes this structural today,
// which is the strongest form — but a builder written later can still pass
// `t.opts.Kubeconfig[:1]`, or a list of its own, and it would compile and pass
// every test above. The same gesture, and the same reason, as
// TestEverySessionKubeconfigIsWrittenWithASessionAWSHome next door: the defect
// is an absence, and an absence has no code path to drive.
func TestEverySessionKubeconfigIsWrittenFromTheWholeList(t *testing.T) {
	entries, err := filepath.Glob(filepath.Join(".", "*.go"))
	if err != nil {
		t.Fatalf("listing the package: %v", err)
	}

	calls := 0
	for _, path := range entries {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if !strings.Contains(line, "writeSessionKubeconfig(") {
				continue
			}
			if strings.Contains(line, "func writeSessionKubeconfig") ||
				strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			calls++
			if !strings.Contains(line, "writeSessionKubeconfig(t.opts.Kubeconfig,") {
				t.Errorf("%s:%d does not build its session from the whole kubeconfig list:\n\t%s\n"+
					"A subset means a session opened on a context from any other file gets a "+
					"kubeconfig without it, and kubectl there answers \"no context is "+
					"currently set\" — which reads as a broken cluster.",
					path, i+1, strings.TrimSpace(line))
			}
		}
	}
	if calls < 2 {
		t.Fatalf("found %d session-kubeconfig call sites, expected at least the shell's and "+
			"Claude's — this test has stopped looking at anything", calls)
	}
}

// profile answers the one question sessionKubeconfig asks of the manager.
type profile bool

func (p profile) InCluster() bool { return bool(p) }

// Under the server profile the terminal copies no context out of `KUBECONFIG`.
// The manager's cluster is the pod's own and has no kubeconfig, and the startup
// line says `KUBECONFIG` is not read - which would be false if an empty list here
// fell back to the environment, because a session would then reach whatever
// file the variable named inside the pod.
func TestTheServerProfileCopiesNoContextOutOfKUBECONFIG(t *testing.T) {
	named := filepath.Join(t.TempDir(), "somebody-elses")
	t.Setenv(cluster.EnvKubeconfig, named)

	if got := sessionKubeconfig(profile(true), nil); len(got) != 0 {
		t.Errorf("in-cluster: the session kubeconfig is %v, want none - %s is not read under the profile",
			got, cluster.EnvKubeconfig)
	}
	if got := sessionKubeconfig(profile(false), nil); len(got) != 1 || got[0] != named {
		t.Errorf("desktop: the session kubeconfig is %v, want %s from the environment", got, named)
	}
	given := cluster.Kubeconfig{"handed-in"}
	for _, inCluster := range []bool{true, false} {
		if got := sessionKubeconfig(profile(inCluster), given); len(got) != 1 || got[0] != "handed-in" {
			t.Errorf("inCluster=%v: a list handed in became %v", inCluster, got)
		}
	}
	// And what an empty list does with it: refuses, rather than reading a default.
	if _, err := sessionKubeconfig(profile(true), nil).Load(); err == nil {
		t.Error("an empty session kubeconfig loaded something")
	}
}
