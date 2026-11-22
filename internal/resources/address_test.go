package resources

// The Address column on Ingresses and the External column on Services, and the
// one word they share.
//
// A column whose value the person reading it cannot interpret is not doing its
// job, whatever it is technically reporting, and nothing on the page explains a
// bare "pending".
//
// So what is pinned here is not the word. It is that the word never travels
// alone, that the sentence beside it names the field it read, and that the two
// causes an object can distinguish — a controller that has not got to it, and no
// class for any controller to match — are told apart rather than folded into one
// answer.

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ingress(name string, class *string, annotations map[string]string, addresses ...string) *networkingv1.Ingress {
	points := make([]networkingv1.IngressLoadBalancerIngress, 0, len(addresses))
	for _, a := range addresses {
		points = append(points, networkingv1.IngressLoadBalancerIngress{IP: a})
	}
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Annotations: annotations},
		Spec: networkingv1.IngressSpec{
			IngressClassName: class,
			Rules:            []networkingv1.IngressRule{{Host: name + ".example.com"}},
		},
		Status: networkingv1.IngressStatus{
			LoadBalancer: networkingv1.IngressLoadBalancerStatus{Ingress: points},
		},
	}
}

func TestAnIngressWithNoAddressSaysWhatPendingMeans(t *testing.T) {
	traefik := "traefik"
	table, err := listing(client(
		ingress("published", &traefik, nil, "203.0.113.250"),
		ingress("classed", &traefik, nil),
		ingress("unclassed", nil, nil),
		ingress("annotated", nil, map[string]string{"kubernetes.io/ingress.class": "nginx"}),
	), "ingresses", "", "")
	if err != nil {
		t.Fatalf("listing ingresses: %v", err)
	}

	if got := cellOf(t, table, "published", "Address"); got.Text != "203.0.113.250" || got.Hint != "" {
		t.Errorf("a published address = %q with hint %q; an address states itself and a hint "+
			"on a cell that already says everything is what trains people to ignore hints",
			got.Text, got.Hint)
	}

	for _, c := range []struct {
		row  string
		says string
	}{
		// A named class points at one controller, and the sentence names it: the
		// operator's next gesture is to go and look at that controller.
		{row: "classed", says: `"traefik" controller`},
		{row: "annotated", says: `"nginx" controller`},
		// No class at all is a different errand — there is no controller to go
		// and look at, only the question of which IngressClass carries the
		// default annotation — and folding it into the sentence above would send
		// somebody looking for a controller that was never named.
		{row: "unclassed", says: "names no ingress class"},
	} {
		t.Run(c.row, func(t *testing.T) {
			cell := cellOf(t, table, c.row, "Address")
			if cell.Text != "pending" {
				t.Fatalf("Address = %q, want pending", cell.Text)
			}
			if cell.Hint == "" {
				t.Fatal("the cell says \"pending\" and nothing else. That is the whole finding " +
					"this test was written for: a word that is right and that the operator " +
					"reading it cannot expand into anything")
			}
			if !strings.Contains(cell.Hint, "status.loadBalancer") {
				t.Errorf("the hint does not name the field it read (%q). Naming it is what "+
					"makes the answer checkable against the object's own YAML", cell.Hint)
			}
			if !strings.Contains(cell.Hint, c.says) {
				t.Errorf("the hint is %q, and does not say %q — the two causes have been "+
					"folded into one answer, which sends half the readers to the wrong place",
					cell.Hint, c.says)
			}
		})
	}
}

// The same word on the Services list, because it is the same word and an
// explanation on one list only is how an operator learns that the hover is
// unreliable.
func TestALoadBalancerWithNoExternalAddressSaysWhatPendingMeans(t *testing.T) {
	table, err := listing(client(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "waiting", Namespace: "default"},
			Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "internal", Namespace: "default"},
			Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP},
		},
	), "services", "", "")
	if err != nil {
		t.Fatalf("listing services: %v", err)
	}

	cell := cellOf(t, table, "waiting", "External")
	if cell.Text != "pending" {
		t.Fatalf("External = %q, want pending", cell.Text)
	}
	if !strings.Contains(cell.Hint, "status.loadBalancer") {
		t.Errorf("the hint is %q and does not name the field it read", cell.Hint)
	}

	// A ClusterIP has no external address to be pending: an em dash is the whole
	// truth about it, and "pending" would report a wait that will never end.
	if got := cellOf(t, table, "internal", "External"); got.Text != Missing || got.Hint != "" {
		t.Errorf("a ClusterIP's External = %q with hint %q, want an em dash and no hint",
			got.Text, got.Hint)
	}
}
