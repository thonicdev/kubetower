package web

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/edit"
)

// **No plan family hands the client a wire slug to draw**, and these are the
// families that are not `specFor`'s.
//
// # What this guards
//
// `PlanDialog` draws `facts.button ?? facts.verb ?? 'Apply'`. A family with no
// verb spec behind it would reach the middle term of that chain: the wire slug
// on the loudest control in the console - `write` on a data
// write, `apply` on a manifest, `nodeshell` on a node shell, `discard` on a
// state clear, `empty` on the archive, `evict` on a drain's second stage.
//
// `empty` is easy to miss: it is a sixth family, found by
// walking every `api.PlanFacts{` in the tree - and had it been missed, dropping
// the fallback would have made *this* the control that says "Apply" on the act
// that destroys the only copy of the archive.
//
// # And none of these is `spec.Button`
//
// `specFor`'s sentences are written in the singular -
// "Delete it", "Evict it" - and they are the verb's words on one object, not
// this plan's. Each phrase below is composed for its own family, from what the
// server resolved for *this* plan: the key being written, the kind being
// applied, the node the shell opens on, the preferences resolved, the pods
// the drain would evict. Nothing is keyed on the verb's name, and nothing here
// decides the destructive treatment.
func TestNoPlanFamilyLeavesTheClientDrawingTheVerbSlug(t *testing.T) {
	ctx := context.Background()

	for _, one := range []struct {
		what  string
		facts func(t *testing.T) api.PlanFacts
		want  string
	}{
		{
			what: "a data write",
			facts: func(t *testing.T) api.PlanFacts {
				t.Helper()
				facts, err := writeHarness(t).PreviewData(ctx, "prod", api.DataWrite{
					Kind: "configmaps", Namespace: "argo", Name: "settings", ResourceVersion: "12",
					Entries: []api.DataWriteEntry{{Key: "settings.yaml", Value: "a: 2\n"}},
				})
				if err != nil {
					t.Fatalf("previewing a data write: %v", err)
				}
				return facts
			},
			want: "Write settings.yaml",
		},
		{
			what: "a manifest apply",
			facts: func(t *testing.T) api.PlanFacts {
				t.Helper()
				facts, err := writeHarness(t).PreviewManifest(ctx, "prod", api.ManifestWrite{
					Document:        editedConfigMapYAML,
					ResourceVersion: "12",
				})
				if err != nil {
					t.Fatalf("previewing a manifest apply: %v", err)
				}
				return facts
			},
			want: "Apply this ConfigMap",
		},
		{
			what: "a node shell",
			facts: func(t *testing.T) api.PlanFacts {
				t.Helper()
				acts := apiActs{s: &Server{claude: &fakeNodeShell{image: "busybox:1.36"}}}
				facts, err := acts.PreviewNodeShell(ctx, "prod", api.NodeShellRequest{Node: "node-a"})
				if err != nil {
					t.Fatalf("previewing a node shell: %v", err)
				}
				return facts
			},
			want: "Open a root shell on node-a",
		},
		{
			what:  "a state discard, over the two preferences",
			facts: clearFacts("preferences"),
			want:  "Discard 2 preferences",
		},
		{
			what:  "a state discard, over one",
			facts: clearFacts("clusterState"),
			want:  "Discard 1 preference",
		},
		{
			what:  "a state discard, over all of them",
			facts: clearFacts("all"),
			want:  "Discard 3 preferences",
		},
		{
			what: "emptying the archive",
			facts: func(t *testing.T) api.PlanFacts {
				t.Helper()
				facts, err := (apiSettings{&Server{archive: filledArchive()}}).PreviewClear("archive")
				if err != nil {
					t.Fatalf("previewing an archive clear: %v", err)
				}
				return facts
			},
			want: "Empty this archive",
		},
		{
			what: "a drain's second stage",
			facts: func(t *testing.T) api.PlanFacts {
				t.Helper()
				h := newActsHarness(t, fakeNode("node-a"), fakePod("prod", "api-1"), fakePod("prod", "api-2"))
				facts, err := h.acts.PreviewAction(ctx, "prod", api.ActionRequest{
					Verb:   string(actions.Drain),
					Target: api.PlanTarget{Kind: "nodes", Name: "node-a"},
					Stage:  actions.StageEvict,
				})
				if err != nil {
					t.Fatalf("previewing a drain's second stage: %v", err)
				}
				return facts
			},
			want: "Evict 2 pods",
		},
	} {
		t.Run(one.what, func(t *testing.T) {
			facts := one.facts(t)
			if facts.Button == "" {
				t.Fatalf("%s carries no button, so the control says the bare verb %q", one.what, facts.Verb)
			}
			if facts.Button == facts.Verb {
				t.Errorf("%s commits with %q, which is the wire slug", one.what, facts.Button)
			}
			if facts.Button != one.want {
				t.Errorf("%s commits with %q, and it should read %q", one.what, facts.Button, one.want)
			}
		})
	}
}

// clearFacts previews one scope of the state clear.
func clearFacts(scope string) func(*testing.T) api.PlanFacts {
	return func(t *testing.T) api.PlanFacts {
		t.Helper()
		facts, err := (apiSettings{&Server{}}).PreviewClear(scope)
		if err != nil {
			t.Fatalf("previewing a clear of %s: %v", scope, err)
		}
		return facts
	}
}

// The document the manifest preview is driven with: the stored ConfigMap below
// with one value changed, so the patch is not empty and the dry run has
// something to accept.
const editedConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: settings
  namespace: argo
  resourceVersion: "12"
data:
  settings.yaml: |
    a: 2
`

var fakeConfigMapGVR = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}

// fakeWriteResolver serves the one kind the two write paths are exercised on.
// `fakeActsResolver` beside it serves pods and nodes, which have no `data` and
// are refused by `internal/edit` before a button is composed.
type fakeWriteResolver struct{ dyn dynamic.Interface }

func (f fakeWriteResolver) Locate(_ context.Context, cluster, kind string) (edit.Location, error) {
	switch strings.ToLower(kind) {
	case "configmaps", "configmap":
		return edit.Location{
			Cluster: cluster, Resource: fakeConfigMapGVR, Kind: "ConfigMap",
			Namespaced: true, Dynamic: f.dyn,
		}, nil
	}
	return edit.Location{}, fmt.Errorf("%q is not a kind this cluster serves", kind)
}

// writeHarness is an `apiActs` with a write path over one ConfigMap.
func writeHarness(t *testing.T) apiActs {
	t.Helper()
	stored := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name": "settings", "namespace": "argo", "resourceVersion": "12",
		},
		"data": map[string]any{"settings.yaml": "a: 1\n"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{fakeConfigMapGVR: "ConfigMapList"}, stored)
	return apiActs{s: &Server{writer: edit.New(edit.Options{
		Resolve: fakeWriteResolver{dyn: dyn},
		Logf:    func(string, ...any) {},
		Access:  accesstest.Allow(),
	})}}
}

// fakeNodeShell answers the one question `PreviewNodeShell` asks of the
// terminal layer; the embedded interface satisfies the rest.
type fakeNodeShell struct {
	Terminals
	image string
}

func (f *fakeNodeShell) NodeShellImage() string { return f.image }
