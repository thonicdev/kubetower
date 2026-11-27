package netinspect

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// shopSelf is an AdminNetworkPolicy rule from shop to shop, on some ports.
func shopSelf(action, ports string) string {
	return "subject:\n  namespaces: {matchLabels: {team: shop}}\ningress:\n  - name: own\n    action: " + action +
		"\n    from:\n      - namespaces: {matchLabels: {team: shop}}\n" + ports
}

// shopWith is the shop namespace and identities more, named one to n.
func shopWith(t *testing.T, n int, objects ...string) *fakeHost {
	h := adminShop(t, objects...)
	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("zz%02d", i)
		h.pod(t, "shop", name, fmt.Sprintf("10.0.2.%d", i), map[string]string{"app": name})
	}
	return h
}

// The diagonal never states what the engine would not: every port whose
// answer can differ is asked, ANP and BANP subjects included, every identity
// is asked, and a namespace too large to ask in full is partial.
func TestTheDiagonalAsksEveryPortAndIdentity(t *testing.T) {
	on5432 := "    ports:\n      - portNumber: {protocol: TCP, port: 5432}\n"
	for _, c := range []struct {
		name  string
		build func() *fakeHost
		want  string
		flows []askedFlow
	}{
		{"an admin deny on one port no pod declares", func() *fakeHost {
			return adminShop(t, anp("no-db", 10, shopSelf("Deny", on5432)))
		}, diagMixed, []askedFlow{{"shop/web", "shop/api", "5432", "no"}, {"shop/web", "shop/api", "80", "yes"}}},
		{"a baseline deny on one port", func() *fakeHost {
			return adminShop(t, banp(shopSelf("Deny", on5432)))
		}, diagMixed, []askedFlow{{"shop/web", "shop/api", "5432", "no"}, {"shop/web", "shop/api", "80", "yes"}}},
		{"an admin deny on a range", func() *fakeHost {
			return adminShop(t, anp("no-range", 10, shopSelf("Deny", "    ports:\n      - portRange: {protocol: TCP, start: 1, end: 65535}\n")))
		}, diagDenied, []askedFlow{{"shop/web", "shop/api", "80", "no"}}},
		{"a source whose egress only reaches 5432", func() *fakeHost {
			h := adminShop(t)
			p := np("shop", "web-out", map[string]string{"app": "web"})
			p.Spec.PolicyTypes = types(networkingv1.PolicyTypeEgress)
			p.Spec.Egress = []networkingv1.NetworkPolicyEgressRule{{Ports: []networkingv1.NetworkPolicyPort{tcp(5432)}}}
			h.policy(t, p)
			return h
		}, diagInside, []askedFlow{{"shop/web", "shop/api", "80", "no"}, {"shop/web", "shop/api", "5432", "yes"}}},
		{"a destination isolated with nothing allowed", func() *fakeHost {
			h := adminShop(t)
			p := np("shop", "api-closed", map[string]string{"app": "api"})
			p.Spec.PolicyTypes = types(networkingv1.PolicyTypeIngress)
			h.policy(t, p)
			return h
		}, diagMixed, []askedFlow{{"shop/web", "shop/api", "80", "no"}, {"shop/api", "shop/web", "80", "yes"}}},
		{"a deny to the sixth identity", func() *fakeHost {
			deny := "subject:\n  pods:\n    namespaceSelector: {matchLabels: {team: shop}}\n    podSelector: {matchLabels: {app: zz03}}\n" +
				"ingress:\n  - name: own\n    action: Deny\n    from:\n      - namespaces: {matchLabels: {team: shop}}\n"
			return shopWith(t, 3, anp("no-zz", 10, deny))
		}, diagMixed, []askedFlow{{"shop/web", "shop/zz03", "80", "no"}, {"shop/web", "shop/api", "80", "yes"}}},
		{"a default deny lifted for the sixth identity alone", func() *fakeHost {
			h := shopWith(t, 3)
			h.policy(t, defaultDenyIngress("shop"))
			p := np("shop", "zz03-in", map[string]string{"app": "zz03"})
			p.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{nsPeer(map[string]string{"team": "shop"})}}}
			h.policy(t, p)
			return h
		}, diagInside, []askedFlow{{"shop/web", "shop/zz03", "80", "yes"}, {"shop/web", "shop/api", "80", "no"}}},
		{"more identities than are asked in full", func() *fakeHost {
			return shopWith(t, diagIdentities)
		}, diagPartial, nil},
		{"C9b: an admin deny with an unknown peer key beside namespaces", func() *fakeHost {
			return adminShop(t, anp("odd", 10, "subject:\n  namespaces: {matchLabels: {team: shop}}\ningress:\n  - name: own\n    action: Deny\n"+
				"    from:\n      - namespaces: {matchLabels: {team: shop}}\n        sameLabels: [team]\n"))
		}, diagUnevaluated, []askedFlow{{"shop/web", "shop/api", "80", "no"}}},
		{"C9c: an admin deny with an empty peer beside a namespaces one", func() *fakeHost {
			return adminShop(t, anp("odd", 10, "subject:\n  namespaces: {matchLabels: {team: shop}}\ningress:\n  - name: own\n    action: Deny\n"+
				"    from:\n      - namespaces: {matchLabels: {team: shop}}\n      - {}\n"))
		}, diagUnevaluated, []askedFlow{{"shop/web", "shop/api", "80", "no"}}},
	} {
		h := c.build()
		if got := shopDiagonal(t, h); got != c.want {
			t.Errorf("%s: diagonal %q, want %q", c.name, got, c.want)
		}
		for _, f := range c.flows {
			if v := verdictOf(t, h, f.from, f.to, f.port); v.Answer != f.want {
				t.Errorf("%s: the engine answers %s for %s to %s on %s, the test expects %s", c.name, v.Answer, f.from, f.to, f.port, f.want)
			}
		}
	}
}

