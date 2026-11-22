package netinspect

import (
	"strings"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/thonicdev/kubetower/extension"
)

// withCalico serves Calico's three kinds, as crd.projectcalico.org does.
func withCalico(h *fakeHost) {
	h.crds = append(h.crds,
		extension.CRD{Group: "crd.projectcalico.org", Version: "v1", Plural: "networkpolicies", Kind: "NetworkPolicy", Namespaced: true},
		extension.CRD{Group: "crd.projectcalico.org", Version: "v1", Plural: "globalnetworkpolicies", Kind: "GlobalNetworkPolicy"},
		extension.CRD{Group: "crd.projectcalico.org", Version: "v1", Plural: "tiers", Kind: "Tier"},
	)
}

func withAdmin(h *fakeHost) {
	h.crds = append(h.crds,
		extension.CRD{Group: "policy.networking.k8s.io", Version: "v1alpha1", Plural: "adminnetworkpolicies", Kind: "AdminNetworkPolicy"},
		extension.CRD{Group: "policy.networking.k8s.io", Version: "v1alpha1", Plural: "baselineadminnetworkpolicies", Kind: "BaselineAdminNetworkPolicy"},
	)
}

// obj adds one Calico or AdminNetworkPolicy object written as YAML.
func (h *fakeHost) obj(t *testing.T, text string) {
	t.Helper()
	var o map[string]any
	if err := yaml.Unmarshal([]byte(text), &o); err != nil {
		t.Fatal(err)
	}
	u := unstructured.Unstructured{Object: o}
	key := map[string]string{
		"NetworkPolicy":              "calico.networkpolicies",
		"GlobalNetworkPolicy":        "calico.globalnetworkpolicies",
		"Tier":                       "calico.tiers",
		"AdminNetworkPolicy":         "adminnetworkpolicies",
		"BaselineAdminNetworkPolicy": "baselineadminnetworkpolicies",
	}[u.GetKind()]
	if key == "" {
		t.Fatalf("no key for %s", u.GetKind())
	}
	h.objects[key] = append(h.objects[key], u)
}

func calicoShop(t *testing.T, objects ...string) *fakeHost {
	h := shop(t)
	withCalico(h)
	for _, o := range objects {
		h.obj(t, o)
	}
	return h
}

type askedFlow struct{ from, to, port, want string }

func check(t *testing.T, h *fakeHost, flows []askedFlow) {
	t.Helper()
	for _, f := range flows {
		if v := verdictOf(t, h, f.from, f.to, f.port); v.Answer != f.want {
			t.Errorf("%s -> %s:%s: %s, want %s\negress:  %s\ningress: %s", f.from, f.to, f.port, v.Answer, f.want, v.Egress.Reason, v.Ingress.Reason)
		}
	}
}

func gnp(name, spec string) string {
	return "apiVersion: crd.projectcalico.org/v1\nkind: GlobalNetworkPolicy\nmetadata: {name: " + name + "}\nspec:\n" + indent(spec)
}

func cnp(ns, name, spec string) string {
	return "apiVersion: crd.projectcalico.org/v1\nkind: NetworkPolicy\nmetadata: {name: " + name + ", namespace: " + ns + "}\nspec:\n" + indent(spec)
}

func indent(s string) string {
	lines := strings.Split(strings.Trim(s, "\n"), "\n")
	for i := range lines {
		lines[i] = "  " + lines[i]
	}
	return strings.Join(lines, "\n")
}

const denyWebToAPI = `
order: 10
selector: app == 'api'
types: [Ingress]
ingress:
  - action: Deny
    source: {selector: app == 'web'}
  - action: Allow
`

