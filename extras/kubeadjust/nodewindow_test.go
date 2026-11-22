package kubeadjust

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/extras/kubeadjust/rightsizing"
)

// The node tab's usage window covers the namespaces of that node's
// pods, and nothing else.

// spread is a two-node cluster over four namespaces. worker-1 runs three of
// shop's api replicas and billing's ledger-0; worker-2 runs api's fourth
// replica, data's db-0 and kube-system's coredns-1. Prometheus has a window
// for every one of those seven containers.
func spread(t *testing.T) *fakeHost {
	h := shop(t)
	h.add("nodes", node("worker-2", "8", "16Gi"))
	h.add("namespaces", namespaceObj("billing"), namespaceObj("kube-system"))
	h.add("pods",
		runningPod("shop", "api-7d9f-d", "api-7d9f", "worker-2", app("250m", "512Mi", "500m", "1Gi")),
		runningPod("billing", "ledger-0", "", "worker-1", app("500m", "1Gi", "", "")),
		runningPod("data", "db-0", "", "worker-2", app("1", "2Gi", "", "")),
		runningPod("kube-system", "coredns-1", "", "worker-2", app("100m", "70Mi", "", "")),
	)
	for k, v := range map[string][2]int64{
		"shop/api-7d9f-d/app":       {160, 920 << 20},
		"billing/ledger-0/app":      {300, 600 << 20},
		"data/db-0/app":             {700, 1500 << 20},
		"kube-system/coredns-1/app": {20, 30 << 20},
	} {
		h.usage.Containers[k] = extension.Resources{CPUMillicores: v[0], MemoryBytes: v[1]}
	}
	h.window = extension.Window{State: extension.WindowRead, Detail: "Prometheus, 6h at 2m", Complete: true,
		Containers: map[string]extension.ContainerWindow{}}
	for k := range h.usage.Containers {
		h.window.Containers[k] = extension.ContainerWindow{
			CPU:    []extension.Sample{{T: 1, V: 100}, {T: 121, V: 140}},
			Memory: []extension.Sample{{T: 1, V: 800 << 20}, {T: 121, V: 900 << 20}},
		}
	}
	return h
}

// resetWindow forgets what the window reads cost so far.
func (h *fakeHost) resetWindow() {
	h.windowFor, h.promQueries, h.seriesHanded, h.handed = nil, 0, 0, nil
}

// The measurement, on the fake collector: the page's one read is every
// namespace's series; the node tab's is three queries per namespace its pods
// are in, and only those namespaces' series. Against the cluster-wide read this
// test fails: windowFor would be [""] and the series handed back would carry
// data's and kube-system's.
func TestTheNodeTabReadsTheWindowOfItsPodsNamespacesOnly(t *testing.T) {
	h := spread(t)

	page(t, h, url.Values{})
	if strings.Join(h.windowFor, ",") != "" || len(h.windowFor) != 1 {
		t.Fatalf("the page asked the window for %q, want one cluster-wide read", h.windowFor)
	}
	wholeQueries, wholeSeries := h.promQueries, h.seriesHanded
	if wholeQueries != 3 || wholeSeries != 14 {
		t.Errorf("the cluster-wide read cost %d queries and %d series, want 3 and 14", wholeQueries, wholeSeries)
	}

	h.resetWindow()
	a := nodeTab(t, h, "worker-1")
	asked := slices.Sorted(slices.Values(h.windowFor))
	if strings.Join(asked, ",") != "billing,shop" {
		t.Errorf("the node tab asked the window for %q, want billing and shop: the namespaces of worker-1's pods", asked)
	}
	for _, k := range h.handed {
		if strings.HasPrefix(k, "data/") || strings.HasPrefix(k, "kube-system/") {
			t.Errorf("the node tab was handed %s, whose namespace has no pod on worker-1", k)
		}
	}
	// shop's four api replicas - the one on worker-2 too, since a workload's
	// suggestion pools every replica - and billing's ledger-0, each with a CPU
	// and a memory series.
	if h.promQueries != 6 || h.seriesHanded != 10 {
		t.Errorf("the node tab cost %d queries and %d series, want 6 (3 per namespace) and 10", h.promQueries, h.seriesHanded)
	}
	t.Logf("cluster-wide read: %d queries, %d series; worker-1's tab: %d queries, %d series",
		wholeQueries, wholeSeries, h.promQueries, h.seriesHanded)

	n := noteOf(t, answer{Notes: a.Notes}, "History")
	if !n.OK || !strings.Contains(n.Detail, "read for the 2 namespaces this node's pods are in") {
		t.Errorf("History note = %+v, want the window said to be the node's namespaces'", n)
	}
	// The denominator is this node's containers - api a, b, c and ledger-0 -
	// not every container on the cluster.
	if !strings.Contains(n.Detail, "a window for all 4 containers") {
		t.Errorf("History note = %q, want the coverage over worker-1's 4 containers", n.Detail)
	}
	if len(a.Suggestions) == 0 {
		t.Error("no suggestion on worker-1: the window should not have cost the tab its findings")
	}
}

