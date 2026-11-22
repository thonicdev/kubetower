package netinspect

import (
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// shop is the scene measured on kindnet: api (nginx, port 80 named http), web
// and stranger, in one namespace.
func shop(t *testing.T) *fakeHost {
	h := newHost()
	h.namespace(t, "shop", map[string]string{"team": "shop"})
	h.namespace(t, "ops", map[string]string{"team": "ops"})
	h.pod(t, "shop", "api", "10.0.0.10", map[string]string{"app": "api"}, corev1.ContainerPort{Name: "http", ContainerPort: 80})
	h.pod(t, "shop", "web", "10.0.0.11", map[string]string{"app": "web"})
	h.pod(t, "shop", "stranger", "10.0.0.12", map[string]string{"app": "stranger"})
	h.pod(t, "ops", "probe", "10.0.1.10", map[string]string{"app": "probe"})
	return h
}

func defaultDenyIngress(ns string) *networkingv1.NetworkPolicy {
	p := np(ns, "default-deny-ingress", nil)
	p.Spec.PolicyTypes = types(networkingv1.PolicyTypeIngress)
	return p
}

func apiFromWeb() *networkingv1.NetworkPolicy {
	p := np("shop", "api-from-web", map[string]string{"app": "api"})
	p.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{
		From:  []networkingv1.NetworkPolicyPeer{podPeer(map[string]string{"app": "web"})},
		Ports: []networkingv1.NetworkPolicyPort{named("http")},
	}}
	return p
}

func webEgress(port int) *networkingv1.NetworkPolicy {
	p := np("shop", "web-egress-api", map[string]string{"app": "web"})
	p.Spec.PolicyTypes = types(networkingv1.PolicyTypeEgress)
	p.Spec.Egress = []networkingv1.NetworkPolicyEgressRule{{
		To:    []networkingv1.NetworkPolicyPeer{podPeer(map[string]string{"app": "api"})},
		Ports: []networkingv1.NetworkPolicyPort{tcp(port)},
	}}
	return p
}

// The table measured on kindnet v20250512 (the study's §8.1), answered from
// the rules: each row is what the cluster did.
func TestTheVerdictsAgreeWithWhatKindnetDid(t *testing.T) {
	h := shop(t)
	v := verdictOf(t, h, "shop/web", "shop/api", "80")
	if v.Answer != "yes" || v.Egress.State != sideOpen || v.Ingress.State != sideOpen {
		t.Fatalf("no policy: %+v", v)
	}

	h.policy(t, defaultDenyIngress("shop"))
	h.policy(t, apiFromWeb())
	v = verdictOf(t, h, "shop/web", "shop/api", "80")
	if v.Answer != "yes" || v.Ingress.State != sideAllowed || v.Ingress.Decided == nil ||
		v.Ingress.Decided.Name != "api-from-web" || v.Ingress.Decided.Rule != 1 {
		t.Errorf("deny + allow web on http: %+v", v.Ingress)
	}
	if len(v.Ingress.Isolating) != 2 {
		t.Errorf("api is isolated by both policies, got %+v", v.Ingress.Isolating)
	}
	v = verdictOf(t, h, "shop/stranger", "shop/api", "80")
	if v.Answer != "no" || v.Ingress.State != sideDenied || v.Egress.State != sideOpen {
		t.Errorf("stranger: %+v", v)
	}
	if !strings.Contains(v.Summary, "destination may not receive") {
		t.Errorf("the summary should say which side refuses: %q", v.Summary)
	}

	deny := np("shop", "web-egress-deny", map[string]string{"app": "web"})
	deny.Spec.PolicyTypes = types(networkingv1.PolicyTypeEgress)
	h.policy(t, deny)
	v = verdictOf(t, h, "shop/web", "shop/api", "80")
	if v.Answer != "no" || v.Egress.State != sideDenied || v.Ingress.State != sideAllowed {
		t.Errorf("web egress deny: %+v", v)
	}

	h.policy(t, webEgress(8080))
	if v = verdictOf(t, h, "shop/web", "shop/api", "80"); v.Answer != "no" {
		t.Errorf("an egress rule on 8080 lets 80 out: %+v", v.Egress)
	}
	h.policy(t, webEgress(80))
	if v = verdictOf(t, h, "shop/web", "shop/api", "80"); v.Answer != "yes" || v.Egress.State != sideAllowed {
		t.Errorf("web egress to api:80: %+v", v.Egress)
	}
}

