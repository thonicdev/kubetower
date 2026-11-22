package argocd_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/extension"
	argocd "github.com/thonicdev/kubetower/extras/argocd"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// One object as Argo CD sees it, against guestbook-ui as Argo CD's example
// application runs it on docker-desktop.

const guestbookMark = "guestbook:apps/Deployment:guestbook/guestbook-ui"

func trackedHost() *argoHost {
	host := newMultiHost(applications)
	host.discovery = map[string][]metav1.APIResource{
		"apps/v1": {{Name: "deployments", Kind: "Deployment", Namespaced: true}, {Name: "deployments/scale", Kind: "Scale"}},
		"v1":      {{Name: "services", Kind: "Service", Namespaced: true}},
	}
	host.pages["applications"] = []unstructured.Unstructured{managing("OutOfSync")}
	return &argoHost{host}
}

type argoHost struct{ *multiHost }

// managing is the guestbook Application with guestbook-ui among its resources,
// in the given sync word.
func managing(sync string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"kind":     "Application",
		"metadata": map[string]any{"namespace": "argocd", "name": "guestbook"},
		"status": map[string]any{
			"sync":   map[string]any{"status": sync, "revision": "bbbbbbbbbb"},
			"health": map[string]any{"status": "Healthy"},
			"resources": []any{
				map[string]any{"group": "apps", "version": "v1", "kind": "Deployment", "namespace": "guestbook", "name": "guestbook-ui", "status": sync},
			},
			"operationState": map[string]any{"phase": "Succeeded", "syncResult": map[string]any{"revision": "aaaaaaaaaa"}},
		},
	}}
}

// deployment is guestbook-ui with the given replicas live, as Argo CD applied
// it with one.
func deployment(replicas int64, mark string) unstructured.Unstructured {
	applied := map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "guestbook-ui", "namespace": "guestbook",
			"annotations": map[string]any{argocd.TrackingAnnotation: guestbookMark}},
		"spec": map[string]any{"replicas": 1},
	}
	raw, _ := json.Marshal(applied)
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "guestbook-ui", "namespace": "guestbook",
			"annotations": map[string]any{
				argocd.TrackingAnnotation:                          mark,
				"kubectl.kubernetes.io/last-applied-configuration": string(raw),
			}},
		"spec": map[string]any{"replicas": replicas, "progressDeadlineSeconds": int64(600)},
	}}
}

func askTracked(t *testing.T, host *argoHost, query string) (argocd.Tracked, int, []byte) {
	t.Helper()
	body, status := serveAt(t, host.multiHost, "/resource?"+query)
	var got argocd.Tracked
	if status == http.StatusOK {
		decode(t, body, &got)
	}
	return got, status, body
}

func TestAScaleByHandIsTheDriftAndArgoSaysOutOfSync(t *testing.T) {
	host := trackedHost()
	host.pages["deployments"] = []unstructured.Unstructured{deployment(3, guestbookMark)}

	got, status, body := askTracked(t, host, "kind=deployments&namespace=guestbook&name=guestbook-ui")
	if status != http.StatusOK {
		t.Fatalf("status %d, body %s", status, body)
	}
	if !got.Managed || got.Via != "annotation" || got.Application == nil || got.Application.Name != "guestbook" {
		t.Fatalf("got %+v, want managed by guestbook through the annotation", got)
	}
	if got.Resource == nil || got.Resource.Status != "OutOfSync" {
		t.Errorf("resource = %+v, want Argo's OutOfSync for it", got.Resource)
	}
	if !got.Drift.Compared || len(got.Drift.Fields) != 1 || got.Drift.Fields[0].Path != "spec.replicas" || got.Drift.Fields[0].Live != "3" {
		t.Errorf("drift = %+v, want spec.replicas 1 -> 3 and nothing else", got.Drift)
	}
	if got.SyncedRevision != "aaaaaaaaaa" || !strings.Contains(got.Note, "git is at bbbbbbb, last applied aaaaaaa") {
		t.Errorf("synced %q, note %q, want both revisions", got.SyncedRevision, got.Note)
	}
}

