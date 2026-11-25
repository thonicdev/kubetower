package storage

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thonicdev/kubetower/extension"
)

// shop is one namespace holding every case the issue names: a Released volume
// kept by Retain, a claim nothing mounts, a claim at 92 %, the claim of a
// StatefulSet that is gone, and one of a StatefulSet scaled down.
func shop(t *testing.T) *fakeHost {
	t.Helper()
	h := newHost()
	h.add("storageclasses",
		class(t, "gp3", "Delete", true),
		class(t, "gp3-retain", "Retain", false),
		class(t, "old-hdd", "Delete", false),
	)
	h.add("persistentvolumes",
		volume(t, "pv-db-0", "Bound", "Retain", "gp3-retain", "20Gi", "shop/data-db-0", 90),
		volume(t, "pv-db-1", "Bound", "Retain", "gp3-retain", "20Gi", "shop/data-db-1", 90),
		volume(t, "pv-cache", "Bound", "Delete", "gp3", "10Gi", "shop/cache", 60),
		volume(t, "pv-logs", "Bound", "Delete", "gp3", "5Gi", "shop/logs", 45),
		volume(t, "pv-gone", "Bound", "Delete", "gp3", "8Gi", "shop/data-gone-0", 120),
		volume(t, "pv-released", "Released", "Retain", "gp3-retain", "50Gi", "shop/old-data", 300),
		volume(t, "pv-spare", "Available", "Delete", "gp3", "1Gi", "", 12),
	)
	h.add("persistentvolumeclaims",
		claim(t, "shop", "data-db-0", "gp3-retain", "20Gi", "pv-db-0", map[string]any{"app": "db"}),
		claim(t, "shop", "data-db-1", "gp3-retain", "20Gi", "pv-db-1", map[string]any{"app": "db"}),
		claim(t, "shop", "cache", "gp3", "10Gi", "pv-cache", nil),
		claim(t, "shop", "logs", "gp3", "5Gi", "pv-logs", nil),
		claim(t, "shop", "data-gone-0", "gp3", "8Gi", "pv-gone", map[string]any{"app": "gone"}),
	)
	h.add("statefulsets", statefulSet(t, "shop", "db", "data"))
	h.add("pods",
		pod(t, "shop", "db-0", "node-a", "db", "data-db-0"),
		pod(t, "shop", "web-7d4b9c-x2x", "node-b", "", "cache"),
	)
	h.measured("node-a", "shop/data-db-0", 92*20*gi/100, 20*gi)
	h.measured("node-b", "shop/cache", 85*10*gi/100, 10*gi)
	h.add("volumesnapshots",
		snapshot(t, "shop", "db-0-tue", "data-db-0", testNow.Add(-48*time.Hour), true),
		snapshot(t, "shop", "db-0-wed", "data-db-0", testNow.Add(-24*time.Hour), true),
	)
	h.add("volumesnapshotclasses", snapshotClass(t, "csi-snap"))
	h.add("volumesnapshotcontents", obj(t, map[string]any{"apiVersion": "snapshot.storage.k8s.io/v1",
		"kind": "VolumeSnapshotContent", "metadata": map[string]any{"name": "snapcontent-1"}}))
	return h
}

func overview(t *testing.T, h *fakeHost, query string) Answer {
	t.Helper()
	return decode[Answer](t, serve(t, h, "/x/prod/storage/"+query))
}

func claimNamed(t *testing.T, a Answer, ns, name string) Claim {
	t.Helper()
	for _, c := range a.Claims {
		if c.Namespace == ns && c.Name == name {
			return c
		}
	}
	t.Fatalf("no claim %s/%s in %+v", ns, name, a.Claims)
	return Claim{}
}

func volumeNamed(t *testing.T, a Answer, name string) Volume {
	t.Helper()
	for _, v := range a.Volumes {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("no volume %s", name)
	return Volume{}
}

func classNamed(t *testing.T, a Answer, name string) Class {
	t.Helper()
	for _, c := range a.Classes {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no class %s", name)
	return Class{}
}

func TestEveryOrphanIsNamedWithItsReason(t *testing.T) {
	at(t)
	a := overview(t, shop(t), "")

	for _, want := range []struct{ name, orphan, mount string }{
		{"logs", OrphanUnmounted, MountUnmounted},
		{"data-gone-0", OrphanWorkloadGone, MountUnmounted},
		// db scaled down: the StatefulSet that owns the name is there.
		{"data-db-1", OrphanUnmounted, MountUnmounted},
		{"data-db-0", "", MountMounted},
		{"cache", "", MountMounted},
	} {
		c := claimNamed(t, a, "shop", want.name)
		if c.Orphan != want.orphan || c.Mount != want.mount {
			t.Errorf("%s: orphan %q mount %q, want %q %q", want.name, c.Orphan, c.Mount, want.orphan, want.mount)
		}
	}
	if v := volumeNamed(t, a, "pv-released"); v.Orphan != OrphanReleased || v.Reclaim != "Retain" || v.Claim != "shop/old-data" {
		t.Errorf("pv-released = %+v, want a Released orphan kept by Retain, naming its deleted claim", v)
	}
	if v := volumeNamed(t, a, "pv-spare"); v.Orphan != OrphanAvailable {
		t.Errorf("pv-spare orphan = %q, want available", v.Orphan)
	}
	if v := volumeNamed(t, a, "pv-db-0"); v.Orphan != "" {
		t.Errorf("a bound volume is an orphan: %q", v.Orphan)
	}
	if c := classNamed(t, a, "old-hdd"); c.Orphan != OrphanUnusedClass {
		t.Errorf("old-hdd orphan = %q, want unusedClass", c.Orphan)
	}
	if c := classNamed(t, a, "gp3"); c.Orphan != "" || c.Claims != 3 || c.Volumes != 4 || !c.Default {
		t.Errorf("gp3 = %+v, want the default class, used by 3 claims and 4 volumes", c)
	}
	n := a.Counts
	if n.OrphanClaims != 3 || n.OrphanVolumes != 2 || n.OrphanClasses != 1 || n.Orphans != 6 || n.RetainedOrphans != 1 {
		t.Errorf("counts = %+v, want 3 claims, 2 volumes, 1 class, 1 kept by Retain", n)
	}
	if c := claimNamed(t, a, "shop", "data-db-0"); c.Workload == nil || *c.Workload != (Workload{Kind: "StatefulSet", Name: "db"}) || c.Reclaim != "Retain" {
		t.Errorf("data-db-0 = %+v, want its StatefulSet and its volume's Retain", c)
	}
	if c := claimNamed(t, a, "shop", "cache"); len(c.Pods) != 1 || c.Pods[0] != "web-7d4b9c-x2x" || c.Workload != nil {
		t.Errorf("cache = pods %v workload %+v, want its one pod and no controller", c.Pods, c.Workload)
	}
}

func TestTheThresholdsGradeAt80And90(t *testing.T) {
	for _, c := range []struct {
		percent float64
		want    string
	}{{0, LevelOK}, {79.99, LevelOK}, {80, LevelWarn}, {89.99, LevelWarn}, {90, LevelBad}, {100, LevelBad}} {
		if got := level(c.percent); got != c.want {
			t.Errorf("level(%v) = %q, want %q", c.percent, got, c.want)
		}
	}

	at(t)
	a := overview(t, shop(t), "")
	full := claimNamed(t, a, "shop", "data-db-0")
	if full.Fill.State != FillMeasured || full.Fill.Level != LevelBad || full.Fill.Percent < 91.9 || full.Fill.Percent > 92.1 {
		t.Errorf("the claim at 92%% = %+v, want measured and bad", full.Fill)
	}
	if warm := claimNamed(t, a, "shop", "cache"); warm.Fill.Level != LevelWarn {
		t.Errorf("the claim at 85%% = %+v, want warn", warm.Fill)
	}
	if a.Counts.Unread != 0 {
		t.Errorf("unread = %d, want 0: both mounted claims were measured", a.Counts.Unread)
	}
	if a.Counts.Bad != 1 || a.Counts.Warn != 1 || a.Counts.Measured != 2 {
		t.Errorf("counts = %+v, want one bad, one warn, two measured", a.Counts)
	}
	// The fullest first.
	if a.Claims[0].Name != "data-db-0" || a.Claims[1].Name != "cache" {
		t.Errorf("order = %s, %s; want the 92%% claim then the 85%% one", a.Claims[0].Name, a.Claims[1].Name)
	}
	if logs := claimNamed(t, a, "shop", "logs"); logs.Fill.State != FillUnmounted || logs.Fill.Percent != 0 || logs.Fill.Level != "" {
		t.Errorf("an unmounted claim = %+v, want unmounted with no figure", logs.Fill)
	}
}

// A share reports the whole export: its used bytes stay, its percentage is
// never drawn.
func TestAShareIsNotGradedAgainstSomebodyElsesDisk(t *testing.T) {
	at(t)
	h := shop(t)
	h.measured("node-b", "shop/cache", 3*gi, 40<<40)
	c := claimNamed(t, overview(t, h, ""), "shop", "cache")
	if c.Fill.State != FillShared || c.Fill.Percent != 0 || c.Fill.Level != "" || c.Fill.UsedBytes != 3*gi {
		t.Errorf("a claim on a share = %+v, want shared, used kept, no percentage", c.Fill)
	}
}

// No kubelet could be read: the source says so in one line naming the
// switch, and no claim carries a figure - never a 0 %.
func TestUnreadableStatsAreSaidAndNeverDrawnAsZero(t *testing.T) {
	at(t)
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "nodes/proxy"}, "node-a", errors.New("no"))
	for _, c := range []struct {
		name   string
		set    func(*fakeHost)
		state  string
		phrase string
	}{
		{"nodes/proxy refused", func(h *fakeHost) {
			h.silent["node-a"], h.silent["node-b"] = forbidden, forbidden
		}, SourceUnreadable, "rbac.nodeProxy"},
		{"nodes not listable", func(h *fakeHost) {
			h.usageErr = fmt.Errorf("%w: list nodes", extension.ErrDenied)
		}, SourceUnreadable, "list nodes"},
		{"one kubelet silent", func(h *fakeHost) { h.silent["node-b"] = errors.New("dial tcp: i/o timeout") }, SourcePartial, "some kubelets"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := shop(t)
			c.set(h)
			a := overview(t, h, "")
			if a.Fill.State != c.state || !strings.Contains(a.Fill.Message, c.phrase) || strings.Contains(a.Fill.Message, "\n") {
				t.Fatalf("fill source = %+v, want %s, one line naming %q", a.Fill, c.state, c.phrase)
			}
			for _, claim := range a.Claims {
				if claim.Fill.State == FillMeasured {
					continue
				}
				if claim.Fill.Percent != 0 || claim.Fill.Level != "" {
					t.Errorf("%s carries a figure it was never given: %+v", claim.Name, claim.Fill)
				}
			}
			if c.state == SourceUnreadable && a.Counts.Unread != 2 {
				t.Errorf("unread = %d, want the 2 mounted claims", a.Counts.Unread)
			}
			cache := claimNamed(t, a, "shop", "cache")
			if cache.Fill.State != FillUnread {
				t.Errorf("cache = %+v, want unread: mounted, and no kubelet gave its figure", cache.Fill)
			}
		})
	}
}

func TestNoMountedClaimAsksNoKubelet(t *testing.T) {
	at(t)
	h := shop(t)
	h.objects["pods"] = nil
	a := overview(t, h, "")
	if a.Fill.State != SourceNone || len(h.asked) != 0 {
		t.Errorf("fill = %+v after asking %v, want none and no kubelet asked", a.Fill, h.asked)
	}
}

// Absent and unreadable snapshot kinds are three different answers.
func TestSnapshotKindsAbsentRefusedAndRead(t *testing.T) {
	at(t)
	read := overview(t, shop(t), "")
	if s := read.Snapshots; s.State != StateRead || s.Total != 2 || s.Ready != 2 || s.Contents != 1 || len(s.Classes) != 1 {
		t.Errorf("snapshots = %+v, want read: 2 snapshots, 1 content, 1 class", s)
	}
	last := claimNamed(t, read, "shop", "data-db-0")
	if last.Snapshot == nil || last.Snapshot.Name != "db-0-wed" || last.Snapshots != 2 {
		t.Errorf("data-db-0's last snapshot = %+v (%d), want db-0-wed of 2", last.Snapshot, last.Snapshots)
	}
	if read.Counts.Snapshotted != 1 {
		t.Errorf("snapshotted = %d, want 1", read.Counts.Snapshotted)
	}

	absentHost := shop(t)
	absentHost.registry = fakeRegistry{served: map[string]extension.CRD{}}
	if s := overview(t, absentHost, "").Snapshots; s.State != StateAbsent || s.Message != "Not installed" {
		t.Errorf("no snapshot CRDs = %+v, want absent", s)
	}

	refusedHost := shop(t)
	refusedHost.listErr["volumesnapshots"] = fmt.Errorf("%w: list volumesnapshots", extension.ErrDenied)
	s := overview(t, refusedHost, "").Snapshots
	if s.State != StateRefused || s.Message != "Refused" {
		t.Errorf("snapshot CRDs present, list refused = %+v, want refused", s)
	}

	failedHost := shop(t)
	failedHost.listErr["volumesnapshots"] = errors.New("the server is currently unable to handle the request")
	if s := overview(t, failedHost, "").Snapshots; s.State != StateFailed || s.Detail == "" {
		t.Errorf("snapshot CRDs present, list failed = %+v, want failed with its error", s)
	}
}