func TestACalicoDenyWinsAndTheRestFallsToTheNextRule(t *testing.T) {
	h := calicoShop(t, gnp("deny-web", denyWebToAPI))
	check(t, h, []askedFlow{
		{"shop/web", "shop/api", "80", "no"},
		{"shop/stranger", "shop/api", "80", "yes"}, // the Allow rule
		{"ops/probe", "shop/api", "80", "yes"},
		{"shop/web", "shop/stranger", "80", "yes"}, // not selected: Calico's default profile allows
	})
	v := verdictOf(t, h, "shop/web", "shop/api", "80")
	if d := v.Ingress.Decided; d == nil || d.Kind != "deny" || d.Rule != 1 || d.Name != "deny-web" || d.PolicyKind != "Calico GlobalNetworkPolicy" {
		t.Errorf("decided: %+v", d)
	}
	if !strings.Contains(v.Ingress.Reason, "Denied by Calico GlobalNetworkPolicy deny-web, ingress rule 1 (tier default)") {
		t.Errorf("the reason names the policy, rule and tier: %q", v.Ingress.Reason)
	}
	if len(v.Ingress.Isolating) != 1 || v.Ingress.Isolating[0].Name != "deny-web" {
		t.Errorf("isolating: %+v", v.Ingress.Isolating)
	}
	if v.Policy != "" {
		t.Errorf("no allow policy is offered for a deny rule: %q", v.Policy)
	}
	// What Calico evaluates is no longer listed as not evaluated.
	if len(v.Unevaluated) != 0 {
		t.Errorf("unevaluated: %+v", v.Unevaluated)
	}
}

func TestACalicoPolicyThatSelectsAPodEndsItsTierWithADeny(t *testing.T) {
	h := calicoShop(t, gnp("api-from-web", `
selector: app == 'api'
ingress:
  - action: Allow
    source: {selector: app == 'web'}
`))
	check(t, h, []askedFlow{
		{"shop/web", "shop/api", "80", "yes"},
		{"shop/stranger", "shop/api", "80", "no"},
		{"shop/api", "shop/web", "80", "yes"}, // types absent and no egress rule: ingress only
	})
	v := verdictOf(t, h, "shop/stranger", "shop/api", "80")
	if !strings.Contains(v.Ingress.Reason, "end of tier default") || v.Ingress.Decided != nil {
		t.Errorf("%q %+v", v.Ingress.Reason, v.Ingress.Decided)
	}
	if v.Policy == "" {
		t.Error("an end-of-tier deny in the default tier is lifted by an allow policy: one is offered")
	}
}

func TestCalicoTypesAreIngressAndEgressWhenEgressRulesExist(t *testing.T) {
	h := calicoShop(t,
		gnp("both", "selector: app == 'web'\ningress:\n  - action: Allow\negress:\n  - action: Allow\n    destination: {selector: app == 'api'}\n"),
		gnp("ingress-only", "selector: app == 'stranger'\ntypes: [Ingress]\ningress:\n  - action: Deny\negress:\n  - action: Deny\n"))
	check(t, h, []askedFlow{
		{"shop/web", "shop/api", "80", "yes"},
		{"shop/web", "shop/stranger", "80", "no"},  // web's egress is isolated: only api; stranger's ingress is denied too
		{"shop/stranger", "shop/api", "80", "yes"}, // types lists Ingress only: its egress rule is not evaluated
		{"ops/probe", "shop/web", "80", "yes"},     // web's ingress: its Allow has no source, so everyone
	})
}

func TestCalicoOrderRunsLowFirstAndAnAbsentOrderRunsLast(t *testing.T) {
	deny := func(name, order string) string {
		return gnp(name, order+"selector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Deny\n")
	}
	allow := func(name, order string) string {
		return gnp(name, order+"selector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Allow\n")
	}
	for _, tc := range []struct {
		name  string
		pols  []string
		want  string
		decid string
	}{
		{"low deny first", []string{allow("a", "order: 20\n"), deny("d", "order: 10\n")}, "no", "d"},
		{"low allow first", []string{allow("a", "order: 10\n"), deny("d", "order: 20\n")}, "yes", "a"},
		{"absent order is last", []string{deny("d", ""), allow("a", "order: 1000000\n")}, "yes", "a"},
		{"equal orders go by name", []string{deny("b", "order: 5\n"), allow("a", "order: 5\n")}, "yes", "a"},
		{"fractions", []string{allow("a", "order: 1.5\n"), deny("d", "order: 1.25\n")}, "no", "d"},
	} {
		h := calicoShop(t, tc.pols...)
		v := verdictOf(t, h, "shop/web", "shop/api", "80")
		if v.Answer != tc.want || v.Ingress.Decided == nil || v.Ingress.Decided.Name != tc.decid {
			t.Errorf("%s: %s %+v", tc.name, v.Answer, v.Ingress.Decided)
		}
	}
}

