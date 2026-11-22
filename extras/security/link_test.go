package security

import (
	"strings"
	"testing"
	"time"
)

func rowOf(t *testing.T, o Overview, kind, name string) WorkloadRow {
	t.Helper()
	for _, w := range o.Workloads {
		if w.Kind == kind && w.Name == name {
			return w
		}
	}
	t.Fatalf("no %s %s in %+v", kind, name, o.Workloads)
	return WorkloadRow{}
}

func hasRow(o Overview, kind, name string) bool {
	for _, w := range o.Workloads {
		if w.Kind == kind && w.Name == name {
			return true
		}
	}
	return false
}

// A Deployment is scanned on its ReplicaSets: its row takes the report of the
// set of the highest revision, never an older set's, and never shows the sets
// themselves as workloads.
func TestADeploymentTakesItsReplicaSetOfTheHighestRevision(t *testing.T) {
	h := newHost(t)
	h.add("replicasets", replicaSet("shop", "web-6f7c", "web", 1), replicaSet("shop", "web-9d8b", "web", 3), replicaSet("shop", "web-1a2b", "web", 2))
	h.add("pods", pod("shop", "web-9d8b-x1", map[string]string{"app": "web", "pod-template-hash": "9d8b"}, "ReplicaSet", "web-9d8b"))
	h.add(KindVulnerability,
		vulnReport("replicaset-web-6f7c-app", scan{"shop", "ReplicaSet", "web-6f7c", "app"}, "shop/web", "1.0", Counts{Critical: 9}),
		vulnReport("replicaset-web-1a2b-app", scan{"shop", "ReplicaSet", "web-1a2b", "app"}, "shop/web", "1.1", Counts{Critical: 4}),
		vulnReport("replicaset-web-9d8b-app", scan{"shop", "ReplicaSet", "web-9d8b", "app"}, "shop/web", "1.2", Counts{High: 2}),
		vulnReport("replicaset-web-9d8b-sidecar", scan{"shop", "ReplicaSet", "web-9d8b", "sidecar"}, "envoy", "1.30", Counts{Medium: 1}))
	o := decode[Overview](t, serve(t, h, "/"))
	w := rowOf(t, o, "Deployment", "web")
	if w.Counts != (Counts{High: 2, Medium: 1}) || strings.Join(w.Images, ",") != "envoy:1.30,shop/web:1.2" || w.Scanned == "" {
		t.Errorf("deployment row %+v", w)
	}
	if hasRow(o, "ReplicaSet", "web-6f7c") || hasRow(o, "ReplicaSet", "web-9d8b") || len(o.Workloads) != 1 {
		t.Errorf("rows %+v, want the Deployment alone", o.Workloads)
	}
	if o.Totals != (Counts{High: 2, Medium: 1}) {
		t.Errorf("totals %+v: an older set's report was counted", o.Totals)
	}
}

// A Job's report counts for its CronJob, the newest Job's; a report on the
// CronJob itself wins over any Job's.
func TestACronJobTakesItsOwnReportElseItsNewestJob(t *testing.T) {
	h := newHost(t)
	h.add("jobs", job("ops", "backup-100", "backup", testNow.Add(-2*time.Hour)), job("ops", "backup-200", "backup", testNow.Add(-time.Hour)),
		job("ops", "migrate-1", "", testNow.Add(-time.Hour)))
	h.add(KindVulnerability,
		vulnReport("job-backup-100-run", scan{"ops", "Job", "backup-100", "run"}, "ops/backup", "1", Counts{Critical: 7}),
		vulnReport("job-backup-200-run", scan{"ops", "Job", "backup-200", "run"}, "ops/backup", "2", Counts{High: 1}),
		vulnReport("job-migrate-1-run", scan{"ops", "Job", "migrate-1", "run"}, "ops/migrate", "1", Counts{Low: 1}))
	o := decode[Overview](t, serve(t, h, "/"))
	if w := rowOf(t, o, "CronJob", "backup"); w.Counts != (Counts{High: 1}) {
		t.Errorf("cronjob from its newest job: %+v", w.Counts)
	}
	if w := rowOf(t, o, "Job", "migrate-1"); w.Counts != (Counts{Low: 1}) {
		t.Errorf("a job no CronJob owns is its own workload: %+v", w.Counts)
	}

	h.add(KindVulnerability, vulnReport("cronjob-backup-run", scan{"ops", "CronJob", "backup", "run"}, "ops/backup", "3", Counts{Medium: 5}))
	o = decode[Overview](t, serve(t, h, "/"))
	if w := rowOf(t, o, "CronJob", "backup"); w.Counts != (Counts{Medium: 5}) {
		t.Errorf("a report on the CronJob itself wins: %+v", w.Counts)
	}
}