func TestOutOfSyncWithNoDriftIsAChangeStillInGit(t *testing.T) {
	host := trackedHost()
	host.pages["deployments"] = []unstructured.Unstructured{deployment(1, guestbookMark)}
	got, _, _ := askTracked(t, host, "kind=deployments&namespace=guestbook&name=guestbook-ui")
	if len(got.Drift.Fields) != 0 || !strings.Contains(got.Note, "the difference is in git") {
		t.Errorf("got %+v, want no field and a note saying the change is in git", got)
	}
	_, _, body := askTracked(t, host, "kind=deployments&namespace=guestbook&name=guestbook-ui")
	if !strings.Contains(string(body), `"fields":[]`) {
		t.Errorf("body %s, want an empty list, never null: the page reads its length", body)
	}
}

func TestACopiedAnnotationIsNotTrusted(t *testing.T) {
	host := trackedHost()
	host.pages["deployments"] = []unstructured.Unstructured{deployment(1, "guestbook:apps/Deployment:guestbook/other")}
	got, _, _ := askTracked(t, host, "kind=deployments&namespace=guestbook&name=guestbook-ui")
	if got.Managed || !got.Copied || got.Application != nil {
		t.Errorf("got %+v, want a copied annotation, not managed", got)
	}
}

func TestAnUnmarkedObjectIsNotManaged(t *testing.T) {
	host := trackedHost()
	host.pages["services"] = []unstructured.Unstructured{{Object: map[string]any{
		"kind": "Service", "metadata": map[string]any{"namespace": "guestbook", "name": "plain"},
	}}}
	got, status, _ := askTracked(t, host, "kind=services&namespace=guestbook&name=plain")
	if status != http.StatusOK || got.Managed || !strings.Contains(got.Note, "names an Argo CD Application") {
		t.Errorf("status %d got %+v, want 200 and not managed", status, got)
	}
}

func TestApplicationsRefusedStillDrawTheDrift(t *testing.T) {
	host := trackedHost()
	host.pages["deployments"] = []unstructured.Unstructured{deployment(2, guestbookMark)}
	host.listErr["applications"] = forbidden("argoproj.io", "applications")
	got, _, _ := askTracked(t, host, "kind=deployments&namespace=guestbook&name=guestbook-ui")
	if got.ApplicationCode != "list_forbidden" || got.Subject == "" || len(got.Drift.Fields) != 1 {
		t.Errorf("got %+v, want the refusal named and the drift read from the object alone", got)
	}
}

func TestTheObjectRefusedIsA403AndAbsentIsA404(t *testing.T) {
	host := trackedHost()
	host.listErr["deployments"] = forbidden("apps", "deployments")
	if _, status, _ := askTracked(t, host, "kind=deployments&namespace=guestbook&name=guestbook-ui"); status != http.StatusForbidden {
		t.Errorf("refused: status %d, want 403", status)
	}
	host = trackedHost()
	_, status, body := askTracked(t, host, "kind=deployments&namespace=guestbook&name=nope")
	if status != http.StatusNotFound || !strings.Contains(string(body), "object_not_found") {
		t.Errorf("absent: status %d body %s, want 404 object_not_found", status, body)
	}
	_, status, body = askTracked(t, host, "kind=widgets&namespace=guestbook&name=x")
	if status != http.StatusNotFound || !strings.Contains(string(body), "kind_unknown") {
		t.Errorf("unknown kind: status %d body %s, want 404 kind_unknown", status, body)
	}
}

func TestALastAppliedWithoutArgosMarkIsSomebodyElses(t *testing.T) {
	host := trackedHost()
	obj := deployment(3, guestbookMark)
	annotations := obj.GetAnnotations()
	annotations["kubectl.kubernetes.io/last-applied-configuration"] = `{"spec":{"replicas":5}}`
	obj.SetAnnotations(annotations)
	host.pages["deployments"] = []unstructured.Unstructured{obj}
	got, _, _ := askTracked(t, host, "kind=deployments&namespace=guestbook&name=guestbook-ui")
	if got.Drift.Compared || !strings.Contains(got.Drift.Note, "not written by Argo CD") {
		t.Errorf("drift = %+v, want not compared, written by hand", got.Drift)
	}
}

func TestHelmsOwnInstanceLabelIsNotArgos(t *testing.T) {
	host := trackedHost()
	host.pages["deployments"] = []unstructured.Unstructured{{Object: map[string]any{
		"kind": "Deployment",
		"metadata": map[string]any{"namespace": "kubetower", "name": "kubetower",
			"labels":      map[string]any{argocd.InstanceLabel: "kubetower"},
			"annotations": map[string]any{argocd.HelmRelease: "kubetower"}},
	}}}
	got, status, _ := askTracked(t, host, "kind=deployments&namespace=kubetower&name=kubetower")
	if status != http.StatusOK || got.Managed || got.Via != "" {
		t.Errorf("status %d got %+v, want a Helm release not taken for Argo CD's", status, got)
	}
}

