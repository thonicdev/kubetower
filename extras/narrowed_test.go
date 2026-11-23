package extras_test

// Two people, one of them refused `list` in one namespace: every extra shows
// the refused person nothing of that namespace, and shows it to the other.
//
// Driven at the extras' level, through one host for all of them: the core's
// List is replaced by what it answers (internal/web's host tests hold that it
// answers so: hostlistscope_test.go, hostlistlimit_test.go,
// hostconfigured_test.go). What this pins is the other half - that no extra
// reaches a namespace's objects any way but through the reviewed doors. The
// host's Client answers nothing, as the console's answers discovery only, and
// the doors that read as the console name nothing for these pages.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/extras/argocd"
	"github.com/thonicdev/kubetower/extras/certmanager"
	"github.com/thonicdev/kubetower/extras/cnpg"
	"github.com/thonicdev/kubetower/extras/events"
	"github.com/thonicdev/kubetower/extras/kubeadjust"
	"github.com/thonicdev/kubetower/extras/kubecron"
	"github.com/thonicdev/kubetower/extras/netinspect"
	"github.com/thonicdev/kubetower/extras/pending"
	"github.com/thonicdev/kubetower/extras/redis"
	"github.com/thonicdev/kubetower/extras/runtimeinspect"
	"github.com/thonicdev/kubetower/extras/security"
)

const (
	// hidden is the namespace the refused person may list nothing in; seen is
	// one both may list.
	hidden = "team-x"
	seen   = "team-y"
	// Every object of hidden carries hiddenMark in its name, every object of
	// seen seenMark: a body holding hiddenMark showed something of hidden.
	hiddenMark = "xonly"
	seenMark   = "yseen"

	personHeader = "X-Test-Person"
	refused      = "refused"
	permitted    = "allowed"
)

// object is one cluster object as the test host holds it, by resource.
type object struct {
	resource string
	doc      map[string]any
}

// clusterHost is extension.Host over a set of objects, with the person on the
// request: List narrows as the console's does for a person refused in hidden.
type clusterHost struct {
	mu      sync.Mutex
	objects map[string][]unstructured.Unstructured
	crds    []extension.CRD
}

func newClusterHost(t *testing.T, crds []extension.CRD, objects []object) *clusterHost {
	t.Helper()
	h := &clusterHost{objects: map[string][]unstructured.Unstructured{}, crds: crds}
	for _, o := range objects {
		h.objects[o.resource] = append(h.objects[o.resource], unstructured.Unstructured{Object: o.doc})
	}
	return h
}

func isRefused(r *http.Request) bool { return r.Header.Get(personHeader) == refused }

// denied is the refusal the console's List answers, classifiable as one.
func denied(resource, namespace string) error {
	return fmt.Errorf("%w: you may not list %s in namespace %q", extension.ErrDenied, resource, namespace)
}

func (h *clusterHost) List(r *http.Request, _ string, q extension.ListQuery) (extension.Page, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if q.GVR.Group == "" && q.GVR.Resource == "secrets" {
		return extension.Page{}, extension.ErrSecretsNotListed
	}
	if isRefused(r) && q.Namespace == hidden {
		return extension.Page{}, denied(q.GVR.Resource, hidden)
	}
	narrowed := false
	var out []unstructured.Unstructured
	for _, item := range h.objects[q.GVR.Resource] {
		if q.Namespace != "" && item.GetNamespace() != "" && item.GetNamespace() != q.Namespace {
			continue
		}
		if name, ok := strings.CutPrefix(q.FieldSelector, "metadata.name="); ok && item.GetName() != name {
			continue
		}
		if key, value, ok := strings.Cut(q.LabelSelector, "="); ok && item.GetLabels()[key] != value {
			continue
		}
		if isRefused(r) && item.GetNamespace() == hidden {
			narrowed = true
			continue
		}
		out = append(out, *item.DeepCopy())
	}
	if isRefused(r) && q.Namespace == "" {
		narrowed = true
	}
	if q.Each != nil {
		// A read page by page: the host walks what the person may list, and
		// keeps nothing, as the console's does.
		for _, item := range out {
			if err := q.Each(item); err != nil {
				return extension.Page{}, err
			}
		}
		out = nil
	}
	var page extension.Page
	if q.Limit > 0 && int64(len(out)) > q.Limit {
		page = extension.Page{Items: out[:q.Limit], Note: "limited"}
	} else if narrowed {
		page = extension.Page{Items: out, Note: "You may not list " + q.GVR.Resource +
			" in every namespace, so this holds only the namespaces where you may."}
	} else {
		page = extension.Page{Items: out, Complete: true}
	}
	if q.Columns {
		page = asTable(q.GVR.Resource, page)
	}
	return page, nil
}