// A bare pod is its own workload; a pod of a StatefulSet climbs to it, and a
// pod with no report lists its workload as not scanned, last, never clean.
func TestPodsClimbToTheirWorkloadAndUnscannedOnesAreNotClean(t *testing.T) {
	h := newHost(t)
	h.add("pods",
		pod("tools", "debug", nil, "", ""),
		pod("data", "db-0", map[string]string{"app": "db"}, "StatefulSet", "db"),
		pod("data", "cache-0", map[string]string{"app": "cache"}, "StatefulSet", "cache"))
	h.add(KindVulnerability,
		vulnReport("pod-debug-shell", scan{"tools", "Pod", "debug", "shell"}, "busybox", "1.36", Counts{Low: 3}),
		vulnReport("statefulset-db-postgres", scan{"data", "StatefulSet", "db", "postgres"}, "postgres", "16", Counts{High: 1}))
	o := decode[Overview](t, serve(t, h, "/"))
	if w := rowOf(t, o, "Pod", "debug"); w.Counts.Low != 3 {
		t.Errorf("bare pod: %+v", w)
	}
	if w := rowOf(t, o, "StatefulSet", "db"); w.Counts.High != 1 {
		t.Errorf("statefulset: %+v", w)
	}
	cache := rowOf(t, o, "StatefulSet", "cache")
	if cache.Scanned != "" || cache.Config != nil || cache.Secrets != nil {
		t.Errorf("an unscanned workload must say so: %+v", cache)
	}
	if last := o.Workloads[len(o.Workloads)-1]; last.Name != "cache" {
		t.Errorf("the unscanned workload is listed last, got %+v", o.Workloads)
	}

	// The brief and the cell say "not scanned" by having no counts: never clean.
	b := decode[Brief](t, serve(t, h, "/brief/data/cache-0?kind=pods"))
	if b.Scanned || b.Counts != nil || b.Workload == nil || b.Workload.Kind != "StatefulSet" || b.Workload.Name != "cache" {
		t.Errorf("brief of an unscanned pod: %+v", b)
	}
	cells := decode[map[string]Cell](t, serve(t, h, "/cells?kind=statefulsets"))
	if _, ok := cells["data/cache"]; ok {
		t.Errorf("an unscanned workload has a cell: %+v", cells)
	}
	if c := cells["data/db"]; c.Text != "1 high" || c.Tone != "warn" {
		t.Errorf("db cell %+v", c)
	}
}

// A report with no owner reference is linked by trivy-operator's labels; one
// whose name is only a hash there, and with no owner, is linked to nothing
// rather than to a wrong name.
func TestALabelLinksAReportWithNoOwnerAndAHashLinksNothing(t *testing.T) {
	h := newHost(t)
	byLabel := vulnReport("daemonset-agent-agent", scan{"infra", "DaemonSet", "agent", "agent"}, "agent", "5", Counts{High: 2})
	byLabel.owners = nil
	hashed := vulnReport("daemonset-6c9f8b7d5", scan{"infra", "DaemonSet", "", "x"}, "long", "1", Counts{Critical: 1})
	hashed.owners = nil
	hashed.labels[labelNameHash] = "6c9f8b7d5"
	delete(hashed.labels, labelName)
	h.add(KindVulnerability, byLabel, hashed)
	o := decode[Overview](t, serve(t, h, "/"))
	if w := rowOf(t, o, "DaemonSet", "agent"); w.Counts.High != 2 {
		t.Errorf("label-linked: %+v", w)
	}
	if len(o.Workloads) != 1 {
		t.Errorf("a hashed name with no owner was linked: %+v", o.Workloads)
	}
}

// The owner reference wins over a label: a name over 63 characters is a hash
// in the label, and the owner carries it whole.
func TestTheOwnerReferenceWinsOverTheLabel(t *testing.T) {
	h := newHost(t)
	long := strings.Repeat("a", 70)
	rep := vulnReport("statefulset-x-app", scan{"ns", "StatefulSet", long, "app"}, "img", "1", Counts{Low: 1})
	delete(rep.labels, labelName)
	rep.labels[labelNameHash] = "abc123"
	h.add(KindVulnerability, rep)
	o := decode[Overview](t, serve(t, h, "/"))
	rowOf(t, o, "StatefulSet", long)
}
