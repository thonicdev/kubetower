package netinspect

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/thonicdev/kubetower/extension"
)

// withCilium serves Cilium's three kinds on the fake host.
func withCilium(h *fakeHost) {
	h.crds = append(h.crds,
		extension.CRD{Group: "cilium.io", Version: "v2", Plural: "ciliumnetworkpolicies", Kind: "CiliumNetworkPolicy", Namespaced: true},
		extension.CRD{Group: "cilium.io", Version: "v2", Plural: "ciliumclusterwidenetworkpolicies", Kind: "CiliumClusterwideNetworkPolicy"},
		extension.CRD{Group: "cilium.io", Version: "v2", Plural: "ciliumcidrgroups", Kind: "CiliumCIDRGroup"},
	)
}

// manifest adds one object written as YAML, under its kind's plural.
func (h *fakeHost) manifest(t *testing.T, text string) {
	t.Helper()
	var obj map[string]any
	if err := yaml.Unmarshal([]byte(text), &obj); err != nil {
		t.Fatal(err)
	}
	u := unstructured.Unstructured{Object: obj}
	plural := map[string]string{
		"CiliumNetworkPolicy":            "ciliumnetworkpolicies",
		"CiliumClusterwideNetworkPolicy": "ciliumclusterwidenetworkpolicies",
		"CiliumCIDRGroup":                "ciliumcidrgroups",
	}[u.GetKind()]
	if plural == "" {
		t.Fatalf("no plural for %s", u.GetKind())
	}
	h.objects[plural] = append(h.objects[plural], u)
}

// A clusterwide baseline, as a cluster running Cilium in default deny has
// one: ingress from the cluster and the nodes, egress to the cluster, the
// API server, DNS, and a site's addresses on two ports.
const baseline = `
apiVersion: cilium.io/v2
kind: CiliumClusterwideNetworkPolicy
metadata: {name: baseline}
spec:
  endpointSelector: {}
  ingress:
    - fromEntities: [cluster, host, remote-node]
  egress:
    - toEntities: [cluster, kube-apiserver]
    - toEntities: [host, remote-node]
      toPorts:
        - ports: [{port: "10250", protocol: TCP}]
    - toCIDR: [192.0.2.0/24]
      toPorts:
        - ports: [{port: "6443", protocol: TCP}]
    - toEndpoints:
        - matchLabels: {io.kubernetes.pod.namespace: kube-system, k8s-app: kube-dns}
      toPorts:
        - ports: [{port: "53", protocol: UDP}, {port: "53", protocol: TCP}]
`

func ciliumShop(t *testing.T) *fakeHost {
	h := shop(t)
	withCilium(h)
	h.manifest(t, baseline)
	return h
}

func TestACiliumBaselineIsolatesEveryPodAndAllowsTheCluster(t *testing.T) {
	h := ciliumShop(t)
	for _, tc := range []struct{ from, to, port, want string }{
		{"shop/web", "shop/api", "80", "yes"},     // cluster both ways
		{"shop/web", "ops/probe", "9999", "yes"},  // any port inside the cluster
		{"shop/web", "198.51.100.7", "443", "no"}, // world is not allowed out
		{"shop/web", "192.0.2.10", "6443", "yes"}, // toCIDR with its port
		{"shop/web", "192.0.2.10", "22", "no"},    // toCIDR, another port
		{"198.51.100.7", "shop/api", "80", "no"},  // world is not allowed in
	} {
		v := verdictOf(t, h, tc.from, tc.to, tc.port)
		if v.Answer != tc.want {
			t.Errorf("%s → %s:%s = %s, want %s (egress %s: %s; ingress %s: %s)", tc.from, tc.to, tc.port, v.Answer, tc.want,
				v.Egress.State, v.Egress.Reason, v.Ingress.State, v.Ingress.Reason)
		}
	}
	o := overviewOf(t, h, "")
	if !o.Cilium.Served || o.Cilium.Policies != 1 || o.Summary.IsolatedIn != 4 || o.Summary.IsolatedOut != 4 {
		t.Errorf("cilium %+v, summary %+v", o.Cilium, o.Summary)
	}
	for _, u := range o.Unevaluated {
		if strings.HasPrefix(u.Kind, "Cilium") {
			t.Errorf("Cilium's policies are evaluated now, and must not be listed as not evaluated: %+v", u)
		}
	}
}