// A Pod is Argo CD's through its ReplicaSet's Deployment: the answer is
// the Deployment's, with the chain it was reached through.
func TestAPodIsAnsweredThroughItsManagedOwner(t *testing.T) {
	host := trackedHost()
	host.discovery["apps/v1"] = append(host.discovery["apps/v1"], metav1.APIResource{Name: "replicasets", Kind: "ReplicaSet", Namespaced: true})
	host.discovery["v1"] = append(host.discovery["v1"], metav1.APIResource{Name: "pods", Kind: "Pod", Namespaced: true})
	controller := true
	owned := func(kind, name, ownerKind, ownerName string) unstructured.Unstructured {
		obj := unstructured.Unstructured{Object: map[string]any{"kind": kind, "metadata": map[string]any{"namespace": "guestbook", "name": name}}}
		obj.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "apps/v1", Kind: ownerKind, Name: ownerName, Controller: &controller}})
		return obj
	}
	host.pages["deployments"] = []unstructured.Unstructured{deployment(3, guestbookMark)}
	rs := owned("ReplicaSet", "guestbook-ui-5f9", "Deployment", "guestbook-ui")
	// The Deployment controller copies the Deployment's annotations onto its
	// ReplicaSets, Argo CD's mark included: the walk must go past it.
	rs.SetAnnotations(map[string]string{argocd.TrackingAnnotation: guestbookMark})
	host.pages["replicasets"] = []unstructured.Unstructured{rs}
	host.pages["pods"] = []unstructured.Unstructured{owned("Pod", "guestbook-ui-5f9-x7", "ReplicaSet", "guestbook-ui-5f9")}

	got, status, body := askTracked(t, host, "kind=pods&namespace=guestbook&name=guestbook-ui-5f9-x7")
	if status != http.StatusOK {
		t.Fatalf("status %d, body %s", status, body)
	}
	if !got.Managed || got.Kind != "Deployment" || got.Name != "guestbook-ui" || got.Of == nil || got.Of.Kind != "Pod" {
		t.Fatalf("got %+v, want the Deployment's answer, of the Pod", got)
	}
	if len(got.Through) != 2 || got.Through[0].Kind != "ReplicaSet" || got.Through[1].Path != "deployments" {
		t.Errorf("through %+v, want ReplicaSet then Deployment, each addressed", got.Through)
	}
	if len(got.Drift.Fields) != 1 || got.Drift.Fields[0].Path != "spec.replicas" {
		t.Errorf("drift %+v, want the Deployment's", got.Drift)
	}

	// The ReplicaSet itself, carrying the copied mark, answers through its
	// Deployment rather than as a copy.
	got, _, _ = askTracked(t, host, "kind=replicasets&namespace=guestbook&name=guestbook-ui-5f9")
	if !got.Managed || got.Copied || got.Kind != "Deployment" || got.Of == nil || got.Of.Kind != "ReplicaSet" {
		t.Errorf("replicaset %+v, want answered through its Deployment", got)
	}

	// A Pod with no controller is not managed, and says so.
	host.pages["pods"] = []unstructured.Unstructured{{Object: map[string]any{"kind": "Pod", "metadata": map[string]any{"namespace": "guestbook", "name": "bare"}}}}
	got, _, _ = askTracked(t, host, "kind=pods&namespace=guestbook&name=bare")
	if got.Managed || !strings.Contains(got.Note, "none of its owners") {
		t.Errorf("got %+v, want not managed, owners named", got)
	}
}

