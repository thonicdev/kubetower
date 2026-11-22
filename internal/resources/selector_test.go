package resources

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

func labelled(namespace, name, app string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: map[string]string{"app": app}}}
}

// selectorsAsked is every list's label selector as the fake API server
// received it, in order.
type selectorsAsked struct {
	mu   sync.Mutex
	seen []string
}

func (s *selectorsAsked) record(c *fake.Clientset) {
	c.PrependReactor("list", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.seen = append(s.seen, action.(k8stesting.ListAction).GetListRestrictions().Labels.String())
		return false, nil, nil
	})
}

func (s *selectorsAsked) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

func rowNames(table Table) string {
	var out []string
	for _, row := range table.Rows {
		out = append(out, row.Namespace+"/"+row.Name)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// A selector typed in the search reaches the API server as the list's
// labelSelector, and the rows are those it selects.
func TestASelectorReachesTheListAsItsLabelSelector(t *testing.T) {
	clients := fake.NewClientset(
		labelled("shop", "web-1", "web"), labelled("shop", "web-2", "web"), labelled("shop", "db-0", "db"))
	asked := &selectorsAsked{}
	asked.record(clients)
	c := &cluster.Client{Context: "test", Clients: clients}

	table, err := List(context.Background(), accesstest.Allow(), c, Query{Kind: "pods", Selector: "app=web"}, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := rowNames(table); got != "shop/web-1,shop/web-2" {
		t.Errorf("the list holds %s, want the two web pods", got)
	}
	if got := asked.all(); len(got) != 1 || got[0] != "app=web" {
		t.Errorf("the API server was asked with selectors %q, want one list with app=web", got)
	}
}

// A selector that does not parse is refused as one, before anything is read:
// a list read without it would show every row as if each matched.
func TestAMalformedSelectorIsRefusedAndNothingIsRead(t *testing.T) {
	clients := fake.NewClientset(labelled("shop", "web-1", "web"))
	c := &cluster.Client{Context: "test", Clients: clients}

	_, err := List(context.Background(), accesstest.Allow(), c, Query{Kind: "pods", Selector: "app=web,=db"}, nil)
	var invalid *InvalidSelector
	if !errors.As(err, &invalid) {
		t.Fatalf("a malformed selector answered %v, want InvalidSelector", err)
	}
	if !strings.Contains(err.Error(), "app=web,=db") {
		t.Errorf("the refusal %q does not name the selector", err)
	}
	if actions := clients.Actions(); len(actions) != 0 {
		t.Errorf("the cluster was asked %d times for a selector that does not parse", len(actions))
	}
}

func TestParseSelector(t *testing.T) {
	for raw, want := range map[string]string{
		"":                      "",
		"  ":                    "",
		"app=web":               "app=web",
		" app = web ":           "app=web",
		"tier in (a,b),!canary": "!canary,tier in (a,b)",
	} {
		got, err := ParseSelector(raw)
		if err != nil || got != want {
			t.Errorf("ParseSelector(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"app=web,=db", "app in (", "a b"} {
		if _, err := ParseSelector(raw); err == nil {
			t.Errorf("ParseSelector(%q) parsed; want a refusal", raw)
		}
	}
}

// A person who may list Pods in one namespace only: the list is read across
// the cluster and narrowed to that namespace, and the selector still goes
// with the read.
func TestANarrowedListKeepsTheSelector(t *testing.T) {
	clients := fake.NewClientset(
		labelled("shop", "web-1", "web"), labelled("shop", "db-0", "db"), labelled("billing", "web-9", "web"))
	asked := &selectorsAsked{}
	asked.record(clients)
	c := &cluster.Client{Context: "test", Clients: clients}
	shopPods := func(q accesstest.Question) (bool, error) {
		a := q.Attributes
		return a.Resource == "pods" && a.Verb == "list" && a.Namespace == "shop", nil
	}
	gate := access.New(access.Options{Reviews: (&accesstest.Reviews{Answer: shopPods}).For, InCluster: true})

	table, err := List(alice(), gate, c, Query{Kind: "pods", Selector: "app=web"}, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !table.Access.Narrowed {
		t.Error("the list was not narrowed; the test is not on the narrowed path")
	}
	if got := rowNames(table); got != "shop/web-1" {
		t.Errorf("alice was shown %s, want shop's web pod alone", got)
	}
	selectors := asked.all()
	if len(selectors) == 0 {
		t.Fatal("the API server was asked for no list")
	}
	for _, selector := range selectors {
		if selector != "app=web" {
			t.Errorf("a narrowed read asked with selector %q, want app=web", selector)
		}
	}
}

// The Secrets list narrowed per namespace reads each namespace with the
// selector, and finds where the Secrets are with it too.
func TestANarrowedSecretsListKeepsTheSelector(t *testing.T) {
	s := &secretsServer{}
	r := secretsReader(t, s, shopOnly)
	r.Selector = "app=web"

	if _, _, err := fetchSecrets(alice(), r, ""); err != nil {
		t.Fatalf("fetchSecrets: %v", err)
	}
	selectors := s.selectorsAsked()
	if len(selectors) != 2 {
		t.Fatalf("the console made %d requests, want a metadata list and shop's list: %q", len(selectors), selectors)
	}
	for _, selector := range selectors {
		if selector != "app=web" {
			t.Errorf("a request asked with labelSelector %q, want app=web", selector)
		}
	}
}