// A namespaced policy's fromEndpoints stays in its namespace unless it names
// one; naming one reaches across.
func TestFromEndpointsStayInThePolicysNamespaceUnlessTheyNameOne(t *testing.T) {
	h := shop(t)
	withCilium(h)
	h.manifest(t, `
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata: {name: api-in, namespace: shop}
spec:
  endpointSelector: {matchLabels: {app: api}}
  ingress:
    - fromEndpoints: [{matchLabels: {app: probe}}]
    - fromEndpoints: [{matchLabels: {app: web}}]
      toPorts: [{ports: [{port: "80"}]}]
`)
	if v := verdictOf(t, h, "ops/probe", "shop/api", "80"); v.Answer != "no" {
		t.Errorf("app=probe in another namespace is not selected without a namespace label: %+v", v.Ingress)
	}
	if v := verdictOf(t, h, "shop/web", "shop/api", "80"); v.Answer != "yes" || v.Ingress.Decided == nil || v.Ingress.Decided.Rule != 2 {
		t.Errorf("web on 80, any protocol: %+v", v.Ingress)
	}
	h.manifest(t, `
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata: {name: api-from-ops, namespace: shop}
spec:
  endpointSelector: {matchLabels: {app: api}}
  ingress:
    - fromEndpoints: [{matchLabels: {k8s:io.kubernetes.pod.namespace: ops, k8s:app: probe}}]
`)
	if v := verdictOf(t, h, "ops/probe", "shop/api", "80"); v.Answer != "yes" {
		t.Errorf("naming the namespace reaches across: %+v", v.Ingress)
	}
}

// A deny wins over every allow, a NetworkPolicy's included, and matches with
// matchExpressions on the namespace.
func TestADenyWinsOverEveryAllow(t *testing.T) {
	h := ciliumShop(t)
	h.namespace(t, "data", nil)
	h.pod(t, "data", "loader", "10.0.2.10", map[string]string{"app": "loader"})
	h.manifest(t, `
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata: {name: api-only-from-friends, namespace: shop}
spec:
  endpointSelector: {matchLabels: {app: api}}
  ingressDeny:
    - fromEndpoints:
        - matchExpressions:
            - {key: io.kubernetes.pod.namespace, operator: NotIn, values: [shop, ops]}
      toPorts: [{ports: [{port: "80", protocol: TCP}]}]
`)
	v := verdictOf(t, h, "data/loader", "shop/api", "80")
	if v.Answer != "no" || v.Ingress.State != sideDenied || v.Ingress.Decided == nil || v.Ingress.Decided.Kind != "deny" {
		t.Errorf("data → api:80 is denied by the deny rule, over the baseline's cluster allow: %+v", v.Ingress)
	}
	if v := verdictOf(t, h, "data/loader", "shop/api", "81"); v.Answer != "yes" {
		t.Errorf("another port is not denied: %+v", v.Ingress)
	}
	if v := verdictOf(t, h, "ops/probe", "shop/api", "80"); v.Answer != "yes" {
		t.Errorf("ops is not in the deny: %+v", v.Ingress)
	}
	// A NetworkPolicy allowing it changes nothing.
	p := np("shop", "api-open", map[string]string{"app": "api"})
	p.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{}}
	h.policy(t, p)
	if v := verdictOf(t, h, "data/loader", "shop/api", "80"); v.Answer != "no" {
		t.Errorf("a NetworkPolicy allow does not beat a Cilium deny: %+v", v.Ingress)
	}
}