// Two people, two namespaces, one node. Each one's tab reads the window of
// their own namespace and no other, and is handed none of the other's series.
func TestEachPersonsNodeTabReadsTheWindowOfTheirOwnNamespaces(t *testing.T) {
	for _, tc := range []struct {
		who     string
		allowed string
		other   string
	}{
		{"alice", "shop", "billing"},
		{"bob", "billing", "shop"},
	} {
		t.Run(tc.who, func(t *testing.T) {
			h := spread(t)
			h.allowed, h.nodesListable = []string{tc.allowed}, true
			rec := getNode(t, h, "worker-1")
			if rec.Code != 200 {
				t.Fatalf("GET node/worker-1 = %d %s", rec.Code, rec.Body.String())
			}
			if strings.Join(h.windowFor, ",") != tc.allowed {
				t.Errorf("%s's tab asked the window for %q, want %s alone", tc.who, h.windowFor, tc.allowed)
			}
			if len(h.handed) == 0 {
				t.Errorf("%s was handed no series of %s", tc.who, tc.allowed)
			}
			for _, k := range h.handed {
				if !strings.HasPrefix(k, tc.allowed+"/") {
					t.Errorf("%s was handed %s", tc.who, k)
				}
			}
			if strings.Contains(rec.Body.String(), tc.other+"/") || strings.Contains(rec.Body.String(), `"`+tc.other+`"`) {
				t.Errorf("%s's tab names %s: %s", tc.who, tc.other, rec.Body.String())
			}
		})
	}
}

