package kubeadjust

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/extras/kubeadjust/rightsizing"
	"github.com/thonicdev/kubetower/safego"
)

// The reads of one display, together.
//
// Three at a time and never more, one per line of reads that does not wait
// for another:
//
//   - **the window** - seven days of Prometheus, by far the longest read of a
//     first display, kept by the core afterwards (Host.UsageWindow);
//   - **the lists** the engine attributes, caps and sizes volumes with, one
//     after the other: ReplicaSets and Jobs, ControllerRevisions, the
//     autoscalers, the nodes, the claims, the namespaces;
//   - **the pods**, then what needs them: the node tab's window (its
//     namespaces are its pods') and the kubelets' disk figures (the nodes
//     hosting them).
//
// A display then waits for the slowest line, not for the sum of them. Three
// and not one per list: a console serves many pages, and each list is reviewed
// for the person behind it, which is requests of its own.
//
// Each line writes only its own fields, and they are read once gather has
// returned. A read that ends the request stops the others (stop), and the
// answer is that read's, never the cancellation it caused (failure).

// windowWait is how long a page waits for the usage window before it is drawn
// without it. A first read of a week on a large cluster, or a slow
// Prometheus, takes far longer than the rest of the page (up to the core's
// whole read budget, over two minutes); past this the page is drawn from the
// live usage and says the history is still being read. A variable so a test
// can shorten it.
//
// One and a half seconds: the page's other reads take about a second on a
// cluster of 3000 pods (largecluster_test.go), so a longer wait only delays a
// display that is otherwise ready, while a Prometheus that answers within it
// (a small cluster, a kept window) never shows a pending page. The week itself
// is neither shortened nor sampled differently: P95, burst ratio, trend and
// the confidence levels are still defined over what the engine is handed, and
// a page without its week sizes nothing and offers no command.
var windowWait = 1500 * time.Millisecond

// WindowPending is the window state of a page drawn before its window
// arrived: the read goes on, and the page asks again until it is kept.
const WindowPending extension.WindowState = "pending"

// pendingHistory is the History of a page drawn before its window arrived.
func pendingHistory() rightsizing.History {
	return rightsizing.HistoryFromWindow(extension.Window{State: WindowPending,
		Detail: "Prometheus is still reading the last seven days (more than " + windowWait.String() +
			" so far); this page asks again on its own and draws them once they are read"})
}

// gathered is what the three lines read.
type gathered struct {
	// The window's line, for the page; the node tab's is on the pods' line.
	history   rightsizing.History
	windowErr error

	// The pods' line.
	podsPage     extension.Page
	pods         []corev1.Pod
	badPods      int
	podsErr      error
	nodeWindErr  error
	storage      map[string]rightsizing.PodStorage
	storageState rightsizing.Availability
	diskErr      error

	// The lists' line.
	owners         map[string]metav1.OwnerReference
	replicaSets    []appsv1.ReplicaSet
	replicaSetsErr error
	ownersErr      error
	revisions      []appsv1.ControllerRevision
	revisionsErr   error
	autoscalers    map[string][]rightsizing.Autoscaler
	autoscalersErr error
	nodes          []corev1.Node
	badNodes       int
	nodesErr       error
	claims         map[string]int64
	claimsErr      error
	namespacesPage extension.Page
	namespacesErr  error
	// listsPanicked is a panic on the lists' line: every list it had not
	// answered yet is that error.
	listsPanicked error

	// How long each line's reads took, overlapping one another.
	windowTook, listsTook, podsTook, diskTook time.Duration

	parent  context.Context
	stopped atomic.Bool
}

// gather reads everything a measurable display needs, the three lines at once.
func gather(r *http.Request, h extension.Host, cluster, namespace, node string) *gathered {
	g := &gathered{parent: r.Context()}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	rc := r.WithContext(ctx)
	stop := func() { g.stopped.Store(true); cancel() }

	var wg sync.WaitGroup
	if node == "" {
		wg.Add(1)
		safego.Go("kubeadjust: the usage window", func() error {
			start := time.Now()
			wait, waited := context.WithTimeout(ctx, windowWait)
			window, err := h.UsageWindow(rc.WithContext(wait), cluster, namespace)
			late := errors.Is(wait.Err(), context.DeadlineExceeded) && ctx.Err() == nil
			waited()
			g.windowTook = time.Since(start)
			if late {
				// Not a failure: the core's window cache goes on reading
				// on a context of its own, and keeps what it reads for the
				// next display, which the page asks for on its own.
				g.history = pendingHistory()
				return nil
			}
			if ends(err) {
				g.windowErr = err
				stop()
				return nil
			}
			g.history = historyOf(window, err)
			return nil
		}, func(err error) {
			if err != nil {
				// A panic is a window that could not be read, as any other
				// failure of it: said, and the rest of the page drawn.
				g.history = historyOf(extension.Window{}, err)
			}
			wg.Done()
		})
	}

	wg.Add(1)
	safego.Go("kubeadjust: the lists", func() error {
		start := time.Now()
		g.readLists(rc, h, cluster, namespace)
		g.listsTook = time.Since(start)
		return nil
	}, func(err error) {
		if err != nil {
			g.listsPanicked = err
		}
		wg.Done()
	})

	// The pods' line runs here, on the request's own goroutine.
	g.readPods(rc, h, cluster, namespace, node, stop)
	wg.Wait()
	if g.listsPanicked != nil {
		g.listsFailed(g.listsPanicked)
	}
	return g
}