// toCIDRSet with except and cidrGroupRef, in an egress deny, beside a world
// allow.
func TestCIDRSetsExceptAndGroups(t *testing.T) {
	h := ciliumShop(t)
	h.manifest(t, `
apiVersion: cilium.io/v2
kind: CiliumCIDRGroup
metadata: {name: storage}
spec: {externalCIDRs: [203.0.113.0/28]}
`)
	h.manifest(t, `
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata: {name: web-out, namespace: shop}
spec:
  endpointSelector: {matchLabels: {app: web}}
  egress:
    - toEntities: [world]
      toPorts: [{ports: [{port: "443", protocol: TCP}]}]
    - toCIDRSet: [{cidrGroupRef: storage}]
      toPorts: [{ports: [{port: "2049", protocol: TCP}]}]
  egressDeny:
    - toCIDRSet:
        - cidr: 10.0.0.0/8
        - cidr: 172.16.0.0/12
          except: [172.16.0.0/16]
`)
	for _, tc := range []struct{ to, port, want string }{
		{"198.51.100.7", "443", "yes"}, // world
		{"10.9.9.9", "443", "no"},      // denied range
		{"172.20.0.1", "443", "no"},    // denied range
		{"172.16.3.3", "443", "yes"},   // the except
		{"203.0.113.5", "2049", "yes"}, // the CIDR group
		{"203.0.113.50", "2049", "no"}, // outside the group
	} {
		if v := verdictOf(t, h, "shop/web", tc.to, tc.port); v.Answer != tc.want {
			t.Errorf("web → %s:%s = %s, want %s (%s)", tc.to, tc.port, v.Answer, tc.want, v.Egress.Reason)
		}
	}
	// A Cilium CIDR never selects a pod of the cluster, even one whose
	// address is in it: the pod is reached as "cluster".
	if v := verdictOf(t, h, "shop/web", "shop/api", "80"); v.Answer != "yes" || len(v.Egress.Caveats) != 0 {
		t.Errorf("web → api inside 10/8 is still cluster traffic: %+v", v.Egress)
	}
}

// What is not evaluated is said, and never answered as allow or deny.
func TestRulesThatAreNotEvaluatedAreSaid(t *testing.T) {
	h := ciliumShop(t)
	h.manifest(t, `
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata: {name: web-to-forge, namespace: shop}
spec:
  endpointSelector: {matchLabels: {app: web}}
  egress:
    - toFQDNs: [{matchName: forge.example.com}, {matchPattern: "*.forge.example.com"}]
      toPorts: [{ports: [{port: "443", protocol: TCP}]}]
`)
	h.manifest(t, `
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata: {name: api-http, namespace: shop}
spec:
  endpointSelector: {matchLabels: {app: api}}
  ingress:
    - fromEndpoints: [{matchLabels: {app: stranger}}]
      toPorts:
        - ports: [{port: "80", protocol: TCP}]
          rules:
            http: [{method: GET, path: "/health"}]
`)
	v := verdictOf(t, h, "shop/web", "198.51.100.7", "443")
	if v.Answer != "unevaluated" || v.Egress.State != sideUnevaluated || v.Egress.Decided == nil || v.Egress.Decided.Kind != "unevaluated" {
		t.Errorf("only a toFQDNs rule could allow it: %+v", v)
	}
	// The baseline allows the cluster at layer 4, which is enough: the
	// layer-7 rule is beside it, not in front of it.
	v = verdictOf(t, h, "shop/stranger", "shop/api", "80")
	if v.Ingress.State != sideAllowed {
		t.Errorf("the baseline's cluster allow stands: %+v", v.Ingress)
	}
	o := overviewOf(t, h, "")
	if strings.Join(o.Cilium.NotEvaluated, ";") != "layer-7 rules: 1;toFQDNs: 1" {
		t.Errorf("not evaluated: %v", o.Cilium.NotEvaluated)
	}
	g := o.Graph
	found := false
	for _, e := range g.Edges {
		if e.Kind == "unevaluated" && e.To == "unevaluated:toFQDNs" {
			found = true
		}
		if e.Kind == "allow" && e.To == "unevaluated:toFQDNs" {
			t.Errorf("a toFQDNs rule is drawn as an allow: %+v", e)
		}
	}
	if !found {
		t.Errorf("no not-evaluated edge to the FQDNs: %+v", g.Edges)
	}
}

