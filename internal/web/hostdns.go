package web

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/metrics"
)

// Host.DNSWindow: CoreDNS's figures out of the cluster's Prometheus.
//
// The order is the point. The CoreDNS pods are found first, as the console,
// because the question the person is reviewed on is `list pods` in the
// namespace they run in, and that namespace is not known before. What that
// finds is used for one thing, the namespace; nothing of it reaches the person
// until the review has let them through. Refused, or not reviewable, and
// Prometheus is never asked.

// coreDNSSelectors find CoreDNS's pods: the label kubeadm, kind, k3s and EKS
// set, and the one the Helm chart sets.
var coreDNSSelectors = []string{"k8s-app=kube-dns", "app.kubernetes.io/name=coredns"}

// coreDNSPods lists the pods carrying either label, in every namespace, once
// each, as the console.
func (h host) coreDNSPods(ctx context.Context, client *cluster.Client) ([]corev1.Pod, error) {
	var found []corev1.Pod
	seen := map[string]bool{}
	attrs := access.Attributes{Verb: "list", Version: "v1", Resource: "pods"}
	for _, selector := range coreDNSSelectors {
		_, err := h.s.gate.Read(ctx, client.Context, attrs, func(ctx context.Context) error {
			list, err := client.Clients.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: selector})
			if err != nil {
				return err
			}
			for _, pod := range list.Items {
				key := pod.Namespace + "/" + pod.Name
				if seen[key] || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
					continue
				}
				seen[key] = true
				found = append(found, pod)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return found, nil
}

// inMostPopulatedNamespace picks where CoreDNS runs: the namespace holding the
// most of the pods, the first by name on a tie, with the others counted.
func inMostPopulatedNamespace(pods []corev1.Pod) (namespace string, in []corev1.Pod, others int) {
	count := map[string]int{}
	for _, p := range pods {
		count[p.Namespace]++
	}
	for ns, n := range count {
		if n > count[namespace] || (n == count[namespace] && ns < namespace) {
			namespace = ns
		}
	}
	for _, p := range pods {
		if p.Namespace == namespace {
			in = append(in, p)
		}
	}
	return namespace, in, len(count) - 1
}

// DNSWindow answers extension.Host.DNSWindow.
func (h host) DNSWindow(r *http.Request, name string, span extension.DNSSpan) (extension.DNS, error) {
	if !span.Valid() {
		_, err := extension.ParseDNSSpan(string(span))
		return extension.DNS{}, err
	}
	client, err := h.client(r, name)
	if err != nil {
		return extension.DNS{}, err
	}
	ctx, err := h.asConsoleFor(r)
	if err != nil {
		return extension.DNS{}, fmt.Errorf("%w: %w", extension.ErrUnreviewable, err)
	}
	found, err := h.coreDNSPods(ctx, client)
	if err != nil {
		return extension.DNS{State: extension.WindowUnreadable,
			Detail: "the CoreDNS pods could not be looked for: " + halfReason(err)}, nil
	}
	if len(found) == 0 {
		return extension.DNS{State: extension.WindowEmpty, Detail: "no CoreDNS pod was found"}, nil
	}
	namespace, pods, others := inMostPopulatedNamespace(found)

	// The review: all or nothing, and before anything is read from Prometheus.
	if _, err := h.reviewPods(r, client.Context, namespace); err != nil {
		return extension.DNS{}, err
	}

	refs := make([]metrics.DNSPodRef, 0, len(pods))
	for _, p := range pods {
		if p.Status.PodIP != "" {
			refs = append(refs, metrics.DNSPodRef{Name: p.Name, Node: p.Spec.NodeName, IP: p.Status.PodIP})
		}
	}
	var q metrics.RangeQuerier
	if h.s.series != nil {
		q = h.s.series
	}
	var read metrics.DNSRead
	if h.s.dns != nil {
		read, err = h.s.dns.Read(r.Context(), q, client.Context, metrics.DNSSpan(span), namespace, refs)
	} else {
		read, err = metrics.ReadDNS(r.Context(), q, client.Context, metrics.DNSSpan(span), namespace, refs)
	}
	if err != nil {
		return extension.DNS{}, err
	}
	out := publicDNS(read)
	out.Namespace = namespace
	if others > 0 && read.State == metrics.WindowRead {
		out.Partial = joinNotes(out.Partial, fmt.Sprintf("%d other namespaces hold CoreDNS pods and were not read", others))
	}
	return out, nil
}

func joinNotes(a, b string) string {
	if a == "" {
		return b
	}
	return strings.Join([]string{a, b}, "; ")
}

func publicDNSFigures(f metrics.DNSFigures) extension.DNSFigures {
	return extension.DNSFigures(f)
}

// publicDNS translates the metrics layer's read into the contract's types.
func publicDNS(read metrics.DNSRead) extension.DNS {
	out := extension.DNS{State: extension.WindowState(read.State), Detail: read.Detail, ReadAt: read.ReadAt}
	if read.State != metrics.WindowRead {
		return out
	}
	out.Now = publicDNSFigures(read.Now)
	out.Version, out.Partial = read.Version, read.Partial
	out.Series = extension.DNSSeries{Step: read.Series.Step, QPS: publicSamples(read.Series.QPS),
		P50: publicSamples(read.Series.P50), P99: publicSamples(read.Series.P99),
		CacheHit: publicSamples(read.Series.CacheHit)}
	if len(read.Series.ByRcode) > 0 {
		out.Series.ByRcode = make(map[string][]extension.Sample, len(read.Series.ByRcode))
		for code, points := range read.Series.ByRcode {
			out.Series.ByRcode[code] = publicSamples(points)
		}
	}
	out.Pods = make([]extension.DNSPod, len(read.Pods))
	for i, p := range read.Pods {
		out.Pods[i] = extension.DNSPod{Pod: p.Name, Node: p.Node, IP: p.IP, Figures: publicDNSFigures(p.Figures),
			Scraped: p.Scraped}
	}
	sort.Slice(out.Pods, func(i, j int) bool { return out.Pods[i].Pod < out.Pods[j].Pod })
	out.Upstreams = make([]extension.DNSUpstream, len(read.Upstreams))
	for i, u := range read.Upstreams {
		out.Upstreams[i] = extension.DNSUpstream(u)
	}
	return out
}
