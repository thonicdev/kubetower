package detail

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// addressRows is the labels and values of a Pod's address rows, in order.
func addressRows(facts []Fact) [][3]string {
	var rows [][3]string
	for _, f := range facts {
		switch f.Label {
		case "Pod IP", "Pod IPv4", "Pod IPv6", "Node IP":
			rows = append(rows, [3]string{f.Label, f.Value, f.Copy})
		}
	}
	return rows
}

func TestAPodsSummaryCarriesItsAddressesCopyable(t *testing.T) {
	cases := map[string]struct {
		status string
		want   [][3]string
	}{
		"one address": {
			status: "podIP: 10.244.1.7\n  podIPs:\n  - ip: 10.244.1.7\n  hostIP: 172.18.0.2",
			want:   [][3]string{{"Pod IP", "10.244.1.7", "10.244.1.7"}, {"Node IP", "172.18.0.2", "172.18.0.2"}},
		},
		"dual stack, one row per family": {
			status: "podIP: 10.244.1.7\n  podIPs:\n  - ip: 10.244.1.7\n  - ip: fd00:10:244:1::7\n  hostIP: 172.18.0.2",
			want: [][3]string{
				{"Pod IPv4", "10.244.1.7", "10.244.1.7"},
				{"Pod IPv6", "fd00:10:244:1::7", "fd00:10:244:1::7"},
				{"Node IP", "172.18.0.2", "172.18.0.2"},
			},
		},
		"podIP alone, as an older API server writes it": {
			status: "podIP: 10.244.1.7\n  hostIP: 172.18.0.2",
			want:   [][3]string{{"Pod IP", "10.244.1.7", "10.244.1.7"}, {"Node IP", "172.18.0.2", "172.18.0.2"}},
		},
		"no address yet: both rows, empty, nothing to copy": {
			status: "phase: Pending",
			want:   [][3]string{{"Pod IP", "", ""}, {"Node IP", "", ""}},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			pod := object(t, "apiVersion: v1\nkind: Pod\nmetadata:\n  name: web-0\n  namespace: shop\nspec:\n  nodeName: worker-1\nstatus:\n  "+c.status+"\n")
			got := addressRows(factsOf(pod, schema.GroupVersionKind{Version: "v1", Kind: "Pod"}))
			if len(got) != len(c.want) {
				t.Fatalf("address rows %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("row %d is %v, want %v", i, got[i], c.want[i])
				}
			}
		})
	}
}

// The rows follow Node, where a person reading where the pod runs looks next.
func TestThePodsAddressesFollowItsNode(t *testing.T) {
	pod := object(t, "apiVersion: v1\nkind: Pod\nmetadata:\n  name: web-0\n  namespace: shop\nspec:\n  nodeName: worker-1\nstatus:\n  podIP: 10.244.1.7\n  hostIP: 172.18.0.2\n")
	facts := factsOf(pod, schema.GroupVersionKind{Version: "v1", Kind: "Pod"})
	for i, f := range facts {
		if f.Label == "Node" {
			if i+2 >= len(facts) || facts[i+1].Label != "Pod IP" || facts[i+2].Label != "Node IP" {
				t.Fatalf("after Node: %+v", facts[i+1:])
			}
			return
		}
	}
	t.Fatal("no Node row")
}

// Only a Pod: a Service has a cluster IP of its own, and a Deployment none.
func TestOnlyAPodGainsAddressRows(t *testing.T) {
	svc := object(t, "apiVersion: v1\nkind: Service\nmetadata:\n  name: web\n  namespace: shop\nstatus:\n  podIP: 10.244.1.7\n  hostIP: 172.18.0.2\n")
	if rows := addressRows(factsOf(svc, schema.GroupVersionKind{Version: "v1", Kind: "Service"})); len(rows) != 0 {
		t.Errorf("a Service gained address rows: %v", rows)
	}
	for _, f := range factsOf(svc, schema.GroupVersionKind{Version: "v1", Kind: "Service"}) {
		if f.Copy != "" {
			t.Errorf("%s offers a copy button on a Service", f.Label)
		}
	}
}
