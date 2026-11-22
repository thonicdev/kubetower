package kubeadjust

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/extras/kubeadjust/rightsizing"
	"github.com/thonicdev/kubetower/safego"
)

// The node tab's usage window.
//
// The page reads one window for its whole scope. The node tab used to do the
// same - every namespace's series, seven days at fifteen minutes, to draw one
// node. It now reads the window of **the namespaces this node's pods are in**,
// one Host.UsageWindow per namespace, and nothing else.
//
// Namespaces and not pods, because Host.UsageWindow is scoped by namespace:
// narrowing it to pods would change the contract, and the namespace is the
// shape the contract already reviews (`list pods` in that namespace). It is
// also the right unit for what the tab draws: a workload's suggestion pools
// every replica's window, and a workload lives in one namespace, so the
// suggestions concerning this node are sized exactly as the page sizes them.
//
// The namespaces come from the pods the person was allowed to list, so a
// namespace they may not list is never asked for - and the core would refuse
// it if it were.
//
// The cost, against the one cluster-wide read: three Prometheus queries per
// namespace (the listing, then CPU and memory, per batch of containers) rather
// than three, and the series of
// those namespaces rather than of every one. The queries are asked at most
// windowConcurrency at a time. Nothing caps the total: a node whose pods are in
// N namespaces costs 3N queries, about 330 for a node spread over 110
// namespaces, and the tab never falls back to the cluster-wide read. The trade
// is deliberate: a node's namespaces are bounded by what it runs, and the
// cluster-wide read hands every namespace's series to draw one node.

// windowConcurrency is how many namespaces' windows are read at once. Each
// read is itself two range queries in parallel, so four namespaces is eight
// queries in flight - a node rarely hosts more than a handful of namespaces,
// and a Prometheus is not this tab's to flood.
const windowConcurrency = 4

// nodeNamespaces is the namespaces of the pods placed on node, among those the
// person was allowed to list, sorted. A pod that has finished uses nothing and
// is not drawn on the tab, so its namespace is not read for.
func nodeNamespaces(node string, pods []corev1.Pod) []string {
	seen := map[string]bool{}
	var out []string
	for i := range pods {
		p := &pods[i]
		if p.Spec.NodeName != node || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		if !seen[p.Namespace] {
			seen[p.Namespace] = true
			out = append(out, p.Namespace)
		}
	}
	sort.Strings(out)
	return out
}

// noPodOnNode is the node tab's History when the node runs nothing: no window
// was asked for, and with no container there is no suggestion for the page's
// "every suggestion below rests on a single sample" to be about.
const noPodOnNode = "no pod runs on this node, so no window was asked for"

// namespaceWindow is what one namespace's read answered.
type namespaceWindow struct {
	namespace string
	window    extension.Window
	err       error
}

// nodeWindow reads the window of the namespaces node's pods are in and merges
// it into one History. Its error is the request's - an unknown cluster, a
// canceled request; every other failure is that namespace's, said in the note.
func nodeWindow(r *http.Request, h extension.Host, cluster, node string, pods []corev1.Pod) (rightsizing.History, error) {
	namespaces := nodeNamespaces(node, pods)
	if len(namespaces) == 0 {
		return rightsizing.HistoryFromWindow(extension.Window{State: extension.WindowEmpty, Detail: noPodOnNode}), nil
	}

	answers := make([]namespaceWindow, len(namespaces))
	slots := make(chan struct{}, windowConcurrency)
	var wg sync.WaitGroup
	ctx := r.Context()
	for i, ns := range namespaces {
		answers[i].namespace = ns
		// A request that has gone away starts no further read: the rest of its
		// namespaces are its cancellation, not reads nobody will draw.
		if ctx.Err() != nil {
			answers[i].err = ctx.Err()
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			answers[i].err = ctx.Err()
			continue
		}
		wg.Add(1)
		// Through safego: a panic in one namespace's read is that namespace's
		// error, and the others are drawn.
		safego.Go("kubeadjust: the window of namespace "+ns, func() error {
			w, err := h.UsageWindow(r, cluster, ns)
			answers[i].window = w
			return err
		}, func(err error) { answers[i].err = err; <-slots; wg.Done() })
	}
	wg.Wait()

	for _, a := range answers {
		if errors.Is(a.err, extension.ErrUnknownCluster) || errors.Is(a.err, context.Canceled) {
			return rightsizing.History{}, a.err
		}
	}
	return historyOf(mergeWindows(answers), nil), nil
}