func TestLogDoesNothingAndPassLeavesTheTier(t *testing.T) {
	// Log is skipped: the Deny after it decides, and its number counts the Log.
	h := calicoShop(t, gnp("log-then-deny", "selector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Log\n  - action: Deny\n"))
	v := verdictOf(t, h, "shop/web", "shop/api", "80")
	if v.Answer != "no" || v.Ingress.Decided == nil || v.Ingress.Decided.Rule != 2 {
		t.Errorf("%s %+v", v.Answer, v.Ingress.Decided)
	}
	// Pass hands the askedFlow to the next tier, past the tier's own later rules.
	h = calicoShop(t,
		"apiVersion: crd.projectcalico.org/v1\nkind: Tier\nmetadata: {name: security}\nspec: {order: 100}\n",
		gnp("security.pass-web", "tier: security\norder: 1\nselector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Pass\n    source: {selector: app == 'web'}\n  - action: Deny\n"),
		gnp("allow-all-ingress", "selector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Allow\n"))
	check(t, h, []askedFlow{
		{"shop/web", "shop/api", "80", "yes"},     // passed, then the default tier allows
		{"shop/stranger", "shop/api", "80", "no"}, // the security tier's Deny
		{"ops/probe", "shop/api", "80", "no"},
	})
	v = verdictOf(t, h, "shop/web", "shop/api", "80")
	if !strings.Contains(v.Ingress.Reason, "Passed by Calico GlobalNetworkPolicy security.pass-web to the next tier") ||
		!strings.Contains(v.Ingress.Reason, "tier default") {
		t.Errorf("the trail says where the askedFlow went: %q", v.Ingress.Reason)
	}
}

func TestACalicoPassIntoADefaultDenyIsRefused(t *testing.T) {
	h := calicoShop(t,
		"apiVersion: crd.projectcalico.org/v1\nkind: Tier\nmetadata: {name: security}\nspec: {order: 100}\n",
		gnp("security.pass-web", "tier: security\nselector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Pass\n    source: {selector: app == 'web'}\n"),
		gnp("default-deny", "order: 10\nselector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Deny\n    source: {selector: app == 'web'}\n"))
	check(t, h, []askedFlow{{"shop/web", "shop/api", "80", "no"}})
}

func TestTiersRunInTheirOrderAndAskTheirDefaultAction(t *testing.T) {
	tier := func(name, spec string) string {
		return "apiVersion: crd.projectcalico.org/v1\nkind: Tier\nmetadata: {name: " + name + "}\nspec: " + spec + "\n"
	}
	// A tier whose defaultAction is Pass does not end the walk when nothing matches.
	h := calicoShop(t,
		tier("platform", "{order: 50, defaultAction: Pass}"),
		gnp("platform.only-probe", "tier: platform\nselector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Allow\n    source: {selector: app == 'probe'}\n"),
		gnp("allow-web", "selector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Allow\n    source: {selector: app == 'web'}\n"))
	check(t, h, []askedFlow{
		{"ops/probe", "shop/api", "80", "yes"},
		{"shop/web", "shop/api", "80", "yes"},     // platform passes (defaultAction), default allows
		{"shop/stranger", "shop/api", "80", "no"}, // the default tier ends in a deny
	})
	// The same tier with the default defaultAction (Deny) ends it.
	h = calicoShop(t,
		tier("platform", "{order: 50}"),
		gnp("platform.only-probe", "tier: platform\nselector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Allow\n    source: {selector: app == 'probe'}\n"),
		gnp("allow-web", "selector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Allow\n    source: {selector: app == 'web'}\n"))
	v := verdictOf(t, h, "shop/web", "shop/api", "80")
	if v.Answer != "no" || !strings.Contains(v.Ingress.Reason, "end of tier platform") {
		t.Errorf("%s %q", v.Answer, v.Ingress.Reason)
	}
	if v.Policy != "" {
		t.Errorf("a K8s NetworkPolicy would not lift a refusal of another tier: %q", v.Policy)
	}
	// A tier with a lower order runs first, whatever its name sorts as.
	h = calicoShop(t,
		tier("zzz", "{order: 1}"), tier("aaa", "{order: 2}"),
		gnp("zzz.deny", "tier: zzz\nselector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Deny\n"),
		gnp("aaa.allow", "tier: aaa\nselector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Allow\n"))
	check(t, h, []askedFlow{{"shop/web", "shop/api", "80", "no"}})
}

