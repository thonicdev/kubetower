package pending

import (
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func owned(p *corev1.Pod, kind, name string, labels map[string]string) *corev1.Pod {
	yes := true
	p.OwnerReferences = []metav1.OwnerReference{{Kind: kind, Name: name, Controller: &yes}}
	p.Labels = labels
	return p
}

// workloads is pods owned the four ways a workload owns them, a refused
// Deployment and a refusal whose quota has room again.
func workloads(t *testing.T) *fakeHost {
	h := newHost(t)
	h.add("nodes", node("small-1", "2", "4Gi", nil))
	hash := map[string]string{"pod-template-hash": "7f9c"}
	h.add("pods",
		owned(unschedulable("shop", "api-7f9c-a", msgMemory, 10*time.Minute, container("100m", "64Gi")), "ReplicaSet", "api-7f9c", hash),
		owned(unschedulable("shop", "api-7f9c-b", msgMemory, 9*time.Minute, container("100m", "64Gi")), "ReplicaSet", "api-7f9c", hash),
		owned(unschedulable("shop", "db-0", msgGPU, 5*time.Minute, container("100m", "64Mi")), "StatefulSet", "db", nil),
		owned(unschedulable("batch", "import-x1", msgMemory, 3*time.Minute, container("100m", "64Gi")), "Job", "import", map[string]string{"job-name": "import"}),
		// A pod nothing owns belongs to no workload.
		unschedulable("shop", "loose", msgMemory, time.Minute, container("100m", "64Gi")),
	)
	h.add("events",
		event("shop", "ReplicaSet", "web-5d8c", "FailedCreate", `Error creating: pods "web-5d8c-a" is forbidden: exceeded quota: small, requested: requests.cpu=3, used: requests.cpu=2, limited: requests.cpu=2`, testNow.Add(-time.Minute), 1),
		// The quota of this one has room again: the event is older than the fix.
		event("shop", "ReplicaSet", "old-1a2b", "FailedCreate", `Error creating: pods "old-1a2b-a" is forbidden: exceeded quota: big, requested: requests.cpu=1, used: requests.cpu=1, limited: requests.cpu=8`, testNow.Add(-time.Minute), 1),
		event("shop", "StatefulSet", "cache", "FailedCreate", `create Pod cache-0 in StatefulSet cache failed error: admission webhook "x" denied the request`, testNow.Add(-time.Minute), 1),
	)
	h.add("resourcequotas",
		&corev1.ResourceQuota{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ResourceQuota"}, ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "small"},
			Status: corev1.ResourceQuotaStatus{Hard: quantities("requests.cpu", "2"), Used: quantities("requests.cpu", "2")}},
		&corev1.ResourceQuota{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ResourceQuota"}, ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "big"},
			Status: corev1.ResourceQuotaStatus{Hard: quantities("requests.cpu", "8"), Used: quantities("requests.cpu", "1")}},
	)
	return h
}

func TestTheColumnCountsPendingPodsPerWorkload(t *testing.T) {
	h := workloads(t)
	got := decode[map[string]CellAnswer](t, serve(t, h, "/cells?kind=deployments"))
	if len(got) != 2 {
		t.Fatalf("deployments with something to say: %v", got)
	}
	api := got["shop/api"]
	if api.Text != "2 pending" || api.Tone != "bad" || api.Hint != "Resources 2" {
		t.Errorf("api: %+v", api)
	}
	if want := "/c/kind/deployments/shop/api?tab=x.pending.workload"; api.Href != want {
		t.Errorf("href %q, want %q", api.Href, want)
	}
	if web := got["shop/web"]; web.Text != "Cannot create" || web.Tone != "bad" || !strings.Contains(web.Hint, "requests.cpu") {
		t.Errorf("web: %+v", web)
	}
	if _, ok := got["shop/old"]; ok {
		t.Error("a refusal the quota would no longer make was drawn")
	}
	if sts := decode[map[string]CellAnswer](t, serve(t, h, "/cells?kind=statefulsets")); sts["shop/db"].Text != "1 pending" || sts["shop/cache"].Text != "Cannot create" {
		t.Errorf("statefulsets: %v", sts)
	}
	if jobs := decode[map[string]CellAnswer](t, serve(t, h, "/cells?kind=jobs")); jobs["batch/import"].Text != "1 pending" || len(jobs) != 1 {
		t.Errorf("jobs: %v", jobs)
	}
	if ds := decode[map[string]CellAnswer](t, serve(t, h, "/cells?kind=daemonsets")); len(ds) != 0 {
		t.Errorf("daemonsets: %v", ds)
	}
}

// One read per list: the pods, the events and, only for a refusal that names
// a quota, the quotas - however many workloads there are.
func TestTheColumnReadsOncePerListNotOncePerWorkload(t *testing.T) {
	h := workloads(t)
	if rec := serve(t, h, "/cells?kind=deployments"); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	reads := map[string]int{}
	for _, q := range h.queries {
		reads[q.GVR.Resource]++
	}
	want := map[string]int{"pods": 1, "events": 1, "resourcequotas": 1}
	for k, n := range want {
		if reads[k] != n {
			t.Errorf("reads of %s = %d, want %d (%v)", k, reads[k], n, reads)
		}
	}
	if len(reads) != len(want) {
		t.Errorf("reads %v", reads)
	}
}

func TestTheTabListsTheWorkloadsPendingPodsAndItsRefusal(t *testing.T) {
	h := workloads(t)
	v := decode[WorkloadView](t, serve(t, h, "/workload/deployments/shop/api"))
	if v.Pending != 2 || len(v.Rows) != 2 || v.Rows[0].Name != "api-7f9c-a" || v.Failure != nil || v.Pods.State != "read" {
		t.Errorf("api: %+v", v)
	}
	w := decode[WorkloadView](t, serve(t, h, "/workload/deployments/shop/web"))
	if w.Pending != 0 || w.Rows == nil || w.Failure == nil || w.Failure.Quota == nil || w.Failure.Quota.Resource != "requests.cpu" {
		t.Errorf("web: %+v", w)
	}
	// Nothing to say is an answer too: no pods, no refusal, both reads made.
	n := decode[WorkloadView](t, serve(t, h, "/workload/deployments/shop/none"))
	if n.Pending != 0 || n.Failure != nil || n.Pods.State != "read" || n.FailuresRead.State != "read" {
		t.Errorf("none: %+v", n)
	}
	if rec := serve(t, h, "/workload/cronjobs/shop/x"); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown kind: %d", rec.Code)
	}
}

func TestARefusedEventReadIsSaidNotDrawnAsNothing(t *testing.T) {
	h := workloads(t)
	h.denied["events"] = true
	// The pods stand, and the view says its refusal read was denied.
	v := decode[WorkloadView](t, serve(t, h, "/workload/deployments/shop/api"))
	if v.Pending != 2 || v.FailuresRead.State != "denied" {
		t.Errorf("%+v", v)
	}
	h.denied["pods"] = true
	if rec := serve(t, h, "/workload/deployments/shop/api"); rec.Code == http.StatusOK {
		t.Errorf("a refused pod read answered %d", rec.Code)
	}
}
