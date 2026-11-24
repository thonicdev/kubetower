package runtimeinspect

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/extension"
)

// owned is p with a controller reference to kind/name, and the
// pod-template-hash label a Deployment's ReplicaSet stamps when hash is set.
func owned(p *corev1.Pod, apiVersion, kind, name, hash string) *corev1.Pod {
	yes := true
	p.OwnerReferences = []metav1.OwnerReference{{APIVersion: apiVersion, Kind: kind, Name: name, Controller: &yes}}
	if hash != "" {
		p.Labels = map[string]string{"pod-template-hash": hash}
	}
	return p
}

// A cluster with one Deployment of two Java pods (one tight, one with room),
// a StatefulSet whose pod is OOMKill-bound, a Java pod nothing owns, and a
// Deployment that runs no Java.
func fleet(t *testing.T) *fakeHost {
	java := func(name, xmx, limit string) corev1.Container {
		return memory(corev1.Container{Name: name, Image: "eclipse-temurin:21-jre", Args: []string{"-Xmx" + xmx, "-jar", "app.jar"}}, "", limit)
	}
	return newHost(t,
		owned(pod("shop", "api-7d4b9-aaaaa", java("api", "900m", "1Gi")), "apps/v1", "ReplicaSet", "api-7d4b9", "7d4b9"),
		owned(pod("shop", "api-7d4b9-bbbbb", java("api", "256m", "1Gi")), "apps/v1", "ReplicaSet", "api-7d4b9", "7d4b9"),
		owned(pod("data", "kafka-0", java("kafka", "4g", "4Gi")), "apps/v1", "StatefulSet", "kafka", ""),
		pod("shop", "batch", java("run", "256m", "1Gi")),
		owned(pod("shop", "web-5f6c7-ccccc", corev1.Container{Name: "web", Image: "nginx:1.27"}), "apps/v1", "ReplicaSet", "web-5f6c7", "5f6c7"),
	)
}

func column(t *testing.T, h *fakeHost, kind string) map[string]ColumnCell {
	t.Helper()
	rec := serve(t, h, "/x/kind/runtime/column?kind="+kind)
	if rec.Code != http.StatusOK {
		t.Fatalf("kind %s: answered %d %s", kind, rec.Code, rec.Body)
	}
	return decode[map[string]ColumnCell](t, rec)
}

// The Pods list: a cell per probably-Java pod, keyed namespace/name, toned by
// its risk, saying its heap against its limit, linking to the page narrowed
// to it - and nothing for a pod the spec does not show as Java.
func TestThePodsColumnHasACellPerJavaPod(t *testing.T) {
	h := fleet(t)
	got := column(t, h, "pods")
	if len(got) != 4 {
		t.Fatalf("cells = %v, want the four Java pods", got)
	}
	tight := got["shop/api-7d4b9-aaaaa"]
	if tight.Text != "Java" || tight.Tone != "warn" || tight.Hint != "api: heap 900.0 MiB of limit 1Gi (88 %, tight)" {
		t.Errorf("tight pod = %+v", tight)
	}
	if got["shop/api-7d4b9-bbbbb"].Tone != "ok" || got["data/kafka-0"].Tone != "bad" {
		t.Errorf("tones = %q, %q; want ok and bad", got["shop/api-7d4b9-bbbbb"].Tone, got["data/kafka-0"].Tone)
	}
	if href := got["data/kafka-0"].Href; href != "/c/kind/x/runtime?ns=data&q=kafka-0" {
		t.Errorf("href = %q", href)
	}
	if _, ok := got["shop/web-5f6c7-ccccc"]; ok {
		t.Error("a pod with no Java hint has a cell")
	}
	if len(h.queries) != 1 || h.queries[0].Namespace != "" || h.queries[0].FieldSelector != "" {
		t.Errorf("queries = %+v, want one pod list of the whole cluster", h.queries)
	}
	if len(h.usageAsked) != 0 {
		t.Errorf("the column asked metrics-server %v: it is the spec's reading only", h.usageAsked)
	}
}

// A workload's cell gathers its pods: a Deployment through its ReplicaSet's
// pod-template-hash, toned by its worst container.
func TestAWorkloadsCellIsItsWorstPod(t *testing.T) {
	h := fleet(t)
	deployments := column(t, h, "deployments")
	api, ok := deployments["shop/api"]
	if !ok || len(deployments) != 1 {
		t.Fatalf("deployments = %v, want shop/api alone", deployments)
	}
	if api.Tone != "warn" || !strings.HasPrefix(api.Hint, "2 Java pods. ") || !strings.Contains(api.Hint, "api-7d4b9-bbbbb/api") ||
		api.Href != "/c/kind/x/runtime?ns=shop&q=api" {
		t.Errorf("shop/api = %+v", api)
	}
	if rs := column(t, h, "replicasets"); rs["shop/api-7d4b9"].Tone != "warn" || len(rs) != 1 {
		t.Errorf("replicasets = %v", rs)
	}
	if sts := column(t, h, "statefulsets"); sts["data/kafka"].Tone != "bad" || len(sts) != 1 {
		t.Errorf("statefulsets = %v", sts)
	}
	if ds := column(t, h, "daemonsets"); len(ds) != 0 {
		t.Errorf("daemonsets = %v, want none", ds)
	}
}

