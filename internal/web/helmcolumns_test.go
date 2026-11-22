package web

import (
	"context"
	"errors"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/resources"
)

// The Helm list's columns are arranged by the same store and the same writes as
// a resource list's, under the kind `helm`: hiding one by the key the list
// answer carries hides it in the next answer, and Reset brings it back.
func TestTheHelmListsColumnsAreKeptByTheColumnStore(t *testing.T) {
	server := helmPageFixture(t,
		helmRevisionSecret(t, 1, "deployed", "Install complete", "", nil),
	)
	reads, prefs := apiReads{server}, apiPrefs{server}

	reading, fail := reads.HelmReleases(context.Background(), "prod", nil)
	if fail != nil {
		t.Fatalf("the release list failed: %+v", fail.Reason)
	}
	if len(reading.Releases) != 1 {
		t.Fatalf("%d releases, want 1; there is nothing to arrange", len(reading.Releases))
	}
	if got, want := len(reading.Columns), len(helmColumns); got != want {
		t.Fatalf("the answer carries %d columns, want the %d the list declares", got, want)
	}
	if reading.ColumnsCustomised {
		t.Error("a list nobody arranged says something is stored for it")
	}
	byLabel := map[string]api.Column{}
	for _, column := range reading.Columns {
		byLabel[column.Label] = column
		if !column.Visible {
			t.Errorf("%q is hidden on a list nobody arranged", column.Label)
		}
		if column.FilterValues != nil {
			t.Errorf("%q carries filterValues; the Helm list collects them in the browser", column.Label)
		}
	}
	for label, filterable := range map[string]bool{"Status": true, "Chart": true, "Namespace": false, "Name": false} {
		if byLabel[label].Filterable != filterable {
			t.Errorf("%q filterable=%v, want %v", label, byLabel[label].Filterable, filterable)
		}
	}
	if byLabel["Name"].LockedNote == "" {
		t.Error("Name can be hidden; it is what says which release each row is")
	}

	description := byLabel["Description"]
	if err := prefs.SetColumnVisible(helmKind, description.Key, false); err != nil {
		t.Fatalf("hiding %q by its key %q: %v", description.Label, description.Key, err)
	}
	after, fail := reads.HelmReleases(context.Background(), "prod", nil)
	if fail != nil {
		t.Fatalf("the release list failed after the write: %+v", fail.Reason)
	}
	if !after.ColumnsCustomised {
		t.Error("the answer does not say something is stored after a column was hidden")
	}
	for _, column := range after.Columns {
		if column.Key == description.Key && column.Visible {
			t.Errorf("%q is drawn after it was hidden", column.Label)
		}
	}

	// A key the list does not declare is a refusal, as on a resource list.
	err := prefs.SetColumnVisible(helmKind, "not-a-column-3", false)
	if !errors.Is(err, api.ErrInvalid) {
		t.Errorf("an unknown key answered %v, want an ErrInvalid", err)
	}

	if err := prefs.ResetColumns(helmKind, "all"); err != nil {
		t.Fatalf("ResetColumns: %v", err)
	}
	reset, _ := reads.HelmReleases(context.Background(), "prod", nil)
	if reset.ColumnsCustomised {
		t.Error("the answer still says something is stored after Reset")
	}
}

// `helm` must stay a name no resource kind answers to: the column store files
// both under one namespace of kinds, and a kind called helm would share its
// arrangement with this list.
func TestHelmIsNotAResourceKind(t *testing.T) {
	if _, ok := resources.Lookup(helmKind); ok {
		t.Fatalf("internal/resources answers to %q; the Helm list's columns would be filed with that kind's", helmKind)
	}
}