func TestEveryTierPassingLeavesCalicosDefaultProfile(t *testing.T) {
	h := calicoShop(t, gnp("pass-all", "selector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Pass\n"))
	v := verdictOf(t, h, "shop/web", "shop/api", "80")
	if v.Answer != "yes" || !strings.Contains(v.Ingress.Reason, "default profile allows") {
		t.Errorf("%s %q", v.Answer, v.Ingress.Reason)
	}
}

func TestCalicoNetsAndNotNetsAreCheckedOnAddresses(t *testing.T) {
	h := calicoShop(t, gnp("egress-sites", `
selector: app == 'web'
types: [Egress]
egress:
  - action: Deny
    destination: {nets: ["192.0.2.0/24"], notNets: ["192.0.2.128/25"]}
  - action: Deny
    destination: {nets: ["198.51.100.7"]}
  - action: Allow
`))
	check(t, h, []askedFlow{
		{"shop/web", "192.0.2.5", "443", "no"},
		{"shop/web", "192.0.2.200", "443", "yes"}, // in notNets
		{"shop/web", "198.51.100.7", "443", "no"}, // a bare address is a /32
		{"shop/web", "198.51.100.8", "443", "yes"},
		{"shop/web", "shop/api", "80", "yes"},
	})
	// An address of a pod is matched like any other: Calico sees the packet.
	h = calicoShop(t, gnp("deny-api-ip", "selector: app == 'web'\ntypes: [Egress]\negress:\n  - action: Deny\n    destination: {nets: [\"10.0.0.10/32\"]}\n  - action: Allow\n"))
	check(t, h, []askedFlow{{"shop/web", "shop/api", "80", "no"}, {"shop/web", "shop/stranger", "80", "yes"}})
}

func TestCalicoPortsNotPortsNamedPortsAndProtocols(t *testing.T) {
	spec := func(rule string) string {
		return "selector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Allow\n" + rule + "  - action: Deny\n"
	}
	for _, tc := range []struct {
		name string
		rule string
		port string
		want string
	}{
		{"number", "    protocol: TCP\n    destination: {ports: [80]}\n", "80", "yes"},
		{"another number", "    protocol: TCP\n    destination: {ports: [80]}\n", "81", "no"},
		{"range", "    protocol: TCP\n    destination: {ports: ['8000:8100']}\n", "8050", "yes"},
		{"outside the range", "    protocol: TCP\n    destination: {ports: ['8000:8100']}\n", "8101", "no"},
		{"named", "    protocol: TCP\n    destination: {ports: [http]}\n", "http", "yes"},
		{"named, another port", "    protocol: TCP\n    destination: {ports: [http]}\n", "8080", "no"},
		{"notPorts keeps the others", "    protocol: TCP\n    destination: {notPorts: [80]}\n", "81", "yes"},
		{"notPorts refuses its own", "    protocol: TCP\n    destination: {notPorts: [80]}\n", "80", "no"},
		{"protocol UDP is not TCP", "    protocol: UDP\n", "80", "no"},
		{"protocol by number", "    protocol: 6\n", "80", "yes"},
		{"notProtocol", "    notProtocol: UDP\n", "80", "yes"},
		{"notProtocol TCP", "    notProtocol: TCP\n", "80", "no"},
		{"ICMP is not a TCP askedFlow", "    protocol: ICMP\n", "80", "no"},
	} {
		h := calicoShop(t, gnp("p", spec(tc.rule)))
		if v := verdictOf(t, h, "shop/web", "shop/api", tc.port); v.Answer != tc.want {
			t.Errorf("%s: %s, want %s (%s)", tc.name, v.Answer, tc.want, v.Ingress.Reason)
		}
	}
}