// A policy with egress rules and no policyTypes isolates for ingress too,
// allowing nothing there (the API reference's defaulting).
func TestPolicyTypesAbsentIsolateIngressAlways(t *testing.T) {
	h := shop(t)
	p := webEgress(80)
	p.Spec.PolicyTypes = nil
	p.Spec.PodSelector = metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}
	p.Spec.Egress[0].To = []networkingv1.NetworkPolicyPeer{podPeer(map[string]string{"app": "web"})}
	h.policy(t, p)
	v := verdictOf(t, h, "shop/web", "shop/api", "80")
	if v.Ingress.State != sideDenied {
		t.Errorf("api should be isolated for ingress by an egress-only policy without policyTypes: %+v", v.Ingress)
	}
	v = verdictOf(t, h, "shop/api", "shop/web", "80")
	if v.Egress.State != sideAllowed {
		t.Errorf("and its egress rule should allow web:80: %+v", v.Egress)
	}
}

// podSelector and namespaceSelector in one element are AND; in two
// elements they are OR.
func TestOneElementIsAndTwoElementsAreOr(t *testing.T) {
	both := networkingv1.NetworkPolicyPeer{
		PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": "probe"}},
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "ops"}},
	}
	for _, tc := range []struct {
		name     string
		peers    []networkingv1.NetworkPolicyPeer
		web, ops string
	}{
		{"and", []networkingv1.NetworkPolicyPeer{both}, "no", "yes"},
		{"or", []networkingv1.NetworkPolicyPeer{podPeer(map[string]string{"app": "web"}), nsPeer(map[string]string{"team": "ops"})}, "yes", "yes"},
		{"namespace alone", []networkingv1.NetworkPolicyPeer{nsPeer(map[string]string{"team": "ops"})}, "no", "yes"},
		{"pods alone stay in the namespace", []networkingv1.NetworkPolicyPeer{podPeer(map[string]string{"app": "probe"})}, "no", "no"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := shop(t)
			p := np("shop", "api-in", map[string]string{"app": "api"})
			p.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{From: tc.peers}}
			h.policy(t, p)
			if v := verdictOf(t, h, "shop/web", "shop/api", "80"); v.Answer != tc.web {
				t.Errorf("web: %s, want %s (%s)", v.Answer, tc.web, v.Ingress.Reason)
			}
			if v := verdictOf(t, h, "ops/probe", "shop/api", "80"); v.Answer != tc.ops {
				t.Errorf("ops/probe: %s, want %s (%s)", v.Answer, tc.ops, v.Ingress.Reason)
			}
		})
	}
}

// An ipBlock allows an address outside the cluster unless an except covers
// it; against a pod's own address it is the plugin's to decide.
func TestAnIPBlockDecidesAddressesAndDefersOnPods(t *testing.T) {
	h := shop(t)
	p := np("shop", "web-out", map[string]string{"app": "web"})
	p.Spec.PolicyTypes = types(networkingv1.PolicyTypeEgress)
	p.Spec.Egress = []networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{{
		IPBlock: &networkingv1.IPBlock{CIDR: "10.0.0.0/8", Except: []string{"10.9.0.0/16"}},
	}}}}
	h.policy(t, p)
	if v := verdictOf(t, h, "shop/web", "10.1.2.3", "443"); v.Answer != "yes" || v.Ingress.State != sideOutside {
		t.Errorf("an address in the block: %+v", v)
	}
	if v := verdictOf(t, h, "shop/web", "10.9.1.1", "443"); v.Answer != "no" {
		t.Errorf("an address in the except: %+v", v.Egress)
	}
	if v := verdictOf(t, h, "shop/web", "192.168.1.1", "443"); v.Answer != "no" {
		t.Errorf("an address outside the block: %+v", v.Egress)
	}
	v := verdictOf(t, h, "shop/web", "shop/api", "80")
	if v.Answer != "depends" || v.Egress.State != sideDepends || len(v.Egress.Caveats) == 0 {
		t.Errorf("a pod whose address is in the block: %+v", v.Egress)
	}
	// Asked by a pod's address, the address is that pod.
	if v := verdictOf(t, h, "shop/web", "10.0.0.10", "80"); v.To != "shop/api" {
		t.Errorf("the address of a pod should be read as the pod, got %q", v.To)
	}
}