// A model of shop's web and api, api declaring 80, with these policies.
func shopModel(policies ...networkingv1.NetworkPolicy) *model {
	pod := func(name, ip string, ports ...corev1.ContainerPort) corev1.Pod {
		return corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: name, Labels: map[string]string{"app": name}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Ports: ports}}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: ip},
		}
	}
	pods := []corev1.Pod{pod("web", "10.0.0.11"), pod("api", "10.0.0.10", corev1.ContainerPort{Name: "http", ContainerPort: 80})}
	return build(pods, policies, map[string]map[string]string{"shop": {"team": "shop"}})
}

// The ports asked are the bounds of every range of either end's rules and the
// first port, per protocol: nothing between two of them answers differently.
func TestTheDiagonalPortsAreTheRulesBounds(t *testing.T) {
	udp := corev1.ProtocolUDP
	end := int32(5100)
	from, to := intstr.FromInt32(5000), intstr.FromInt32(9000)
	in := np("shop", "api-in", map[string]string{"app": "api"})
	in.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: &from, EndPort: &end}}}}
	out := np("shop", "web-out", map[string]string{"app": "web"})
	out.Spec.PolicyTypes = types(networkingv1.PolicyTypeEgress)
	out.Spec.Egress = []networkingv1.NetworkPolicyEgressRule{{Ports: []networkingv1.NetworkPolicyPort{{Port: &to}}}}
	m := shopModel(*in, *out)
	ports, whole := m.diagPortsOf(m.podIndex["shop/web"], m.podIndex["shop/api"])
	want := []flowPort{{1, "TCP"}, {80, "TCP"}, {81, "TCP"}, {9000, "TCP"}, {9001, "TCP"}, {1, "UDP"}, {5000, "UDP"}, {5101, "UDP"}}
	if !whole || fmt.Sprint(ports) != fmt.Sprint(want) {
		t.Errorf("ports %v (whole %v), want %v", ports, whole, want)
	}
}

// A cluster past the budget answers partial for the namespaces it could not
// ask, by name, and never a verdict from a sample.
func TestTheDiagonalBudgetEndsInPartial(t *testing.T) {
	m := shopModel()
	pods := []int{m.podIndex["shop/api"], m.podIndex["shop/web"]}
	budget := 0
	if got := m.diagonalOf(pods, &budget); got != diagPartial {
		t.Errorf("no budget left: %q", got)
	}
	budget = diagBudget
	if got := m.diagonalOf(pods, &budget); got != diagOpen || budget >= diagBudget {
		t.Errorf("a budget: %q, %d left", got, budget)
	}
}