// The namespaces' windows fold into one, and a namespace without one is named
// rather than drawn as a namespace using nothing.
func TestTheNamespacesWindowsFoldIntoOne(t *testing.T) {
	read := func(keys ...string) extension.Window {
		w := extension.Window{State: extension.WindowRead, Detail: "Prometheus, 6h at 2m", Complete: true,
			Containers: map[string]extension.ContainerWindow{}}
		for _, k := range keys {
			w.Containers[k] = extension.ContainerWindow{CPU: []extension.Sample{{T: 1, V: 1}}}
		}
		return w
	}
	partialRead := func(key, note string) extension.Window {
		w := read(key)
		w.Partial, w.Detail = note, w.Detail+" · "+note
		return w
	}
	empty := extension.Window{State: extension.WindowEmpty, Detail: "Prometheus has no container series for namespace x"}
	noProm := extension.Window{State: extension.WindowNoPrometheus, Detail: "this installation has no Prometheus configured"}
	for _, c := range []struct {
		name    string
		answers []namespaceWindow
		state   extension.WindowState
		keys    int
		want    string
	}{
		{"both read", []namespaceWindow{{"a", read("a/p/c"), nil}, {"b", read("b/q/c"), nil}},
			extension.WindowRead, 2, "read for the 2 namespaces"},
		{"one refused", []namespaceWindow{{"a", read("a/p/c"), nil}, {"b", extension.Window{}, errors.New("connection refused")}},
			extension.WindowRead, 1, "no window for b (connection refused)"},
		{"one empty", []namespaceWindow{{"a", read("a/p/c"), nil}, {"b", empty, nil}},
			extension.WindowRead, 1, "read for the 2 namespaces"},
		{"all empty", []namespaceWindow{{"a", empty, nil}, {"b", empty, nil}},
			extension.WindowEmpty, 0, "no container series in the namespaces of this node's pods"},
		{"no Prometheus", []namespaceWindow{{"a", noProm, nil}, {"b", noProm, nil}},
			extension.WindowNoPrometheus, 0, "no Prometheus configured"},
		{"one read in part", []namespaceWindow{{"a", partialRead("a/p/c", "1 of 2 batches could not be read in full (timeout)"), nil}, {"b", read("b/p/c"), nil}},
			extension.WindowRead, 2, "a: 1 of 2 batches could not be read in full (timeout)"},
		{"unreadable and empty", []namespaceWindow{{"a", empty, nil}, {"b", extension.Window{State: extension.WindowUnreadable, Detail: "timeout"}, nil}},
			extension.WindowUnreadable, 0, "no window for b (timeout)"},
	} {
		got := mergeWindows(c.answers)
		if got.State != c.state || len(got.Containers) != c.keys || !strings.Contains(got.Detail, c.want) {
			t.Errorf("%s: %s, %d containers, %q; want %s, %d, containing %q",
				c.name, got.State, len(got.Containers), got.Detail, c.state, c.keys, c.want)
		}
	}
}

// A node running no pod asks Prometheus nothing.
func TestAnEmptyNodesTabAsksForNoWindow(t *testing.T) {
	h := spread(t)
	h.add("nodes", node("worker-3", "8", "16Gi"))
	h.resetWindow()
	nodeTab(t, h, "worker-3")
	if len(h.windowFor) != 0 || h.promQueries != 0 {
		t.Errorf("an empty node's tab asked the window for %q (%d queries)", h.windowFor, h.promQueries)
	}
}

// A finished pod uses nothing and is not drawn, so a namespace only finished
// pods are in is not read for; another node's pods are not either.
func TestANodesFinishedPodsAddNoNamespace(t *testing.T) {
	done := runningPod("batch", "job-1", "", "worker-1", app("100m", "64Mi", "", ""))
	done.Status.Phase = corev1.PodSucceeded
	failed := runningPod("jobs", "job-2", "", "worker-1", app("100m", "64Mi", "", ""))
	failed.Status.Phase = corev1.PodFailed
	pods := []corev1.Pod{
		*runningPod("shop", "api-1", "", "worker-1", app("100m", "64Mi", "", "")),
		*done, *failed,
		*runningPod("data", "db-0", "", "worker-2", app("1", "2Gi", "", "")),
	}
	if got := nodeNamespaces("worker-1", pods); !slices.Equal(got, []string{"shop"}) {
		t.Errorf("worker-1's namespaces are %q, want [shop]: a finished pod's namespace is not read for", got)
	}
}

// slowWindows answers every window after a pause, and counts how many it was
// asked for at once.
type slowWindows struct {
	*fakeHost
	inFlight, most atomic.Int32
}

