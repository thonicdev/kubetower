package detail

// A CRD installed after the mapper was built, as the operator meets it, from
// this side of the drawer.
//
// The cache's own behavior is pinned in `internal/kindref/cache_test.go`. This
// file exists because a predicate tested alone says nothing about its call
// sites: the drawer calls `Reader.Get` and `internal/web/edit.go` calls
// `Reader.Locate`, and a regression on either would be invisible until a CRD
// was installed.

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"os"
	"path/filepath"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// readerWithMapper is readerFor with the mapper under the test's control, so a
// mapper that does not yet know a kind and then does can be expressed without a
// cluster — which is the only way this case can be reached at all: installing a
// CRD to reproduce it is a mutation of a real cluster, which no test here
// makes.
func readerWithMapper(t *testing.T, build func() (meta.RESTMapper, error), objects ...runtime.Object) *Reader {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config")
	body := `apiVersion: v1
kind: Config
clusters:
- name: nowhere
  cluster:
    server: https://127.0.0.1:1
contexts:
- name: prod
  context: {cluster: nowhere, user: nobody}
users:
- name: nobody
  user: {token: none}
current-context: prod
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing a kubeconfig: %v", err)
	}
	clusters, err := cluster.NewManager(path)
	if err != nil {
		t.Fatalf("loading the kubeconfig: %v", err)
	}

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			{Version: "v1", Resource: "pods"}:                                        "PodList",
			{Version: "v1", Resource: "events"}:                                      "EventList",
			{Group: "argoproj.io", Version: "v1alpha1", Resource: "applicationsets"}: "ApplicationSetList",
		},
		objects...,
	)

	reader := NewReader(clusters, accesstest.Allow())
	reader.newMapper = func(context.Context, *cluster.Client) (meta.RESTMapper, error) { return build() }
	reader.dynamicOf = func(*cluster.Client) (dynamic.Interface, error) { return dyn, nil }
	return reader
}

// mapperKnowing builds a mapper that serves exactly these kinds and nothing
// else — a cluster before, or after, somebody installed an operator.
func mapperKnowing(kinds ...schema.GroupVersionKind) meta.RESTMapper {
	groups := make([]schema.GroupVersion, 0, len(kinds))
	for _, k := range kinds {
		groups = append(groups, k.GroupVersion())
	}
	m := meta.NewDefaultRESTMapper(groups)
	for _, k := range kinds {
		m.Add(k, meta.RESTScopeNamespace)
	}
	return m
}

var (
	podKind    = schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
	appSetKind = schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "ApplicationSet"}
)

func applicationSet(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "ApplicationSet",
		"metadata": map[string]any{"name": name, "namespace": namespace, "resourceVersion": "1"},
	}}
}

// The three answers a kind can have, through the call the drawer actually makes.
//
// The middle row is the one a naive "always rebuild" gets wrong in the other
// direction: a typo must be refused, and refused after a bounded number of
// discovery walks, not retried until it comes true.
func TestTheDrawerResolvesAKindTheMapperKnows_DoesNotKnow_AndDoesNotKnowYet(t *testing.T) {
	for _, tc := range []struct {
		name string
		// afterInstall is the mapper every build after the first returns.
		afterInstall []schema.GroupVersionKind
		kind         string
		namespace    string
		object       string
		wantErr      string
		wantResource string
		wantBuilds   int
	}{
		{
			name:         "a kind the mapper knows costs one build and resolves",
			afterInstall: []schema.GroupVersionKind{podKind},
			kind:         "pods",
			namespace:    "shop",
			object:       "api-1",
			wantResource: "pods",
			wantBuilds:   1,
		},
		{
			name:         "a kind nothing will ever serve is refused after one rebuild",
			afterInstall: []schema.GroupVersionKind{podKind},
			kind:         "wibbles.example.com",
			namespace:    "shop",
			object:       "whatever",
			wantErr:      "not a kind this cluster serves",
			wantBuilds:   2,
		},
		{
			name:         "a CRD installed since the mapper was built resolves after one rebuild",
			afterInstall: []schema.GroupVersionKind{podKind, appSetKind},
			kind:         "applicationsets.argoproj.io",
			namespace:    "argocd",
			object:       "platform",
			wantResource: "applicationsets",
			wantBuilds:   2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var builds int
			reader := readerWithMapper(t, func() (meta.RESTMapper, error) {
				builds++
				if builds == 1 {
					return mapperKnowing(podKind), nil // before the operator installed Argo CD
				}
				return mapperKnowing(tc.afterInstall...), nil
			}, applicationSet("argocd", "platform"))

			// Warm the cache the way any other panel in the console would, so
			// the mapper under test is a *held* one rather than a fresh one.
			if _, err := reader.Locate(t.Context(), "prod", "pods"); err != nil {
				t.Fatalf("warming the cache with a kind every cluster serves: %v", err)
			}

			where, err := reader.Locate(t.Context(), "prod", tc.kind)
			switch {
			case tc.wantErr != "":
				if err == nil {
					t.Fatalf("%q resolved; it names nothing this cluster serves", tc.kind)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
				}
			case err != nil:
				t.Fatalf("Locate(%q): %v\n"+
					"Without the rebuild this is what a CRD installed in the last ten "+
					"minutes looks like — the list drew the object and the drawer refuses it", tc.kind, err)
			default:
				if where.Resource.Resource != tc.wantResource {
					t.Errorf("resolved to %q, want %q — the group belongs in its own field",
						where.Resource.Resource, tc.wantResource)
				}
			}

			if builds != tc.wantBuilds {
				t.Errorf("built the mapper %d times, want %d", builds, tc.wantBuilds)
			}

			// And a second ask costs nothing more: the rebuilt mapper is the one
			// now held, and a kind that does not exist is not retried forever.
			before := builds
			_, _ = reader.Locate(t.Context(), "prod", tc.kind)
			if tc.wantErr == "" && builds != before {
				t.Errorf("built the mapper again on the second ask (%d → %d); the rebuild was not kept",
					before, builds)
			}
			if builds > before+1 {
				t.Errorf("one ask cost %d builds; discovery is dozens of round trips and this is per click",
					builds-before)
			}
		})
	}
}

// The Location carries the mapper it *resolved with*, not the stale one it first
// tried. relations.go builds its resolver from that field, so handing back the
// pre-install mapper would give a freshly installed CRD a drawer whose Related
// panel cannot resolve the very kind the drawer is showing.
func TestTheLocationCarriesTheRebuiltMapper(t *testing.T) {
	var builds int
	reader := readerWithMapper(t, func() (meta.RESTMapper, error) {
		builds++
		if builds == 1 {
			return mapperKnowing(podKind), nil
		}
		return mapperKnowing(podKind, appSetKind), nil
	}, applicationSet("argocd", "platform"))

	if _, err := reader.Locate(t.Context(), "prod", "pods"); err != nil {
		t.Fatalf("warming the cache: %v", err)
	}

	where, err := reader.Locate(t.Context(), "prod", "applicationsets.argoproj.io")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if where.mapper == nil {
		t.Fatal("the Location carries no mapper, so relations.go has nothing to resolve with")
	}
	if _, err := where.mapper.KindFor(schema.GroupVersionResource{
		Group: "argoproj.io", Resource: "applicationsets",
	}); err != nil {
		t.Errorf("the Location carries the mapper from before the CRD existed: %v", err)
	}
}

// The drawer's own entry point, end to end: Get on an object of a kind the held
// mapper has never heard of returns the object rather than a refusal.
func TestTheDrawerOpensAnObjectOfAFreshlyInstalledCRD(t *testing.T) {
	var builds int
	reader := readerWithMapper(t, func() (meta.RESTMapper, error) {
		builds++
		if builds == 1 {
			return mapperKnowing(podKind), nil
		}
		return mapperKnowing(podKind, appSetKind), nil
	}, applicationSet("argocd", "platform"))

	if _, err := reader.Locate(t.Context(), "prod", "pods"); err != nil {
		t.Fatalf("warming the cache: %v", err)
	}

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "applicationsets.argoproj.io", Namespace: "argocd", Name: "platform",
	})
	if err != nil {
		t.Fatalf("the drawer refused an object the list had just drawn: %v", err)
	}
	if object.Name != "platform" || object.Kind != "applicationsets" {
		t.Errorf("opened %s/%s, want applicationsets/platform", object.Kind, object.Name)
	}
}

// A cluster that could not be asked is not a cluster that answered "no such
// kind" — an empty answer and a failed one are different answers, at this
// call site rather than in the cache alone.
func TestADiscoveryFailureIsNotReportedAsAnUnknownKind(t *testing.T) {
	reader := readerWithMapper(t, func() (meta.RESTMapper, error) {
		return nil, errRefused
	})

	_, err := reader.Locate(t.Context(), "prod", "pods")
	if err == nil {
		t.Fatal("a cluster that could not be asked answered")
	}
	if !strings.Contains(err.Error(), "discovering") {
		t.Errorf("error = %q, want it phrased as a failed discovery", err)
	}
	if strings.Contains(err.Error(), "not a kind this cluster serves") {
		t.Errorf("error = %q, and a cluster that could not be asked has been told it does not "+
			"serve pods", err)
	}
}

var errRefused = refusal("the server has asked for the client to provide credentials")

type refusal string

func (e refusal) Error() string { return string(e) }