// Ports: protocol and number, a range, a protocol alone, a name resolved on
// the destination pod.
func TestPortsMatchByProtocolNumberRangeAndName(t *testing.T) {
	udp := corev1.ProtocolUDP
	end := int32(9000)
	h := shop(t)
	p := np("shop", "api-in", map[string]string{"app": "api"})
	p.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{Ports: []networkingv1.NetworkPolicyPort{
		func() networkingv1.NetworkPolicyPort { r := tcp(8000); r.EndPort = &end; return r }(),
		{Protocol: &udp},
		named("http"),
	}}}
	h.policy(t, p)
	for _, tc := range []struct{ port, proto, want string }{
		{"8500", "", "yes"}, {"9001", "", "no"}, {"53", "UDP", "yes"}, {"80", "", "yes"}, {"81", "", "no"}, {"http", "", "yes"}, {"80", "SCTP", "no"},
	} {
		w := serve(t, h, "/x/kind/netinspect/verdict?from=shop/web&to=shop/api&port="+tc.port+"&protocol="+tc.proto)
		v := decode[Verdict](t, w)
		if v.Answer != tc.want {
			t.Errorf("port %s/%s: %s, want %s (%s)", tc.port, tc.proto, v.Answer, tc.want, v.Ingress.Reason)
		}
	}
}

// A hostNetwork pod is the plugin's: never "allowed" or "refused".
func TestAHostNetworkPodDependsOnThePlugin(t *testing.T) {
	h := shop(t)
	h.add(t, "pods", &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "agent", Labels: map[string]string{"app": "web"}},
		Spec:       corev1.PodSpec{HostNetwork: true},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "172.18.0.2"},
	})
	h.policy(t, defaultDenyIngress("shop"))
	v := verdictOf(t, h, "shop/stranger", "shop/agent", "80")
	if v.Answer != "depends" || v.Ingress.State != sideDepends {
		t.Errorf("hostNetwork destination: %+v", v)
	}
}

// Namespaces that cannot be read leave the one label the API server sets on
// every namespace, and the answer says it is partial.
func TestUnreadableNamespacesKeepTheirNameLabel(t *testing.T) {
	h := shop(t)
	h.listErr["namespaces"] = errorString("forbidden")
	p := np("shop", "api-in", map[string]string{"app": "api"})
	p.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{
		nsPeer(map[string]string{"kubernetes.io/metadata.name": "ops"}),
	}}}
	h.policy(t, p)
	v := verdictOf(t, h, "ops/probe", "shop/api", "80")
	if v.Answer != "yes" || v.Complete || len(v.Notes) == 0 {
		t.Errorf("by name, with a note: %+v", v)
	}
}

func TestABadQuestionIsA400(t *testing.T) {
	h := shop(t)
	for _, q := range []string{
		"from=1.1.1.1&to=2.2.2.2&port=80",
		"from=shop/web&to=shop/nobody&port=80",
		"from=web&to=shop/api&port=80",
		"from=shop/web&to=shop/api&port=99999",
		"from=shop/web&to=shop/api&port=https",
		"from=shop/web&to=shop/api&port=80&protocol=ICMP",
	} {
		w := serve(t, h, "/x/kind/netinspect/verdict?"+q)
		if p := decode[problem](t, w); w.Code != http.StatusBadRequest || p.Code != CodeBadRequest || p.Message == "" {
			t.Errorf("%s answered %d %+v", q, w.Code, p)
		}
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }
