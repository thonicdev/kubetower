package cnpg

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
)

// Not installed, installed and refused, installed and empty: three answers,
// three codes, never one drawn as another.
func TestAbsentRefusedAndEmptyAreThreeAnswers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(h *fakeHost)
		status int
		code   string
	}{
		{"not installed", func(h *fakeHost) { h.reg.served = map[string]bool{} }, http.StatusNotFound, CodeKindUnknown},
		{"not served", func(h *fakeHost) {
			h.reg.errs = map[string]error{ClusterKind: fmt.Errorf("x: %w", extension.ErrNotServed)}
		}, http.StatusNotFound, CodeKindNotServed},
		{"catalog unreadable", func(h *fakeHost) { h.defErr = errors.New("discovery failed") }, http.StatusBadGateway, CodeCatalogueUnreadable},
		{"catalog refused", func(h *fakeHost) {
			h.defErr = apierrors.NewForbidden(schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"}, "", errors.New("no"))
		}, http.StatusForbidden, CodeListForbidden},
		{"kind resolution refused", func(h *fakeHost) {
			h.reg.errs = map[string]error{ClusterKind: apierrors.NewForbidden(schema.GroupResource{Resource: "customresourcedefinitions"}, "x", errors.New("no"))}
		}, http.StatusForbidden, CodeListForbidden},
		{"kind resolution failed", func(h *fakeHost) {
			h.reg.errs = map[string]error{ClusterKind: apierrors.NewInternalError(errors.New("boom"))}
		}, http.StatusBadGateway, CodeKindUnresolved},
		{"unknown cluster", func(h *fakeHost) { h.defErr = fmt.Errorf("x: %w", extension.ErrUnknownCluster) }, http.StatusNotFound, CodeUnknownCluster},
		{"denied", func(h *fakeHost) { h.listErr["clusters"] = fmt.Errorf("listing: %w", extension.ErrDenied) }, http.StatusForbidden, CodeListForbidden},
		{"unreviewable", func(h *fakeHost) { h.listErr["clusters"] = fmt.Errorf("listing: %w", extension.ErrUnreviewable) }, http.StatusServiceUnavailable, CodeListUnreviewed},
		{"failed", func(h *fakeHost) { h.listErr["clusters"] = errors.New("connection refused") }, http.StatusBadGateway, CodeListFailed},
		{"console not granted", func(h *fakeHost) {
			h.listErr["clusters"] = extension.NotGranted(apierrors.NewForbidden(
				schema.GroupResource{Group: "postgresql.cnpg.io", Resource: "clusters"}, "", errors.New("no")))
		}, http.StatusServiceUnavailable, extension.CodeConsoleNotGranted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost()
			tc.setup(h)
			rec := serve(t, h, "/x/prod/cnpg/")
			p := decode[problem](t, rec)
			if rec.Code != tc.status || p.Code != tc.code {
				t.Fatalf("answered %d %+v, want %d %s", rec.Code, p, tc.status, tc.code)
			}
		})
	}

	// Installed, readable, nothing there: a 200 with no cluster, not a 404.
	rec := serve(t, newHost(), "/x/prod/cnpg/")
	o := decode[Overview](t, rec)
	if rec.Code != http.StatusOK || len(o.Clusters) != 0 || !o.Complete {
		t.Fatalf("empty: %d %+v", rec.Code, o)
	}
}

// The person's refusal says it is about them and that the operator is there.
func TestARefusalSaysTheOperatorIsInstalled(t *testing.T) {
	h := newHost()
	h.listErr["clusters"] = fmt.Errorf("listing: %w", extension.ErrDenied)
	p := decode[problem](t, serve(t, h, "/x/prod/cnpg/"))
	if !strings.Contains(p.Message, "is installed") || !strings.Contains(p.Message, "you may not") {
		t.Fatalf("message %q", p.Message)
	}
}

