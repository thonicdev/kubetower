package rightsizing

import (
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The two shortcuts the engine takes on its hottest path answer what the
// longer way answered: a rank found by selection is the rank a sort finds, and
// instants counted by sorting are the instants a set counts.

func TestASelectedRankIsTheSortedRank(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for trial := 0; trial < 500; trial++ {
		n := 1 + r.IntN(300)
		values := make([]float64, n)
		for i := range values {
			switch trial % 4 {
			case 0: // few distinct values: many ties
				values[i] = float64(r.IntN(5))
			case 1: // in order, as a growing memory series is
				values[i] = float64(i)
			case 2: // in reverse order
				values[i] = float64(n - i)
			default:
				values[i] = r.Float64() * 1000
			}
		}
		sorted := slices.Clone(values)
		slices.Sort(sorted)
		for _, k := range []int{0, n / 2, n - 1, r.IntN(n)} {
			if got := nth(slices.Clone(values), k); got != sorted[k] {
				t.Fatalf("trial %d: rank %d of %v is %v, a sort says %v", trial, k, values, got, sorted[k])
			}
		}
		idx := int(math.Ceil(float64(n)*0.95)) - 1
		if got := percentile95(values); got != sorted[max(idx, 0)] {
			t.Fatalf("trial %d: P95 %v, a sort says %v", trial, got, sorted[max(idx, 0)])
		}
	}
	if percentile95(nil) != 0 {
		t.Fatal("the P95 of nothing is zero")
	}
}

func TestPercentile95LeavesItsInputAlone(t *testing.T) {
	values := []float64{5, 1, 4, 2, 3}
	_ = percentile95(values)
	if !slices.Equal(values, []float64{5, 1, 4, 2, 3}) {
		t.Fatalf("percentile95 reordered its input: %v", values)
	}
}

func TestInstantsCountWhatASetCounts(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for trial := 0; trial < 300; trial++ {
		var series [][]Point
		for s := 0; s < 1+r.IntN(4); s++ {
			start := float64(r.IntN(20)) * 900
			var points []Point
			for i := 0; i < r.IntN(40); i++ {
				t := start + float64(i)*900
				if trial%5 == 1 && i%7 == 3 {
					t += 0.25 // off the grid: the sorted count answers
				}
				points = append(points, Point{T: t, V: 1})
			}
			if trial%3 == 0 {
				r.Shuffle(len(points), func(i, j int) { points[i], points[j] = points[j], points[i] })
			}
			series = append(series, points)
		}
		set := map[float64]bool{}
		first, last := math.Inf(1), math.Inf(-1)
		for _, points := range series {
			for _, p := range points {
				set[p.T] = true
				first, last = math.Min(first, p.T), math.Max(last, p.T)
			}
		}
		seen, f, l := instants(series, seriesStep(series...))
		if seen != len(set) || f != first || l != last {
			t.Fatalf("trial %d: instants said %d [%v, %v], a set says %d [%v, %v]", trial, seen, f, l, len(set), first, last)
		}
	}
}

// BenchmarkAssemble is a page's arithmetic over a week of history: 60
// workloads of two replicas and two containers, every container with a whole
// window. `go test -run - -bench Assemble ./extras/kubeadjust/rightsizing/`.
func BenchmarkAssemble(b *testing.B) {
	in := weekOfInputs(60, 2, 2)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = Assemble(in)
	}
}

// weekOfInputs is a scope of Deployments, each pod running for a month with a
// whole window per container.
func weekOfInputs(workloads, replicas, containers int) Inputs {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	r := rand.New(rand.NewPCG(5, 6))
	in := Inputs{Now: now, Usage: map[string]Usage{}, Owners: map[string]metav1.OwnerReference{}}
	controller := true
	step := 900.0
	end := float64(now.Unix())
	points := int((7*24*time.Hour).Seconds()/step) + 1
	var history []ContainerHistory
	for w := 0; w < workloads; w++ {
		for p := 0; p < replicas; p++ {
			pod := fmt.Sprintf("w%d-%d", w, p)
			var cs []corev1.Container
			for c := 0; c < containers; c++ {
				name := fmt.Sprintf("c%d", c)
				cs = append(cs, summaryContainer(name, 500, 512, 0, 0))
				in.Usage["ns/"+pod+"/"+name] = Usage{CPUMillicores: 20, MemoryBytes: 100 << 20}
				h := ContainerHistory{Namespace: "ns", Pod: pod, Container: name}
				for i := 0; i < points; i++ {
					t := end - float64(points-1-i)*step
					h.CPU = append(h.CPU, Point{T: t, V: 10 + r.Float64()*30})
					h.Memory = append(h.Memory, Point{T: t, V: float64(90<<20) + r.Float64()*float64(20<<20)})
				}
				history = append(history, h)
			}
			running := summaryPod("ns", pod, "node-1", corev1.PodRunning, cs...)
			started := metav1.NewTime(now.Add(-30 * 24 * time.Hour))
			running.Status.StartTime = &started
			rs := fmt.Sprintf("w%d-rs", w)
			running.OwnerReferences = []metav1.OwnerReference{{Kind: "ReplicaSet", Name: rs, Controller: &controller}}
			in.Owners["ReplicaSet/ns/"+rs] = metav1.OwnerReference{Kind: "Deployment", Name: fmt.Sprintf("w%d", w), Controller: &controller}
			in.Pods = append(in.Pods, running)
		}
	}
	in.History = History{Containers: history, State: Availability{OK: true, Detail: "Prometheus, 7d at 15m"}}
	return in
}

// TestTheMemoChangesNoFigure: Assemble takes each container's figures once and
// shares them between the engine and the bars; Compute and Readings, called on
// their own, take them every time. Both ways give the same page.
func TestTheMemoChangesNoFigure(t *testing.T) {
	in := weekOfInputs(6, 3, 2)
	// Some replicas with less of a window, and one crash-looping, so the
	// judgments differ between containers.
	in.History.Containers[0].CPU = in.History.Containers[0].CPU[600:]
	in.History.Containers[3].Memory = in.History.Containers[3].Memory[:40]
	report := Assemble(in)

	workloads, _ := group(in.Pods, in.Owners, in.Usage, in.Now)
	attachFormer(workloads, in.Pods, in.Owners, in.History.Containers)
	suggestions := Compute(workloads, in.History.Containers, nil)
	if !reflect.DeepEqual(report.Suggestions, suggestions) {
		t.Fatalf("the memo changed the suggestions:\n%+v\nwithout it:\n%+v", report.Suggestions, suggestions)
	}
	if want := Attach(Readings(workloads, in.History.Containers), suggestions); !reflect.DeepEqual(report.Readings, want) {
		t.Fatalf("the memo changed the readings:\n%+v\nwithout it:\n%+v", report.Readings, want)
	}
	if len(report.Suggestions) == 0 || len(report.Readings) == 0 {
		t.Fatalf("nothing to compare: %d suggestions, %d readings", len(report.Suggestions), len(report.Readings))
	}
}
