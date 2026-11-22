package cnpg

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func summaryOf(doc map[string]any, backups ...map[string]any) Summary {
	marks := []BackupMark{}
	for _, b := range backups {
		u := unstructured.Unstructured{Object: b}
		marks = append(marks, markOf(&u))
	}
	newestFirst(marks)
	c := unstructured.Unstructured{Object: doc}
	return summarise(&c, marks, 0, 0, testNow)
}

// Measured on CloudNativePG 1.30.1: a cluster with no archive configured
// carries ContinuousArchiving True. Read alone, it would draw an archive that
// does not exist as healthy.
func TestArchivingIsJudgedOnTheSpecFirst(t *testing.T) {
	if got := summaryOf(pgCluster("db", "orders", 2, nil, nil)).Archive.State; got != "none" {
		t.Fatalf("no archive configured, condition True: state %q, want none", got)
	}
	failing := pgCluster("db", "billing", 1, barman(), map[string]any{"conditions": []any{
		map[string]any{"type": "ContinuousArchiving", "status": "False", "reason": "ContinuousArchivingFailing",
			"message": "unexpected failure invoking barman-cloud-wal-archive: exit status 2"},
	}})
	a := summaryOf(failing).Archive
	if a.State != "failing" || a.Message == "" {
		t.Fatalf("archive configured and failing: %+v", a)
	}
	pending := pgCluster("db", "new", 1, barman(), map[string]any{"conditions": []any{}})
	if got := summaryOf(pending).Archive.State; got != "pending" {
		t.Fatalf("configured, no condition yet: %q, want pending", got)
	}
	plugin := pgCluster("db", "plug", 1, map[string]any{"plugins": []any{
		map[string]any{"name": "barman-cloud.cloudnative-pg.io", "isWALArchiver": true},
	}}, nil)
	if got := summaryOf(plugin).Archive.State; got != "ok" {
		t.Fatalf("a WAL-archiver plugin, condition True: %q, want ok", got)
	}
}

// status.lastSuccessfulBackup is never set by a backup plugin: the last
// success is read from the Backup objects.
func TestTheLastBackupComesFromTheBackups(t *testing.T) {
	c := pgCluster("db", "orders", 2, barman(), nil)
	s := summaryOf(c,
		pgBackup("db", "b1", "orders", "completed", testNow.Add(-30*time.Hour)),
		pgBackup("db", "b2", "orders", "completed", testNow.Add(-3*time.Hour)),
	)
	if s.Backup.LastSuccess == nil || !s.Backup.LastSuccess.Equal(testNow.Add(-3*time.Hour)) {
		t.Fatalf("last success %v", s.Backup.LastSuccess)
	}
	if s.Backup.State != "ok" || s.Backup.Late {
		t.Fatalf("a backup three hours old: %+v", s.Backup)
	}

	late := summaryOf(c, pgBackup("db", "b1", "orders", "completed", testNow.Add(-30*time.Hour)))
	if late.Backup.State != "late" || !late.Backup.Late {
		t.Fatalf("thirty hours old: %+v", late.Backup)
	}

	fallback := pgCluster("db", "orders", 1, barman(), map[string]any{"lastSuccessfulBackup": rfc(testNow.Add(-2 * time.Hour))})
	if got := summaryOf(fallback).Backup; got.State != "ok" || got.LastSuccess == nil {
		t.Fatalf("no Backup object, the status field as a fallback: %+v", got)
	}

	never := summaryOf(pgCluster("db", "orders", 1, barman(), nil))
	if never.Backup.State != "never" || !never.Backup.Late {
		t.Fatalf("backed up, never successfully: %+v", never.Backup)
	}

	none := summaryOf(pgCluster("db", "orders", 1, nil, nil))
	if none.Backup.State != "none" || none.Backup.Expected || none.Backup.Late {
		t.Fatalf("nothing backs it up: %+v", none.Backup)
	}

	failed := summaryOf(c,
		pgBackup("db", "b1", "orders", "completed", testNow.Add(-5*time.Hour)),
		pgBackup("db", "b2", "orders", "failed", testNow.Add(-time.Hour)),
	)
	if failed.Backup.State != "failing" || failed.Backup.LastFailure == nil {
		t.Fatalf("the newest failed: %+v", failed.Backup)
	}
}

