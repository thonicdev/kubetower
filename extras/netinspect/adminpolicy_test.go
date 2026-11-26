package netinspect

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
)

func anp(name string, priority int, spec string) string {
	return "apiVersion: policy.networking.k8s.io/v1alpha1\nkind: AdminNetworkPolicy\nmetadata: {name: " + name + "}\nspec:\n  priority: " +
		strconv.Itoa(priority) + "\n" + indent(spec)
}

func banp(spec string) string {
	return "apiVersion: policy.networking.k8s.io/v1alpha1\nkind: BaselineAdminNetworkPolicy\nmetadata: {name: default}\nspec:\n" + indent(spec)
}

func adminShop(t *testing.T, objects ...string) *fakeHost {
	h := shop(t)
	withAdmin(h)
	for _, o := range objects {
		h.obj(t, o)
	}
	return h
}

const apiSubject = "subject:\n  pods:\n    namespaceSelector: {matchLabels: {team: shop}}\n    podSelector: {matchLabels: {app: api}}\n"

func fromWeb(action string) string {
	return apiSubject + "ingress:\n  - name: web\n    action: " + action + "\n    from:\n      - pods:\n          namespaceSelector: {matchLabels: {team: shop}}\n          podSelector: {matchLabels: {app: web}}\n"
}

func TestAnAdminNetworkPolicyDecidesBeforeNetworkPolicies(t *testing.T) {
	// api-from-web would allow web; a Deny of higher precedence refuses it.
	h := adminShop(t, anp("no-web", 10, fromWeb("Deny")))
	h.policy(t, apiFromWeb())
	check(t, h, []askedFlow{
		{"shop/web", "shop/api", "80", "no"},
		{"shop/stranger", "shop/api", "80", "no"}, // no rule matches: api-from-web isolates it
	})
}

func TestAnAdminAllowOverridesANetworkPolicyDefaultDeny(t *testing.T) {
	h := adminShop(t, anp("allow-web", 10, fromWeb("Allow")))
	h.policy(t, defaultDenyIngress("shop"))
	check(t, h, []askedFlow{
		{"shop/web", "shop/api", "80", "yes"},
		{"shop/stranger", "shop/api", "80", "no"}, // the NetworkPolicy's own default deny
	})
	v := verdictOf(t, h, "shop/web", "shop/api", "80")
	if d := v.Ingress.Decided; d == nil || d.Kind != "allow" || d.Name != "allow-web" || d.PolicyKind != "AdminNetworkPolicy" || d.Rule != 1 {
		t.Errorf("decided: %+v", d)
	}
	if !strings.Contains(v.Ingress.Reason, "Allowed by AdminNetworkPolicy allow-web, ingress rule 1") {
		t.Errorf("reason: %q", v.Ingress.Reason)
	}
	if len(v.Unevaluated) != 0 {
		t.Errorf("unevaluated: %+v", v.Unevaluated)
	}
}

func TestAnAdminPassHandsTheFlowToTheNetworkPolicies(t *testing.T) {
	h := adminShop(t, anp("pass-web", 10, fromWeb("Pass")))
	h.policy(t, defaultDenyIngress("shop"))
	check(t, h, []askedFlow{{"shop/web", "shop/api", "80", "no"}})
	h.policy(t, apiFromWeb())
	check(t, h, []askedFlow{{"shop/web", "shop/api", "80", "yes"}})
	// A Pass leaves the lower-precedence AdminNetworkPolicies out of it.
	h = adminShop(t, anp("pass-web", 10, fromWeb("Pass")), anp("deny-web", 20, fromWeb("Deny")))
	check(t, h, []askedFlow{{"shop/web", "shop/api", "80", "yes"}})
}

func TestAdminPriorityOrdersPoliciesAndRulesGoInOrder(t *testing.T) {
	h := adminShop(t, anp("late-deny", 50, fromWeb("Deny")), anp("early-allow", 5, fromWeb("Allow")))
	check(t, h, []askedFlow{{"shop/web", "shop/api", "80", "yes"}})
	h = adminShop(t, anp("late-allow", 50, fromWeb("Allow")), anp("early-deny", 5, fromWeb("Deny")))
	check(t, h, []askedFlow{{"shop/web", "shop/api", "80", "no"}})
	// Rules of one policy: the first that matches.
	h = adminShop(t, anp("two", 1, apiSubject+`ingress:
  - action: Deny
    from: [{pods: {namespaceSelector: {matchLabels: {team: shop}}, podSelector: {matchLabels: {app: web}}}}]
  - action: Allow
    from: [{namespaces: {matchLabels: {team: shop}}}]
`))
	check(t, h, []askedFlow{{"shop/web", "shop/api", "80", "no"}, {"shop/stranger", "shop/api", "80", "yes"}, {"ops/probe", "shop/api", "80", "yes"}})
	v := verdictOf(t, h, "shop/web", "shop/api", "80")
	if v.Ingress.Decided == nil || v.Ingress.Decided.Rule != 1 {
		t.Errorf("%+v", v.Ingress.Decided)
	}
}

