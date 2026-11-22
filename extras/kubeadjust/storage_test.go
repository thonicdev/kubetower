package kubeadjust

import (
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/thonicdev/kubetower/extension"
)

// Tests of the storage reads, which need this extra's reads. The reads are Host
// doors, so they drive the route with a Host. What is pinned below the door -
// the `nodes/{node}/proxy/stats/summary` path, and the writable layer plus the
// logs - is pinned in the core, by internal/kubeletstats's own
// TestTheKubeletIsAskedThroughTheNodeProxy.

// The coverage sentence is computed in Assemble, and a value computed correctly
// and rendered nowhere is a recurring defect: deleting the assignment of
// report.History leaves every engine test green. So this asserts the whole
// path, from a window on the Host to the sentence on the page.
func TestTheWindowReachesTheReportAndNotJustTheCalculation(t *testing.T) {
	flat := func(v float64) []extension.Sample {
		out := make([]extension.Sample, 180)
		for i := range out {
			out[i] = extension.Sample{T: float64(1_700_000_000 + 120*i), V: v}
		}
		return out
	}
	window := func(keys ...string) extension.Window {
		w := extension.Window{State: extension.WindowRead, Detail: "Prometheus, 6h at 2m", Complete: true,
			Containers: map[string]extension.ContainerWindow{}}
		for _, k := range keys {
			w.Containers[k] = extension.ContainerWindow{CPU: flat(180), Memory: flat(950 << 20)}
		}
		return w
	}
	all := []string{"shop/api-7d9f-a/app", "shop/api-7d9f-b/app", "shop/api-7d9f-c/app"}

	t.Run("a full window is named on the page", func(t *testing.T) {
		h := shop(t)
		h.window = window(all...)
		n := noteOf(t, page(t, h, url.Values{"ns": {"shop"}}), "History")
		if !n.OK {
			t.Fatalf("the page says there is no window while one was read: %q", n.Detail)
		}
		for _, want := range []string{"Prometheus", "6h", "2m", "a window for all 3 containers"} {
			if !strings.Contains(n.Detail, want) {
				t.Errorf("History does not say %q: %q", want, n.Detail)
			}
		}
	})

	t.Run("a partial window is named on the page", func(t *testing.T) {
		h := shop(t)
		h.window = window(all[0])
		n := noteOf(t, page(t, h, url.Values{"ns": {"shop"}}), "History")
		if !strings.Contains(n.Detail, "1 of 3 containers have a window") {
			t.Errorf("History does not name the partial coverage: %q", n.Detail)
		}
	})

	t.Run("a narrowed window says so", func(t *testing.T) {
		h := shop(t)
		h.window = window(all...)
		h.window.Complete, h.window.Note = false, "You may not list pods in every namespace."
		n := noteOf(t, page(t, h, url.Values{}), "History")
		if !strings.Contains(n.Detail, "You may not list pods") {
			t.Errorf("a narrowed window is drawn as a whole one: %q", n.Detail)
		}
	})

	// No window still says what that costs, rather than an empty line.
	t.Run("no window still says what that costs", func(t *testing.T) {
		n := noteOf(t, page(t, shop(t), url.Values{}), "History")
		if n.OK {
			t.Error("a page with no window claims one")
		}
		for _, want := range []string{"no P95", "no burst ratio", "no trend"} {
			if !strings.Contains(n.Detail, want) {
				t.Errorf("History does not say %q: %q", want, n.Detail)
			}
		}
	})

	t.Run("a window the core could not ask for is said, not dropped", func(t *testing.T) {
		h := shop(t)
		h.windowErr = errors.New("\"Bad_Namespace\" is not a namespace name")
		n := noteOf(t, page(t, h, url.Values{}), "History")
		if n.OK || !strings.Contains(n.Detail, "not a namespace name") {
			t.Errorf("History = %+v", n)
		}
	})
}

