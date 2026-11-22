package kubecron

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// KubeCron on a Job or a Pod, and whether a run's pod is still there.

const attemptsRuns = `{"runs":[
  {"id":"r3","pod_name":"nightly-29190-y8","trigger":"scheduled","started_at":"2026-10-20T11:02:00Z","status":"succeeded","exit_code":0,"duration_ms":4000},
  {"id":"r2","pod_name":"nightly-29190-x7","trigger":"scheduled","started_at":"2026-10-20T11:00:00Z","status":"failed","exit_code":3,"duration_ms":5000},
  {"id":"r1","pod_name":"nightly-1","trigger":"scheduled","started_at":"2026-10-20T10:00:00Z","status":"failed","exit_code":3,"duration_ms":5000}],"next_cursor":""}`

func ownedPod(t *testing.T, ns, name, job string) unstructured.Unstructured {
	meta := map[string]any{"namespace": ns, "name": name}
	if job != "" {
		meta["ownerReferences"] = []any{map[string]any{"apiVersion": "batch/v1", "kind": "Job", "name": job, "uid": "j", "controller": true}}
		meta["labels"] = map[string]any{"job-name": job}
	}
	return obj(t, map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": meta})
}

func hostWithAttempts(t *testing.T) *fakeHost {
	h, fc := hostWithCollector(t)
	fc.answers["/api/v1/clusters/docker-desktop/cronjobs/ops/nightly/runs"] = attemptsRuns
	h.objects["jobs"] = []unstructured.Unstructured{
		job(t, "ops", "nightly-29190", "nightly", testNow.Add(-time.Hour), testNow, "Failed"),
		job(t, "ops", "by-hand", "", testNow.Add(-time.Hour), testNow, "Complete"),
		job(t, "secret-team", "hidden-29190", "hidden", testNow.Add(-time.Hour), testNow, "Failed"),
	}
	h.objects["pods"] = []unstructured.Unstructured{
		ownedPod(t, "ops", "nightly-29190-x7", "nightly-29190"),
		ownedPod(t, "ops", "lonely", ""),
		ownedPod(t, "ops", "by-hand-abc", "by-hand"),
		ownedPod(t, "secret-team", "hidden-29190-z1", "hidden-29190"),
	}
	return h
}

func ownerOfPath(t *testing.T, h *fakeHost, kind, namespace, name string) (ownerAnswer, *httpRecorder) {
	t.Helper()
	rec := serve(t, testExtra().owner(h, kind), "/x/docker-desktop/kubecron/"+kind+"/"+namespace+"/"+name,
		map[string]string{"namespace": namespace, "name": name})
	var out ownerAnswer
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return out, &httpRecorder{rec.Code, rec.Body.String()}
}

type httpRecorder struct {
	code int
	body string
}

func TestAPodIsFollowedToItsJobItsCronJobAndItsOneRun(t *testing.T) {
	out, rec := ownerOfPath(t, hostWithAttempts(t), "pods", "ops", "nightly-29190-x7")
	if rec.code != http.StatusOK {
		t.Fatalf("%d %s", rec.code, rec.body)
	}
	if out.CronJob == nil || *out.CronJob != (CronJobRef{Namespace: "ops", Name: "nightly"}) || out.Job != "nightly-29190" {
		t.Fatalf("owners %+v / %q", out.CronJob, out.Job)
	}
	if len(out.Runs) != 1 || out.Runs[0].ID != "r2" || out.Collector.State != "ok" {
		t.Errorf("runs %+v, want exactly this pod's run", out.Runs)
	}
}

func TestAJobCarriesEveryAttemptAndNoOtherRun(t *testing.T) {
	out, rec := ownerOfPath(t, hostWithAttempts(t), "jobs", "ops", "nightly-29190")
	if rec.code != http.StatusOK {
		t.Fatalf("%d %s", rec.code, rec.body)
	}
	ids := []string{}
	for _, r := range out.Runs {
		ids = append(ids, r.ID)
	}
	if strings.Join(ids, ",") != "r3,r2" {
		t.Errorf("runs %v, want the Job's two attempts, newest first, and not nightly-1", ids)
	}
}

func TestWhatNoCronJobStartedIsSaidAndNeverAnEmptyHistory(t *testing.T) {
	h := hostWithAttempts(t)
	out, rec := ownerOfPath(t, h, "pods", "ops", "lonely")
	if rec.code != http.StatusOK || out.CronJob != nil || !strings.Contains(out.Note, "not started by a Job") {
		t.Errorf("lonely pod: %d %+v", rec.code, out)
	}
	out, _ = ownerOfPath(t, h, "pods", "ops", "by-hand-abc")
	if out.CronJob != nil || !strings.Contains(out.Note, "not started by a CronJob") {
		t.Errorf("by-hand pod: %+v", out)
	}
}

func TestACronJobThePersonMayNotListIsNotNamed(t *testing.T) {
	out, rec := ownerOfPath(t, hostWithAttempts(t), "pods", "secret-team", "hidden-29190-z1")
	if rec.code != http.StatusOK || out.CronJob != nil || len(out.Runs) != 0 {
		t.Fatalf("%d %+v, want no CronJob and no run", rec.code, out)
	}
	if strings.Contains(rec.body, "r9") || strings.Contains(rec.body, "stats") {
		t.Errorf("the hidden CronJob's history leaked: %s", rec.body)
	}
}

func TestAnObjectNobodyMayReadIsNotFound(t *testing.T) {
	_, rec := ownerOfPath(t, hostWithAttempts(t), "pods", "ops", "nope")
	if rec.code != http.StatusNotFound || !strings.Contains(rec.body, CodeObjectNotFound) {
		t.Errorf("%d %s", rec.code, rec.body)
	}
}

// The collector on docker-desktop records the Job's name as `pod_name`:
// the Pod and the Job still find their run, and the
// run still finds its live pod, through the `job-name` label.
func TestARunNamedAfterItsJobIsStillFoundBothWays(t *testing.T) {
	h, fc := hostWithCollector(t)
	fc.answers["/api/v1/clusters/docker-desktop/cronjobs/ops/nightly/runs"] = `{"runs":[
	  {"id":"r7","pod_name":"nightly-29190","trigger":"scheduled","started_at":"2026-10-20T11:00:00Z","status":"failed","exit_code":3}],"next_cursor":""}`
	fc.answers["/api/v1/runs/r7"] = `{"run":{"id":"r7","pod_name":"nightly-29190","started_at":"2026-10-20T11:00:00Z","status":"failed"},"cronjob":{"namespace":"ops","name":"nightly"}}`
	h.objects["jobs"] = []unstructured.Unstructured{job(t, "ops", "nightly-29190", "nightly", testNow.Add(-time.Hour), testNow, "Failed")}
	h.objects["pods"] = []unstructured.Unstructured{ownedPod(t, "ops", "nightly-29190-x7", "nightly-29190")}

	for _, kind := range []string{"pods", "jobs"} {
		name := map[string]string{"pods": "nightly-29190-x7", "jobs": "nightly-29190"}[kind]
		out, rec := ownerOfPath(t, h, kind, "ops", name)
		if rec.code != http.StatusOK || len(out.Runs) != 1 || out.Runs[0].ID != "r7" {
			t.Errorf("%s: %d %+v, want run r7", kind, rec.code, out.Runs)
		}
	}

	rec := serve(t, testExtra().run(h), "/x/docker-desktop/kubecron/runs/r7", map[string]string{"id": "r7"})
	var out runAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if out.PodExists == nil || !*out.PodExists || out.LivePod != "nightly-29190-x7" {
		t.Errorf("podExists %v livePod %q, want the Job's pod", out.PodExists, out.LivePod)
	}
	if out.Job != "nightly-29190" {
		t.Errorf("job %q, want the Job the run is named after, to link its page", out.Job)
	}
}

func TestARunSaysWhetherItsPodIsStillThere(t *testing.T) {
	h := hostWithAttempts(t)
	read := func() runAnswer {
		rec := serve(t, testExtra().run(h), "/x/docker-desktop/kubecron/runs/r1", map[string]string{"id": "r1"})
		var out runAnswer
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return out
	}
	if out := read(); out.PodExists == nil || *out.PodExists || out.Job != "" {
		t.Errorf("podExists %v job %q, want false and no Job: nightly-1 is gone", out.PodExists, out.Job)
	}
	h.objects["pods"] = append(h.objects["pods"], ownedPod(t, "ops", "nightly-1", "nightly-1"))
	if out := read(); out.PodExists == nil || !*out.PodExists {
		t.Errorf("podExists %v, want true once the pod is there", out.PodExists)
	}
}