// mergeWindows folds the namespaces' windows into the one the page's engine
// reads. A window read for some namespaces and not others is a window, and
// the note names those it lacks; none read is the most telling of their states.
func mergeWindows(answers []namespaceWindow) extension.Window {
	out := extension.Window{Containers: map[string]extension.ContainerWindow{}, Complete: true}
	var (
		readDetail string
		partial    []string     // "namespace: what of its window could not be read"
		cpuFailed  failedHalves // the namespaces whose CPU half failed, by reason
		memFailed  failedHalves
		notes      []string
		missing    []string // "namespace (why)", for the note
		empty      int
		firstState extension.WindowState
		firstWhy   string
		noProm     *extension.Window
	)
	var readAt time.Time // the oldest of the namespaces' reads: what the tab draws is that old
	for _, a := range answers {
		w := a.window
		if a.err != nil {
			w = extension.Window{State: extension.WindowUnreadable, Detail: rightsizing.ShortError(a.err)}
		}
		if !w.ReadAt.IsZero() && (readAt.IsZero() || w.ReadAt.Before(readAt)) {
			readAt = w.ReadAt
		}
		switch w.State {
		case extension.WindowRead:
			// The core's Detail is the span, then one clause per half that
			// failed. The span is the same for every namespace; the halves are
			// each namespace's own, so they are rebuilt from CPUFailed and
			// MemoryFailed with the namespace named, never taken from one
			// namespace and said of the node.
			if readDetail == "" {
				readDetail, _, _ = strings.Cut(w.Detail, " · ")
			}
			cpuFailed.add(a.namespace, w.CPUFailed)
			memFailed.add(a.namespace, w.MemoryFailed)
			if w.Partial != "" {
				partial = append(partial, a.namespace+": "+w.Partial)
			}
			for k, c := range w.Containers {
				out.Containers[k] = c
			}
			if !w.Complete {
				out.Complete = false
				if w.Note != "" && !slices.Contains(notes, w.Note) {
					notes = append(notes, w.Note)
				}
			}
			continue
		case extension.WindowEmpty:
			empty++
			continue
		case extension.WindowNoPrometheus:
			// About the installation or the cluster, not the namespace: the
			// same sentence for every one of them.
			if noProm == nil {
				noProm = &w
			}
		}
		if firstState == "" {
			firstState, firstWhy = w.State, w.Detail
		}
		missing = append(missing, a.namespace+" ("+w.Detail+")")
	}

	scope := fmt.Sprintf("read for the %s this node's pods are in, not the whole cluster",
		plural(len(answers), "namespace"))
	switch {
	case len(out.Containers) > 0:
		out.State = extension.WindowRead
		out.Detail = readDetail + " · " + scope
		for _, g := range cpuFailed {
			out.Detail += " · " + g.names() + ": memory only, the CPU window failed (" + g.reason + ")"
		}
		for _, g := range memFailed {
			out.Detail += " · " + g.names() + ": CPU only, the memory window failed (" + g.reason + ")"
		}
		for _, p := range partial {
			out.Detail += " · " + p
		}
		out.CPUFailed = cpuFailed.String()
		out.MemoryFailed = memFailed.String()
		out.Partial = strings.Join(partial, "; ")
		out.Note = strings.Join(notes, " ")
		out.ReadAt = readAt
		if len(missing) > 0 {
			out.Detail += " · no window for " + strings.Join(missing, ", ")
		}
		return out
	case noProm != nil:
		return extension.Window{State: extension.WindowNoPrometheus, Detail: noProm.Detail, ReadAt: readAt}
	case len(missing) == 0:
		// Every namespace answered, with nothing: empty, said as one.
		return extension.Window{State: extension.WindowEmpty,
			Detail: "Prometheus has no container series in the namespaces of this node's pods", ReadAt: readAt}
	case len(missing) == 1 && empty == 0:
		return extension.Window{State: firstState, Detail: firstWhy, ReadAt: readAt}
	default:
		return extension.Window{State: firstState, Detail: "no window for " + strings.Join(missing, ", "), ReadAt: readAt}
	}
}

// failedHalves groups the namespaces whose same half failed by the reason it
// failed with, in the order they were read: a Prometheus timing out one query
// for twenty namespaces is one cause, said once, not twenty times.
type failedHalves []failedHalf

type failedHalf struct {
	reason     string
	namespaces []string
}

func (f *failedHalves) add(namespace, reason string) {
	if reason == "" {
		return
	}
	for i := range *f {
		if (*f)[i].reason == reason {
			(*f)[i].namespaces = append((*f)[i].namespaces, namespace)
			return
		}
	}
	*f = append(*f, failedHalf{reason: reason, namespaces: []string{namespace}})
}

func (g failedHalf) names() string { return strings.Join(g.namespaces, ", ") }

// String is "billing, shop: timeout; data: refused", or "" when no half failed.
func (f failedHalves) String() string {
	var out []string
	for _, g := range f {
		out = append(out, g.names()+": "+g.reason)
	}
	return strings.Join(out, "; ")
}

// nodeHistoryNote is the History sentence with the node's containers as its
// denominator: those of the pods placed on it that metrics-server measured -
// the containers the tab draws.
func nodeHistoryNote(history rightsizing.History, node string, pods []corev1.Pod, usage map[string]rightsizing.Usage) rightsizing.Availability {
	if len(nodeNamespaces(node, pods)) == 0 {
		return rightsizing.Availability{Detail: noPodOnNode}
	}
	index := rightsizing.BuildHistoryMap(history.Containers)
	var cpu, memory, total int
	for i := range pods {
		p := &pods[i]
		if p.Spec.NodeName != node || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		for _, c := range p.Spec.Containers {
			if _, measured := usage[extension.ContainerKey(p.Namespace, p.Name, c.Name)]; !measured {
				continue
			}
			total++
			got, ok := index[rightsizing.HistoryKey(p.Namespace, p.Name, c.Name)]
			if !ok {
				continue
			}
			if len(got.CPU) >= 2 {
				cpu++
			}
			if len(got.Memory) >= 2 {
				memory++
			}
		}
	}
	return rightsizing.HistoryNote(history.State, cpu, memory, total)
}