// tables is each kind's printer columns after Name, for a read as a table:
// a cell is the object's "cells" field under the column's name.
var tables = map[string][]string{
	"vulnerabilityreports": {"Repository", "Tag", "Scanner", "Age", "Critical", "High", "Medium", "Low", "Unknown"},
	"configauditreports":   {"Scanner", "Age", "Critical", "High", "Medium", "Low"},
	"exposedsecretreports": {"Repository", "Tag", "Scanner", "Age", "Critical", "High", "Medium", "Low"},
	"pods":                 {"Ready", "Status", "Restarts", "Age"},
	"replicasets":          {"Desired", "Current", "Ready", "Age"},
	"jobs":                 {"Status", "Completions", "Duration", "Age"},
}

// asTable is page as the API server's table: the rows carry the metadata and
// the cells, and Items is empty.
func asTable(resource string, page extension.Page) extension.Page {
	page.Columns = append([]string{"Name"}, tables[resource]...)
	for _, item := range page.Items {
		cells := make([]any, len(page.Columns))
		given, _ := item.Object["cells"].(map[string]any)
		for i, c := range page.Columns {
			if c == "Name" {
				cells[i] = item.GetName()
				continue
			}
			cells[i] = given[c]
		}
		var annotations map[string]string
		if a := item.GetAnnotations(); len(a) > 0 {
			annotations = a
		}
		var labels map[string]string
		if l := item.GetLabels(); len(l) > 0 {
			labels = l
		}
		page.Rows = append(page.Rows, extension.Row{Namespace: item.GetNamespace(), Name: item.GetName(),
			Labels: labels, Annotations: annotations, Owners: item.GetOwnerReferences(),
			Created: item.GetCreationTimestamp().Time, Cells: cells})
	}
	page.Items = nil
	return page
}

// Client answers nothing: the console's answers discovery only, and an extra
// that reached an object through it would bypass the review this test is about.
func (h *clusterHost) Client(*http.Request, string) (extension.Clients, error) {
	return extension.Clients{}, errors.New("the clients handed to an extension answer discovery only")
}

func (h *clusterHost) Definitions(*http.Request, string) (extension.CRDRegistry, error) {
	return catalogue{h.crds}, nil
}

type catalogue struct{ crds []extension.CRD }

func (c catalogue) All(context.Context) ([]extension.CRD, error) { return c.crds, nil }

func (c catalogue) Lookup(_ context.Context, kind string) (extension.CRD, error) {
	for _, crd := range c.crds {
		if kind == crd.Plural || kind == crd.Plural+"."+crd.Group || strings.EqualFold(kind, crd.Kind) {
			return crd, nil
		}
	}
	return extension.CRD{}, fmt.Errorf("%q: %w", kind, extension.ErrUnknownKind)
}

func (h *clusterHost) Reveal(r *http.Request, _, namespace, _, _ string) (string, error) {
	if isRefused(r) && namespace == hidden {
		return "", extension.ErrRevealDenied
	}
	return "", extension.ErrNoSuchSecret
}

// The doors that read as the console name nothing these pages are given.
func (h *clusterHost) ConfiguredSecret(*http.Request, string, string, string, string) (string, error) {
	return "", extension.ErrNotConfigured
}

func (h *clusterHost) ConfiguredService(*http.Request, string, string, string) (corev1.Service, error) {
	return corev1.Service{}, extension.ErrNotConfigured
}

func (h *clusterHost) Exec(*http.Request, string, extension.ExecQuery) (extension.ExecResult, error) {
	return extension.ExecResult{}, extension.ErrDenied
}

func (h *clusterHost) MayReadLogs(r *http.Request, _, namespace string) error {
	if isRefused(r) && namespace == hidden {
		return extension.ErrLogsDenied
	}
	return nil
}

// Usage holds each pod the host knows on one node, narrowed for the person.
func (h *clusterHost) Usage(r *http.Request, _ string, nodes []string) (extension.Usage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := extension.Usage{Nodes: map[string]extension.NodeUsage{}, Asked: len(nodes), Answered: len(nodes), Complete: !isRefused(r)}
	for _, node := range nodes {
		usage := extension.NodeUsage{}
		for _, pod := range h.objects["pods"] {
			if isRefused(r) && pod.GetNamespace() == hidden {
				continue
			}
			usage.Pods = append(usage.Pods, extension.PodUsage{Name: pod.GetName(), Namespace: pod.GetNamespace()})
		}
		out.Nodes[node] = usage
	}
	return out, nil
}