func demoHost() *fakeHost {
	h := newHost()
	h.add("clusters",
		pgCluster("shop", "orders", 3, barman(), nil),
		pgCluster("shop", "carts", 2, nil, map[string]any{"readyInstances": int64(1)}),
		pgCluster("billing", "ledger", 2, barman(), map[string]any{
			"phase": "Switchover in progress", "targetPrimary": "ledger-2",
			"conditions": []any{map[string]any{"type": "ContinuousArchiving", "status": "False", "reason": "ContinuousArchivingFailing", "message": "exit status 2"}},
		}),
	)
	h.add("backups",
		pgBackup("shop", "orders-1", "orders", "completed", testNow.Add(-2*time.Hour)),
		pgBackup("shop", "orders-0", "orders", "completed", testNow.Add(-26*time.Hour)),
		pgBackup("billing", "ledger-0", "ledger", "completed", testNow.Add(-40*time.Hour)),
	)
	h.add("scheduledbackups", map[string]any{"metadata": map[string]any{"namespace": "shop", "name": "nightly"},
		"spec":   map[string]any{"cluster": map[string]any{"name": "orders"}, "schedule": "0 0 2 * * *"},
		"status": map[string]any{"lastScheduleTime": rfc(testNow.Add(-10 * time.Hour))}})
	h.add("poolers", map[string]any{"metadata": map[string]any{"namespace": "shop", "name": "orders-rw"},
		"spec":   map[string]any{"cluster": map[string]any{"name": "orders"}, "type": "rw", "instances": int64(2)},
		"status": map[string]any{"phase": "inactive", "phaseReason": `AuthUserSecret "orders-pooler" not found`}})
	h.add("databases", map[string]any{"metadata": map[string]any{"namespace": "shop", "name": "app"},
		"spec": map[string]any{"cluster": map[string]any{"name": "orders"}, "name": "app"}, "status": map[string]any{"applied": true}})
	h.add("pods", podDoc("shop", "orders", 1, "primary", "node-a"), podDoc("shop", "orders", 2, "replica", "node-b"),
		podDoc("shop", "orders", 3, "replica", "node-b"))
	h.add("events", map[string]any{"metadata": map[string]any{"namespace": "shop", "name": "e1"},
		"involvedObject": map[string]any{"kind": "Cluster", "name": "orders"}, "type": "Normal", "reason": "FailoverTarget",
		"message": "Failing over from orders-2 to orders-1", "lastTimestamp": rfc(testNow.Add(-10 * time.Minute))})
	return h
}

func TestTheOverviewCountsWhatTheTilesFilter(t *testing.T) {
	rec := serve(t, demoHost(), "/x/prod/cnpg/")
	o := decode[Overview](t, rec)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	want := Counts{Clusters: 3, Healthy: 1, Degraded: 1, Switchover: 1, ArchiveFailing: 1, BackupLate: 1, NotBackedUp: 1}
	if o.Counts != want {
		t.Fatalf("counts %+v, want %+v", o.Counts, want)
	}
	// Worst first: the switchover, then the degraded, then the healthy.
	if o.Clusters[0].Name != "ledger" || o.Clusters[1].Name != "carts" || o.Clusters[2].Name != "orders" {
		t.Fatalf("order %s %s %s", o.Clusters[0].Name, o.Clusters[1].Name, o.Clusters[2].Name)
	}
	orders := o.Clusters[2]
	if orders.Poolers != 1 || orders.Backup.Scheduled != 1 || len(orders.Backup.Recent) != 2 || orders.Backup.Recent[0].Name != "orders-1" {
		t.Fatalf("orders: %+v", orders)
	}
	for _, s := range o.Sources {
		if s.State != "read" {
			t.Fatalf("source %+v", s)
		}
	}
}

// A refused Backup list is a note, and the page is still drawn.
func TestARefusedSecondaryReadIsANote(t *testing.T) {
	h := demoHost()
	h.listErr["backups"] = fmt.Errorf("listing: %w", extension.ErrDenied)
	h.reg.served[PoolerKind] = false
	o := decode[Overview](t, serve(t, h, "/x/prod/cnpg/"))
	if len(o.Clusters) != 3 {
		t.Fatalf("clusters %d", len(o.Clusters))
	}
	states := map[string]string{}
	for _, s := range o.Sources {
		states[s.Kind] = s.State
	}
	if states[BackupKind] != "refused" || states[PoolerKind] != "absent" || states[ScheduledKind] != "read" {
		t.Fatalf("sources %v", states)
	}
}

func TestEveryListIsCapped(t *testing.T) {
	h := demoHost()
	serve(t, h, "/x/prod/cnpg/")
	serve(t, h, "/x/prod/cnpg/clusters/shop/orders")
	serve(t, h, "/x/prod/cnpg/pods")
	for _, q := range h.listed {
		if q.Limit <= 0 {
			t.Errorf("an uncapped list: %+v", q)
		}
	}
	if len(h.listed) == 0 {
		t.Fatal("nothing listed")
	}
}