// A Backup the operator never started has no status at all - measured on one
// asked of a cluster with nothing to back up to.
func TestABackupWithNoStatusReadsNeverStarted(t *testing.T) {
	u := unstructured.Unstructured{Object: pgBackup("db", "manual", "orders", "", testNow)}
	if got := markOf(&u).Phase; got != "not started" {
		t.Fatalf("phase %q, want not started", got)
	}
}

func TestHealth(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status map[string]any
		want   Health
	}{
		{"healthy", nil, HealthHealthy},
		{"switchover", map[string]any{"phase": "Switchover in progress", "targetPrimary": "pg-2"}, HealthSwitchover},
		{"target differs", map[string]any{"phase": "Waiting for the instances to become active", "targetPrimary": "pg-2"}, HealthSwitchover},
		{"failing over", map[string]any{"phase": "Failing over"}, HealthSwitchover},
		{"one not ready", map[string]any{"readyInstances": int64(2)}, HealthDegraded},
		{"one failed", map[string]any{"instancesStatus": map[string]any{"healthy": []any{"pg-1", "pg-2"}, "failed": []any{"pg-3"}}}, HealthDegraded},
		{"unrecoverable", map[string]any{"phase": "Cluster is unrecoverable and needs manual intervention"}, HealthFailing},
		{"none ready", map[string]any{"phase": "Waiting for the instances to become active", "readyInstances": int64(0)}, HealthFailing},
		{"setting up", map[string]any{"phase": "Setting up primary", "readyInstances": int64(0)}, HealthProgressing},
		{"upgrading", map[string]any{"phase": "Upgrading cluster"}, HealthProgressing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := summaryOf(pgCluster("db", "pg", 3, nil, tc.status)).Health; got != tc.want {
				t.Fatalf("health %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInstancesPrimaryFirstWithStateAndTarget(t *testing.T) {
	s := summaryOf(pgCluster("db", "pg", 3, nil, map[string]any{
		"currentPrimary": "pg-2", "targetPrimary": "pg-3",
		"instancesStatus": map[string]any{"healthy": []any{"pg-2", "pg-3"}, "replicating": []any{"pg-1"}},
	}))
	got := s.Instances
	if len(got) != 3 || got[0].Name != "pg-2" || got[0].Role != "primary" {
		t.Fatalf("primary first: %+v", got)
	}
	if got[1].Name != "pg-1" || got[1].State != "replicating" || got[2].Name != "pg-3" || !got[2].Target {
		t.Fatalf("replicas by name, state and target kept: %+v", got)
	}
}

// The operator writes an expiry as Go's time.String().
func TestCertificatesAreJudgedOnTheEarliest(t *testing.T) {
	soon := testNow.Add(3 * 24 * time.Hour).Format("2006-01-02 15:04:05 -0700 MST")
	far := testNow.Add(80 * 24 * time.Hour).Format("2006-01-02 15:04:05 -0700 MST")
	gone := testNow.Add(-time.Hour).Format("2006-01-02 15:04:05 -0700 MST")
	for _, tc := range []struct {
		exp  map[string]any
		want string
	}{
		{map[string]any{"pg-ca": far, "pg-server": far}, "ok"},
		{map[string]any{"pg-ca": far, "pg-server": soon}, "warn"},
		{map[string]any{"pg-ca": gone, "pg-server": far}, "bad"},
		{map[string]any{}, "unknown"},
		{map[string]any{"pg-ca": "not a date"}, "unknown"},
	} {
		c := summaryOf(pgCluster("db", "pg", 1, nil, map[string]any{"certificates": map[string]any{"expirations": tc.exp}})).Certs
		if c.State != tc.want {
			t.Errorf("%v: state %q, want %q", tc.exp, c.State, tc.want)
		}
	}
	measured := "2027-02-09 14:21:33 +0000 UTC" // as read on a real cluster
	if _, ok := parseCertTime(measured); !ok {
		t.Fatalf("the operator's own format is not read: %q", measured)
	}
}

func TestDeclaredStorageIsRead(t *testing.T) {
	s := summaryOf(pgCluster("db", "pg", 1, map[string]any{"walStorage": map[string]any{"size": "512Mi"}}, nil))
	if s.StorageBytes != 1<<30 || s.WALStorageBytes != 512<<20 {
		t.Fatalf("storage %d wal %d", s.StorageBytes, s.WALStorageBytes)
	}
}