// Only the layer-7 rule could allow it: not evaluated.
func TestALayer7RuleAloneIsNotEvaluated(t *testing.T) {
	h := shop(t)
	withCilium(h)
	h.manifest(t, `
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata: {name: api-http, namespace: shop}
spec:
  endpointSelector: {matchLabels: {app: api}}
  ingress:
    - fromEndpoints: [{matchLabels: {app: web}}]
      toPorts:
        - ports: [{port: "80", protocol: TCP}]
          rules:
            http: [{method: GET}]
`)
	if v := verdictOf(t, h, "shop/web", "shop/api", "80"); v.Answer != "unevaluated" {
		t.Errorf("web → api:80 rests on an HTTP rule: %+v", v.Ingress)
	}
	if v := verdictOf(t, h, "shop/stranger", "shop/api", "80"); v.Answer != "no" {
		t.Errorf("stranger is no peer of that rule: %+v", v.Ingress)
	}
}

// matchExpressions in endpointSelector, enableDefaultDeny off, a host
// policy, and specs.
func TestSelectorsDefaultDenyOffAndHostPolicies(t *testing.T) {
	h := shop(t)
	withCilium(h)
	h.manifest(t, `
apiVersion: cilium.io/v2
kind: CiliumClusterwideNetworkPolicy
metadata: {name: audit-dns}
spec:
  endpointSelector: {matchExpressions: [{key: app, operator: Exists}]}
  enableDefaultDeny: {egress: false}
  egress:
    - toEndpoints: [{matchLabels: {io.kubernetes.pod.namespace: ops}}]
`)
	h.manifest(t, `
apiVersion: cilium.io/v2
kind: CiliumClusterwideNetworkPolicy
metadata: {name: nodes}
spec:
  nodeSelector: {}
  ingress:
    - fromEntities: [cluster]
`)
	o := overviewOf(t, h, "")
	if o.Summary.IsolatedOut != 0 || o.Cilium.HostPolicies != 1 || o.Cilium.Policies != 1 {
		t.Errorf("enableDefaultDeny off isolates nothing, the host policy is counted: %+v %+v", o.Summary, o.Cilium)
	}
	if v := verdictOf(t, h, "shop/web", "198.51.100.7", "443"); v.Answer != "yes" || v.Egress.State != sideOpen {
		t.Errorf("open egress: %+v", v.Egress)
	}
}

func TestCiliumPoliciesThatCannotBeListedAreANote(t *testing.T) {
	h := shop(t)
	withCilium(h)
	h.listErr["ciliumnetworkpolicies"] = fmt.Errorf("list: %w", extension.ErrDenied)
	o := overviewOf(t, h, "")
	if o.Complete || !strings.Contains(strings.Join(o.Notes, " "), "CiliumNetworkPolicies") {
		t.Errorf("a refused Cilium listing makes the answer partial: %v %v", o.Complete, o.Notes)
	}
	if w := serve(t, h, "/x/kind/netinspect/"); w.Code != http.StatusOK {
		t.Errorf("and does not refuse the page: %d", w.Code)
	}
}

// The pod's panel names each rule's kind.
func TestAPodNamesCiliumRules(t *testing.T) {
	h := ciliumShop(t)
	a := podOf(t, h, "shop", "web")
	if !a.Ingress.Isolated || !a.Egress.Isolated {
		t.Fatalf("web: %+v", a)
	}
	kinds := map[string]bool{}
	for _, r := range a.Egress.Rules {
		for _, p := range r.Peers {
			kinds[p.Kind] = true
		}
	}
	if !kinds["entity"] || !kinds["cidr"] || !kinds["pods"] {
		t.Errorf("egress peers: %+v", a.Egress.Rules)
	}
}
