package overview

// A panic in any gathered task is that panel's failure, and not the process's.
//
// **A test that panics one task proves one task**, so this one is driven over
// the set, and the set is discovered rather than written out.

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// populated holds one of every kind the two dashboards ask for, so that each
// list call is really made and really answers.
func populated() []runtime.Object {
	meta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: "default"}
	}
	return []runtime.Object{
		node("node-1", "4", "16Gi", "110", ready(corev1.ConditionTrue)),
		pod("web-0", "node-1", corev1.PodRunning, corev1.Container{Name: "app", Resources: requesting("100m", "128Mi")}),
		&appsv1.Deployment{ObjectMeta: meta("web")},
		&appsv1.StatefulSet{ObjectMeta: meta("db")},
		&appsv1.DaemonSet{ObjectMeta: meta("agent")},
		&batchv1.Job{ObjectMeta: meta("import")},
		&batchv1.CronJob{ObjectMeta: meta("nightly")},
		&corev1.Event{ObjectMeta: meta("something"), Type: corev1.EventTypeWarning, Reason: "BackOff"},
	}
}

// call is one thing a dashboard asks of the API server, as the fake records it.
type call struct{ verb, resource string }

func (c call) String() string { return c.verb + " " + c.resource }

// asked runs one dashboard once and reports every call it made.
//
// Discovered rather than listed, and that is the property worth the most here: a
// thirteenth task that reaches the API server is driven by the loop below
// without anyone remembering to add it. A list written out by hand is
// correct on the day it is written and silent ever after — which is how
// unprotected tasks survive a test named after all of them.
//
// Per dashboard and not for both at once, because the two pages ask for
// different things: panicking the node list proves nothing about the workloads
// page, which never asks for nodes, and an assertion that expected it to notice
// would only be teaching the test to accept silence.
func asked(t *testing.T, draw func(*testing.T, *cluster.Client)) []call {
	t.Helper()
	set := fake.NewClientset(populated()...)
	draw(t, &cluster.Client{Context: "test", Clients: set})

	seen := map[call]bool{}
	var out []call
	for _, a := range set.Actions() {
		c := call{a.GetVerb(), a.GetResource().Resource}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// panicsOn is the cluster of the moment: everything answers except one call,
// which panics the way client-go can when it is handed something it did not
// expect.
func panicsOn(c call) *fake.Clientset {
	set := fake.NewClientset(populated()...)
	set.PrependReactor(c.verb, c.resource, func(k8stesting.Action) (bool, runtime.Object, error) {
		panic("client-go returned something this code did not expect: " + c.String())
	})
	return set
}

// saidSo is the assertion, and it is deliberately the weak one: *somewhere* on
// the page it says this could not be read. Which panel says it differs per call
// and is asserted case by case below; what is general is that the page is still
// drawn and that the failure is not silent.
func saidSo(reasons []string) bool {
	for _, r := range reasons {
		if strings.Contains(r, "panicked") {
			return true
		}
	}
	return false
}

// dashboard is one page under test: how to draw it, and everything it has to
// say when a panel could not be filled.
//
// The pair is what lets one loop drive both pages. Adding a third dashboard is
// three lines here and no new test.
type dashboard struct {
	name string
	// draw returns what the page says about its own failures, or an error when
	// the page could not be drawn at all — which is a legitimate outcome for the
	// two calls each page treats as its proof that the cluster answered.
	draw func(*cluster.Client) ([]string, error)
}

func dashboards() []dashboard {
	return []dashboard{
		{"cluster", func(c *cluster.Client) ([]string, error) {
			out, err := Cluster(context.Background(), accesstest.Allow(), c, stubMetrics{})
			if err != nil {
				return nil, err
			}
			said := []string{out.CPUHistory.Unavailable, out.MemoryHistory.Unavailable, out.History.Detail}
			for _, d := range out.Degraded {
				said = append(said, d.Panel+": "+d.Reason)
			}
			return said, nil
		}},
		{"workloads", func(c *cluster.Client) ([]string, error) {
			out, err := Workloads(context.Background(), accesstest.Allow(), c, "", stubMetrics{})
			if err != nil {
				return nil, err
			}
			said := []string{out.TopCPU.Unavailable, out.TopMemory.Unavailable, out.Usage.Detail}
			for _, d := range out.Degraded {
				said = append(said, d.Panel+": "+d.Reason)
			}
			return said, nil
		}},
	}
}

// Every call either dashboard makes, panicked in turn.
//
// **The failure mode is the test binary dying, not a red assertion.** An
// unrecovered panic on a goroutine gather started ends the process, so a
// regression here takes every other test in this package with it — which is the
// right amount of noise for a defect that takes the console, every other tab and
// every dock terminal down over one panel.
func TestAPanicInAnyGatheredCallDegradesItsPanelAndNotTheDashboard(t *testing.T) {
	// The cluster page makes four calls — nodes, pods, events and the
	// version — and the workloads page six: pods, Deployments, StatefulSets,
	// DaemonSets, Jobs and CronJobs. A floor rather than an equality, because a
	// thirteenth task must widen this loop and not break it; a floor all the
	// same, because a discovery pass that stopped seeing anything would otherwise
	// pass with nothing driven, which is the failure this whole file is about.
	const leastCalls = 4

	for _, d := range dashboards() {
		t.Run(d.name, func(t *testing.T) {
			calls := asked(t, func(t *testing.T, c *cluster.Client) {
				if _, err := d.draw(c); err != nil {
					t.Fatalf("the %s dashboard failed before anything was made to panic: %v", d.name, err)
				}
			})
			if len(calls) < leastCalls {
				t.Fatalf("only %d calls discovered for the %s dashboard (%v); it makes at least %d, so "+
					"the discovery pass is not seeing them and this proves nothing", len(calls), d.name, calls, leastCalls)
			}
			t.Logf("driving %d discovered calls: %v", len(calls), calls)

			for _, c := range calls {
				t.Run(c.String(), func(t *testing.T) {
					said, err := d.draw(&cluster.Client{Context: "test", Clients: panicsOn(c)})
					if err != nil {
						// Legitimate for the one call each page treats as its
						// proof that the cluster answered at all: a dashboard of
						// zeroes for a cluster nobody could read would be worse. It
						// must still say a panic is what happened.
						if !strings.Contains(err.Error(), "panicked") {
							t.Errorf("the page failed with %q, which does not say a panic happened", err)
						}
						return
					}
					if !saidSo(said) {
						t.Errorf("the page was drawn with nothing saying %s could not be read: %q", c, said)
					}
				})
			}
		})
	}
}

// panickingMetrics is the other half of the set: the history charts are a
// gathered task too and they never touch the API server, so the loop above
// cannot reach them.
type panickingMetrics struct{}

func (panickingMetrics) Query(context.Context, string, string) ([]byte, error) {
	panic("the Prometheus client did something unexpected")
}

func (panickingMetrics) QueryRange(context.Context, string, string, time.Time, time.Time, time.Duration) ([]byte, error) {
	panic("the Prometheus client did something unexpected")
}

func TestAPanickingMetricsBackendDegradesItsChartsAndNotTheDashboard(t *testing.T) {
	client := &cluster.Client{Context: "test", Clients: fake.NewClientset(populated()...)}

	out, err := Cluster(context.Background(), accesstest.Allow(), client, panickingMetrics{})
	if err != nil {
		t.Fatalf("the whole cluster dashboard failed over the history charts: %v", err)
	}
	if out.Total != 1 {
		t.Errorf("the node panel was lost too: Total = %d, want 1", out.Total)
	}
	// Both charts, and not only the Availability beside them: a History with no
	// points and no reason means "the backend answered and the window was
	// empty", which is the wrong answer here.
	for _, h := range []History{out.CPUHistory, out.MemoryHistory} {
		if !strings.Contains(h.Unavailable, "panicked") {
			t.Errorf("%q says %q, which reads as a backend that answered with nothing", h.Title, h.Unavailable)
		}
		if len(h.Points) != 0 {
			t.Errorf("%q drew %d points out of a query that panicked", h.Title, len(h.Points))
		}
	}
	if h := out.CPUHistory; h.Title == "" || h.Unit != UnitCores {
		t.Errorf("the failed chart lost its own name and unit: %+v", h)
	}

	wout, werr := Workloads(context.Background(), accesstest.Allow(), client, "", panickingMetrics{})
	if werr != nil {
		t.Fatalf("the whole workloads dashboard failed over the leaderboards: %v", werr)
	}
	if wout.Pods != 1 {
		t.Errorf("the pod panel was lost too: Pods = %d, want 1", wout.Pods)
	}
	if !strings.Contains(wout.TopCPU.Unavailable, "panicked") {
		t.Errorf("the CPU leaderboard says %q, which does not say the query panicked", wout.TopCPU.Unavailable)
	}
}

// --- the mechanism, and the thirteenth task -------------------------------------

// gather confines the panic to the task that raised it, and the others still
// finish. An error and not a zero value, because the two are different answers.
func TestGatherConfinesAPanicAndTheOtherTasksStillRun(t *testing.T) {
	var boom, fine error
	var ran bool
	gather(
		guard(&boom, "the task that blew up", func() { panic("boom") }),
		guard(&fine, "the task beside it", func() { ran = true }),
	)
	if boom == nil {
		t.Fatal("the task that panicked reported no error — a recover that swallows is worse than the crash")
	}
	if !strings.Contains(boom.Error(), "the task that blew up panicked: boom") {
		t.Errorf("the reason does not name the task or what happened: %v", boom)
	}
	if fine != nil {
		t.Errorf("the task beside it was failed by its neighbor: %v", fine)
	}
	if !ran {
		t.Error("the task beside it never ran")
	}
}

// A task built with nowhere to put its failure is a defect, and it still must
// not end the process: its panic is re-raised on the caller's goroutine, where
// net/http's own recover can see it. One request dies instead of the binary.
func TestATaskWithNowhereToFailReRaisesOnTheCaller(t *testing.T) {
	var ran bool
	var caught any
	func() {
		defer func() { caught = recover() }()
		gather(
			task{run: func() { panic("nowhere to go") }},
			guard(new(error), "the task beside it", func() { ran = true }),
		)
	}()
	if caught == nil {
		t.Fatal("the panic vanished: a task with no fail must not be swallowed either")
	}
	if !ran {
		t.Error("the re-raise cut the wave short; every task must finish first")
	}
}

// And the property that makes this hold: a thirteenth task cannot be added
// without somewhere to put its failure.
//
// gather takes ...task, so the compiler already refuses a bare func(). What it
// cannot refuse is a task built by hand
// with a nil fail — `task{run: f}` compiles, and it is the shape somebody in a
// hurry writes. So the package's own source is read: the only composite literal
// of task is the one inside attempt, and no call to attempt passes a nil fail.
func TestEveryGatheredTaskNamesWhereItsFailureGoes(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(f os.FileInfo) bool {
		return !strings.HasSuffix(f.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("reading this package's own source: %v", err)
	}
	pkg := pkgs["overview"]
	if pkg == nil || len(pkg.Files) < 5 {
		// An empty parse would pass every assertion below, which is the one way
		// this check could be worse than not existing.
		t.Fatalf("parsed %d source files of package overview; it has more than that, so this check is "+
			"not reading the package it is about", len(pkg.Files))
	}

	var offences []string
	for _, file := range pkg.Files {
		var inAttempt bool
		ast.Inspect(file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.FuncDecl:
				inAttempt = v.Name.Name == "attempt"
			case *ast.CompositeLit:
				if id, ok := v.Type.(*ast.Ident); ok && id.Name == "task" && !inAttempt {
					offences = append(offences, fmt.Sprintf("%s: a task built by hand rather than by guard, "+
						"degrading or attempt — its fail may be nil, and a nil fail is a panic with "+
						"nowhere to go", fset.Position(v.Pos())))
				}
			case *ast.CallExpr:
				id, ok := v.Fun.(*ast.Ident)
				if !ok || id.Name != "attempt" || len(v.Args) != 2 {
					return true
				}
				if arg, ok := v.Args[1].(*ast.Ident); ok && arg.Name == "nil" {
					offences = append(offences, fmt.Sprintf("%s: attempt(..., nil) — the panic would be "+
						"logged and then dropped, and the panel would draw a zero", fset.Position(v.Pos())))
				}
			}
			return true
		})
	}
	for _, o := range offences {
		t.Error(o)
	}
}

// A degrading task that panicked keeps the crash as its Availability's Cause,
// typed, so internal/web classifies it as a failure. With the sentence alone
// it would read as the ordinary "no metrics source" absence, which the
// Overview hides.
func TestADegradingPanicKeepsTheCrashAsItsCause(t *testing.T) {
	var av Availability
	gather(degrading(&av, "the pod usage read", func() { panic("a nil map") }))
	if av.OK || av.Detail != "the pod usage read panicked: a nil map" {
		t.Fatalf("availability = %+v", av)
	}
	var crash Crash
	if !errors.As(av.Cause, &crash) {
		t.Fatalf("cause = %v; want the Crash, so a client is told a read failed rather than that nothing is there", av.Cause)
	}
}

// The history's recover writes the crash into both lines as a failed read,
// with the Crash as their error, and not as lines with no reason code.
func TestAPanickedHistoryIsAFailedReadOnBothLines(t *testing.T) {
	av, cpu, memory := crashedHistory("a nil map")
	if av.OK || !strings.Contains(av.Detail, "reading this cluster's history panicked: a nil map") {
		t.Fatalf("availability = %+v", av)
	}
	for _, line := range []History{cpu, memory} {
		var crash Crash
		if line.Missing != MissingFailed || !errors.As(line.Err, &crash) || line.Unavailable != av.Detail {
			t.Errorf("%q = missing %v, err %v, %q; want MissingFailed with the Crash", line.Title, line.Missing, line.Err, line.Unavailable)
		}
	}
	if cpu.Title != "CPU used" || memory.Title != "Memory used" {
		t.Errorf("the lines lost their names: %q, %q", cpu.Title, memory.Title)
	}
}
