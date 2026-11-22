package resources

// An address on a list is two different affordances, and this file is about
// keeping them apart.
//
// Opening an address and copying it are easy to want at once, and they are not
// the same feature. Opening needs a scheme, which is a decision that can be *wrong*:
// `http://` onto a TLS-only host answers "The plain HTTP request was sent to an
// HTTPS port", which reads as a broken Ingress rather than as a wrong link.
// Copying needs no scheme and cannot be wrong, so it is offered in the several
// places opening is refused.
//
// So what is pinned here is not that a link exists. It is which values get one,
// which deliberately do not, and that the clipboard carries what the column had
// no room to print.

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

func ingressWith(name string, rules []networkingv1.IngressRule, tls []networkingv1.IngressTLS) *networkingv1.Ingress {
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "web"},
		Spec:       networkingv1.IngressSpec{Rules: rules, TLS: tls},
	}
}

func hostRules(hosts ...string) []networkingv1.IngressRule {
	out := make([]networkingv1.IngressRule, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, networkingv1.IngressRule{Host: h})
	}
	return out
}

func ingressCell(t *testing.T, i *networkingv1.Ingress, column string) Cell {
	t.Helper()
	table, err := List(context.Background(), accesstest.Allow(), client(i), Query{Kind: "ingresses"}, nil)
	if err != nil {
		t.Fatalf("listing ingresses: %v", err)
	}
	return cellOf(t, table, i.Name, column)
}

// The scheme comes from `spec.tls` and from nowhere else. That field is the
// Ingress saying, in the place designed for it, which of its hosts are served
// over TLS — the same standing `appProtocol` has at the top of
// internal/forward/scheme.go's three-step order, which is why an Ingress needs
// none of the rest of that table.
func TestAnIngressHostIsLinkedWithTheSchemeItsTLSBlockDeclares(t *testing.T) {
	for name, c := range map[string]struct {
		rules []networkingv1.IngressRule
		tls   []networkingv1.IngressTLS
		want  map[string]string
	}{
		"a host under spec.tls is https": {
			hostRules("api.example.com", "www.example.com"),
			[]networkingv1.IngressTLS{{Hosts: []string{"api.example.com"}}},
			map[string]string{
				"api.example.com": "https://api.example.com",
				"www.example.com": "http://www.example.com",
			},
		},
		"no TLS block at all is http": {
			hostRules("plain.example.com"), nil,
			map[string]string{"plain.example.com": "http://plain.example.com"},
		},
		"a TLS block naming no hosts covers every host in the Ingress": {
			hostRules("a.example.com", "b.example.com"),
			[]networkingv1.IngressTLS{{SecretName: "wildcard-cert"}},
			map[string]string{
				"a.example.com": "https://a.example.com",
				"b.example.com": "https://b.example.com",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			cell := ingressCell(t, ingressWith("ing", c.rules, c.tls), "Hosts")
			got := map[string]string{}
			for _, a := range cell.Addresses {
				got[a.Text] = a.Href
			}
			for host, want := range c.want {
				if got[host] != want {
					t.Errorf("%s links to %q, want %q — a wrong scheme answers "+
						"\"The plain HTTP request was sent to an HTTPS port\" or a "+
						"certificate interstitial, and both read as a broken Ingress",
						host, got[host], want)
				}
			}
		})
	}
}