// readPods is the pods' line: the pods, then the node tab's window, then the
// kubelets'.
func (g *gathered) readPods(r *http.Request, h extension.Host, cluster, namespace, node string, stop func()) {
	start := time.Now()
	g.podsPage, g.podsErr = h.List(r, cluster, extension.ListQuery{GVR: podsGVR, Namespace: namespace})
	if g.podsErr != nil {
		g.podsTook = time.Since(start)
		stop()
		return
	}
	g.pods, g.badPods = typed[corev1.Pod](g.podsPage.Items)
	g.podsTook = time.Since(start)

	if node != "" {
		start := time.Now()
		g.history, g.nodeWindErr = nodeWindow(r, h, cluster, node, g.pods)
		g.windowTook = time.Since(start)
		if g.nodeWindErr != nil {
			stop()
			return
		}
	}

	start = time.Now()
	defer func() { g.diskTook = time.Since(start) }()
	asked := rightsizing.NodesHosting(g.pods)
	if node != "" {
		asked = only(asked, node)
	}
	if len(asked) == 0 {
		g.storage, g.storageState = rightsizing.StorageFromUsage(nil, extension.Usage{})
		return
	}
	disk, err := h.Usage(r, cluster, asked)
	switch {
	case ends(err):
		g.diskErr = err
		stop()
	case err != nil:
		g.storageState = rightsizing.NoStorage(err)
	default:
		g.storage, g.storageState = rightsizing.StorageFromUsage(asked, disk)
		if node != "" && g.storageState.OK {
			g.storageState.Detail += " · this node's kubelet only: a workload's disk figures here are its pods on this node"
		}
	}
}

// readLists is the lists' line. None of its failures ends the request: each
// is a note on the page, as when they were read one after the other.
func (g *gathered) readLists(r *http.Request, h extension.Host, cluster, namespace string) {
	g.owners, g.replicaSets, g.replicaSetsErr, g.ownersErr = owners(r, h, cluster, namespace)
	g.revisions, g.revisionsErr = controllerRevisions(r, h, cluster, namespace)
	g.autoscalers, g.autoscalersErr = autoscalers(r, h, cluster, namespace)
	page, err := h.List(r, cluster, extension.ListQuery{GVR: nodesGVR})
	if err != nil {
		g.nodesErr = err
	} else {
		g.nodes, g.badNodes = typed[corev1.Node](page.Items)
	}
	g.claims, g.claimsErr = claims(r, h, cluster, namespace)
	g.namespacesPage, g.namespacesErr = h.List(r, cluster, extension.ListQuery{GVR: namespacesGVR})
}

// listsFailed fills every list the lists' line had not answered with err.
func (g *gathered) listsFailed(err error) {
	set := func(target *error) {
		if *target == nil {
			*target = err
		}
	}
	if g.owners == nil {
		g.owners = map[string]metav1.OwnerReference{}
		set(&g.ownersErr)
		set(&g.replicaSetsErr)
	}
	if g.revisions == nil {
		set(&g.revisionsErr)
	}
	if g.autoscalers == nil {
		set(&g.autoscalersErr)
	}
	if g.nodes == nil {
		set(&g.nodesErr)
	}
	if g.claims == nil {
		set(&g.claimsErr)
	}
	if g.namespacesPage.Items == nil {
		set(&g.namespacesErr)
	}
}

// failure is the read that ends the request, in the order they were read one
// after the other: the window's unknown cluster or cancellation, then the
// pods, then the node tab's window, then the kubelets'. An error that is only
// the cancellation another read caused is never the answer.
func (g *gathered) failure() error {
	for _, err := range []error{g.windowErr, g.podsErr, g.nodeWindErr, g.diskErr} {
		if err != nil && !g.ours(err) {
			return err
		}
	}
	return nil
}

// ours is whether err is only the cancellation stop made, after another read
// ended the request. A cancellation the request itself was given is not ours.
func (g *gathered) ours(err error) bool {
	return errors.Is(err, context.Canceled) && g.parent.Err() == nil && g.stopped.Load()
}

// timings records the lines' durations, which overlap: their sum is more than
// the time the display waited, which is the `reads` phase.
func (g *gathered) timings(t *timing) {
	t.add("window", g.windowTook)
	t.add("lists", g.listsTook)
	t.add("pods", g.podsTook)
	t.add("disk", g.diskTook)
}