func (s *slowWindows) UsageWindow(r *http.Request, cluster, namespace string) (extension.Window, error) {
	n := s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	for {
		m := s.most.Load()
		if n <= m || s.most.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	return s.fakeHost.UsageWindow(r, cluster, namespace)
}

// A node spread over many namespaces reads windowConcurrency of them at a
// time, never all at once, and still reads every one.
func TestTheNodeTabReadsItsNamespacesWindowsABoundedFewAtATime(t *testing.T) {
	// The bound the comment on windowConcurrency promises, written out: a test
	// that compared with windowConcurrency itself would pass at any value.
	const bound = 4
	h := &slowWindows{fakeHost: spread(t)}
	var pods []corev1.Pod
	for i := range 3 * bound {
		ns := "ns-" + string(rune('a'+i))
		pods = append(pods, *runningPod(ns, "p", "", "worker-1", app("100m", "64Mi", "", "")))
	}
	h.resetWindow()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, err := nodeWindow(r, h, "kind", "worker-1", pods); err != nil {
		t.Fatal(err)
	}
	if len(h.windowFor) != len(pods) {
		t.Errorf("read %d windows for %d namespaces", len(h.windowFor), len(pods))
	}
	if got := int(h.most.Load()); got > bound || got < 2 {
		t.Errorf("%d windows were read at once; want between 2 and %d", got, bound)
	}
}

// A node tab's window is as old as the oldest of its namespaces' reads.
func TestTheNodeTabsWindowIsAsOldAsItsOldestRead(t *testing.T) {
	older := time.Unix(1_788_294_000, 0)
	newer := older.Add(7 * time.Minute)
	read := func(key string, at time.Time) extension.Window {
		return extension.Window{State: extension.WindowRead, Detail: "Prometheus, 7d at 15m", Complete: true, ReadAt: at,
			Containers: map[string]extension.ContainerWindow{key: {}}}
	}
	got := mergeWindows([]namespaceWindow{
		{namespace: "billing", window: read("billing/l/c", newer)},
		{namespace: "shop", window: read("shop/a/c", older)},
		{namespace: "data", window: extension.Window{State: extension.WindowNoPrometheus}},
	})
	if !got.ReadAt.Equal(older) {
		t.Errorf("the merged window says it was read at %v, want the oldest, %v", got.ReadAt, older)
	}
}

// heldWindows holds every window read until release is closed, and counts
// the reads started.
type heldWindows struct {
	*fakeHost
	started atomic.Int32
	release chan struct{}
}

func (s *heldWindows) UsageWindow(r *http.Request, _, _ string) (extension.Window, error) {
	s.started.Add(1)
	<-s.release
	return extension.Window{}, r.Context().Err()
}

// A request that goes away while its first reads are held starts no read for
// the namespaces still waiting for a slot, and the tab answers its
// cancellation.
func TestTheNodeTabStopsReadingWindowsOnceTheRequestIsGone(t *testing.T) {
	h := &heldWindows{fakeHost: spread(t), release: make(chan struct{})}
	var pods []corev1.Pod
	for i := range 2 * windowConcurrency {
		pods = append(pods, *runningPod("ns-"+string(rune('a'+i)), "p", "", "worker-1", app("100m", "64Mi", "", "")))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := nodeWindow(r, h, "kind", "worker-1", pods)
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for h.started.Load() < windowConcurrency {
		if time.Now().After(deadline) {
			t.Fatalf("%d reads started, want %d in flight", h.started.Load(), windowConcurrency)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	close(h.release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("nodeWindow answered %v, want the cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nodeWindow did not return after the request was canceled")
	}
	if got := h.started.Load(); got != windowConcurrency {
		t.Errorf("%d window reads started for %d namespaces, want %d: none after the request was gone",
			got, len(pods), windowConcurrency)
	}
}

// One namespace's failed half is said of that namespace, by name, whichever
// order the namespaces are read in - never dropped, never said of the node.
func TestAFailedHalfIsSaidOfItsOwnNamespace(t *testing.T) {
	clean := extension.Window{State: extension.WindowRead, Detail: "Prometheus, 6h at 2m", Complete: true,
		Containers: map[string]extension.ContainerWindow{"billing/l/c": {}}}
	noCPU := extension.Window{State: extension.WindowRead, Complete: true, CPUFailed: "timeout",
		Detail:     "Prometheus, 6h at 2m · memory only, the CPU window failed (timeout)",
		Containers: map[string]extension.ContainerWindow{"shop/p/c": {}}}
	for _, c := range []struct {
		name    string
		answers []namespaceWindow
	}{
		{"the failing one read second", []namespaceWindow{{"billing", clean, nil}, {"shop", noCPU, nil}}},
		{"the failing one read first", []namespaceWindow{{"shop", noCPU, nil}, {"billing", clean, nil}}},
	} {
		got := mergeWindows(c.answers)
		if !strings.Contains(got.Detail, "shop: memory only, the CPU window failed (timeout)") {
			t.Errorf("%s: %q does not say shop's CPU window failed", c.name, got.Detail)
		}
		if !strings.HasPrefix(got.Detail, "Prometheus, 6h at 2m · read for the 2 namespaces") {
			t.Errorf("%s: %q does not start with the span and the scope", c.name, got.Detail)
		}
		if strings.Count(got.Detail, "memory only") != 1 {
			t.Errorf("%s: %q says memory only more than once, or of the node", c.name, got.Detail)
		}
		if got.CPUFailed != "shop: timeout" || got.MemoryFailed != "" {
			t.Errorf("%s: CPUFailed %q, MemoryFailed %q; want \"shop: timeout\" and none", c.name, got.CPUFailed, got.MemoryFailed)
		}
	}
}

// Each narrowed namespace's note is kept, once each.
func TestEveryNarrowedNamespacesNoteIsKept(t *testing.T) {
	narrowed := func(key, note string) extension.Window {
		return extension.Window{State: extension.WindowRead, Detail: "Prometheus, 6h at 2m", Note: note,
			Containers: map[string]extension.ContainerWindow{key: {}}}
	}
	got := mergeWindows([]namespaceWindow{
		{"a", narrowed("a/p/c", "narrowed: one."), nil},
		{"b", narrowed("b/p/c", "narrowed: two."), nil},
		{"c", narrowed("c/p/c", "narrowed: one."), nil},
	})
	if got.Complete || got.Note != "narrowed: one. narrowed: two." {
		t.Errorf("complete %v, note %q; want incomplete with both notes once each", got.Complete, got.Note)
	}
}

// A node running nothing says so, and not that the suggestions below rest on
// a single sample: there are none.
func TestAnEmptyNodesHistorySaysOnlyThatNothingRuns(t *testing.T) {
	h := rightsizing.HistoryFromWindow(extension.Window{State: extension.WindowEmpty, Detail: noPodOnNode})
	got := nodeHistoryNote(h, "worker-3", nil, nil)
	if got.Detail != noPodOnNode {
		t.Errorf("an empty node's History is %q, want %q", got.Detail, noPodOnNode)
	}
}

// The memory half is said the same way, and one reason shared by several
// namespaces is said once, with every namespace it concerns.
func TestAFailedMemoryHalfAndASharedReasonAreSaidOnce(t *testing.T) {
	half := func(key, cpu, mem string) extension.Window {
		return extension.Window{State: extension.WindowRead, Complete: true, CPUFailed: cpu, MemoryFailed: mem,
			Detail:     "Prometheus, 6h at 2m · something the core added",
			Containers: map[string]extension.ContainerWindow{key: {}}}
	}
	got := mergeWindows([]namespaceWindow{
		{"billing", half("billing/l/c", "", "refused"), nil},
		{"data", half("data/d/c", "timeout", ""), nil},
		{"shop", half("shop/p/c", "timeout", ""), nil},
	})
	for _, want := range []string{
		"billing: CPU only, the memory window failed (refused)",
		"data, shop: memory only, the CPU window failed (timeout)",
	} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("%q does not say %q", got.Detail, want)
		}
	}
	if n := strings.Count(got.Detail, "(timeout)"); n != 1 {
		t.Errorf("%q says the shared reason %d times, want once", got.Detail, n)
	}
	if got.MemoryFailed != "billing: refused" || got.CPUFailed != "data, shop: timeout" {
		t.Errorf("MemoryFailed %q, CPUFailed %q; want \"billing: refused\" and \"data, shop: timeout\"",
			got.MemoryFailed, got.CPUFailed)
	}
}
