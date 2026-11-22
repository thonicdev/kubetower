package netinspect

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/yaml"
)

func apiService(target intstr.IntOrString) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api"},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "api"},
			Ports:    []corev1.ServicePort{{Name: "web", Port: 80, TargetPort: target, Protocol: corev1.ProtocolTCP}},
		},
	}
}

func askService(t *testing.T, h *fakeHost, from, to, port string) Verdict {
	t.Helper()
	return verdictOf(t, h, url.QueryEscape(from), url.QueryEscape(servicePrefix+to), port)
}

// applyPolicy feeds generated YAML, document by document, back to the cluster.
func applyPolicy(t *testing.T, h *fakeHost, text string) int {
	t.Helper()
	n := 0
	for _, doc := range strings.Split(text, "---\n") {
		var p networkingv1.NetworkPolicy
		if err := yaml.UnmarshalStrict([]byte(doc), &p); err != nil {
			t.Fatalf("the policy is not a NetworkPolicy: %v\n%s", err, doc)
		}
		if p.Kind != "NetworkPolicy" || p.Namespace == "" || p.Name == "" {
			t.Fatalf("the policy is incomplete:\n%s", doc)
		}
		h.policy(t, &p)
		n++
	}
	return n
}

// shopWithBackends is shop with two api replicas behind a Service whose
// target port is named, and default-deny in both directions for web.
func shopWithBackends(t *testing.T) *fakeHost {
	h := shop(t)
	h.pod(t, "shop", "api-2", "10.0.0.20", map[string]string{"app": "api", "pod-template-hash": "abc"}, corev1.ContainerPort{Name: "http", ContainerPort: 8080})
	h.add(t, "services", apiService(intstr.FromString("http")))
	h.policy(t, defaultDenyIngress("shop"))
	return h
}

func TestAServiceIsAnsweredPodByPodOnItsTargetPort(t *testing.T) {
	h := shopWithBackends(t)
	v := askService(t, h, "shop/web", "shop/api", "web")
	if v.Service == nil || v.Service.Total != 2 || len(v.Service.Backends) != 2 {
		t.Fatalf("two backends expected: %+v", v.Service)
	}
	ports := map[string]int32{}
	for _, b := range v.Service.Backends {
		ports[b.Pod] = b.Port
		if b.Answer != "no" {
			t.Errorf("%s: %+v", b.Pod, b)
		}
	}
	// api names http as 80, api-2 as 8080: one Service port, two targets.
	if ports["shop/api"] != 80 || ports["shop/api-2"] != 8080 {
		t.Errorf("the named target port is resolved per pod: %v", ports)
	}
	if v.Answer != "no" || v.Port != 80 || v.To != "shop/api" || v.Service.TargetPort != "http" {
		t.Errorf("%+v", v)
	}
	// By number too.
	if w := askService(t, h, "shop/web", "shop/api", "80"); w.Service == nil || w.Service.PortName != "web" {
		t.Errorf("%+v", w.Service)
	}
}

func TestThePolicyForAServiceAllowsEveryBackend(t *testing.T) {
	h := shopWithBackends(t)
	v := askService(t, h, "shop/web", "shop/api", "web")
	if v.Policy == "" || strings.Contains(v.Policy, "Egress") {
		t.Fatalf("only the destination's ingress refuses, so only an ingress policy is offered:\n%s", v.Policy)
	}
	if n := applyPolicy(t, h, v.Policy); n != 1 {
		t.Fatalf("one policy expected, got %d", n)
	}
	v = askService(t, h, "shop/web", "shop/api", "web")
	if v.Answer != "yes" || v.Policy != "" {
		t.Fatalf("the generated policy, fed back to the engine, must allow it: %+v\n%s", v, v.Policy)
	}
	for _, b := range v.Service.Backends {
		if b.Answer != "yes" {
			t.Errorf("%+v", b)
		}
	}
	// Exactly the flow: another source is still refused.
	if s := askService(t, h, "shop/stranger", "shop/api", "web"); s.Answer != "no" {
		t.Errorf("the policy must not open the Service to everyone: %+v", s)
	}
}