func TestEqualAdminPrioritiesAreOrderedByNameAndSaySo(t *testing.T) {
	h := adminShop(t, anp("b-deny", 7, fromWeb("Deny")), anp("a-allow", 7, fromWeb("Allow")))
	v := verdictOf(t, h, "shop/web", "shop/api", "80")
	if v.Answer != "yes" || v.Ingress.Decided.Name != "a-allow" {
		t.Errorf("%s %+v", v.Answer, v.Ingress.Decided)
	}
	if len(v.Ingress.Caveats) != 1 || !strings.Contains(v.Ingress.Caveats[0], "share priority 7") {
		t.Errorf("caveats: %v", v.Ingress.Caveats)
	}
}

func TestABaselineAppliesOnlyWhereNoNetworkPolicySelectsThePod(t *testing.T) {
	baseline := banp("subject:\n  namespaces: {matchLabels: {team: shop}}\ningress:\n  - action: Deny\n    from: [{namespaces: {matchLabels: {team: ops}}}]\n  - action: Allow\n    from: [{namespaces: {matchLabels: {team: shop}}}]\n")
	h := adminShop(t, baseline)
	check(t, h, []askedFlow{
		{"ops/probe", "shop/api", "80", "no"},
		{"shop/web", "shop/api", "80", "yes"},
	})
	v := verdictOf(t, h, "ops/probe", "shop/api", "80")
	if d := v.Ingress.Decided; d == nil || d.PolicyKind != "BaselineAdminNetworkPolicy" || d.Name != "default" {
		t.Errorf("%+v", d)
	}
	// A NetworkPolicy that selects api takes over entirely: the baseline is not consulted.
	h = adminShop(t, baseline)
	h.policy(t, apiFromWeb())
	check(t, h, []askedFlow{{"shop/web", "shop/api", "80", "yes"}, {"ops/probe", "shop/api", "80", "no"}, {"shop/stranger", "shop/api", "80", "no"}})
}

func TestTheFullOrderIsAdminThenNetworkPoliciesThenBaseline(t *testing.T) {
	// ANP denies web; the NP would allow stranger; the baseline would deny
	// everyone: ANP decides web, the NP decides the rest.
	h := adminShop(t,
		anp("no-web", 10, fromWeb("Deny")),
		banp("subject:\n  namespaces: {matchLabels: {team: shop}}\ningress:\n  - action: Deny\n    from: [{namespaces: {}}]\n"))
	check(t, h, []askedFlow{
		{"shop/web", "shop/api", "80", "no"},
		{"shop/stranger", "shop/api", "80", "no"}, // no NetworkPolicy selects api: the baseline denies
	})
	h.policy(t, apiFromWeb())
	check(t, h, []askedFlow{
		{"shop/web", "shop/api", "80", "no"},      // the ANP, before the NP that allows it
		{"shop/stranger", "shop/api", "80", "no"}, // the NP's isolation, never the baseline
	})
	v := verdictOf(t, h, "shop/stranger", "shop/api", "80")
	if v.Ingress.Decided != nil && v.Ingress.Decided.PolicyKind != "" {
		t.Errorf("the baseline must not decide: %+v", v.Ingress.Decided)
	}
}

func TestAdminEgressAndPorts(t *testing.T) {
	h := adminShop(t, anp("web-egress", 10, `
subject:
  pods:
    namespaceSelector: {matchLabels: {team: shop}}
    podSelector: {matchLabels: {app: web}}
egress:
  - action: Allow
    to: [{pods: {namespaceSelector: {matchLabels: {team: shop}}, podSelector: {matchLabels: {app: api}}}}]
    ports:
      - portNumber: {protocol: TCP, port: 80}
      - portRange: {protocol: TCP, start: 8000, end: 8100}
  - action: Deny
    to: [{namespaces: {matchLabels: {team: ops}}}]
    ports:
      - namedPort: metrics
`))
	h.pod(t, "ops", "metrics", "10.0.1.20", map[string]string{"app": "m"}, corev1.ContainerPort{Name: "metrics", ContainerPort: 9100})
	check(t, h, []askedFlow{
		{"shop/web", "shop/api", "80", "yes"},
		{"shop/web", "shop/api", "8050", "yes"},
		{"shop/web", "shop/api", "8101", "yes"},      // no rule: nothing else decides, default allow
		{"shop/web", "ops/metrics", "metrics", "no"}, // a named port, resolved on the destination
		{"shop/web", "ops/probe", "80", "yes"},
	})
}

func TestAdminNetworksAndNodesPeersAreNotEvaluated(t *testing.T) {
	h := adminShop(t, anp("to-net", 10, `
subject: {namespaces: {matchLabels: {team: shop}}}
egress:
  - action: Deny
    to: [{networks: ["192.0.2.0/24"]}]
    ports: [{portNumber: {protocol: TCP, port: 443}}]
`))
	check(t, h, []askedFlow{
		{"shop/web", "192.0.2.5", "443", "unevaluated"},
		{"shop/web", "192.0.2.5", "80", "yes"}, // another port: the rule cannot apply
	})
	o := overviewOf(t, h, "")
	if len(o.Admin.NotEvaluated) != 1 || o.Admin.NotEvaluated[0] != "networks: 1" || !o.Admin.Served || o.Admin.Policies != 1 {
		t.Errorf("admin: %+v", o.Admin)
	}
	if len(o.Unevaluated) != 0 {
		t.Errorf("unevaluated: %+v", o.Unevaluated)
	}
}

