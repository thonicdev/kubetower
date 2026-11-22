package web

import "testing"

// Where one column is both the first and the namespace, it is the namespace.
//
// The arm testing `eq $i $.List.NamespaceAt` comes *before* the one testing
// `eq $i $.List.NameAt`, so on such a table the namespace control is drawn and
// never the `.name` span.
//
// The Events descriptor's first column is Namespace. With `case 0` tested first,
// every Events cell would come back `role: "name"` and `role: "namespace"` would never be
// produced on that list at all - so the one gesture a namespace cell exists for
// ("a table of four hundred pods across thirty namespaces is read by finding one
// interesting row and then wanting its neighbors") would be missing from the one list
// that most needs it, and nothing would fail.
func TestANamespaceColumnIsANamespaceEvenWhenItIsAlsoTheFirst(t *testing.T) {
	// The Events shape: namespace first.
	if got := roleOf(0, 0); got != "namespace" {
		t.Errorf("the first column is also the namespace and came back %q; the list page "+
			"draws the namespace control there and not the name styling", got)
	}

	// The ordinary shape, unchanged: name first, namespace second.
	if got := roleOf(0, 1); got != "name" {
		t.Errorf("the first column of a table whose namespace is elsewhere came back %q", got)
	}
	if got := roleOf(1, 1); got != "namespace" {
		t.Errorf("the namespace column came back %q", got)
	}
	if got := roleOf(2, 1); got != "other" {
		t.Errorf("an ordinary column came back %q", got)
	}

	// A cluster-scoped table has no namespace at all, which is why an absent role
	// has to be expressible: -1 must not collide with a real position.
	if got := roleOf(0, -1); got != "name" {
		t.Errorf("the first column of a cluster-scoped table came back %q", got)
	}
	if got := roleOf(3, -1); got != "other" {
		t.Errorf("a column of a cluster-scoped table came back %q", got)
	}
}
