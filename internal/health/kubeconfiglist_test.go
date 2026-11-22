package health

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// What `/kt-healthz` degraded means when KUBECONFIG is a list.
//
// With one file "the kubeconfig no longer reads" has one reading. With five it
// has two, and the answer is a choice — see kubeconfigProblem for the argument.
// These tests are that decision, one case per arm, and the third of them is the
// one that keeps the decision from being the easy wrong one.

// kubeconfigNaming writes a kubeconfig declaring one context, at a chosen path.
func kubeconfigNaming(t *testing.T, path, name string) string {
	t.Helper()
	doc := fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: %[1]s
clusters:
- name: %[1]s
  cluster:
    server: https://%[1]s.example:6443
    insecure-skip-tls-verify: true
contexts:
- name: %[1]s
  context:
    cluster: %[1]s
    user: %[1]s
users:
- name: %[1]s
  user:
    token: TEST-TOKEN
`, name)
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// **One broken file of several is degraded, and the reason names it.**
//
// The weak reading — degrade only when *none* of them reads — is wrong, and
// wrong in the reassuring direction: four files broken and one still reading is
// four fifths of the operator's clusters gone from the rail behind a page that
// draws the fifth and says nothing. Worse than the usual failed answer drawn as
// an empty one, because the rail cannot even name what is missing: it never saw
// those contexts.
//
// And the reason has to carry the path. "The kubeconfig no longer reads" over
// five files is a sentence nobody can act on, and being actionable from
// `docker inspect` is the whole job of this endpoint.
func TestOneBrokenFileOfSeveralIsDegradedAndTheReasonNamesIt(t *testing.T) {
	dir := t.TempDir()
	good := kubeconfigNaming(t, filepath.Join(dir, "good"), "works")
	broken := filepath.Join(dir, "broken")
	if err := os.WriteFile(broken, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	files := cluster.Kubeconfig{good, broken}
	h := New(Options{Kubeconfig: files})

	// Broken *after* the snapshot, which is the real sequence: a mount that goes
	// away, or a file half-rewritten by an editor, while the process runs.
	if err := os.WriteFile(broken, []byte("this is not a kubeconfig: {{{"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := h.Readiness(context.Background())
	if got.OK {
		t.Fatalf("one of two kubeconfig files stopped parsing and the probe still reported "+
			"healthy: %s", got.Reason)
	}
	if !strings.Contains(got.Reason, broken) {
		t.Errorf("the reason is %q and does not name the file that broke. Naming which of "+
			"several files went is the entire value of reading several", got.Reason)
	}
	if strings.Contains(got.Reason, good) {
		t.Errorf("the reason names %q, which is fine, so an operator is sent to look at a "+
			"healthy file: %s", good, got.Reason)
	}
}

// **A path that was never there does not degrade the probe.**
//
// `Precedence` skips a missing file exactly as kubectl does, and a `KUBECONFIG`
// naming a file that lives on the operator's other laptop is a line somebody
// wrote on purpose. Degrading over it would restart the container for a
// deliberate configuration — and would do it on every start, which is how a
// health endpoint stops being believed.
//
// This is the arm that makes the decision above defensible rather than merely
// strict, so it is asserted rather than left as a comment.
func TestAPathThatWasNeverThereDoesNotDegradeTheProbe(t *testing.T) {
	dir := t.TempDir()
	good := kubeconfigNaming(t, filepath.Join(dir, "good"), "works")
	never := filepath.Join(dir, "other-laptop")

	clusters, err := cluster.NewManager(good, never)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	h := New(Options{Clusters: clusters, Kubeconfig: cluster.Kubeconfig{good, never}})
	h.cache["works"] = Cluster{Name: "works", State: StateOK, CheckedAt: h.now()}

	got := h.Readiness(context.Background())
	if !got.OK {
		t.Errorf("a KUBECONFIG entry that has never existed made the process degraded: %q. "+
			"kubectl says nothing about a missing file, and neither may this — the "+
			"container would be restarted over a line the operator wrote deliberately",
			got.Reason)
	}
}

// **A file that was readable at startup and has gone is degraded.**
//
// The vanished mount: the mount went away, the container keeps listening, and
// the one thing this process exists to do is impossible. It is the asymmetry
// that makes the arm above safe — "absent" is only benign when it was always
// absent — and it is why `New` takes a snapshot rather than deciding from what
// it can see now.
func TestAFileThatWasReadAtStartupAndHasGoneIsDegraded(t *testing.T) {
	dir := t.TempDir()
	stays := kubeconfigNaming(t, filepath.Join(dir, "stays"), "stays")
	vanishes := kubeconfigNaming(t, filepath.Join(dir, "vanishes"), "vanishes")

	h := New(Options{Kubeconfig: cluster.Kubeconfig{stays, vanishes}})

	if err := os.Remove(vanishes); err != nil {
		t.Fatal(err)
	}

	got := h.Readiness(context.Background())
	if got.OK {
		t.Fatalf("a kubeconfig file that was read at startup vanished and the probe stayed "+
			"healthy: %s. That is the mount going away, and the reason this "+
			"endpoint is allowed to fail at all", got.Reason)
	}
	if !strings.Contains(got.Reason, vanishes) {
		t.Errorf("the reason is %q and does not name the file that went", got.Reason)
	}
}

// Nothing readable at all, over a list. Named as the list's problem because it
// is not one file's, and degraded rather than drawn as a kubeconfig with no
// clusters in it — which is what an empty bind mount looks like.
func TestNotOneFileOfTheListBeingThereIsDegraded(t *testing.T) {
	dir := t.TempDir()
	files := cluster.Kubeconfig{filepath.Join(dir, "one"), filepath.Join(dir, "two")}

	got := New(Options{Kubeconfig: files}).Readiness(context.Background())
	if got.OK {
		t.Fatalf("a KUBECONFIG whose every file is missing reported healthy: %s", got.Reason)
	}
	for _, want := range files {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("the reason %q does not name %s", got.Reason, want)
		}
	}
}

// The wiring, and it is the half a helper test cannot see.
//
// `readableNow` and `kubeconfigProblem` can both be perfect while `New` records
// nothing — the map would be empty, every absence would read as "never there",
// and the vanished-mount arm above would be unreachable. There is no seam to observe
// that through, so this asserts the snapshot itself.
func TestTheStartupSnapshotIsTakenAndHoldsTheFilesThatRead(t *testing.T) {
	dir := t.TempDir()
	good := kubeconfigNaming(t, filepath.Join(dir, "good"), "works")
	never := filepath.Join(dir, "never")

	h := New(Options{Kubeconfig: cluster.Kubeconfig{good, never}})
	if !h.kubeconfigRead[good] {
		t.Errorf("the file that read at startup is not in the snapshot (%v), so its "+
			"disappearance would be indistinguishable from a path nobody ever had",
			h.kubeconfigRead)
	}
	if h.kubeconfigRead[never] {
		t.Errorf("a path that was never there is recorded as having read (%v), so a "+
			"deliberate KUBECONFIG entry for another machine degrades the process",
			h.kubeconfigRead)
	}
}