func TestACalicoRuleSelectorStaysInTheNamespaceOfANamespacedPolicy(t *testing.T) {
	apiFrom := func(source string) string {
		return cnp("shop", "api-in", "selector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Allow\n    source: "+source+"\n")
	}
	// ops/probe is in another namespace: a selector alone does not reach it...
	h := calicoShop(t, apiFrom("{selector: app == 'probe'}"))
	check(t, h, []askedFlow{{"ops/probe", "shop/api", "80", "no"}})
	// ...a namespaceSelector does...
	h = calicoShop(t, apiFrom("{selector: app == 'probe', namespaceSelector: team == 'ops'}"))
	check(t, h, []askedFlow{{"ops/probe", "shop/api", "80", "yes"}, {"shop/web", "shop/api", "80", "no"}})
	// ...and a GlobalNetworkPolicy's selector spans every namespace.
	h = calicoShop(t, gnp("global", "selector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Allow\n    source: {selector: app == 'probe'}\n"))
	check(t, h, []askedFlow{{"ops/probe", "shop/api", "80", "yes"}})
	// A namespaced policy selects its own namespace only.
	h = calicoShop(t, cnp("ops", "ops-only", "selector: all()\ntypes: [Ingress]\ningress: []\n"))
	check(t, h, []askedFlow{{"shop/web", "shop/api", "80", "yes"}, {"shop/web", "ops/probe", "80", "no"}})
	// The endpoint's namespace is a label too.
	h = calicoShop(t, gnp("by-ns-label", "selector: projectcalico.org/namespace == 'shop' && app == 'api'\ntypes: [Ingress]\ningress: []\n"))
	check(t, h, []askedFlow{{"shop/web", "shop/api", "80", "no"}, {"shop/web", "shop/stranger", "80", "yes"}})
	// A GlobalNetworkPolicy's namespaceSelector chooses the namespaces it selects in.
	h = calicoShop(t, gnp("ops-ns", "namespaceSelector: team == 'ops'\ntypes: [Ingress]\ningress: []\n"))
	check(t, h, []askedFlow{{"shop/web", "ops/probe", "80", "no"}, {"ops/probe", "shop/api", "80", "yes"}})
}

func TestNotSelectorAndServiceAccountNamesNarrowAnEntity(t *testing.T) {
	h := calicoShop(t, gnp("not-web", "selector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Allow\n    source: {selector: all(), notSelector: app == 'web'}\n"))
	check(t, h, []askedFlow{{"shop/web", "shop/api", "80", "no"}, {"shop/stranger", "shop/api", "80", "yes"}})
	// A pod whose service account is not known cannot be told from one that matches.
	h = calicoShop(t, gnp("sa", "selector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Allow\n    source: {serviceAccounts: {names: [builder]}}\n"))
	check(t, h, []askedFlow{{"shop/web", "shop/api", "80", "unevaluated"}})
}

