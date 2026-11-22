package access_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

var listSecrets = access.Attributes{Verb: "list", Version: "v1", Resource: "secrets"}

// secretReaders: alice may list Secrets in `shop` only, carol everywhere, bob
// nowhere.
func secretReaders(q accesstest.Question) (bool, error) {
	a := q.Attributes
	for _, g := range q.Groups {
		switch {
		case g == "oidc:shop-admins" && a.Resource == "secrets" && a.Verb == "list" && a.Namespace == "shop":
			return true, nil
		case g == "oidc:platform" && a.Resource == "secrets" && a.Verb == "list":
			return true, nil
		}
	}
	return false, nil
}

// perNamespace is a cluster's Secrets read one namespace at a time, counting
// which namespaces were read and whether each read was told it was narrowing.
type perNamespace struct {
	rows      []row
	mu        sync.Mutex
	read      []string
	narrowing []bool
	located   int
}

func (p *perNamespace) namespaces(ctx context.Context) ([]string, error) {
	p.mu.Lock()
	p.located++
	p.narrowing = append(p.narrowing, access.Narrowing(ctx))
	p.mu.Unlock()
	out := make([]string, 0, len(p.rows))
	for _, r := range p.rows {
		out = append(out, r.namespace)
	}
	return out, nil
}

func (p *perNamespace) in(ctx context.Context, ns string) ([]row, error) {
	p.mu.Lock()
	p.read = append(p.read, ns)
	p.narrowing = append(p.narrowing, access.Narrowing(ctx))
	p.mu.Unlock()
	var out []row
	for _, r := range p.rows {
		if ns == "" || r.namespace == ns {
			out = append(out, r)
		}
	}
	return out, nil
}

// TestANarrowedListInReadsOnlyThePersonsNamespaces is the Secrets case:
// alice, who may list Secrets in `shop` only, is shown shop's - and the
// console read no other namespace's rows to show them to her. The namespaces
// were located once, and both reads were told they were narrowing.
func TestANarrowedListInReadsOnlyThePersonsNamespaces(t *testing.T) {
	g := inCluster(&accesstest.Reviews{Answer: secretReaders}, nil)
	p := &perNamespace{rows: clusterRows}

	items, seen, err := access.ListIn(alice(), g, "prod", listSecrets, p.namespaces, p.in)
	if err != nil {
		t.Fatalf("ListIn: %v", err)
	}
	if !seen.Narrowed {
		t.Error("the list was not marked narrowed")
	}
	if len(items) != 2 || items[0].namespace != "shop" || items[1].namespace != "shop" {
		t.Errorf("alice was shown %v, want shop's two rows", items)
	}
	if len(p.read) != 1 || p.read[0] != "shop" {
		t.Errorf("rows were read in %q, want shop only: a narrowed list read rows it then dropped", p.read)
	}
	if p.located != 1 {
		t.Errorf("the namespaces were located %d times, want once", p.located)
	}
	for i, narrowing := range p.narrowing {
		if !narrowing {
			t.Errorf("read %d of a narrowed list was not told it was narrowing, so it has no bound", i)
		}
	}
}