// A Pod of a Helm chart Argo CD renders carries the release's instance label,
// and so does its ReplicaSet: neither is listed by the Application, and the
// walk goes on past them to the Deployment Argo CD applied, rather than
// stopping at the label and reading the Pod "not managed". Both spellings of the release: named after the Application, so
// the label finds it and it does not list the Pod, and named otherwise, so the
// label finds nothing.
func TestAHelmChartsPodIsAnsweredThroughItsDeployment(t *testing.T) {
	for _, release := range []string{"guestbook", "web-frontend"} {
		t.Run(release, func(t *testing.T) {
			host := trackedHost()
			host.discovery["apps/v1"] = append(host.discovery["apps/v1"], metav1.APIResource{Name: "replicasets", Kind: "ReplicaSet", Namespaced: true})
			host.discovery["v1"] = append(host.discovery["v1"], metav1.APIResource{Name: "pods", Kind: "Pod", Namespaced: true})
			controller := true
			owned := func(kind, name, ownerKind, ownerName string) unstructured.Unstructured {
				obj := unstructured.Unstructured{Object: map[string]any{"kind": kind, "metadata": map[string]any{"namespace": "guestbook", "name": name}}}
				obj.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "apps/v1", Kind: ownerKind, Name: ownerName, Controller: &controller}})
				obj.SetLabels(map[string]string{argocd.InstanceLabel: release})
				return obj
			}
			host.pages["deployments"] = []unstructured.Unstructured{deployment(1, guestbookMark)}
			rs := owned("ReplicaSet", "guestbook-ui-5f9", "Deployment", "guestbook-ui")
			rs.SetAnnotations(map[string]string{argocd.TrackingAnnotation: guestbookMark})
			host.pages["replicasets"] = []unstructured.Unstructured{rs}
			host.pages["pods"] = []unstructured.Unstructured{owned("Pod", "guestbook-ui-5f9-x7", "ReplicaSet", "guestbook-ui-5f9")}

			got, status, body := askTracked(t, host, "kind=pods&namespace=guestbook&name=guestbook-ui-5f9-x7")
			if status != http.StatusOK {
				t.Fatalf("status %d, body %s", status, body)
			}
			if !got.Managed || got.Kind != "Deployment" || got.Name != "guestbook-ui" || got.Of == nil || got.Of.Name != "guestbook-ui-5f9-x7" {
				t.Fatalf("got %+v, want the Deployment's answer, of the Pod", got)
			}
			if len(got.Through) != 2 || got.Through[0].Kind != "ReplicaSet" || got.Through[1].Kind != "Deployment" {
				t.Errorf("through %+v, want ReplicaSet then Deployment", got.Through)
			}
		})
	}
}

// The column answers a whole list from the Applications alone.
func TestTheColumnAnswersEachManagedObjectOfTheKind(t *testing.T) {
	host := trackedHost()
	body, status := serveAt(t, host.multiHost, "/column?kind=deployments")
	if status != http.StatusOK {
		t.Fatalf("status %d, body %s", status, body)
	}
	var got map[string]argocd.ColumnCell
	decode(t, body, &got)
	cell, ok := got["guestbook/guestbook-ui"]
	if len(got) != 1 || !ok || cell.Text != "OutOfSync" || cell.Tone != "warn" ||
		cell.Href != "/c/a-cluster/deployments/guestbook/guestbook-ui?tab=x.argocd.sync" || !strings.Contains(cell.Hint, "argocd/guestbook") {
		t.Errorf("got %+v, want guestbook-ui OutOfSync, linked to its tab", got)
	}
	body, _ = serveAt(t, host.multiHost, "/column?kind=services")
	var services map[string]argocd.ColumnCell
	decode(t, body, &services)
	if len(services) != 0 {
		t.Errorf("services %+v, want none: the Application lists no Service", services)
	}
	host.listErr["applications"] = forbidden("argoproj.io", "applications")
	if _, status := serveAt(t, host.multiHost, "/column?kind=deployments"); status != http.StatusForbidden {
		t.Errorf("refused: status %d, want 403, so the list draws no column", status)
	}
}

// `pods` is the core group's and metrics.k8s.io's: the Pod wins, whatever
// order discovery lists them in.
func TestPodsIsThePodAndNeverItsMetrics(t *testing.T) {
	host := trackedHost()
	host.discovery["metrics.k8s.io/v1beta1"] = []metav1.APIResource{{Name: "pods", Kind: "PodMetrics", Namespaced: true}}
	host.discovery["v1"] = append(host.discovery["v1"], metav1.APIResource{Name: "pods", Kind: "Pod", Namespaced: true})
	host.pages["pods"] = []unstructured.Unstructured{{Object: map[string]any{"kind": "Pod", "metadata": map[string]any{"namespace": "guestbook", "name": "p"}}}}
	for i := 0; i < 5; i++ {
		got, status, body := askTracked(t, host, "kind=pods&namespace=guestbook&name=p")
		if status != http.StatusOK || got.Kind != "Pod" {
			t.Fatalf("status %d kind %q body %s, want the Pod", status, got.Kind, body)
		}
	}
}

