package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/metrics"
	"github.com/thonicdev/kubetower/textutil"
)

// Host.ContainerUsage and Host.UsageWindow: each
// container's CPU and memory, now and over a window, for right-sizing.
//
// A file of their own rather than more of host.go, because both read through
// the metrics layer rather than through a cluster client, and both are reviewed
// the same way: **as `list pods`, before anything is read.** Neither backend
// knows who is asking - metrics-server is read with the console's credentials,
// and Prometheus has no RBAC at all - so the review is the only thing standing
// between a person and every container's figures in the cluster.
//
//   - One namespace asked and denied: refused, and nothing is read.
//   - Every namespace asked and denied: read once, then narrowed to the
//     namespaces the person may list pods in, with Complete false and a Note -
//     the answer List gives, through the same access.Keep.

// podsQuestion is the review both methods are asked under: the figures are about
// pods and their containers, so who may see them is who may list the pods.
func podsQuestion(namespace string) access.Attributes {
	return access.Attributes{Verb: "list", Version: "v1", Resource: "pods", Namespace: namespace}
}

// reviewPods asks the question before anything is read. narrowed says the
// person may list pods in some namespaces and not in every one, so whatever is
// read must go through keepNamespaces before it is handed over.
func (h host) reviewPods(r *http.Request, cluster, namespace string) (narrowed bool, err error) {
	if namespace != "" {
		if problems := validation.IsDNS1123Label(namespace); len(problems) > 0 {
			return false, fmt.Errorf("%q is not a namespace name: %s", namespace, strings.Join(problems, "; "))
		}
	}
	// No rows yet: Keep answers the list's own question - refused, whole, or
	// narrowed - without a row to narrow.
	_, seen, err := access.Keep[string](r.Context(), h.s.gate, cluster, podsQuestion(namespace), nil, namespaceOfKey)
	if err != nil {
		return false, err
	}
	return seen.Narrowed, nil
}

// keepNamespaces narrows a set of ContainerKeys to the namespaces the person may
// list pods in, one review per namespace, asked together.
func (h host) keepNamespaces(r *http.Request, cluster string, keys []string) (map[string]bool, error) {
	sort.Strings(keys)
	kept, _, err := access.Keep(r.Context(), h.s.gate, cluster, podsQuestion(""), keys, namespaceOfKey)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(kept))
	for _, k := range kept {
		out[k] = true
	}
	return out, nil
}

// namespaceOfKey reads the namespace out of a ContainerKey. None of the three
// parts can hold a slash, so the first one is the namespace's end.
func namespaceOfKey(key *string) string {
	namespace, _, _ := strings.Cut(*key, "/")
	return namespace
}

// narrowedNote is List's sentence, for the same narrowing.
func narrowedNote() string {
	return "You may not list " + qualifiedResource(podsQuestion("")) +
		" in every namespace, so this holds only the namespaces where you may."
}

// ContainerUsage reads metrics-server through metrics.ContainerUsage.
func (h host) ContainerUsage(r *http.Request, name, namespace string) (extension.LiveUsage, error) {
	client, err := h.client(r, name)
	if err != nil {
		return extension.LiveUsage{}, err
	}
	narrowed, err := h.reviewPods(r, client.Context, namespace)
	if err != nil {
		return extension.LiveUsage{}, err
	}
	if h.s.usage == nil {
		return extension.LiveUsage{}, &extension.MetricsUnavailable{Reason: extension.MetricsAbsent,
			Detail: "this KubeTower runs without a metrics layer, so nothing can be measured"}
	}
	figures, err := h.s.usage.ContainerUsage(r.Context(), client.Context, namespace)
	if err != nil {
		return extension.LiveUsage{}, publicMetricsError(err)
	}

	out := extension.LiveUsage{Containers: make(map[string]extension.Resources, len(figures)), Complete: true}
	var allowed map[string]bool
	if narrowed {
		keys := make([]string, 0, len(figures))
		for k := range figures {
			keys = append(keys, k)
		}
		if allowed, err = h.keepNamespaces(r, client.Context, keys); err != nil {
			return extension.LiveUsage{}, err
		}
		out.Complete, out.Note = false, narrowedNote()
	}
	for k, u := range figures {
		if narrowed && !allowed[k] {
			continue
		}
		out.Containers[k] = extension.Resources{CPUMillicores: u.CPUMillicores, MemoryBytes: u.MemoryBytes}
	}
	return out, nil
}

// publicMetricsError maps the metrics layer's three kinds onto the contract's
// three reasons. Anything else - an answer metrics-server sent that could not be
// read - stays the error it was: still a failure, never an empty map.
func publicMetricsError(err error) error {
	var gone *metrics.Unavailable
	if !errors.As(err, &gone) {
		return err
	}
	switch gone.Kind {
	case metrics.KindNone:
		return &extension.MetricsUnavailable{Reason: extension.MetricsAbsent, Detail: gone.Detail}
	case metrics.KindUnauthorised:
		return &extension.MetricsUnavailable{Reason: extension.MetricsRefused, Detail: gone.Detail}
	case metrics.KindUnreachable:
		return &extension.MetricsUnavailable{Reason: extension.MetricsUnreachable, Detail: gone.Detail}
	}
	return err
}