// The kubelets asked are exactly the nodes hosting a running pod in scope, and
// what they answer lands on the container and the volume it describes.
func TestTheKubeletIsAskedThroughTheNodeProxy(t *testing.T) {
	h := shop(t)
	h.add("nodes", node("worker-2", "4", "8Gi"))
	h.add("pods", runningPod("data", "db-0", "", "worker-2", app("100m", "128Mi", "", "")))
	h.usage.Containers["data/db-0/app"] = extension.Resources{CPUMillicores: 50, MemoryBytes: 64 << 20}

	// The first pod in `shop` gets an emptyDir called cache, to read its volume back.
	pods := h.objects["pods"]
	var api corev1.Pod
	fromUnstructured(t, pods[0].Object, &api)
	limit := resource.MustParse("2Gi")
	api.Spec.Volumes = []corev1.Volume{{Name: "cache", VolumeSource: corev1.VolumeSource{
		EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &limit}}}}
	h.objects["pods"] = pods[1:]
	h.add("pods", &api)

	h.disk = extension.Usage{Asked: 1, Answered: 1, Nodes: map[string]extension.NodeUsage{
		"worker-1": {Pods: []extension.PodUsage{{Name: "api-7d9f-a", Namespace: "shop",
			Containers: []extension.ContainerUsage{{Name: "app", EphemeralBytes: 1500}},
			Volumes:    []extension.VolumeUsage{{Name: "cache", FS: extension.FS{UsedBytes: 1 << 30, CapacityBytes: 40 << 30}}},
		}}},
	}}

	a := page(t, h, url.Values{"ns": {"shop"}})
	if len(h.diskAsked) != 1 || !slices.Equal(h.diskAsked[0], []string{"worker-1"}) {
		t.Fatalf("asked %v, want exactly [worker-1]: only the nodes hosting the page's pods", h.diskAsked)
	}
	if d := noteOf(t, a, "Disk"); !d.OK || !strings.Contains(d.Detail, "1 node(s)") {
		t.Fatalf("a kubelet answered and the page says disk is unavailable: %+v", d)
	}
	var cache *volume
	for i := range a.Cards[0].Volumes {
		if a.Cards[0].Volumes[i].Pod == "api-7d9f-a" && a.Cards[0].Volumes[i].Name == "cache" {
			cache = &a.Cards[0].Volumes[i]
		}
	}
	if cache == nil || !cache.Measured || cache.Used != "1.00 GiB" || cache.Capacity != "2.00 GiB" || cache.CeilingWord != "sizeLimit" {
		t.Errorf("the kubelet's figure did not land on its volume: %+v (all: %+v)", cache, a.Cards[0].Volumes)
	}
	if e := a.Cards[0].Containers[0].Ephemeral; !e.Measured {
		t.Errorf("the ephemeral figure did not land on its container: %+v", e)
	}

	// And cluster-wide, every hosting node and no other, sorted.
	h.diskAsked = nil
	page(t, h, url.Values{})
	if len(h.diskAsked) != 1 || !slices.Equal(h.diskAsked[0], []string{"worker-1", "worker-2"}) {
		t.Errorf("asked %v cluster-wide, want [worker-1 worker-2]", h.diskAsked)
	}
}

// A cluster that refuses `nodes/proxy` must say so: the engine skips a volume
// whose usage is zero, so the sentence is the only difference between "nothing
// is wrong" and "nothing was asked".
func TestARefusedNodeProxyIsSaidRatherThanDrawnAsCleanDisks(t *testing.T) {
	h := shop(t)
	h.disk = extension.Usage{Asked: 1, Answered: 0, Nodes: map[string]extension.NodeUsage{},
		FirstErr: errors.New(`nodes "worker-1" is forbidden: User cannot get resource "nodes/proxy"`)}
	d := noteOf(t, page(t, h, url.Values{"ns": {"shop"}}), "Disk")
	if d.OK {
		t.Fatal("a cluster that refused every kubelet reports disk as available")
	}
	if !strings.Contains(d.Detail, "nodes/proxy") || !strings.Contains(d.Detail, "unmeasured rather than empty") {
		t.Errorf("the reason is %q, which does not name nodes/proxy", d.Detail)
	}
}

// Some answered and some did not: three of forty kubelets is a real answer about
// three nodes and no answer at all about thirty-seven.
func TestAPartialAnswerSaysHowPartial(t *testing.T) {
	h := shop(t)
	h.add("nodes", node("worker-2", "4", "8Gi"))
	h.add("pods", runningPod("data", "db-0", "", "worker-2", app("100m", "128Mi", "", "")))
	h.usage.Containers["data/db-0/app"] = extension.Resources{CPUMillicores: 50, MemoryBytes: 64 << 20}
	h.disk = extension.Usage{Asked: 2, Answered: 1, Nodes: map[string]extension.NodeUsage{"worker-1": {}},
		FirstErr: errors.New("kubelet unreachable")}
	d := noteOf(t, page(t, h, url.Values{}), "Disk")
	if !d.OK {
		t.Fatalf("one kubelet answered and the whole capability is reported off: %q", d.Detail)
	}
	if !strings.Contains(d.Detail, "1 of 2") || !strings.Contains(d.Detail, "kubelet unreachable") {
		t.Errorf("the note is %q; it must say how much of the cluster it covers", d.Detail)
	}
}