// apiService and gateway are what the console's catalog answers for those
// kinds (Registry.Definition): an APIService at its `plural.group` alone, a
// Gateway there and at its CRD's bare alias.
var (
	apiService = extension.CRD{Group: "apiregistration.k8s.io", Version: "v1", Plural: "apiservices", Kind: "APIService"}
	gateway    = extension.CRD{Group: "gateway.networking.k8s.io", Version: "v1", Plural: "gateways", Kind: "Gateway", Namespaced: true}
)

func withProjectKinds(host *argoHost) *argoHost {
	host.discovery["apiregistration.k8s.io/v1"] = []metav1.APIResource{{Name: "apiservices", Kind: "APIService"}}
	host.discovery["gateway.networking.k8s.io/v1"] = []metav1.APIResource{{Name: "gateways", Kind: "Gateway", Namespaced: true}}
	host.crds["apiservices.apiregistration.k8s.io"] = apiService
	host.crds["gateways.gateway.networking.k8s.io"] = gateway
	host.crds["gateways"] = gateway
	return host
}

// The console's menu links a Kubernetes project kind it does not type by hand
// as `plural.group`: the column on that page reads it in
// that spelling. The bare `apiservices` is an address the console's page
// answers "unknown kind" for, and so does the column.
func TestTheColumnReadsAProjectKindAsThePageDoes(t *testing.T) {
	host := withProjectKinds(trackedHost())
	if body, status := serveAt(t, host.multiHost, "/column?kind=apiservices.apiregistration.k8s.io"); status != http.StatusOK {
		t.Errorf("status %d, body %s", status, body)
	}
	for _, segment := range []string{"apiservices", "apiservices.example.io"} {
		if _, status := serveAt(t, host.multiHost, "/column?kind="+segment); status != http.StatusNotFound {
			t.Errorf("%s, which the page does not open: status %d, want 404", segment, status)
		}
	}
}

// Two groups defining the plural is the page's 409, and the tab's.
func TestAnAmbiguousSegmentIsAConflictAsOnThePage(t *testing.T) {
	host := trackedHost()
	host.lookupErr = fmt.Errorf("looking up %q: %w", "widgets", extension.ErrAmbiguousKind)
	if _, status, body := askTracked(t, host, "kind=widgets&name=w"); status != http.StatusConflict {
		t.Errorf("status %d body %s, want 409", status, body)
	}
}

// The tab reads a project kind in the menu's spelling too:
// the menu links an APIService and a Gateway as `plural.group`, and the tab on
// those pages answers about the object on screen rather than "this cluster
// serves no apiservices.apiregistration.k8s.io". An older link's bare
// `gateways` opens the Gateway list through its CRD's alias, and the tab
// answers there too; the bare `apiservices` opens nothing, and neither does it.
func TestTheTabReadsAProjectKindAsThePageDoes(t *testing.T) {
	host := withProjectKinds(trackedHost())
	host.pages["apiservices"] = []unstructured.Unstructured{{Object: map[string]any{"kind": "APIService", "metadata": map[string]any{"name": "v1beta1.metrics.k8s.io"}}}}
	host.pages["gateways"] = []unstructured.Unstructured{{Object: map[string]any{"kind": "Gateway", "metadata": map[string]any{"namespace": "guestbook", "name": "edge"}}}}

	for _, c := range []struct{ query, kind string }{
		{"kind=apiservices.apiregistration.k8s.io&name=v1beta1.metrics.k8s.io", "APIService"},
		{"kind=gateways.gateway.networking.k8s.io&namespace=guestbook&name=edge", "Gateway"},
		// The bare plural an older link carries still names the same kind.
		{"kind=gateways&namespace=guestbook&name=edge", "Gateway"},
	} {
		got, status, body := askTracked(t, host, c.query)
		if status != http.StatusOK || got.Kind != c.kind {
			t.Errorf("%s: status %d kind %q body %s, want the %s", c.query, status, got.Kind, body, c.kind)
		}
	}
	for _, query := range []string{"kind=gateways.example.io&namespace=guestbook&name=edge", "kind=apiservices&name=v1beta1.metrics.k8s.io"} {
		if _, status, _ := askTracked(t, host, query); status != http.StatusNotFound {
			t.Errorf("%s, which the page does not open: status %d, want 404", query, status)
		}
	}
}
