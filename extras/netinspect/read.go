package netinspect

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/textutil"
)

var (
	policiesGVR   = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}
	podsGVR       = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	namespacesGVR = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	daemonSetsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}
)

// The caps on what one request reads: past them the analysis says it is
// partial (Page.Complete false) rather than holding a cluster's worth of
// objects for one answer. A cluster at these sizes is far beyond the load
// test's 5 000 pods and 1 000 policies.
const (
	maxPods       = 20_000
	maxPolicies   = 5_000
	maxNamespaces = 5_000
	maxDaemonSets = 500
)

// read is what one request read, and how sure it is.
type read struct {
	model      *model
	notes      []string
	complete   bool
	cni        CNI
	unevaluate []Unevaluated
	cilium     CiliumRead
	calico     PolicyRead
	admin      PolicyRead
	// evaluated is the foreign kinds (group/plural) that were read and are
	// evaluated: they are not listed as unevaluated.
	evaluated map[string]bool
	// groups remembers workloadOfGroup for this request.
	groups map[string]string
}

// errPoliciesRefused and its kin carry which read failed, so the handler can
// answer the right sentence.
type readError struct {
	what string
	err  error
}

func (e *readError) Error() string { return e.what + ": " + e.err.Error() }
func (e *readError) Unwrap() error { return e.err }

// gather reads what the engine needs, for the person behind r, and builds
// the model. NetworkPolicies and pods are required: without either there is
// no answer, and the error says which. Namespaces, DaemonSets and the
// catalog are not: each one that cannot be read is a note, and the
// analysis goes on with less.
func gather(r *http.Request, h extension.Host, cluster string) (*read, error) {
	out := &read{complete: true, evaluated: map[string]bool{}}

	policyPage, err := h.List(r, cluster, extension.ListQuery{GVR: policiesGVR, Limit: maxPolicies})
	if err != nil {
		return nil, &readError{what: "networkpolicies", err: err}
	}
	out.partial(policyPage, "NetworkPolicies")
	policies := make([]networkingv1.NetworkPolicy, len(policyPage.Items))
	for i := range policyPage.Items {
		if err := convert(&policyPage.Items[i], &policies[i]); err != nil {
			return nil, &readError{what: "networkpolicies", err: err}
		}
	}
	policyPage.Items = nil

	podPage, err := h.List(r, cluster, extension.ListQuery{GVR: podsGVR, Limit: maxPods})
	if err != nil {
		return nil, &readError{what: "pods", err: err}
	}
	out.partial(podPage, "Pods")
	pods := make([]corev1.Pod, len(podPage.Items))
	for i := range podPage.Items {
		podFrom(&podPage.Items[i], &pods[i])
	}
	podPage.Items = nil

	namespaces := map[string]map[string]string{}
	nsPage, err := h.List(r, cluster, extension.ListQuery{GVR: namespacesGVR, Limit: maxNamespaces})
	switch {
	case errors.Is(err, extension.ErrUnknownCluster):
		return nil, err
	case err != nil:
		out.complete = false
		out.notes = append(out.notes, "The namespaces could not be read ("+short(err)+"): a namespaceSelector is "+
			"evaluated only on kubernetes.io/metadata.name, the one label every namespace carries.")
	default:
		out.partial(nsPage, "Namespaces")
		for i := range nsPage.Items {
			namespaces[nsPage.Items[i].GetName()] = nsPage.Items[i].GetLabels()
		}
	}

	out.model = build(pods, policies, namespaces)
	out.cni = recognise(r, h, cluster)
	served := servedKinds(r, h, cluster)
	out.cilium = readCilium(r, h, cluster, served, out)
	out.calico = readCalico(r, h, cluster, served, out)
	out.admin = readAdmin(r, h, cluster, served, out)
	out.unevaluate = unevaluated(r, h, cluster, served, out.evaluated)
	return out, nil
}

// partial notes a listing that did not reach its end.
func (rd *read) partial(page extension.Page, what string) {
	if page.Complete {
		return
	}
	rd.complete = false
	note := page.Note
	if note == "" {
		note = "the listing stopped before its end"
	}
	rd.notes = append(rd.notes, what+": "+note+". What is drawn covers only what was read.")
}

