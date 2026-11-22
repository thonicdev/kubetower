package web

// The landing page must not report an unreadable kubeconfig as an empty one.
//
// `Manager.Contexts()` is a bare walk of the merged config, so a file that
// failed to parse contributes nothing and zero comes back looking exactly like
// an operator who has never configured a cluster. The two need opposite acts:
// one writes a kubeconfig, the other fixes the one they have, at a line number
// the parser already named.
//
// The branch lives on the clusters endpoint, whose own contract
// (`internal/api/backend.go` on `Clusters.List`) says: *"The envelope's state covers
// reading the configuration - a KUBECONFIG naming five files, one of which
// cannot be parsed, is `unreadable` here, and that is a different fact from five
// clusters that are all down."*

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// A kubeconfig that parses and declares nothing. Valid YAML, valid Config, no
// contexts - the genuine "you have not set one up yet".
const emptyKubeconfig = `apiVersion: v1
kind: Config
clusters: []
contexts: []
users: []
`

// And one that does not parse at all. The shape a half-written merge or a
// truncated write leaves behind.
const brokenKubeconfig = `apiVersion: v1
kind: Config
contexts:
  - name: prod
   context: {cluster: c, user: u}
     bad indentation all the way down
`

// clustersOver builds a server whose manager holds `configured` and whose setup
// survey describes `surveyed`, and returns the clusters endpoint's answer.
//
// Through `Server.Clusters`, the in-process door the macOS window's Cluster
// menu reads, which is `apiClusters.List` itself: every
// case below is then also a case of the menu's empty-versus-unreadable split.
func clustersOver(t *testing.T, configured string, surveyed ...string) ([]api.Cluster, *api.Failure) {
	t.Helper()

	manager, err := cluster.NewManager(configured)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	server, err := New(Options{
		Clusters: manager,
		Setup:    SetupInfo{Kubeconfig: cluster.Kubeconfig(surveyed).Survey()},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return server.Clusters(context.Background())
}

func write(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

func TestAKubeconfigThatWouldNotParseIsNotAKubeconfigWithNoClusters(t *testing.T) {
	good := write(t, "config", emptyKubeconfig)
	broken := write(t, "broken", brokenKubeconfig)

	items, fail := clustersOver(t, good, good, broken)
	if fail == nil {
		t.Fatalf("List = %v, nil; a file that would not parse came back as an empty list, which is the whole finding", items)
	}
	if fail.State != api.StateUnreadable {
		t.Errorf("state = %q, want %q", fail.State, api.StateUnreadable)
	}
	if fail.Reason.Code != api.CodeUndecodable {
		t.Errorf("code = %q, want %q", fail.Reason.Code, api.CodeUndecodable)
	}
	// The path, because "a kubeconfig failed to parse" is not actionable when
	// KUBECONFIG names three files.
	if !strings.Contains(fail.Reason.Message, filepath.Base(broken)) {
		t.Errorf("message = %q, want it to name %s", fail.Reason.Message, broken)
	}
	// And the parser's own words, which carry the line number.
	if fail.Reason.Detail == "" {
		t.Errorf("detail is empty; the parser said where the file stops being a kubeconfig and this drops it")
	}
	// Not retryable: every client was built from one merge at startup, so a file
	// fixed now is picked up on a restart and a retry button would return the
	// identical answer.
	if fail.Reason.Retryable != nil && *fail.Reason.Retryable {
		t.Errorf("retryable = true; nothing about this answer changes until the console restarts")
	}
}

// The other half of the distinction. A kubeconfig that read perfectly well and
// declares no contexts is an empty list and not a failure, and reporting it as
// one would be the same collapse pointing the other way.
func TestAKubeconfigThatReadsAndDeclaresNothingIsStillEmpty(t *testing.T) {
	good := write(t, "config", emptyKubeconfig)

	items, fail := clustersOver(t, good, good)
	if fail != nil {
		t.Fatalf("List = _, %+v; nothing failed - there is genuinely no cluster configured", fail)
	}
	if len(items) != 0 {
		t.Errorf("List = %v, want none", items)
	}
}

// A file named by KUBECONFIG that is simply not there is skipped by the merge,
// exactly as kubectl skips it, and is not a problem. `Survey` keeps `Absent`
// apart from `Problem` for this reason and the branch has to honor it, or
// naming a path that does not exist yet would take the console down.
func TestAKubeconfigFileThatIsNotThereIsNotAFailure(t *testing.T) {
	good := write(t, "config", emptyKubeconfig)
	missing := filepath.Join(t.TempDir(), "never-written")

	if _, fail := clustersOver(t, good, good, missing); fail != nil {
		t.Errorf("List = _, %+v; an absent file is skipped by the merge and is not a failure", fail)
	}
}
