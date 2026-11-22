package customresources

import (
	"errors"
	"testing"

	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/resources"
)

// A custom kind's list hands the selector to the API server, and the rows are
// those it selects.
func TestACustomKindsListTakesTheSelector(t *testing.T) {
	web := widget(t, "web", "apps", `{"metadata": {"labels": {"app": "web"}}}`)
	db := widget(t, "db", "apps", `{"metadata": {"labels": {"app": "db"}}}`)
	registry, client, fake := newRegistry(t, object(t, widgetCRD), web, db)

	table, err := registry.List(t.Context(), accesstest.Allow(), client, "widgets.example.io", "", nil, "app=web", "", nil)
	if err != nil {
		t.Fatalf("listing widgets: %v", err)
	}
	if len(table.Rows) != 1 || table.Rows[0].Name != "web" {
		t.Errorf("the list holds %+v, want the web widget alone", table.Rows)
	}
	asked := false
	for _, action := range fake.Actions() {
		list, ok := action.(k8stesting.ListAction)
		if !ok || action.GetResource() != widgetGVR {
			continue
		}
		asked = true
		if got := list.GetListRestrictions().Labels.String(); got != "app=web" {
			t.Errorf("the widgets were listed with selector %q, want app=web", got)
		}
	}
	if !asked {
		t.Error("the widgets were never listed")
	}
}

func TestACustomKindsListRefusesAMalformedSelector(t *testing.T) {
	registry, client, fake := newRegistry(t, object(t, widgetCRD))

	_, err := registry.List(t.Context(), accesstest.Allow(), client, "widgets.example.io", "", nil, "app in (", "", nil)
	var invalid *resources.InvalidSelector
	if !errors.As(err, &invalid) {
		t.Fatalf("a malformed selector answered %v, want InvalidSelector", err)
	}
	if n := len(fake.Actions()); n != 0 {
		t.Errorf("the cluster was asked %d times for a selector that does not parse", n)
	}
}