// podFrom reads the few fields of a pod the engine uses, straight from the
// listing's maps and without copying them: the reflective converter, or the
// copying unstructured accessors, cost several times more for 20 000 pods,
// and the rest of a pod is never read.
func podFrom(u *unstructured.Unstructured, p *corev1.Pod) {
	p.Namespace, p.Name, p.Labels = u.GetNamespace(), u.GetName(), u.GetLabels()
	for _, ref := range u.GetOwnerReferences() {
		if ref.Controller != nil && *ref.Controller {
			p.OwnerReferences = []metav1.OwnerReference{ref}
			break
		}
	}
	spec, _ := u.Object["spec"].(map[string]any)
	status, _ := u.Object["status"].(map[string]any)
	p.Spec.HostNetwork, _ = spec["hostNetwork"].(bool)
	p.Spec.ServiceAccountName, _ = spec["serviceAccountName"].(string)
	ip, _ := status["podIP"].(string)
	phase, _ := status["phase"].(string)
	p.Status.PodIP, p.Status.Phase = ip, corev1.PodPhase(phase)
	containers, _ := spec["containers"].([]any)
	for _, c := range containers {
		cm, _ := c.(map[string]any)
		ports, _ := cm["ports"].([]any)
		var container corev1.Container
		for _, port := range ports {
			pm, _ := port.(map[string]any)
			name, _ := pm["name"].(string)
			if name == "" {
				continue
			}
			protocol, _ := pm["protocol"].(string)
			container.Ports = append(container.Ports, corev1.ContainerPort{Name: name, ContainerPort: int32(number(pm["containerPort"])), Protocol: corev1.Protocol(protocol)})
		}
		if len(container.Ports) > 0 {
			p.Spec.Containers = append(p.Spec.Containers, container)
		}
	}
}

// number is a JSON number as the decoder or the converter left it.
func number(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

func convert(u *unstructured.Unstructured, into any) error {
	return runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, into)
}

// --- the network plugin ----------------------------------------------------------

// CNI is the network plugin as its DaemonSets name it: a presumption on
// names, said as one.
type CNI struct {
	Name string `json:"name"`
	// Enforces is "yes", "no" or "unknown".
	Enforces string `json:"enforces"`
	Detail   string `json:"detail"`
}

// plugins are the DaemonSet names a network plugin ships under, and whether
// it applies NetworkPolicies. Flannel does not: a cluster running it alone
// declares rules that nothing applies.
var plugins = []struct {
	match    func(string) bool
	name     string
	enforces bool
}{
	{func(n string) bool { return n == "cilium" }, "Cilium", true},
	{func(n string) bool { return n == "calico-node" }, "Calico", true},
	{func(n string) bool { return n == "antrea-agent" }, "Antrea", true},
	{func(n string) bool { return n == "kube-router" }, "kube-router", true},
	{func(n string) bool { return n == "weave-net" }, "Weave Net", true},
	{func(n string) bool { return n == "aws-network-policy-agent" }, "Amazon VPC CNI with its network policy agent", true},
	{func(n string) bool { return n == "kindnet" }, "kindnet", true},
	{func(n string) bool { return strings.HasPrefix(n, "kube-flannel") || n == "flannel" }, "flannel", false},
	{func(n string) bool { return n == "aws-node" }, "Amazon VPC CNI", false},
}

// recognize reads the DaemonSets and names the plugin. An enforcing plugin
// found anywhere wins over one that does not enforce (Canal is flannel and
// Calico together; EKS runs aws-node beside its policy agent).
func recognise(r *http.Request, h extension.Host, cluster string) CNI {
	page, err := h.List(r, cluster, extension.ListQuery{GVR: daemonSetsGVR, Limit: maxDaemonSets})
	if err != nil {
		return CNI{Enforces: "unknown", Detail: "The DaemonSets could not be read (" + short(err) +
			"), so the network plugin is not known: whether these rules are applied cannot be said."}
	}
	var found []int
	for i := range page.Items {
		name := page.Items[i].GetName()
		for pi, p := range plugins {
			if p.match(name) {
				found = append(found, pi)
			}
		}
	}
	sort.Ints(found)
	for _, pi := range found {
		if plugins[pi].enforces {
			return CNI{Name: plugins[pi].name, Enforces: "yes",
				Detail: "Recognized from its DaemonSet: " + plugins[pi].name + " applies NetworkPolicies."}
		}
	}
	if len(found) > 0 {
		p := plugins[found[0]]
		return CNI{Name: p.name, Enforces: "no",
			Detail: p.name + " does not apply NetworkPolicies on its own: the rules below are declared and nothing enforces them."}
	}
	return CNI{Enforces: "unknown", Detail: "No known network plugin was recognized from the DaemonSets: whether these rules are applied cannot be said."}
}

// --- the policies this extra does not evaluate -------------------------------------

// Unevaluated is a kind of policy that applies on top of NetworkPolicies and
// that could not be evaluated: its listing failed, so what it holds is unknown.
type Unevaluated struct {
	Kind  string `json:"kind"`
	Group string `json:"group"`
	// Count is "at least 1" (one object asked for), or "" when the kind is
	// served and could not be listed.
	Count string `json:"count"`
}