func TestAdminPoliciesAreListedAndDrawn(t *testing.T) {
	fromOps := apiSubject + "ingress:\n  - action: Allow\n    from: [{namespaces: {matchLabels: {team: ops}}}]\n"
	h := adminShop(t, anp("allow-ops", 10, fromOps), anp("pass-web", 20, fromWeb("Pass")))
	o := overviewOf(t, h, "")
	if len(o.Policies) != 2 || o.Policies[0].Kind != "AdminNetworkPolicy" || o.Policies[0].Selects != 1 || o.Policies[0].Namespace != "" {
		t.Errorf("%+v", o.Policies)
	}
	pa := podOf(t, h, "shop", "api")
	if pa.Ingress.Isolated {
		t.Error("an AdminNetworkPolicy does not isolate")
	}
	if len(pa.Ingress.Rules) != 2 || pa.Ingress.Rules[1].Kind != "pass" || pa.Ingress.Rules[0].PolicyKind != "AdminNetworkPolicy" {
		t.Errorf("%+v", pa.Ingress.Rules)
	}
	if len(o.Graph.Edges) != 1 || o.Graph.Edges[0].Kind != "allow" {
		t.Errorf("edges: %+v", o.Graph.Edges)
	}
}

func TestAnUnreadableAdminPolicyIsNotEvaluated(t *testing.T) {
	h := adminShop(t, "apiVersion: policy.networking.k8s.io/v1alpha1\nkind: AdminNetworkPolicy\nmetadata: {name: broken}\nspec: {priority: 1}\n")
	v := verdictOf(t, h, "shop/web", "shop/api", "80")
	if v.Answer != "yes" || len(v.Ingress.Caveats) != 1 {
		t.Errorf("%s %v", v.Answer, v.Ingress.Caveats)
	}
}

// A baseline's rules are drawn apart: the page sets them aside where a
// NetworkPolicy isolates that side, as the engine does.
func TestTheGraphMarksTheBaselinesRules(t *testing.T) {
	h := adminShop(t, banp("subject:\n  namespaces: {matchLabels: {team: shop}}\ningress:\n  - name: ops\n    action: Allow\n    from:\n      - namespaces: {matchLabels: {team: ops}}\n"))
	p := np("shop", "api-from-ops", map[string]string{"app": "api"})
	p.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{nsPeer(map[string]string{"team": "ops"})}}}
	h.policy(t, p)
	var baseline, plain int
	for _, e := range overviewOf(t, h, "").Graph.Edges {
		if e.From != "ns:ops" || e.To != "ns:shop" {
			continue
		}
		if e.Baseline {
			baseline++
			if strings.Join(e.Policies, ",") != "default" {
				t.Errorf("baseline edge %+v", e)
			}
		} else {
			plain++
		}
	}
	if baseline != 1 || plain != 1 {
		t.Errorf("ops to shop: %d baseline edges, %d others", baseline, plain)
	}
}

// A namespace's rules between its own pods are sorted as the engine reads
// them: a baseline's allow under a NetworkPolicy's default deny lets nothing
// in, and an AdminNetworkPolicy's deny wins.
func TestSelfRulesAreSortedAsTheEngineReadsThem(t *testing.T) {
	shopOf := func(h *fakeHost) *Node {
		for _, n := range overviewOf(t, h, "").Graph.Nodes {
			if n.ID == "ns:shop" {
				return &n
			}
		}
		return nil
	}
	selfIngress := "ingress:\n  - name: own\n    action: %s\n    from:\n      - namespaces: {matchLabels: {team: shop}}\n"
	h := adminShop(t, banp("subject:\n  namespaces: {matchLabels: {team: shop}}\n"+fmt.Sprintf(selfIngress, "Allow")))
	h.policy(t, defaultDenyIngress("shop"))
	if n := shopOf(h); n == nil || n.Self == nil || *n.Self != (SelfRules{}) {
		t.Errorf("a baseline allow under a default deny: %+v", n)
	}
	h = adminShop(t, anp("no-self", 10, "subject:\n  namespaces: {matchLabels: {team: shop}}\n"+fmt.Sprintf(selfIngress, "Deny")))
	if n := shopOf(h); n == nil || n.Self == nil || n.Self.Deny != 1 || n.Self.AllowIn != 0 {
		t.Errorf("an admin deny of shop to shop: %+v", n)
	}
	h = adminShop(t, banp("subject:\n  namespaces: {matchLabels: {team: shop}}\n"+fmt.Sprintf(selfIngress, "Deny")))
	if n := shopOf(h); n == nil || n.Self == nil || n.Self.BaselineDenyIn != 1 || n.Self.Deny != 0 {
		t.Errorf("a baseline deny of shop to shop: %+v", n)
	}
}