// Three refusals, each of which would otherwise be a link that goes nowhere —
// and a link that fails reads as the object being broken, which is worse than no
// link at all.
func TestTheThingsThatAreNotAddressesAreNotLinked(t *testing.T) {
	t.Run("a rule with no host", func(t *testing.T) {
		cell := ingressCell(t, ingressWith("ing", hostRules(""), nil), "Hosts")
		if len(cell.Addresses) != 1 || cell.Addresses[0].Text != "*" {
			t.Fatalf("addresses = %+v, want a single \"*\"", cell.Addresses)
		}
		if cell.Addresses[0].Href != "" {
			t.Errorf("\"*\" links to %q — it matches everything that reaches the "+
				"controller and is not somewhere", cell.Addresses[0].Href)
		}
	})

	t.Run("a wildcard host", func(t *testing.T) {
		cell := ingressCell(t, ingressWith("ing", hostRules("*.example.com"), nil), "Hosts")
		if len(cell.Addresses) != 1 {
			t.Fatalf("addresses = %+v, want one", cell.Addresses)
		}
		if cell.Addresses[0].Href != "" {
			t.Errorf("%q links to %q — a wildcard is a pattern, there is no such "+
				"name to resolve, and the failure looks like the Ingress being broken",
				cell.Addresses[0].Text, cell.Addresses[0].Href)
		}
	})

	t.Run("the Address column", func(t *testing.T) {
		i := ingressWith("ing", hostRules("api.example.com"), nil)
		i.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: "10.0.0.7"}}
		cell := ingressCell(t, i, "Address")
		if len(cell.Addresses) != 0 {
			t.Errorf("the load balancer's own address is linked (%+v) — dialling it "+
				"without the Host header gets the controller's default backend, which "+
				"is a 404 that reads as a broken Ingress", cell.Addresses)
		}
		if cell.Copy != "10.0.0.7" {
			t.Errorf("Copy = %q, want the address — it is what goes in a DNS record "+
				"or a `curl -H Host:`, which is the reason it is a column at all", cell.Copy)
		}
	})
}

// The two renderings of one value must read back as each other. A cell that
// sorts and filters on one string while showing another is the defect
// Cell.Addresses' own comment is about, and it is invisible until somebody sorts.
func TestACellsAddressesReadBackAsItsText(t *testing.T) {
	for name, hosts := range map[string][]string{
		"under the cap":               {"a.example.com", "b.example.com"},
		"exactly at it":               {"a.example.com", "b.example.com", "c.example.com"},
		"over it":                     {"a.example.com", "b.example.com", "c.example.com", "d.example.com", "e.example.com"},
		"with a bare rule among them": {"a.example.com", "", "c.example.com", "d.example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			cell := ingressCell(t, ingressWith("ing", hostRules(hosts...), nil), "Hosts")
			got := ""
			for _, a := range cell.Addresses {
				got += a.Text
			}
			if got != cell.Text {
				t.Errorf("the pieces read back as %q and the cell prints %q — the "+
					"template concatenates them verbatim, so a table that sorts and "+
					"filters on one string while showing another is what this drift "+
					"produces", got, cell.Text)
			}
		})
	}
}

// Shown is not the same as obtainable. The column has room for
// three hosts and the two it dropped are exactly the ones somebody wants the
// clipboard for.
func TestTheClipboardCarriesTheHostsTheColumnHadNoRoomFor(t *testing.T) {
	hosts := []string{"a.example.com", "b.example.com", "c.example.com", "d.example.com", "e.example.com"}
	cell := ingressCell(t, ingressWith("ing", hostRules(hosts...), nil), "Hosts")

	if !strings.Contains(cell.Text, "+2 more") {
		t.Fatalf("text = %q, want it capped — this test is about what the cap loses", cell.Text)
	}
	want := strings.Join(hosts, ", ")
	if cell.Copy != want {
		t.Errorf("Copy = %q, want all five hosts %q — a clipboard that carried "+
			"\"+2 more\" would put the words on the operator's clipboard instead of "+
			"the addresses", cell.Copy, want)
	}
}

// A copy control on an em dash would put an em dash on the clipboard, and a
// control on the word `pending` would put that. Both are worse than no control:
// the operator finds out only after pasting.
func TestNothingOffersToCopyAValueThatIsNotThere(t *testing.T) {
	t.Run("an Ingress with no address published", func(t *testing.T) {
		cell := ingressCell(t, ingressWith("ing", hostRules("api.example.com"), nil), "Address")
		if cell.Text != "pending" {
			t.Fatalf("text = %q, want the pending word this test is about", cell.Text)
		}
		if cell.Copy != "" {
			t.Errorf("Copy = %q on a cell holding %q — copying a status word is how "+
				"an operator discovers, in another window, that they pasted the wrong "+
				"thing", cell.Copy, cell.Text)
		}
	})

	t.Run("a Service with no cluster IP", func(t *testing.T) {
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "headless", Namespace: "web"},
			Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP},
		}
		table, err := List(context.Background(), accesstest.Allow(), client(svc), Query{Kind: "services"}, nil)
		if err != nil {
			t.Fatalf("listing services: %v", err)
		}
		cell := cellOf(t, table, "headless", "Cluster IP")
		if cell.Text != Missing {
			t.Fatalf("text = %q, want an em dash", cell.Text)
		}
		if cell.Copy != "" {
			t.Errorf("Copy = %q on an em dash", cell.Copy)
		}
	})
}