// A ReplicaSet whose pods carry no matching pod-template-hash is not taken
// for a Deployment's.
func TestAReplicaSetWithoutTheHashStaysOne(t *testing.T) {
	java := memory(corev1.Container{Name: "app", Image: "eclipse-temurin:21-jre"}, "", "1Gi")
	h := newHost(t, owned(pod("shop", "lone-x", java), "apps/v1", "ReplicaSet", "lone", ""))
	if got := column(t, h, "deployments"); len(got) != 0 {
		t.Errorf("deployments = %v", got)
	}
	if got := column(t, h, "replicasets"); got["shop/lone"].Text != "Java" {
		t.Errorf("replicasets = %v", got)
	}
}

// A kind the column is not drawn on is refused before anything is read.
func TestTheColumnRefusesAKindWithoutPods(t *testing.T) {
	h := fleet(t)
	rec := serve(t, h, "/x/kind/runtime/column?kind=services")
	if p := decode[problem](t, rec); rec.Code != http.StatusBadRequest || p.Code != CodeBadRequest {
		t.Errorf("answered %d %+v", rec.Code, p)
	}
	if len(h.queries) != 0 {
		t.Errorf("read %+v before refusing", h.queries)
	}
}

// The inventory carries each pod's workload and each Java container's working
// set when metrics-server measured it, and null when it did not.
func TestTheInventoryCarriesOwnersAndWorkingSets(t *testing.T) {
	h := fleet(t)
	h.live = map[string]extension.Resources{
		extension.ContainerKey("shop", "api-7d4b9-aaaaa", "api"): {MemoryBytes: 700 << 20},
		extension.ContainerKey("data", "kafka-0", "kafka"):       {MemoryBytes: 0},
	}
	got := decode[inventoryAnswer](t, serve(t, h, "/x/kind/runtime/?ns=shop"))
	if got.Usage.State != usageRead || !got.Usage.Complete || len(h.usageAsked) != 1 || h.usageAsked[0] != "shop" {
		t.Fatalf("usage = %+v, asked %v", got.Usage, h.usageAsked)
	}
	byName := map[string]Pod{}
	for _, p := range got.Pods {
		byName[p.Name] = p
	}
	api := byName["api-7d4b9-aaaaa"]
	if api.Owner == nil || *api.Owner != (Owner{Kind: "Deployment", Name: "api", ReplicaSet: "api-7d4b9"}) {
		t.Errorf("owner = %+v", api.Owner)
	}
	if ws := api.Containers[0].WorkingSet; ws == nil || ws.Bytes != 700<<20 || ws.Text != "700.0 MiB" {
		t.Errorf("working set = %+v", ws)
	}
	if byName["batch"].Owner != nil || byName["batch"].Containers[0].WorkingSet != nil {
		t.Errorf("batch = %+v, want no owner and no working set", byName["batch"])
	}
	// A measured zero is a figure, not an absence.
	got = decode[inventoryAnswer](t, serve(t, h, "/x/kind/runtime/?ns=data"))
	if ws := got.Pods[0].Containers[0].WorkingSet; ws == nil || ws.Bytes != 0 {
		t.Errorf("kafka's measured zero = %+v", ws)
	}
	if !strings.Contains(serve(t, h, "/x/kind/runtime/?ns=shop").Body.String(), `"workingSet":null`) {
		t.Error("an unmeasured container does not say null")
	}
}

// Every way the working sets were not read is its own state, and the pods
// are still answered: the spec's reading does not wait on metrics-server.
func TestEveryUsageFailureIsItsOwnState(t *testing.T) {
	for _, tc := range []struct {
		err   error
		state string
	}{
		{nil, usageAbsent}, // the fake's default: no metrics-server
		{&extension.MetricsUnavailable{Reason: extension.MetricsRefused, Detail: "403"}, usageRefused},
		{&extension.MetricsUnavailable{Reason: extension.MetricsUnreachable, Detail: "dial"}, usageUnreachable},
		{fmt.Errorf("usage: %w", extension.ErrDenied), usageForbidden},
		{errors.New("boom"), usageFailed},
	} {
		h := fleet(t)
		h.usageErr = tc.err
		rec := serve(t, h, "/x/kind/runtime/")
		got := decode[inventoryAnswer](t, rec)
		if rec.Code != http.StatusOK || got.Usage.State != tc.state || got.Usage.Detail == "" || len(got.Pods) != 4 {
			t.Errorf("%v: answered %d usage %+v with %d pods, want state %s", tc.err, rec.Code, got.Usage, len(got.Pods), tc.state)
		}
		for _, p := range got.Pods {
			for _, c := range p.Containers {
				if c.WorkingSet != nil {
					t.Errorf("%v: %s/%s has a working set", tc.err, p.Name, c.Name)
				}
			}
		}
	}
}