// ContainerUsage measures each container of each pod at one figure, narrowed.
func (h *clusterHost) ContainerUsage(r *http.Request, _, namespace string) (extension.LiveUsage, error) {
	if isRefused(r) && namespace == hidden {
		return extension.LiveUsage{}, denied("pods", hidden)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := extension.LiveUsage{Containers: map[string]extension.Resources{}, Complete: !isRefused(r)}
	for _, pod := range h.objects["pods"] {
		if (namespace != "" && pod.GetNamespace() != namespace) || (isRefused(r) && pod.GetNamespace() == hidden) {
			continue
		}
		containers, _, _ := unstructured.NestedSlice(pod.Object, "spec", "containers")
		for _, c := range containers {
			name, _ := c.(map[string]any)["name"].(string)
			out.Containers[extension.ContainerKey(pod.GetNamespace(), pod.GetName(), name)] =
				extension.Resources{CPUMillicores: 50, MemoryBytes: 64 << 20}
		}
	}
	return out, nil
}

func (h *clusterHost) UsageWindow(r *http.Request, _, namespace string) (extension.Window, error) {
	if isRefused(r) && namespace == hidden {
		return extension.Window{}, denied("pods", hidden)
	}
	return extension.Window{State: extension.WindowNoPrometheus}, nil
}

func (h *clusterHost) DNSWindow(*http.Request, string, extension.DNSSpan) (extension.DNS, error) {
	return extension.DNS{}, nil
}

func (h *clusterHost) Forward(context.Context, string, string, string, int) (extension.Tunnel, error) {
	return extension.Tunnel{}, errors.New("no tunnel in this test")
}

func (h *clusterHost) ForwardAsConsole(*http.Request, string, string, string, int) (extension.Tunnel, error) {
	return extension.Tunnel{}, errors.New("no tunnel in this test")
}

func (h *clusterHost) Setting(*http.Request, string, string) string { return "" }
func (h *clusterHost) RefreshApplication(*http.Request, string, string, string) error {
	return errors.New("RefreshApplication is not used here")
}

func (h *clusterHost) ConfirmToken(*http.Request) string { return "" }

var _ extension.Host = (*clusterHost)(nil)

// --- the objects ---------------------------------------------------------------

func meta(namespace, name string, labels map[string]string) map[string]any {
	m := map[string]any{"namespace": namespace, "name": name, "uid": namespace + "-" + name,
		"creationTimestamp": "2026-11-01T00:00:00Z"}
	if labels != nil {
		l := map[string]any{}
		for k, v := range labels {
			l[k] = v
		}
		m["labels"] = l
	}
	return m
}

// mark is the name marker of namespace's objects.
func mark(namespace string) string {
	if namespace == hidden {
		return hiddenMark
	}
	return seenMark
}

func namespaces() []object {
	var out []object
	for _, ns := range []string{hidden, seen} {
		out = append(out, object{"namespaces", map[string]any{"apiVersion": "v1", "kind": "Namespace",
			"metadata": map[string]any{"name": ns, "uid": ns}}})
	}
	return out
}

func node() object {
	return object{"nodes", map[string]any{"apiVersion": "v1", "kind": "Node",
		"metadata": map[string]any{"name": "node-1", "uid": "node-1"},
		"status": map[string]any{
			"allocatable": map[string]any{"cpu": "4", "memory": "8Gi", "pods": "110"},
			"capacity":    map[string]any{"cpu": "4", "memory": "8Gi", "pods": "110"},
			"conditions":  []any{map[string]any{"type": "Ready", "status": "True"}},
		}}}
}

func pod(namespace, name string, phase string, extra func(map[string]any)) object {
	doc := map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": meta(namespace, name, map[string]string{"app": name}),
		"spec": map[string]any{"nodeName": "node-1", "containers": []any{map[string]any{
			"name": "app", "image": "registry.example/app:1",
			"resources": map[string]any{"requests": map[string]any{"cpu": "100m", "memory": "128Mi"}},
		}}},
		"status": map[string]any{"phase": phase, "podIP": "10.0.0.1"}}
	if extra != nil {
		extra(doc)
	}
	return object{"pods", doc}
}