// A person who may list every namespace is read once, across all of them, and
// nothing is located; a person who may list nowhere is shown nothing and the
// console reads no row.
func TestAListInAllowedWholeIsOneReadAndDeniedEverywhereIsNone(t *testing.T) {
	g := inCluster(&accesstest.Reviews{Answer: secretReaders}, nil)

	whole := &perNamespace{rows: clusterRows}
	items, seen, err := access.ListIn(carol(), g, "prod", listSecrets, whole.namespaces, whole.in)
	if err != nil || seen.Narrowed || len(items) != len(clusterRows) {
		t.Fatalf("carol, who may list everywhere, got %d rows (narrowed %t, %v)", len(items), seen.Narrowed, err)
	}
	if len(whole.read) != 1 || whole.read[0] != "" || whole.located != 0 {
		t.Errorf("a whole list read %q and located %d times, want one read across every namespace", whole.read, whole.located)
	}

	none := &perNamespace{rows: clusterRows}
	items, seen, err = access.ListIn(bob(), g, "prod", listSecrets, none.namespaces, none.in)
	if err != nil || !seen.Narrowed || len(items) != 0 || items == nil {
		t.Fatalf("bob, who may list nowhere, got %v (narrowed %t, %v), want an empty narrowed list", items, seen.Narrowed, err)
	}
	if len(none.read) != 0 {
		t.Errorf("rows were read in %q for a person who may list none of them", none.read)
	}

	one := &perNamespace{rows: clusterRows}
	inShop := listSecrets
	inShop.Namespace = "billing"
	if _, _, err := access.ListIn(alice(), g, "prod", inShop, one.namespaces, one.in); err == nil {
		t.Error("alice was answered a namespace she may not list")
	}
	if len(one.read) != 0 || one.located != 0 {
		t.Errorf("a refused namespace read %q and located %d times", one.read, one.located)
	}
}

// Past NarrowedReads namespaces the person may list, ListIn reads none of them
// one at a time and asks for a namespace: twenty is what List's narrowing read
// costs in pages.
func TestAListInAcrossTooManyAllowedNamespacesIsRefused(t *testing.T) {
	g := inCluster(&accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		return q.Attributes.Namespace != "", nil
	}}, nil)
	p := &perNamespace{rows: rowsIn(access.NarrowedReads + 1)}

	_, _, err := access.ListIn(bob(), g, "prod", listSecrets, p.namespaces, p.in)
	var refused *access.NotNarrowed
	if !errors.As(err, &refused) {
		t.Fatalf("a list the person may make in %d namespaces was not refused: %v", access.NarrowedReads+1, err)
	}
	if len(p.read) != 0 {
		t.Errorf("%d namespaces were read before the refusal", len(p.read))
	}

	at := &perNamespace{rows: rowsIn(access.NarrowedReads)}
	items, _, err := access.ListIn(bob(), g, "prod", listSecrets, at.namespaces, at.in)
	if err != nil || len(items) != access.NarrowedReads || len(at.read) != access.NarrowedReads {
		t.Errorf("at the bound: %d rows from %d reads (%v), want %d of each", len(items), len(at.read), err, access.NarrowedReads)
	}
}

// Past NarrowedNamespaces the list is refused before a single review per
// namespace, as List refuses it.
func TestAListInAcrossTooManyNamespacesAsksNoReviewPerNamespace(t *testing.T) {
	counter := &inFlight{}
	g := inCluster(&accesstest.Reviews{Answer: counter.answer}, nil)
	p := &perNamespace{rows: rowsIn(access.NarrowedNamespaces + 1)}

	_, _, err := access.ListIn(bob(), g, "prod", listSecrets, p.namespaces, p.in)
	var refused *access.NotNarrowed
	if !errors.As(err, &refused) {
		t.Fatalf("a list across %d namespaces was not refused: %v", access.NarrowedNamespaces+1, err)
	}
	if counter.asked != 1 || len(p.read) != 0 {
		t.Errorf("asked %d reviews and read %d namespaces, want only the list's own review and no read",
			counter.asked, len(p.read))
	}
}

// A locating read past NarrowedRows is the same refusal List gives.
func TestAListInWhoseNamespacesAreTooManyRowsIsRefused(t *testing.T) {
	g := inCluster(&accesstest.Reviews{Answer: secretReaders}, nil)
	_, _, err := access.ListIn(alice(), g, "prod", listSecrets,
		func(context.Context) ([]string, error) { return nil, access.TooLargeToNarrow() },
		func(context.Context, string) ([]row, error) {
			t.Error("rows were read after the locating read was too large")
			return nil, nil
		})
	var refused *access.NotNarrowed
	if !errors.As(err, &refused) {
		t.Fatalf("a locating read past its bound was not refused as NotNarrowed: %v", err)
	}
}