// keepBudget bounds the access reviews that narrow a window's listing, which
// run on a context detached from the request that asked.
const keepBudget = time.Minute

// UsageWindow runs metrics.ReadWindowKept's fixed queries on the cluster's
// Prometheus, through the server's cache of windows, and hands back the series
// of the pods the person may list. A narrowed person's containers are narrowed
// before any range query is sent, so the series of a namespace they may not
// list are never read, and a kept window is keyed by that narrowing, so one
// read for a broader scope is never theirs; the answer is narrowed again on the
// way out, which costs nothing and holds even if the first narrowing were
// wrong.
func (h host) UsageWindow(r *http.Request, name, namespace string) (extension.Window, error) {
	client, err := h.client(r, name)
	if err != nil {
		return extension.Window{}, err
	}
	narrowed, err := h.reviewPods(r, client.Context, namespace)
	if err != nil {
		return extension.Window{}, err
	}
	// A nil Series is a genuine nil here, for the reason history() gives in
	// overview.go: an interface holding a nil pointer is not nil.
	var q metrics.RangeQuerier
	if h.s.series != nil {
		q = h.s.series
	}
	var keep func([]string) (map[string]bool, error)
	if narrowed {
		keep = func(keys []string) (map[string]bool, error) {
			// Not bounded by this request: a kept window's read outlives
			// the display that started it (metrics.WindowCache), and a
			// display that stops waiting must not fail the read the next
			// one is waiting for.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), keepBudget)
			defer cancel()
			return h.keepNamespaces(r.WithContext(ctx), client.Context, keys)
		}
	}
	read, err := h.readWindow(r, q, client.Context, namespace, keep)
	if err != nil {
		return extension.Window{}, err
	}

	out := extension.Window{State: extension.WindowState(read.State), Detail: read.Detail, Complete: !narrowed,
		ReadAt: read.ReadAt}
	if narrowed {
		out.Note = narrowedNote()
	}
	if read.State != metrics.WindowRead {
		return out, nil
	}

	containers := make(map[string]extension.ContainerWindow, max(len(read.CPU), len(read.Memory)))
	for k, points := range read.CPU {
		w := containers[k]
		w.CPU = publicSamples(points)
		containers[k] = w
	}
	for k, points := range read.Memory {
		w := containers[k]
		w.Memory = publicSamples(points)
		containers[k] = w
	}
	if narrowed {
		keys := make([]string, 0, len(containers))
		for k := range containers {
			keys = append(keys, k)
		}
		allowed, err := h.keepNamespaces(r, client.Context, keys)
		if err != nil {
			return extension.Window{}, err
		}
		for k := range containers {
			if !allowed[k] {
				delete(containers, k)
			}
		}
		out.Complete, out.Note = false, narrowedNote()
		if len(containers) == 0 {
			// Read, and nothing of it is theirs: an empty window, said as one,
			// and not a window covering nothing.
			out.State = extension.WindowEmpty
			out.Detail = "Prometheus has no container series in the namespaces where you may list pods"
			return out, nil
		}
	}
	out.Containers = containers
	out.Partial = read.PartialNote
	if read.CPUErr != nil {
		out.CPUFailed = halfReason(read.CPUErr)
	}
	if read.MemoryErr != nil {
		out.MemoryFailed = halfReason(read.MemoryErr)
	}
	return out, nil
}

// readWindow reads through the server's cache of windows when it has one.
func (h host) readWindow(r *http.Request, q metrics.RangeQuerier, cluster, namespace string,
	keep func([]string) (map[string]bool, error)) (metrics.Window, error) {
	if h.s.windows == nil {
		return metrics.ReadWindowKept(r.Context(), q, cluster, namespace, keep)
	}
	return h.s.windows.Read(r.Context(), q, cluster, namespace, keep)
}

// halfReason is why one half of a window failed, in one line and without the
// query's address. The address is the range query's URL: it carries the PromQL
// and the namespace, so it is long, it says nothing the reader does not know,
// and keeping it would make the same Prometheus timeout read as a different
// reason for every namespace a node tab asks about.
func halfReason(err error) string {
	var addressed *url.Error
	if errors.As(err, &addressed) {
		err = addressed.Err
	}
	return textutil.ReasonN(err.Error(), 200)
}

func publicSamples(points []metrics.Point) []extension.Sample {
	out := make([]extension.Sample, len(points))
	for i, p := range points {
		out[i] = extension.Sample(p)
	}
	return out
}