// busy is a running pod requesting most of node-1.
func busy(namespace, name string) object {
	return pod(namespace, name, "Running", func(doc map[string]any) {
		c := doc["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
		c["resources"] = map[string]any{"requests": map[string]any{"cpu": "1500m", "memory": "3Gi"},
			"limits": map[string]any{"cpu": "2", "memory": "3Gi"}}
		doc["status"].(map[string]any)["containerStatuses"] = []any{map[string]any{"name": "app", "ready": true}}
	})
}

func other(namespace string) string {
	if namespace == hidden {
		return seen
	}
	return hidden
}

// perNamespace is build's objects in both namespaces, each named with its mark.
func perNamespace(build func(namespace, mark string) []object) []object {
	var out []object
	for _, ns := range []string{hidden, seen} {
		out = append(out, build(ns, mark(ns))...)
	}
	return out
}

// --- the extras ----------------------------------------------------------------

var (
	applications = extension.CRD{Group: "argoproj.io", Version: "v1alpha1", Plural: "applications", Singular: "application", Kind: "Application", Namespaced: true}
	certificates = extension.CRD{Group: "cert-manager.io", Version: "v1", Plural: "certificates", Singular: "certificate", Kind: "Certificate", Namespaced: true}
	pgClusters   = extension.CRD{Group: "postgresql.cnpg.io", Version: "v1", Plural: "clusters", Singular: "cluster", Kind: "Cluster", Namespaced: true}
)

type extraCase struct {
	name    string
	ext     extension.Extension
	crds    []extension.CRD
	objects []object
	// paths are the routes asked, GET, each by both people: those that name
	// the objects they draw (a route answering only counts says nothing here).
	paths []string
	// totals are routes whose answer is computed across namespaces without
	// necessarily naming an object: compared whole, not searched for a mark.
	totals []string
	// details are routes naming one object, GET: {ns} and {m} stand for a
	// namespace and its mark.
	details []string
	// saysNarrowed are the fields, any index written [*], that differ for the
	// refused person only to say the answer was narrowed: a figure withheld,
	// or a "could not be read" where the cluster-wide listing was cut.
	saysNarrowed []string
}

func extraCases() []extraCase {
	return []extraCase{{
		name: "Redis", ext: redis.New(),
		objects: perNamespace(func(ns, m string) []object {
			return []object{{"services", map[string]any{"apiVersion": "v1", "kind": "Service",
				"metadata": meta(ns, "redis-"+m, nil),
				"spec":     map[string]any{"ports": []any{map[string]any{"name": "redis", "port": int64(6379)}}}}}}
		}),
		paths:   []string{"/"},
		totals:  []string{"/?ns=" + seen},
		details: []string{"/services/{ns}/redis-{m}"},
		// The endpoints behind a Service are read across namespaces: narrowed,
		// a Service with none read says "could not be read", not "nothing".
		saysNarrowed: []string{".namespaces[*].deployments[*].members[*].endpointless"},
	}, {
		name: "Pending", ext: pending.New(),
		objects: append(append(namespaces(), node()), perNamespace(func(ns, m string) []object {
			// busy holds a share of the node: a node's room is what the
			// person may list on it, never the pods of hidden.
			return []object{busy(ns, "busy-"+m), pod(ns, "waiting-"+m, "Pending", func(doc map[string]any) {
				delete(doc["spec"].(map[string]any), "nodeName")
				doc["status"].(map[string]any)["conditions"] = []any{map[string]any{"type": "PodScheduled", "status": "False",
					"reason": "Unschedulable", "message": "0/1 nodes are available: 1 Insufficient cpu.",
					"lastTransitionTime": "2026-11-01T00:00:00Z"}}
			})}
		})...),
		paths:   []string{"/", "/cells?kind=pods"},
		totals:  []string{"/?ns=" + seen},
		details: []string{"/pod/{ns}/waiting-{m}", "/brief/{ns}/waiting-{m}"},
	}, {
		name: "CNPG", ext: cnpg.New(), crds: []extension.CRD{pgClusters},
		objects: perNamespace(func(ns, m string) []object {
			return []object{{"clusters", map[string]any{"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster",
				"metadata": meta(ns, "pg-"+m, nil),
				"spec":     map[string]any{"instances": int64(1)},
				"status":   map[string]any{"instances": int64(1), "readyInstances": int64(1), "phase": "Cluster in healthy state", "currentPrimary": "pg-" + m + "-1"}}},
				pod(ns, "pg-"+m+"-1", "Running", func(doc map[string]any) {
					doc["metadata"].(map[string]any)["labels"] = map[string]any{"cnpg.io/cluster": "pg-" + m, "cnpg.io/instanceRole": "primary", "role": "primary"}
				})}
		}),
		paths:   []string{"/"},
		totals:  []string{"/pods"},
		details: []string{"/clusters/{ns}/pg-{m}"},
	}, {
		name: "Cert Manager", ext: certmanager.New(), crds: []extension.CRD{certificates},
		objects: perNamespace(func(ns, m string) []object {
			return []object{{"certificates", map[string]any{"apiVersion": "cert-manager.io/v1", "kind": "Certificate",
				"metadata": meta(ns, "cert-"+m, nil),
				"spec": map[string]any{"secretName": "cert-" + m + "-tls", "dnsNames": []any{m + ".example.com"},
					"issuerRef": map[string]any{"name": "letsencrypt", "kind": "ClusterIssuer"}},
				"status": map[string]any{"notAfter": "2026-11-20T00:00:00Z",
					"conditions": []any{map[string]any{"type": "Ready", "status": "True", "reason": "Ready"}}}}}}
		}),
		paths:   []string{"/", "/cells?kind=certificates.cert-manager.io"},
		totals:  []string{"/?ns=" + seen},
		details: []string{"/objects/certificates.cert-manager.io/{ns}/cert-{m}"},
	}, {
		name: "Runtime", ext: runtimeinspect.New(),
		objects: perNamespace(func(ns, m string) []object {
			return []object{pod(ns, "java-"+m, "Running", func(doc map[string]any) {
				c := doc["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
				c["image"] = "eclipse-temurin:21-jre"
				c["env"] = []any{map[string]any{"name": "JAVA_TOOL_OPTIONS", "value": "-Xmx512m"}}
			})}
		}),
		paths:   []string{"/", "/column?kind=pods"},
		totals:  []string{"/?ns=" + seen},
		details: []string{"/pod/{ns}/java-{m}"},
	}, {
		name: "Network Inspector", ext: netinspect.New(),
		objects: append(namespaces(), perNamespace(func(ns, m string) []object {
			return []object{
				pod(ns, "web-"+m, "Running", nil),
				{"networkpolicies", map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
					"metadata": meta(ns, "deny-"+m, nil),
					"spec":     map[string]any{"podSelector": map[string]any{}, "policyTypes": []any{"Ingress"}}}},
				// Each namespace admits the other's pods, and sends to them.
				{"networkpolicies", map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
					"metadata": meta(ns, "from-other-"+m, nil),
					"spec": map[string]any{"podSelector": map[string]any{}, "policyTypes": []any{"Ingress", "Egress"},
						"ingress": []any{map[string]any{"from": []any{map[string]any{"namespaceSelector": map[string]any{
							"matchLabels": map[string]any{"kubernetes.io/metadata.name": other(ns)}}}}}},
						"egress": []any{map[string]any{"to": []any{map[string]any{"namespaceSelector": map[string]any{
							"matchLabels": map[string]any{"kubernetes.io/metadata.name": other(ns)}}}}}}}}},
			}
		})...),
		paths:   []string{"/", "/cells?kind=pods"},
		totals:  []string{"/?ns=" + seen},
		details: []string{"/pods/{ns}/web-{m}"},
	}, {
		name: "KubeAdjust", ext: kubeadjust.New(),
		objects: append(append(namespaces(), node()), perNamespace(func(ns, m string) []object {
			return []object{busy(ns, "busy-"+m), pod(ns, "sized-"+m, "Running", func(doc map[string]any) {
				doc["status"].(map[string]any)["containerStatuses"] = []any{map[string]any{"name": "app", "ready": true}}
				doc["status"].(map[string]any)["qosClass"] = "Burstable"
			})}
		})...),
		paths:  []string{"/", "/node/node-1"},
		totals: []string{"/window", "/?ns=" + seen},
		// A node's totals count only the pods the person may list, so the
		// tiles' counts and the findings resting on a total are withheld.
		saysNarrowed: []string{".nodeStates[*].count", ".findings"},
	}, {
		name: "KubeCron", ext: kubecron.New(),
		objects: perNamespace(func(ns, m string) []object {
			return []object{{"cronjobs", map[string]any{"apiVersion": "batch/v1", "kind": "CronJob",
				"metadata": meta(ns, "nightly-"+m, nil),
				"spec":     map[string]any{"schedule": "0 3 * * *", "jobTemplate": map[string]any{"spec": map[string]any{}}},
				"status":   map[string]any{"lastScheduleTime": "2026-11-11T03:00:00Z"}}}}
		}),
		paths:   []string{"/"},
		totals:  []string{"/daily", "/days/2026-11-11", "/status"},
		details: []string{"/cronjobs/{ns}/nightly-{m}"},
	}, {
		name: "Argo CD", ext: argocd.New(), crds: []extension.CRD{applications},
		objects: perNamespace(func(ns, m string) []object {
			return []object{{"applications", map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
				"metadata": meta(ns, "app-"+m, nil),
				"spec": map[string]any{"destination": map[string]any{"namespace": ns},
					"source": map[string]any{"repoURL": "https://git.example/" + m, "path": "."}},
				"status": map[string]any{"sync": map[string]any{"status": "Synced"}, "health": map[string]any{"status": "Healthy"}}}}}
		}),
		paths:   []string{"/"},
		totals:  []string{"/flux", "/report", "/column?kind=applications.argoproj.io"},
		details: []string{"/applications/{ns}/app-{m}", "/resource?kind=applications.argoproj.io&namespace={ns}&name=app-{m}"},
	}, {
		name: "Security", ext: security.New(), crds: reportKinds,
		objects: perNamespace(func(ns, m string) []object {
			return []object{
				pod(ns, "api-"+m, "Running", nil),
				report("vulnerabilityreports", ns, "pod-api-"+m+"-app", "api-"+m, map[string]any{
					"Repository": "library/app-" + m, "Tag": "1", "Critical": int64(2), "High": int64(1), "Medium": int64(0), "Low": int64(0), "Unknown": int64(0)}),
				report("configauditreports", ns, "pod-api-"+m, "api-"+m, map[string]any{
					"Critical": int64(0), "High": int64(3), "Medium": int64(1), "Low": int64(0)}),
			}
		}),
		paths:   []string{"/", "/cells?kind=pods"},
		totals:  []string{"/?ns=" + seen, "/config?w=" + seen + "/Pod/api-" + seenMark + "&w=" + hidden + "/Pod/api-" + hiddenMark},
		details: []string{"/workload/{ns}/Pod/api-{m}", "/brief/{ns}/api-{m}?kind=pods"},
	}, {
		name: "Event Intelligence", ext: events.New(),
		objects: append(namespaces(), perNamespace(func(ns, m string) []object {
			// Enough Warnings to make the suggestion, each on a pod of the
			// namespace, owned by a Deployment whose ReplicaSet and ConfigMap
			// are the namespace's own and named with its mark.
			now := time.Now().UTC()
			objs := []object{
				{"replicasets", map[string]any{"apiVersion": "apps/v1", "kind": "ReplicaSet",
					"metadata": map[string]any{"namespace": ns, "name": "web-" + m + "-7f9c8d6b5", "uid": ns + "-rs",
						"creationTimestamp": now.Add(-20 * time.Minute).Format(time.RFC3339),
						"annotations":       map[string]any{"deployment.kubernetes.io/revision": "2"},
						"ownerReferences":   []any{map[string]any{"kind": "Deployment", "name": "web-" + m, "controller": true}}},
					"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
						"containers": []any{map[string]any{"name": "app", "image": "app:" + m}},
						"volumes":    []any{map[string]any{"name": "c", "configMap": map[string]any{"name": "cfg-" + m}}}}}}}},
				{"configmaps", map[string]any{"apiVersion": "v1", "kind": "ConfigMap",
					"metadata": map[string]any{"namespace": ns, "name": "cfg-" + m, "creationTimestamp": "2026-11-01T00:00:00Z",
						"managedFields": []any{map[string]any{"manager": "kubectl", "time": now.Add(-15 * time.Minute).Format(time.RFC3339)}}}}},
			}
			for i := 0; i < 25; i++ {
				name := fmt.Sprintf("web-%s-7f9c8d6b5-p%d", m, i)
				objs = append(objs,
					pod(ns, name, "Running", func(doc map[string]any) {
						doc["metadata"].(map[string]any)["ownerReferences"] = []any{map[string]any{"kind": "ReplicaSet", "name": "web-" + m + "-7f9c8d6b5", "controller": true}}
					}),
					object{"events", map[string]any{"apiVersion": "v1", "kind": "Event",
						"metadata":       map[string]any{"namespace": ns, "name": name + ".bo", "creationTimestamp": now.Format(time.RFC3339)},
						"involvedObject": map[string]any{"kind": "Pod", "namespace": ns, "name": name, "uid": ns + name},
						"reason":         "BackOff", "type": "Warning", "message": "Back-off restarting failed container in pod " + name,
						"count": int64(3), "firstTimestamp": now.Add(-10 * time.Minute).Format(time.RFC3339), "lastTimestamp": now.Add(-time.Minute).Format(time.RFC3339)}})
			}
			return objs
		})...),
		paths:   []string{"/"},
		totals:  []string{"/?namespace=" + seen, "/?namespace=" + seen + "&window=3h"},
		details: []string{"/object/deployments/{ns}/web-{m}", "/object/pods/{ns}/web-{m}-7f9c8d6b5-p0", "/line/pods/{ns}/web-{m}-7f9c8d6b5-p0", "/line/deployments/{ns}/web-{m}"},
	}}
}

var reportKinds = []extension.CRD{
	{Group: "aquasecurity.github.io", Version: "v1alpha1", Plural: "vulnerabilityreports", Kind: "VulnerabilityReport", Namespaced: true},
	{Group: "aquasecurity.github.io", Version: "v1alpha1", Plural: "configauditreports", Kind: "ConfigAuditReport", Namespaced: true},
	{Group: "aquasecurity.github.io", Version: "v1alpha1", Plural: "exposedsecretreports", Kind: "ExposedSecretReport", Namespaced: true},
}

// report is a trivy-operator report on one pod, read as a table.
func report(resource, namespace, name, pod string, cells map[string]any) object {
	labels := map[string]string{"trivy-operator.resource.kind": "Pod", "trivy-operator.resource.name": pod,
		"trivy-operator.resource.namespace": namespace, "trivy-operator.container.name": "app"}
	return object{resource, map[string]any{"apiVersion": "aquasecurity.github.io/v1alpha1",
		"metadata": meta(namespace, name, labels), "cells": cells}}
}

// serve mounts the extra's routes as the core does and asks path as person.
func serve(t *testing.T, ext extension.Extension, h extension.Host, path, person string) (int, string) {
	t.Helper()
	mux := http.NewServeMux()
	for _, route := range ext.Routes(h) {
		pattern := "/x/{cluster}/" + ext.Key() + route.Pattern
		if route.Method != "" {
			pattern = route.Method + " " + pattern
		}
		mux.Handle(pattern, route.Handler)
	}
	req := httptest.NewRequest(http.MethodGet, "/x/prod/"+ext.Key()+path, nil)
	req.Header.Set(personHeader, person)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestEveryExtraShowsAPersonNothingOfANamespaceTheyMayNotList(t *testing.T) {
	for _, c := range extraCases() {
		t.Run(c.name, func(t *testing.T) {
			h := newClusterHost(t, c.crds, c.objects)
			for _, path := range c.paths {
				code, body := serve(t, c.ext, h, path, permitted)
				if code != http.StatusOK {
					t.Fatalf("%s for the allowed person answered %d: %s", path, code, body)
				}
				if !json.Valid([]byte(body)) {
					t.Fatalf("%s for the allowed person is not JSON: %s", path, body)
				}
				if !strings.Contains(body, seenMark) || !strings.Contains(body, hiddenMark) {
					t.Errorf("%s shows the allowed person %s and %s: %t, %t; want both namespaces' objects:\n%s",
						path, seen, hidden, strings.Contains(body, seenMark), strings.Contains(body, hiddenMark), clip(body))
				}

				code, body = serve(t, c.ext, h, path, refused)
				if code != http.StatusOK {
					t.Fatalf("%s for the refused person answered %d: %s", path, code, body)
				}
				if strings.Contains(body, hiddenMark) {
					t.Errorf("%s shows the person refused in %s an object of it:\n%s", path, hidden, clip(body))
				}
				if !strings.Contains(body, seenMark) {
					t.Errorf("%s shows the refused person nothing of %s, where they may list:\n%s", path, seen, clip(body))
				}
			}
		})
	}
}

func clip(s string) string {
	if len(s) > 1500 {
		return s[:1500] + "..."
	}
	return s
}

// --- what a refused namespace leaves in an answer ------------------------------

// without is objects less every one of namespace: the cluster as the refused
// person would see it if namespace held nothing.
func without(objects []object, namespace string) []object {
	var out []object
	for _, o := range objects {
		if md, _ := o.doc["metadata"].(map[string]any); md["namespace"] == namespace {
			continue
		}
		out = append(out, o)
	}
	return out
}

// comparable is body decoded for comparing what it counts and lists, not how
// it words it: every string becomes one placeholder unless it names something
// of the refused namespace (the marks are searched for by the test above),
// and the fields that say an answer was narrowed, or when it was read, go.
// What is left - every number, every flag, every list's length - must not
// depend on a namespace the person may not list.
func comparable(t *testing.T, body string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("not JSON: %v: %s", err, clip(body))
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, inner := range x {
				if k == "complete" || k == "note" || k == "notes" || strings.HasSuffix(k, "Note") || strings.HasSuffix(k, "Ms") {
					delete(x, k)
					continue
				}
				x[k] = walk(inner)
			}
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
		case string:
			if strings.Contains(x, hidden) || strings.Contains(x, hiddenMark) {
				return x
			}
			return "…"
		}
		return v
	}
	return walk(v)
}

// The refused person's answer is the answer over a cluster where the refused
// namespace holds nothing, but for saying it was narrowed: no count, cell,
// total or suggestion carries anything of it.
func TestEveryExtraAnswersARefusedPersonAsIfTheNamespaceHeldNothing(t *testing.T) {
	for _, c := range extraCases() {
		t.Run(c.name, func(t *testing.T) {
			full := newClusterHost(t, c.crds, c.objects)
			empty := newClusterHost(t, c.crds, without(c.objects, hidden))
			var paths []string
			paths = append(paths, c.paths...)
			paths = append(paths, c.totals...)
			for _, d := range c.details {
				paths = append(paths, strings.NewReplacer("{ns}", seen, "{m}", seenMark).Replace(d))
			}
			for _, path := range paths {
				code, got := serve(t, c.ext, full, path, refused)
				wantCode, want := serve(t, c.ext, empty, path, permitted)
				if code != wantCode {
					t.Errorf("%s answered the refused person %d, and %d over a cluster without %s:\n%s", path, code, wantCode, hidden, clip(got))
					continue
				}
				if diffs := differences("", comparable(t, got), comparable(t, want), c.saysNarrowed); len(diffs) > 0 {
					t.Errorf("%s differs for the refused person from the answer over a cluster without %s:\n%s",
						path, hidden, strings.Join(diffs, "\n"))
				}
			}
			s, ok := c.ext.(extension.Suggester)
			if !ok {
				return
			}
			got, err := s.Suggest(personRequest(refused), full, "prod")
			if err != nil {
				t.Fatalf("Suggest for the refused person: %v", err)
			}
			want, err := s.Suggest(personRequest(permitted), empty, "prod")
			if err != nil {
				t.Fatalf("Suggest over a cluster without %s: %v", hidden, err)
			}
			if strings.TrimPrefix(got, "At least ") != want {
				t.Errorf("Suggest tells the refused person %q, and %q over a cluster without %s", got, want, hidden)
			}
		})
	}
}

// A route naming one object of a namespace the person may not list answers
// a refusal, an absence, or reads that were refused - never the object or
// anything read about it; the other person is answered it.
func TestNoExtraOpensAnObjectOfANamespaceRefused(t *testing.T) {
	for _, c := range extraCases() {
		if len(c.details) == 0 {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			h := newClusterHost(t, c.crds, c.objects)
			for _, d := range c.details {
				path := strings.NewReplacer("{ns}", hidden, "{m}", hiddenMark).Replace(d)
				if code, body := serve(t, c.ext, h, path, permitted); code != http.StatusOK || !strings.Contains(body, hiddenMark) {
					t.Errorf("%s for the allowed person answered %d: %s", path, code, clip(body))
				}
				// An answer that says the reads were refused may name what was
				// asked: the name in the address, and nothing else of it.
				code, body := serve(t, c.ext, h, path, refused)
				asked := path[strings.LastIndex(strings.SplitN(path, "?", 2)[0], "/")+1:]
				asked = strings.SplitN(asked, "?", 2)[0]
				if code != http.StatusForbidden && code != http.StatusNotFound &&
					strings.Contains(strings.ReplaceAll(body, asked, ""), hiddenMark) {
					t.Errorf("%s for the refused person answered %d with something of %s:\n%s", path, code, hidden, clip(body))
				}
			}
		})
	}
}

func personRequest(person string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set(personHeader, person)
	return r
}

// differences names each place a and b differ, as refused against without,
// but for the allowed fields.
func differences(at string, a, b any, allowed []string) []string {
	general := indexes.ReplaceAllString(at, "[*]")
	for _, field := range allowed {
		if general == field {
			return nil
		}
	}
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			break
		}
		var out []string
		for k := range x {
			out = append(out, differences(at+"."+k, x[k], y[k], allowed)...)
		}
		for k := range y {
			if _, ok := x[k]; !ok {
				out = append(out, differences(at+"."+k, nil, y[k], allowed)...)
			}
		}
		sort.Strings(out)
		return out
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			break
		}
		var out []string
		for i := range x {
			out = append(out, differences(fmt.Sprintf("%s[%d]", at, i), x[i], y[i], allowed)...)
		}
		return out
	}
	if reflect.DeepEqual(a, b) {
		return nil
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return []string{fmt.Sprintf("  %s: refused %s, without %s", at, clip(string(ja)), clip(string(jb)))}
}

var indexes = regexp.MustCompile(`\[[0-9]+\]`)