var foreign = []struct{ group, plural, kind string }{
	{"crd.projectcalico.org", "networkpolicies", "Calico NetworkPolicy"},
	{"crd.projectcalico.org", "globalnetworkpolicies", "Calico GlobalNetworkPolicy"},
	{"policy.networking.k8s.io", "adminnetworkpolicies", "AdminNetworkPolicy"},
	{"policy.networking.k8s.io", "baselineadminnetworkpolicies", "BaselineAdminNetworkPolicy"},
}

// unevaluated is every foreign kind the cluster serves, holds at least one
// of and that was not read for evaluation, from the console's catalog and
// one-row listings.
func unevaluated(r *http.Request, h extension.Host, cluster string, served map[string]extension.CRD, evaluated map[string]bool) []Unevaluated {
	out := []Unevaluated{}
	for _, f := range foreign {
		crd, ok := served[f.group+"/"+f.plural]
		if !ok || evaluated[f.group+"/"+f.plural] {
			continue
		}
		page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR(), Limit: 1})
		switch {
		case err != nil:
			out = append(out, Unevaluated{Kind: f.kind, Group: f.group})
		case len(page.Items) > 0:
			out = append(out, Unevaluated{Kind: f.kind, Group: f.group, Count: "at least 1"})
		}
	}
	return out
}

// servedKinds is the cluster's custom kinds by group/plural, from the
// console's catalog; empty when it cannot be read.
func servedKinds(r *http.Request, h extension.Host, cluster string) map[string]extension.CRD {
	served := map[string]extension.CRD{}
	registry, err := h.Definitions(r, cluster)
	if err != nil || registry == nil {
		return served
	}
	all, err := registry.All(r.Context())
	if err != nil {
		return served
	}
	for _, crd := range all {
		served[crd.Group+"/"+crd.Plural] = crd
	}
	return served
}

// short is an error's first line, clipped: for a note, never a stack of
// wrapped causes.
func short(err error) string {
	return textutil.FirstLineN(err.Error(), 200)
}

// CiliumRead is what was read of Cilium's policies: whether the cluster
// serves them, how many, and what in them is not evaluated.
type CiliumRead struct {
	// Served is whether the cluster serves CiliumNetworkPolicy.
	Served   bool `json:"served"`
	Policies int  `json:"policies"`
	// HostPolicies are policies with a nodeSelector: about nodes, not pods,
	// and not evaluated.
	HostPolicies int `json:"hostPolicies"`
	// NotEvaluated counts each construct no rule's answer can rest on, as
	// "toFQDNs: 2".
	NotEvaluated []string `json:"notEvaluated"`
}

// readCilium lists the Cilium policies and CIDR groups the cluster serves,
// for the person, and adds them to the model. A kind that cannot be listed
// is a note and a partial answer, never "no Cilium policy".
func readCilium(r *http.Request, h extension.Host, cluster string, served map[string]extension.CRD, rd *read) CiliumRead {
	out := CiliumRead{NotEvaluated: []string{}}
	cnp, okNS := served["cilium.io/ciliumnetworkpolicies"]
	ccnp, okCW := served["cilium.io/ciliumclusterwidenetworkpolicies"]
	if !okNS && !okCW {
		return out
	}
	out.Served = true
	groups := map[string][]string{}
	if g, ok := served["cilium.io/ciliumcidrgroups"]; ok {
		page, err := h.List(r, cluster, extension.ListQuery{GVR: g.GVR(), Limit: maxPolicies})
		if err != nil {
			rd.complete = false
			rd.notes = append(rd.notes, "CiliumCIDRGroups: "+short(err)+". A rule naming one is not evaluated.")
		} else {
			rd.partial(page, "CiliumCIDRGroups")
			for i := range page.Items {
				cidrs, _, _ := unstructured.NestedStringSlice(page.Items[i].Object, "spec", "externalCIDRs")
				groups[page.Items[i].GetName()] = cidrs
			}
		}
	}
	var compiled []policy
	for _, k := range []struct {
		crd         extension.CRD
		ok          bool
		clusterwide bool
		what        string
	}{{cnp, okNS, false, "CiliumNetworkPolicies"}, {ccnp, okCW, true, "CiliumClusterwideNetworkPolicies"}} {
		if !k.ok {
			continue
		}
		page, err := h.List(r, cluster, extension.ListQuery{GVR: k.crd.GVR(), Limit: maxPolicies})
		if err != nil {
			rd.complete = false
			rd.notes = append(rd.notes, k.what+": "+short(err)+". They apply and are not drawn.")
			continue
		}
		rd.partial(page, k.what)
		for i := range page.Items {
			ps, host := compileCilium(&page.Items[i], k.clusterwide, groups)
			out.HostPolicies += host
			compiled = append(compiled, ps...)
		}
	}
	out.Policies = len(compiled)
	counts := map[string]int{}
	for _, p := range compiled {
		for _, rules := range [][]rule{p.In, p.Out} {
			for _, ru := range rules {
				for _, o := range ru.Opaque {
					counts[strings.SplitN(o, " ", 2)[0]]++
				}
				for _, l := range ru.L7 {
					counts[strings.SplitN(l, " (", 2)[0]]++
				}
			}
		}
	}
	for k, n := range counts {
		out.NotEvaluated = append(out.NotEvaluated, fmt.Sprintf("%s: %d", k, n))
	}
	sort.Strings(out.NotEvaluated)
	rd.model.addCilium(compiled)
	return out
}