func TestKubernetesNetworkPoliciesAreInTheDefaultTierAtOrder1000(t *testing.T) {
	// api-from-web allows web to api's http port, as a NetworkPolicy.
	apiFromWebNP := func(h *fakeHost) { h.policy(t, apiFromWeb()) }
	denyAll := gnp("deny-all-api", "order: %s\nselector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Deny\n")
	// A deny at order 2000 runs after the NetworkPolicy's allow.
	h := calicoShop(t, strings.Replace(denyAll, "%s", "2000", 1))
	apiFromWebNP(h)
	check(t, h, []askedFlow{{"shop/web", "shop/api", "80", "yes"}, {"shop/stranger", "shop/api", "80", "no"}})
	v := verdictOf(t, h, "shop/web", "shop/api", "80")
	if v.Ingress.Decided == nil || v.Ingress.Decided.Name != "api-from-web" || v.Ingress.Decided.PolicyKind != "" {
		t.Errorf("the NetworkPolicy decides: %+v", v.Ingress.Decided)
	}
	// At order 500 it runs first.
	h = calicoShop(t, strings.Replace(denyAll, "%s", "500", 1))
	apiFromWebNP(h)
	check(t, h, []askedFlow{{"shop/web", "shop/api", "80", "no"}})
	// A NetworkPolicy alone, with Calico's policies selecting something else,
	// is still the NetworkPolicy layer: the old answers hold.
	h = calicoShop(t, gnp("elsewhere", "selector: app == 'probe'\ntypes: [Ingress]\ningress: []\n"))
	h.policy(t, defaultDenyIngress("shop"))
	h.policy(t, apiFromWeb())
	check(t, h, []askedFlow{{"shop/web", "shop/api", "80", "yes"}, {"shop/stranger", "shop/api", "80", "no"}})
	// The K8s default deny and a Calico allow in the same tier: the Calico
	// policy at a lower order allows what the NetworkPolicy would not.
	h = calicoShop(t, gnp("stranger-ok", "order: 10\nselector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Allow\n    source: {selector: app == 'stranger'}\n"))
	h.policy(t, defaultDenyIngress("shop"))
	h.policy(t, apiFromWeb())
	check(t, h, []askedFlow{{"shop/stranger", "shop/api", "80", "yes"}, {"shop/web", "shop/api", "80", "yes"}, {"ops/probe", "shop/api", "80", "no"}})
	_ = networkingv1.PolicyTypeIngress
}

func TestWhatCalicoCannotEvaluateIsNotEvaluatedNeverAllowedOrDenied(t *testing.T) {
	for _, tc := range []struct {
		name      string
		obj       string
		askedFlow askedFlow
	}{
		{"an unparseable rule selector",
			gnp("bad", "selector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Allow\n    source: {selector: \"app ==\"}\n"),
			askedFlow{"shop/web", "shop/api", "80", "unevaluated"}},
		{"an http rule",
			gnp("http", "selector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Allow\n    protocol: TCP\n    http: {methods: [GET]}\n"),
			askedFlow{"shop/web", "shop/api", "80", "unevaluated"}},
		{"a services rule",
			gnp("svc", "selector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Deny\n    destination: {services: {name: x, namespace: y}}\n"),
			askedFlow{"shop/web", "shop/api", "80", "unevaluated"}},
		{"an ipVersion rule",
			gnp("ipv", "selector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Allow\n    ipVersion: 4\n"),
			askedFlow{"shop/web", "shop/api", "80", "unevaluated"}},
		{"source ports",
			gnp("sp", "selector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Allow\n    protocol: TCP\n    source: {ports: [1234]}\n"),
			askedFlow{"shop/web", "shop/api", "80", "unevaluated"}},
		// A selector over an address outside every pod could be a NetworkSet.
		{"a network set",
			gnp("ns", "selector: app == 'web'\ntypes: [Egress]\negress:\n  - action: Allow\n    destination: {selector: role == 'external'}\n"),
			askedFlow{"shop/web", "192.0.2.9", "443", "unevaluated"}},
		// An http rule that cannot match the port is still no match.
		{"an http rule on another protocol",
			gnp("udp", "selector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Allow\n    protocol: UDP\n    http: {methods: [GET]}\n"),
			askedFlow{"shop/web", "shop/api", "80", "no"}},
	} {
		h := calicoShop(t, tc.obj)
		check(t, h, []askedFlow{tc.askedFlow})
	}
	// A policy whose own selector cannot be read selects nothing and says so.
	h := calicoShop(t, gnp("unreadable", "selector: \"app ==\"\ntypes: [Ingress]\ningress:\n  - action: Deny\n"))
	v := verdictOf(t, h, "shop/web", "shop/api", "80")
	if v.Answer != "yes" || len(v.Ingress.Caveats) == 0 || !strings.Contains(v.Ingress.Caveats[0], "not evaluated") {
		t.Errorf("%s %v", v.Answer, v.Ingress.Caveats)
	}
	o := overviewOf(t, h, "")
	if len(o.Policies) != 1 || len(o.Policies[0].Broken) == 0 {
		t.Errorf("the page says it is broken: %+v", o.Policies)
	}
	// A policy about the host's traffic is not about pods' flows.
	h = calicoShop(t, gnp("host", "selector: all()\ntypes: [Ingress]\napplyOnForward: true\ningress:\n  - action: Deny\n"))
	check(t, h, []askedFlow{{"shop/web", "shop/api", "80", "yes"}})
}