func TestThePolicyForAPodIsMinimalAndAllowsTheFlow(t *testing.T) {
	h := shop(t)
	h.policy(t, defaultDenyIngress("shop"))
	deny := np("shop", "web-egress-deny", map[string]string{"app": "web"})
	deny.Spec.PolicyTypes = types(networkingv1.PolicyTypeEgress)
	h.policy(t, deny)
	// Cross-namespace source, so the peer carries a namespace selector.
	v := verdictOf(t, h, "ops/probe", "shop/api", "80")
	if v.Answer != "no" || !strings.Contains(v.Policy, "kubernetes.io/metadata.name: ops") || strings.Contains(v.Policy, "Egress") {
		t.Fatalf("ingress only, naming the namespace:\n%s", v.Policy)
	}
	applyPolicy(t, h, v.Policy)
	if v = verdictOf(t, h, "ops/probe", "shop/api", "80"); v.Answer != "yes" {
		t.Fatalf("%+v", v)
	}
	// Both sides isolated: both policies, and the flow passes.
	v = verdictOf(t, h, "shop/web", "shop/api", "80")
	if v.Answer != "no" || strings.Count(v.Policy, "kind: NetworkPolicy") != 2 {
		t.Fatalf("an ingress and an egress policy expected:\n%s", v.Policy)
	}
	applyPolicy(t, h, v.Policy)
	if v = verdictOf(t, h, "shop/web", "shop/api", "80"); v.Answer != "yes" {
		t.Fatalf("%+v", v)
	}
	// The rollout's label never reaches a selector.
	h2 := shop(t)
	h2.pod(t, "shop", "api-3", "10.0.0.30", map[string]string{"app": "api3", "pod-template-hash": "x"})
	h2.policy(t, defaultDenyIngress("shop"))
	if p := verdictOf(t, h2, "shop/web", "shop/api-3", "80").Policy; strings.Contains(p, "pod-template-hash") {
		t.Errorf("a rewritten label in a selector:\n%s", p)
	}
}

func TestAnAddressSourceIsAnIPBlock(t *testing.T) {
	h := shop(t)
	h.policy(t, defaultDenyIngress("shop"))
	v := verdictOf(t, h, "198.51.100.7", "shop/api", "80")
	if v.Answer != "no" || !strings.Contains(v.Policy, "cidr: 198.51.100.7/32") {
		t.Fatalf("%+v\n%s", v, v.Policy)
	}
	applyPolicy(t, h, v.Policy)
	if v = verdictOf(t, h, "198.51.100.7", "shop/api", "80"); v.Answer != "yes" {
		t.Fatalf("%+v", v)
	}
}

func TestNoPolicyIsOfferedWhereNoneIsNeededOrNoneCanHelp(t *testing.T) {
	h := shop(t)
	if v := verdictOf(t, h, "shop/web", "shop/api", "80"); v.Answer != "yes" || v.Policy != "" {
		t.Errorf("an allowed flow offers nothing: %+v", v)
	}
	// A deny rule wins over any allow: copying a NetworkPolicy would not help.
	h.policy(t, defaultDenyIngress("shop"))
	v := verdictOf(t, h, "shop/web", "shop/api", "80")
	v.Ingress.Decided = &RuleRef{Kind: "deny"}
	if wants(v.Ingress) {
		t.Error("a denied side decided by a deny rule is not lifted by an allow")
	}
}

func TestAServiceReadsAreBoundedAndHonest(t *testing.T) {
	h := shop(t)
	h.add(t, "services", &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "headless"},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}}})
	h.add(t, "services", apiService(intstr.FromInt32(80)))
	for _, c := range []struct{ to, port, want string }{
		{"shop/ghost", "80", "no Service shop/ghost"},
		{"shop/headless", "80", "no selector"},
		{"shop/api", "9999", "no TCP port"},
		{"nonsense", "80", "svc:namespace/name"},
	} {
		w := serve(t, h, "/x/kind/netinspect/verdict?from=shop/web&port="+c.port+"&to="+url.QueryEscape(servicePrefix+c.to))
		if w.Code != 400 || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%s: %d %s", c.to, w.Code, w.Body.String())
		}
	}
	for i := 0; i < maxBackends+5; i++ {
		h.pod(t, "shop", "api-r"+strings.Repeat("x", i%3)+string(rune('a'+i/3%26))+string(rune('a'+i%26)), "10.1.0."+string(rune('0'+i%10)), map[string]string{"app": "api"})
	}
	v := askService(t, h, "shop/web", "shop/api", "80")
	if len(v.Service.Backends) != maxBackends || !v.Service.Capped || v.Service.Total <= maxBackends {
		t.Errorf("at most %d pods are answered: %d of %d capped=%v", maxBackends, len(v.Service.Backends), v.Service.Total, v.Service.Capped)
	}
	// An unlistable Service is a refusal or a failure, never "no such Service".
	h.listErr["services"] = errors.New("boom")
	if w := serve(t, h, "/x/kind/netinspect/verdict?from=shop/web&port=80&to=svc:shop/api"); w.Code != 502 {
		t.Errorf("a failed read is 502, got %d %s", w.Code, w.Body.String())
	}
}