// Pods that cannot all be read leave every claim's mount unknown: none is
// called an orphan on a guess.
func TestUnreadPodsCallNoClaimUnmounted(t *testing.T) {
	at(t)
	h := shop(t)
	h.listErr["pods"] = fmt.Errorf("%w: list pods", extension.ErrDenied)
	a := overview(t, h, "")
	if a.MountsRead.State != StateRefused {
		t.Errorf("mounts = %+v, want refused", a.MountsRead)
	}
	for _, c := range a.Claims {
		if c.Mount != MountUnknown || c.Orphan != "" {
			t.Errorf("%s: mount %q orphan %q, want unknown and no orphan", c.Name, c.Mount, c.Orphan)
		}
	}
	if a.Counts.OrphanClaims != 0 {
		t.Errorf("orphan claims = %d, want 0", a.Counts.OrphanClaims)
	}
}

// Without the StatefulSets, an unmounted claim is unmounted, never "gone".
func TestUnreadStatefulSetsCallNoWorkloadGone(t *testing.T) {
	at(t)
	h := shop(t)
	h.listErr["statefulsets"] = errors.New("boom")
	a := overview(t, h, "")
	if c := claimNamed(t, a, "shop", "data-gone-0"); c.Orphan != OrphanUnmounted {
		t.Errorf("data-gone-0 = %q, want unmounted", c.Orphan)
	}
	if a.Complete {
		t.Error("an answer missing the StatefulSets says it is complete")
	}
}

func TestANamespaceShowsItsOwnVolumesAndJudgesNoClass(t *testing.T) {
	at(t)
	h := shop(t)
	h.add("persistentvolumeclaims", claim(t, "other", "x", "old-hdd", "1Gi", "pv-x", nil))
	h.add("persistentvolumes", volume(t, "pv-x", "Bound", "Delete", "old-hdd", "1Gi", "other/x", 3))
	a := overview(t, h, "?ns=shop")
	for _, v := range a.Volumes {
		if v.Name == "pv-x" || v.Name == "pv-spare" {
			t.Errorf("the namespace's answer holds %s", v.Name)
		}
	}
	for _, c := range a.Classes {
		if c.Orphan != "" {
			t.Errorf("a namespace judged class %s unused", c.Name)
		}
	}
	if a.Snapshots.Contents != -1 {
		t.Errorf("contents = %d in a namespace, want -1: a cluster-scoped kind", a.Snapshots.Contents)
	}
}

// A volume names only a claim that was read, or any once every claim was: a
// person narrowed out of a namespace learns none of its claims' names here.
func TestAVolumeNamesNoClaimThatWasNotRead(t *testing.T) {
	at(t)
	h := shop(t)
	h.add("persistentvolumes", volume(t, "pv-hidden", "Bound", "Delete", "gp3", "1Gi", "team-x/secret-claim", 3))
	narrowed := &narrowingHost{fakeHost: h}
	a := overview(t, narrowed.fakeHost, "")
	if v := volumeNamed(t, a, "pv-hidden"); v.Claim != "team-x/secret-claim" {
		t.Errorf("every claim read: pv-hidden names %q", v.Claim)
	}
	b := decode[Answer](t, serve(t, narrowed, "/x/prod/storage/"))
	if v := volumeNamed(t, b, "pv-hidden"); v.Claim != "" {
		t.Errorf("claims narrowed: pv-hidden names %q, a claim that was not read", v.Claim)
	}
	if b.Complete {
		t.Error("a narrowed answer says it is complete")
	}
	for _, c := range b.Classes {
		if c.Orphan != "" {
			t.Errorf("claims narrowed: class %s judged unused", c.Name)
		}
	}
}

// narrowingHost answers the claims as the console does for a person who may
// not list them everywhere.
type narrowingHost struct{ *fakeHost }

func (n *narrowingHost) List(r *http.Request, cluster string, q extension.ListQuery) (extension.Page, error) {
	page, err := n.fakeHost.List(r, cluster, q)
	if err == nil && q.GVR == claimsGVR {
		page.Complete = false
		page.Note = "You may not list persistentvolumeclaims in every namespace."
	}
	return page, err
}

func TestTheClaimTabCarriesItsVolumeClassAndSnapshots(t *testing.T) {
	at(t)
	got := decode[ClaimAnswer](t, serve(t, shop(t), "/x/prod/storage/claim/shop/data-db-0"))
	if got.Claim.Name != "data-db-0" || got.Volume == nil || got.Volume.Name != "pv-db-0" || got.Class == nil || got.Class.Name != "gp3-retain" {
		t.Fatalf("claim tab = %+v", got)
	}
	if len(got.History) != 2 || got.History[0].Name != "db-0-wed" {
		t.Errorf("history = %+v, want newest first", got.History)
	}
	if rec := serve(t, shop(t), "/x/prod/storage/claim/shop/nope"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), CodeNoClaim) {
		t.Errorf("an unknown claim answered %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve(t, shop(t), "/x/prod/storage/claim/shop/Bad_Name"); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad name answered %d", rec.Code)
	}
}

func TestThePodTabListsTheClaimsItMounts(t *testing.T) {
	at(t)
	got := decode[PodAnswer](t, serve(t, shop(t), "/x/prod/storage/pod/shop/db-0"))
	if len(got.Claims) != 1 || got.Claims[0].Name != "data-db-0" {
		t.Errorf("pod tab = %+v, want data-db-0", got.Claims)
	}
	none := decode[PodAnswer](t, serve(t, shop(t), "/x/prod/storage/pod/shop/nobody"))
	if len(none.Claims) != 0 {
		t.Errorf("a pod mounting nothing = %+v", none.Claims)
	}
	h := shop(t)
	h.listErr["pods"] = fmt.Errorf("%w: list pods", extension.ErrDenied)
	if rec := serve(t, h, "/x/prod/storage/pod/shop/db-0"); rec.Code != http.StatusForbidden {
		t.Errorf("pods refused: the pod tab answered %d, want 403 - unknown is not none", rec.Code)
	}
}

func TestRefusedClaimsAreTheRequestsRefusal(t *testing.T) {
	at(t)
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("%w: list persistentvolumeclaims", extension.ErrDenied), http.StatusForbidden, CodeListForbidden},
		{fmt.Errorf("%w", extension.ErrUnreviewable), http.StatusServiceUnavailable, CodeListUnreviewed},
		{fmt.Errorf("%w", extension.ErrUnknownCluster), http.StatusNotFound, CodeUnknownCluster},
		{errors.New("connection refused"), http.StatusBadGateway, CodeListFailed},
	} {
		h := shop(t)
		h.listErr["persistentvolumeclaims"] = c.err
		rec := serve(t, h, "/x/prod/storage/")
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), `"code":"`+c.code+`"`) {
			t.Errorf("%v answered %d %s, want %d %s", c.err, rec.Code, rec.Body.String(), c.status, c.code)
		}
	}
	if rec := serve(t, shop(t), "/x/prod/storage/?ns=Not_A_Namespace"); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad namespace answered %d", rec.Code)
	}
}

// The reads are the narrowest the page needs, and never a Secret.
func TestItReadsOnlyWhatItDeclares(t *testing.T) {
	at(t)
	h := shop(t)
	overview(t, h, "")
	want := map[string]bool{"persistentvolumeclaims": true, "persistentvolumes": true, "storageclasses": true, "pods": true,
		"statefulsets": true, "volumesnapshots": true, "volumesnapshotclasses": true, "volumesnapshotcontents": true}
	for _, q := range h.listed {
		if !want[q.GVR.Resource] {
			t.Errorf("it listed %s", q.GVR.Resource)
		}
	}
}

// A generic ephemeral volume's claim is named `<pod>-<volume>` and owned by
// its pod: the pod mounts it, so it is no orphan, and its kubelet's figure is
// drawn. The name alone is not enough, nor the owner alone.
func TestAnEphemeralVolumesClaimIsMountedByItsPod(t *testing.T) {
	at(t)
	h := shop(t)
	ownedBy := func(c unstructured.Unstructured, uid string) unstructured.Unstructured {
		c.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "job-x", UID: types.UID(uid)}})
		return c
	}
	ephemeralPod := func(name, uid, phase string, volumes ...string) unstructured.Unstructured {
		var vs []any
		for _, v := range volumes {
			vs = append(vs, map[string]any{"name": v, "ephemeral": map[string]any{"volumeClaimTemplate": map[string]any{
				"spec": map[string]any{"accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": "10Gi"}}}}}})
		}
		return obj(t, map[string]any{
			"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"namespace": "shop", "name": name, "uid": uid},
			"spec":   map[string]any{"nodeName": "node-a", "containers": []any{map[string]any{"name": "app", "image": "app:1"}}, "volumes": vs},
			"status": map[string]any{"phase": phase},
		})
	}
	h.add("persistentvolumeclaims",
		ownedBy(claim(t, "shop", "job-x-scratch", "gp3", "10Gi", "pv-scratch", nil), "u-job-x"),
		// Owned by job-x, which has no volume for it.
		ownedBy(claim(t, "shop", "job-x-cache", "gp3", "1Gi", "pv-c", nil), "u-job-x"),
		// Named like job-x's, owned by an earlier job-x: another pod's claim.
		ownedBy(claim(t, "shop", "job-x-old", "gp3", "1Gi", "pv-o", nil), "u-earlier"),
		// Named like web's, and nobody's.
		claim(t, "shop", "web-data", "gp3", "1Gi", "pv-w", nil),
	)
	h.add("pods", ephemeralPod("job-x", "u-job-x", "Running", "scratch", "old"), ephemeralPod("web", "u-web", "Pending", "data"))
	h.measured("node-a", "shop/job-x-scratch", 2*gi, 10*gi)
	a := overview(t, h, "")
	c := claimNamed(t, a, "shop", "job-x-scratch")
	if c.Mount != MountMounted || c.Orphan != "" || len(c.Pods) != 1 || c.Pods[0] != "job-x" || c.Fill.State != FillMeasured {
		t.Errorf("an ephemeral claim = mount %q orphan %q pods %v fill %+v, want mounted by job-x and measured", c.Mount, c.Orphan, c.Pods, c.Fill)
	}
	for _, name := range []string{"job-x-cache", "job-x-old", "web-data"} {
		if c := claimNamed(t, a, "shop", name); c.Mount != MountUnmounted || c.Orphan != OrphanUnmounted {
			t.Errorf("%s = mount %q orphan %q pods %v, want an unmounted orphan", name, c.Mount, c.Orphan, c.Pods)
		}
	}

	// The pod tab follows the same rule: job-x lists the claim it owns, and
	// neither web nor job-x lists a claim that only shares a name.
	names := func(p PodAnswer) []string {
		var out []string
		for _, c := range p.Claims {
			out = append(out, c.Name)
		}
		return out
	}
	if got := names(decode[PodAnswer](t, serve(t, h, "/x/prod/storage/pod/shop/job-x"))); len(got) != 1 || got[0] != "job-x-scratch" {
		t.Errorf("job-x's tab lists %v, want [job-x-scratch]", got)
	}
	if got := names(decode[PodAnswer](t, serve(t, h, "/x/prod/storage/pod/shop/web"))); len(got) != 0 {
		t.Errorf("web's tab lists %v, want nothing: web-data is not its claim", got)
	}
}

// Kubelets that failed are not a missing grant: the line says they did not
// answer, and never asks for nodes/proxy.
func TestFailedKubeletsAreNotARefusal(t *testing.T) {
	at(t)
	h := shop(t)
	h.silent["node-a"], h.silent["node-b"] = errors.New("dial tcp 10.0.0.1:10250: i/o timeout"), errors.New("node is not ready")
	a := overview(t, h, "")
	if a.Fill.State != SourceUnreadable || a.Fill.Message != kubeletsFailed || strings.Contains(a.Fill.Message, "nodes/proxy") {
		t.Errorf("fill = %+v, want unreadable, the kubelets failed", a.Fill)
	}
	h.silent = map[string]error{}
	h.usageErr = errors.New("context deadline exceeded")
	if a := overview(t, h, ""); a.Fill.Message != kubeletsFailed {
		t.Errorf("a failed Usage = %+v, want the kubelets failed", a.Fill)
	}
	h.usageErr = extension.NotGranted(apierrors.NewForbidden(schema.GroupResource{Resource: "nodes/proxy"}, "node-a", errors.New("no")))
	if a := overview(t, h, ""); a.Fill.Message != needsNodeProxy {
		t.Errorf("the console's account refused = %+v, want needs nodes/proxy", a.Fill)
	}
}

// A filesystem the kubelet reports with no size is no figure.
func TestAZeroCapacityIsUnread(t *testing.T) {
	at(t)
	h := shop(t)
	h.measured("node-b", "shop/cache", 0, 0)
	if c := claimNamed(t, overview(t, h, ""), "shop", "cache"); c.Fill.State != FillUnread || c.Fill.UsedBytes != 0 {
		t.Errorf("capacity 0 = %+v, want unread", c.Fill)
	}
}

// A dated name is not a StatefulSet's ordinal.
func TestADatedClaimIsNotAGoneStatefulSets(t *testing.T) {
	at(t)
	h := shop(t)
	h.add("persistentvolumeclaims", claim(t, "shop", "backup-20241101", "gp3", "1Gi", "pv-b", map[string]any{"app": "backup"}))
	if c := claimNamed(t, overview(t, h, ""), "shop", "backup-20241101"); c.Orphan != OrphanUnmounted {
		t.Errorf("backup-20241101 = %q, want unmounted", c.Orphan)
	}
}

// A catalog the person may not read is a refusal of the snapshots, not a
// failure.
func TestARefusedCatalogRefusesTheSnapshots(t *testing.T) {
	at(t)
	h := shop(t)
	h.defErr = fmt.Errorf("%w: list customresourcedefinitions", extension.ErrDenied)
	if s := overview(t, h, "").Snapshots; s.State != StateRefused {
		t.Errorf("snapshots = %+v, want refused", s)
	}
	h.defErr = errors.New("the server is currently unable to handle the request")
	if s := overview(t, h, "").Snapshots; s.State != StateFailed {
		t.Errorf("snapshots = %+v, want failed", s)
	}
}