func TestTheRoutesListEvaluatedCalicoPoliciesAndNoUnevaluatedEntry(t *testing.T) {
	h := calicoShop(t,
		gnp("deny-web", denyWebToAPI),
		cnp("shop", "stranger-in", "selector: app == 'stranger'\ntypes: [Ingress]\ningress:\n  - action: Allow\n"))
	o := overviewOf(t, h, "")
	if len(o.Unevaluated) != 0 {
		t.Errorf("unevaluated: %+v", o.Unevaluated)
	}
	kinds := map[string]PolicyRow{}
	for _, p := range o.Policies {
		kinds[p.Kind] = p
	}
	g, n := kinds["Calico GlobalNetworkPolicy"], kinds["Calico NetworkPolicy"]
	if g.Name != "deny-web" || g.Selects != 1 || len(g.Types) != 1 || g.Types[0] != "Ingress" || len(g.Ingress) != 2 {
		t.Errorf("the global policy: %+v", g)
	}
	if n.Name != "stranger-in" || n.Namespace != "shop" || n.Selects != 1 {
		t.Errorf("the namespaced policy: %+v", n)
	}
	if !o.Calico.Served || o.Calico.Policies != 2 {
		t.Errorf("calico: %+v", o.Calico)
	}
	pa := podOf(t, h, "shop", "api")
	if !pa.Ingress.Isolated || len(pa.Ingress.Policies) != 1 || pa.Ingress.Policies[0].PolicyKind != "Calico GlobalNetworkPolicy" {
		t.Errorf("the pod's panel lists it: %+v", pa.Ingress)
	}
	if len(pa.Ingress.Rules) != 2 || pa.Ingress.Rules[0].Kind != "deny" || pa.Ingress.Rules[1].Kind != "allow" {
		t.Errorf("rules: %+v", pa.Ingress.Rules)
	}
	// A kind that cannot be listed stays unevaluated, and the page is partial.
	h.listErr["calico.globalnetworkpolicies"] = extension.ErrDenied
	o = overviewOf(t, h, "")
	if o.Complete || len(o.Unevaluated) != 1 || o.Unevaluated[0].Kind != "Calico GlobalNetworkPolicy" || o.Unevaluated[0].Count != "" {
		t.Errorf("a refused listing: complete=%v %+v", o.Complete, o.Unevaluated)
	}
}

func TestAPassRuleDrawsNoEdgeAndALogRuleDoesNotExist(t *testing.T) {
	h := calicoShop(t, gnp("pass", "selector: app == 'api'\ntypes: [Ingress]\ningress:\n  - action: Pass\n    source: {selector: app == 'web'}\n  - action: Log\n"))
	o := overviewOf(t, h, "")
	if len(o.Graph.Edges) != 0 {
		t.Errorf("edges: %+v", o.Graph.Edges)
	}
	pa := podOf(t, h, "shop", "api")
	if len(pa.Ingress.Rules) != 1 || pa.Ingress.Rules[0].Kind != "pass" {
		t.Errorf("%+v", pa.Ingress.Rules)
	}
}
