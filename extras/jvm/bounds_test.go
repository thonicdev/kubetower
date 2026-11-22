package jvm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
)

// What keeps the desktop's memory bounded whatever the cluster: the read is
// capped, the answer is capped, nothing is cached, and no goroutine outlives
// its request.

// bigCluster is pods pods in ten namespaces, one in every of them Java with a
// heap and limit spread over every grade, the rest nginx. Each shape is
// converted once and copied, so the fixture itself is cheap.
func bigCluster(t *testing.T, pods, every int) *fakeHost {
	t.Helper()
	h := newHost(t)
	limits := []string{"512Mi", "1Gi", "2Gi", "4Gi"}
	heaps := []string{"-Xmx256m", "-Xmx900m", "-Xmx2g", "-XX:MaxRAMPercentage=50"}
	shapes := map[string]*unstructured.Unstructured{}
	shape := func(c corev1.Container) *unstructured.Unstructured {
		key := c.Image + strings.Join(c.Args, " ") + c.Resources.Limits.Memory().String()
		if u, ok := shapes[key]; ok {
			return u
		}
		p := pod("", "", c)
		yes := true
		p.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "app", Controller: &yes}}
		u := &newHost(t, p).pods[0]
		shapes[key] = u
		return u
	}
	h.pods = make([]unstructured.Unstructured, 0, pods)
	h.live = map[string]extension.Resources{}
	for i := 0; i < pods; i++ {
		ns := fmt.Sprintf("team-%d", i%10)
		name := fmt.Sprintf("app-%05d", i)
		c := corev1.Container{Name: "web", Image: "nginx:1.27"}
		if i%every == 0 {
			c = memory(corev1.Container{Name: "app", Image: "eclipse-temurin:21-jre",
				Args: []string{heaps[i%4], "-jar", "app.jar"}}, "", limits[(i/every)%4])
			h.live[extension.ContainerKey(ns, name, "app")] = extension.Resources{MemoryBytes: int64(i) << 16}
		}
		u := shape(c).DeepCopy()
		u.SetNamespace(ns)
		u.SetName(name)
		h.pods = append(h.pods, *u)
	}
	return h
}

// Five thousand pods, a thousand of them Java, through the real route: the
// answer is whole, and its time and its allocations stay under ceilings set
// far above what it measures (about 2 s and 25 MiB under -race on a 4-core
// machine, 0.2 s without), so
// a loaded runner does not flake it and a regression by an order of
// magnitude does.
func TestAFiveThousandPodClusterStaysUnderItsCeilings(t *testing.T) {
	if testing.Short() {
		t.Skip("a load test")
	}
	h := bigCluster(t, 5000, 5)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	rec := serve(t, h, "/x/kind/jvm/")
	took := time.Since(start)
	runtime.ReadMemStats(&after)

	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d %.200s", rec.Code, rec.Body)
	}
	got := decode[inventoryAnswer](t, rec)
	if got.Read != 5000 || got.Found != 1000 || len(got.Pods) != 1000 || !got.Complete {
		t.Fatalf("read %d, found %d, answered %d, complete %v", got.Read, got.Found, len(got.Pods), got.Complete)
	}
	const timeCeiling, allocCeiling = 10 * time.Second, 256 << 20
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("5000 pods: %v, %d MiB allocated, answer %d KiB", took, allocated>>20, rec.Body.Len()>>10)
	if took > timeCeiling {
		t.Errorf("took %v, ceiling %v", took, timeCeiling)
	}
	if allocated > allocCeiling {
		t.Errorf("allocated %d MiB, ceiling %d MiB", allocated>>20, allocCeiling>>20)
	}
	// The column reads the same cluster in one list, under the same ceilings.
	start = time.Now()
	cells := column(t, h, "pods")
	if len(cells) != 1000 || time.Since(start) > timeCeiling {
		t.Errorf("column: %d cells in %v", len(cells), time.Since(start))
	}
}

// Past MaxJavaPods the answer is cut to the worst ones, says so, and says how
// many there were; the read itself asks the cluster for at most MaxPodsRead.
func TestTheInventoryIsCapped(t *testing.T) {
	h := bigCluster(t, MaxJavaPods+50, 1)
	got := decode[inventoryAnswer](t, serve(t, h, "/x/kind/jvm/"))
	if len(got.Pods) != MaxJavaPods || got.Found != MaxJavaPods+50 || got.Complete ||
		!strings.Contains(got.Note, fmt.Sprint(MaxJavaPods+50)+" Java pods were found") {
		t.Fatalf("answered %d of %d, complete %v, note %q", len(got.Pods), got.Found, got.Complete, got.Note)
	}
	// The worst are kept: every OOMKill-bound pod of the read is in the answer.
	errors := 0
	for _, p := range got.Pods {
		if p.Containers[0].Risk.Level == riskError {
			errors++
		}
	}
	all := 0
	for i := 0; i < MaxJavaPods+50; i++ {
		// Every pod is Java here, so i%4 picks both: -Xmx2g under its 2Gi limit.
		if i%4 == 2 {
			all++
		}
	}
	if errors != all {
		t.Errorf("%d OOMKill-bound pods kept of %d", errors, all)
	}
	// Still in namespace and name order.
	for i := 1; i < len(got.Pods); i++ {
		a, b := got.Pods[i-1], got.Pods[i]
		if a.Namespace > b.Namespace || (a.Namespace == b.Namespace && a.Name > b.Name) {
			t.Fatalf("out of order at %d: %s/%s then %s/%s", i, a.Namespace, a.Name, b.Namespace, b.Name)
		}
	}
	for _, q := range h.queries {
		if q.Limit != MaxPodsRead {
			t.Errorf("a read asked for %d pods, want the cap %d", q.Limit, MaxPodsRead)
		}
	}
	// A read the Host cut is not complete either, and its note comes through.
	h = bigCluster(t, 20, 1)
	q := extension.ListQuery{GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Limit: 10}
	if page, _ := h.List(nil, "kind", q); page.Complete || page.Note == "" {
		t.Fatalf("the fake does not cut at its limit: %+v", page)
	}
}

// A request the page abandons while metrics-server has not answered leaves
// no goroutine behind and writes nothing.
func TestAnAbandonedInventoryLeavesNoGoroutine(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	h := shop(t)
	h.usageHangs = true
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		r := httptest.NewRequest(http.MethodGet, "/x/kind/jvm/", nil).WithContext(ctx)
		r.SetPathValue("cluster", "kind")
		rec := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			New().inventory(h).ServeHTTP(rec, r)
			close(done)
		}()
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the handler did not return once its request was canceled")
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("wrote %q for a canceled request", rec.Body)
		}
	}
}
