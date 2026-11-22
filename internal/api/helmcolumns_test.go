package api

import (
	"context"
	"encoding/json"
	"testing"
)

// The Helm list's column arrangement rides beside its rows, as a ListPage's
// does, and a failed list carries none: it has no rows to draw them over.
func TestTheHelmListCarriesItsColumnsBesideItsRows(t *testing.T) {
	reading, _ := fakeReads{}.HelmReleases(context.Background(), "prod", nil)

	body, err := json.Marshal(helmList(reading, nil))
	if err != nil {
		t.Fatal(err)
	}
	var ok struct {
		Columns           []Column `json:"columns"`
		ColumnsCustomised bool     `json:"columnsCustomised"`
	}
	if err := json.Unmarshal(body, &ok); err != nil {
		t.Fatal(err)
	}
	if len(ok.Columns) == 0 || len(ok.Columns) != len(reading.Columns) || !ok.ColumnsCustomised {
		t.Errorf("the list carries %d columns, customized=%v; want %d and true: %s",
			len(ok.Columns), ok.ColumnsCustomised, len(reading.Columns), body)
	}

	failed, err := json.Marshal(helmList(reading, Unreachable("no route to the API server")))
	if err != nil {
		t.Fatal(err)
	}
	var none map[string]any
	if err := json.Unmarshal(failed, &none); err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{"columns", "columnsCustomised", "accessNote"} {
		if _, there := none[member]; there {
			t.Errorf("a failed list carries %q: %s", member, failed)
		}
	}
}
