package edit

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

// A Namespace is reviewed in itself. kube-apiserver reads
// `/api/v1/namespaces/x` as the namespace x, in namespace x, so a Role in x
// granting get and delete on namespaces lets kubectl read and delete x. The
// console asks the same question; a list or a create names no object and
// stays cluster-wide.

const namespaceX = `
apiVersion: v1
kind: Namespace
metadata:
  name: x
  resourceVersion: "41"
  uid: 7c1e-9
`

// TestANamespaceIsReviewedInItself pins the attributes of every review of one
// Namespace, and of its list and its create.
func TestANamespaceIsReviewedInItself(t *testing.T) {
	where := Location{Cluster: "prod", Resource: namespaceGVR, Kind: "Namespace"}
	x := Target{Cluster: "prod", Kind: "namespaces", Name: "x"}
	for _, tc := range []struct {
		got, want access.Attributes
	}{
		{attributesOf(where, x, "get", ""),
			access.Attributes{Verb: "get", Version: "v1", Resource: "namespaces", Namespace: "x", Name: "x"}},
		{attributesOf(where, x, "delete", ""),
			access.Attributes{Verb: "delete", Version: "v1", Resource: "namespaces", Namespace: "x", Name: "x"}},
		{attributesOf(where, x, "patch", ""),
			access.Attributes{Verb: "patch", Version: "v1", Resource: "namespaces", Namespace: "x", Name: "x"}},
		// Exported, for what a page offers: the same question.
		{Attributes(where, x, "delete", ""),
			access.Attributes{Verb: "delete", Version: "v1", Resource: "namespaces", Namespace: "x", Name: "x"}},
		// A create has no name yet, so no namespace: `POST /api/v1/namespaces`.
		{attributesOf(where, x, "create", ""),
			access.Attributes{Verb: "create", Version: "v1", Resource: "namespaces"}},
		{attributesOf(where, Target{Cluster: "prod", Kind: "namespaces"}, "list", ""),
			access.Attributes{Verb: "list", Version: "v1", Resource: "namespaces"}},
	} {
		if tc.got != tc.want {
			t.Errorf("reviewed %+v, want %+v", tc.got, tc.want)
		}
	}
}

// roleIn answers as RBAC does for a person whose only right is a Role in
// namespace granting get and delete on namespaces: yes to those verbs on a
// request in that namespace, no to anything else.
func roleIn(namespace string) func(accesstest.Question) (bool, error) {
	return func(q accesstest.Question) (bool, error) {
		a := q.Attributes
		return a.Group == "" && a.Resource == "namespaces" && a.Subresource == "" &&
			(a.Verb == "get" || a.Verb == "delete") && a.Namespace == namespace, nil
	}
}

// TestARoleInANamespaceDeletesIt: the Role in x deletes x through the door; a
// Role in another namespace does not, and nothing is sent.
func TestARoleInANamespaceDeletesIt(t *testing.T) {
	for _, tc := range []struct {
		role    string
		allowed bool
	}{
		{"x", true},
		{"y", false},
	} {
		h := newHarness(t, nil, object(t, namespaceX))
		h.reviews.Answer = roleIn(tc.role)
		target := Target{Cluster: "prod", Kind: "namespaces", Name: "x"}
		where, err := h.writer.Locate(carol(), target)
		if err != nil {
			t.Fatal(err)
		}
		_, err = Delete(carol(), where, target, metav1.DeleteOptions{})
		deleted := false
		for _, action := range h.dyn.Actions() {
			if action.GetVerb() == "delete" && action.GetResource().Resource == "namespaces" {
				deleted = true
			}
		}
		switch {
		case tc.allowed && (err != nil || !deleted):
			t.Errorf("a Role in %s: the delete of x answered %v, sent %v", tc.role, err, deleted)
		case !tc.allowed && (err == nil || deleted):
			t.Errorf("a Role in %s: the delete of x answered %v, sent %v; want refused, nothing sent", tc.role, err, deleted)
		}
		asked := h.reviews.Asked()
		if len(asked) != 1 || asked[0].Attributes.Namespace != "x" || asked[0].Attributes.Name != "x" {
			t.Errorf("a Role in %s: the reviews asked %+v", tc.role, asked)
		}
	}
}