func TestAClusterPage(t *testing.T) {
	h := demoHost()
	h.usage = extension.Usage{Asked: 2, Answered: 2, Complete: true, Nodes: map[string]extension.NodeUsage{
		"node-a": {Pods: []extension.PodUsage{{Namespace: "shop", Name: "orders-1", Volumes: []extension.VolumeUsage{
			{Name: "pgdata", PVCRef: "shop/orders-1", FS: extension.FS{UsedBytes: 300 << 20, CapacityBytes: 1 << 30}},
		}}}},
		"node-b": {Pods: []extension.PodUsage{{Namespace: "shop", Name: "orders-2", Volumes: []extension.VolumeUsage{
			{Name: "pgdata", PVCRef: "shop/orders-2", FS: extension.FS{UsedBytes: 200 << 20, CapacityBytes: 40 << 40}},
		}}}},
	}}
	rec := serve(t, h, "/x/prod/cnpg/clusters/shop/orders")
	d := decode[Detail](t, rec)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	if len(d.Members) != 3 || d.Members[0].Role != "primary" || d.Members[0].Pod == nil || d.Members[0].Pod.Node != "node-a" || d.Members[0].Pod.Restarts != 2 {
		t.Fatalf("members %+v", d.Members)
	}
	if d.Members[0].Data == nil || d.Members[0].Data.UsedBytes != 300<<20 || !d.Members[0].Data.Trustworthy {
		t.Fatalf("primary's disk %+v", d.Members[0].Data)
	}
	// A 40 TiB capacity for a 1 GiB claim is somebody else's disk.
	if d.Members[1].Data == nil || d.Members[1].Data.Trustworthy {
		t.Fatalf("a network share's capacity: %+v", d.Members[1].Data)
	}
	if d.Members[2].Data != nil {
		t.Fatalf("a volume no kubelet reported is not drawn: %+v", d.Members[2].Data)
	}
	if len(d.Backups) != 2 || len(d.Scheduled) != 1 || len(d.Poolers) != 1 || d.Poolers[0].Reason == "" || len(d.Declared) != 1 {
		t.Fatalf("objects: backups %d scheduled %d poolers %+v declared %+v", len(d.Backups), len(d.Scheduled), d.Poolers, d.Declared)
	}
	if len(d.Events) != 1 || d.Events[0].Reason != "FailoverTarget" {
		t.Fatalf("events %+v", d.Events)
	}
	if d.WriteService != "orders-rw" || d.DiskNote != "" {
		t.Fatalf("services %q, disk note %q", d.WriteService, d.DiskNote)
	}
}

// Disks the kubelets were not asked for are said, never drawn empty.
func TestUnmeasuredDisksAreSaid(t *testing.T) {
	h := demoHost()
	h.usageErr = fmt.Errorf("nodes: %w", extension.ErrDenied)
	d := decode[Detail](t, serve(t, h, "/x/prod/cnpg/clusters/shop/orders"))
	if !strings.Contains(d.DiskNote, "you may not list nodes") || d.Members[0].Data != nil {
		t.Fatalf("note %q, data %+v", d.DiskNote, d.Members[0].Data)
	}
}

func TestAClusterThatIsNotThereOrNotReadable(t *testing.T) {
	rec := serve(t, demoHost(), "/x/prod/cnpg/clusters/shop/nope")
	if p := decode[problem](t, rec); rec.Code != http.StatusNotFound || p.Code != CodeNotFound {
		t.Fatalf("%d %+v", rec.Code, p)
	}
	h := demoHost()
	h.listErr["clusters"] = fmt.Errorf("x: %w", extension.ErrDenied)
	rec = serve(t, h, "/x/prod/cnpg/clusters/shop/orders")
	if p := decode[problem](t, rec); rec.Code != http.StatusForbidden || p.Code != CodeListForbidden {
		t.Fatalf("%d %+v", rec.Code, p)
	}
}

func TestPodRolesForTheColumn(t *testing.T) {
	h := demoHost()
	h.add("pods", map[string]any{"metadata": map[string]any{"namespace": "shop", "name": "web-0", "labels": map[string]any{"app": "web"}}})
	rec := serve(t, h, "/x/prod/cnpg/pods")
	got := decode[PodRoles](t, rec)
	if rec.Code != http.StatusOK || len(got.Pods) != 3 || got.Pods["shop/orders-1"] != (PodRole{Role: "primary", Cluster: "orders"}) {
		t.Fatalf("%d %+v", rec.Code, got)
	}
	if h.listed[len(h.listed)-1].LabelSelector != instanceSelector {
		t.Fatalf("not filtered by the API server: %+v", h.listed[len(h.listed)-1])
	}
}

func TestAnInstancePodLeadsToItsCluster(t *testing.T) {
	rec := serve(t, demoHost(), "/x/prod/cnpg/pods/shop/orders-2")
	a := decode[PodAnswer](t, rec)
	if rec.Code != http.StatusOK || a.Of.Name != "orders" || a.Role != "replica" {
		t.Fatalf("%d %+v", rec.Code, a)
	}
	h := demoHost()
	h.add("pods", map[string]any{"metadata": map[string]any{"namespace": "shop", "name": "web-0"}})
	rec = serve(t, h, "/x/prod/cnpg/pods/shop/web-0")
	if p := decode[problem](t, rec); rec.Code != http.StatusNotFound || p.Code != CodeNotInstance {
		t.Fatalf("%d %+v", rec.Code, p)
	}
}

func TestEveryRouteIsAGet(t *testing.T) {
	for _, rt := range testExtra().Routes(newHost()) {
		if rt.Method != http.MethodGet {
			t.Fatalf("%s %s: this extra changes nothing, every route is a GET", rt.Method, rt.Pattern)
		}
	}
}