// A LoadBalancer's external addresses copy and do not link, and the reason is
// written in externalAddress: a Service publishes a *set* of ports, and
// deciding http from https for a bare address is the question
// internal/forward/scheme.go already owns. A second copy of that table is what
// this asserts against.
func TestALoadBalancersAddressesCopyAndDoNotLink(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "edge", Namespace: "web"},
		Spec: corev1.ServiceSpec{
			Type:      corev1.ServiceTypeLoadBalancer,
			ClusterIP: "10.96.0.14",
		},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{
					{Hostname: "a-1.eu-west-1.elb.amazonaws.com"},
					{IP: "52.1.2.3"},
					{IP: "52.1.2.4"},
				},
			},
		},
	}
	table, err := List(context.Background(), accesstest.Allow(), client(svc), Query{Kind: "services"}, nil)
	if err != nil {
		t.Fatalf("listing services: %v", err)
	}

	external := cellOf(t, table, "edge", "External")
	if len(external.Addresses) != 0 {
		t.Errorf("external addresses are linked (%+v) — there is no single port to "+
			"open and no scheme to open it with", external.Addresses)
	}
	want := "a-1.eu-west-1.elb.amazonaws.com, 52.1.2.3, 52.1.2.4"
	if external.Copy != want {
		t.Errorf("Copy = %q, want all three %q", external.Copy, want)
	}

	if got := cellOf(t, table, "edge", "Cluster IP"); got.Copy != "10.96.0.14" {
		t.Errorf("the cluster IP does not copy (Copy = %q) — it is the address "+
			"somebody pastes into a curl in the dock's terminal", got.Copy)
	}
}

// The width for the copy control is reserved by the *descriptor*, so it is the
// same before and after the controller writes an address. See Column.Copyable,
// and the rule behind it: a width derived from data that has not arrived is a
// guess.
func TestTheColumnsHoldingAnAddressDeclareThatTheyAreCopyable(t *testing.T) {
	for kind, titles := range map[string][]string{
		"ingresses": {"Hosts", "Address"},
		"services":  {"Cluster IP", "External"},
		"nodes":     {"Internal IP"},
	} {
		for _, title := range titles {
			t.Run(kind+"/"+title, func(t *testing.T) {
				for _, c := range ColumnsOf(kind) {
					if c.Title != title {
						continue
					}
					if !c.Copyable {
						t.Fatalf("%s's %s column is not Copyable, so internal/web's "+
							"defaultWidths reserves nothing for the control — and this "+
							"is the column whose value arrives from a controller after "+
							"the first paint", kind, title)
					}
					return
				}
				t.Fatalf("%s has no %s column", kind, title)
			})
		}
	}
}

// An Ingress's Address is not on the default view. It is secondary — off until
// asked for in the columns menu — and it is the only column of the table that
// is: Hosts, the one an operator reads down, stays drawn. Copyable is held
// beside it, because a column hidden by default must still copy when shown,
// as TestTheColumnsHoldingAnAddressDeclareThatTheyAreCopyable above says too.
func TestAnIngressesAddressIsDrawnOnlyWhenAskedFor(t *testing.T) {
	var shownByDefault []string
	address, found := Column{}, false
	for _, c := range ColumnsOf("ingresses") {
		if c.Title == "Address" {
			address, found = c, true
		}
		if !c.Secondary {
			shownByDefault = append(shownByDefault, c.Title)
		}
	}
	if !found {
		t.Fatal("ingresses has no Address column; it is meant to be hidden, not removed")
	}
	if !address.Secondary {
		t.Error("the Address column is not Secondary, so a fresh Ingresses list draws it")
	}
	if !address.Copyable {
		t.Error("the Address column is no longer Copyable; shown on request, it must still copy")
	}
	want := []string{"Name", "Namespace", "Class", "Hosts", "Age"}
	if strings.Join(shownByDefault, ",") != strings.Join(want, ",") {
		t.Errorf("drawn by default: %v, want %v — Address is the only secondary column",
			shownByDefault, want)
	}
}