// readCalico lists Calico's policies and tiers the cluster serves, for the
// person, and adds them to the model. A kind that cannot be listed is a note
// and a partial answer, and stays in the unevaluated list.
func readCalico(r *http.Request, h extension.Host, cluster string, served map[string]extension.CRD, rd *read) PolicyRead {
	out := PolicyRead{NotEvaluated: []string{}}
	np, okNP := served["crd.projectcalico.org/networkpolicies"]
	gnp, okGNP := served["crd.projectcalico.org/globalnetworkpolicies"]
	if !okNP && !okGNP {
		return out
	}
	out.Served = true
	tiers := map[string]calicoTier{}
	if t, ok := served["crd.projectcalico.org/tiers"]; ok {
		page, err := h.List(r, cluster, extension.ListQuery{GVR: t.GVR(), Limit: maxPolicies})
		if err != nil {
			rd.complete = false
			rd.notes = append(rd.notes, "Calico tiers: "+short(err)+". The default tier and Calico's usual order are assumed.")
		} else {
			rd.partial(page, "Calico tiers")
			for i := range page.Items {
				name := page.Items[i].GetName()
				tier := calicoTier{Name: name, Order: math.Inf(1)}
				if o, ok, _ := unstructured.NestedFloat64(page.Items[i].Object, "spec", "order"); ok {
					tier.Order = o
				} else if o, ok, _ := unstructured.NestedInt64(page.Items[i].Object, "spec", "order"); ok {
					tier.Order = float64(o)
				}
				tier.DefaultAction, _, _ = unstructured.NestedString(page.Items[i].Object, "spec", "defaultAction")
				tiers[name] = tier
			}
		}
	}
	skipped := map[string]int{}
	var compiled []policy
	for _, k := range []struct {
		crd    extension.CRD
		ok     bool
		global bool
		what   string
	}{{np, okNP, false, "Calico NetworkPolicies"}, {gnp, okGNP, true, "Calico GlobalNetworkPolicies"}} {
		if !k.ok {
			continue
		}
		page, err := h.List(r, cluster, extension.ListQuery{GVR: k.crd.GVR(), Limit: maxPolicies})
		if err != nil {
			rd.complete = false
			rd.notes = append(rd.notes, k.what+": "+short(err)+". They apply and are not drawn.")
			continue
		}
		rd.evaluated[k.crd.Group+"/"+k.crd.Plural] = true
		rd.partial(page, k.what)
		for i := range page.Items {
			compiled = append(compiled, compileCalico(&page.Items[i], k.global, skipped))
		}
	}
	out.Policies = len(compiled)
	out.NotEvaluated = countsText(skipped)
	rd.model.addCalico(compiled, tiers)
	return out
}

// readAdmin lists the AdminNetworkPolicies and the BaselineAdminNetworkPolicy
// the cluster serves, for the person, and adds them to the model.
func readAdmin(r *http.Request, h extension.Host, cluster string, served map[string]extension.CRD, rd *read) PolicyRead {
	out := PolicyRead{NotEvaluated: []string{}}
	var compiled []policy
	var subjects []*subjectOf
	skipped := map[string]int{}
	for _, k := range []struct {
		plural   string
		baseline bool
		what     string
	}{{"adminnetworkpolicies", false, "AdminNetworkPolicies"}, {"baselineadminnetworkpolicies", true, "BaselineAdminNetworkPolicies"}} {
		crd, ok := served["policy.networking.k8s.io/"+k.plural]
		if !ok {
			continue
		}
		out.Served = true
		page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR(), Limit: maxPolicies})
		if err != nil {
			rd.complete = false
			rd.notes = append(rd.notes, k.what+": "+short(err)+". They apply and are not drawn.")
			continue
		}
		rd.evaluated[crd.Group+"/"+crd.Plural] = true
		rd.partial(page, k.what)
		for i := range page.Items {
			p, subj := compileAdmin(&page.Items[i], k.baseline)
			compiled, subjects = append(compiled, p), append(subjects, subj)
			for _, rules := range [][]rule{p.In, p.Out} {
				for _, ru := range rules {
					for _, o := range ru.Opaque {
						skipped[o]++
					}
				}
			}
		}
	}
	out.Policies = len(compiled)
	out.NotEvaluated = countsText(skipped)
	rd.model.addAdmin(compiled, subjects)
	return out
}
