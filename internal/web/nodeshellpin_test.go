package web

import (
	"context"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/nodeshell"
)

// **The plan names the image by digest.** The node shell's pod is privileged
// with the node's `/`
// mounted; the dialog that asks for that is the one place the operator reads
// what will run, and since the default is pinned it names the exact image,
// digest and all, rather than a tag the registry could repoint.
func TestTheNodeShellPlanNamesTheImageByDigest(t *testing.T) {
	acts := apiActs{s: &Server{claude: &fakeNodeShell{image: nodeshell.DefaultImage}}}
	facts, err := acts.PreviewNodeShell(context.Background(), "prod", api.NodeShellRequest{Node: "node-a"})
	if err != nil {
		t.Fatalf("previewing a node shell: %v", err)
	}
	if len(facts.Targets) != 1 {
		t.Fatalf("the plan has %d targets, want the one node", len(facts.Targets))
	}
	note := facts.Targets[0].Note
	if !strings.Contains(note, nodeshell.DefaultImage) || !strings.Contains(note, "@sha256:") {
		t.Errorf("the plan's note %q does not name the pinned image %q", note, nodeshell.DefaultImage)
	}
}
