package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// The `filter` parameter is repeatable, and every value reaches the reader.
//
// Two nodes chosen in the filter control are two parameters. `q.Get` returns
// the first, so a handler reading it would narrow "on either node" to "on the
// first" and draw a list that looks complete - the namespace parameter's defect, on
// another repeatable parameter.
func TestEveryFilterOfARepeatedParameterReachesTheReader(t *testing.T) {
	query := url.Values{}
	query.Add("filter", "Node:worker-2")
	query.Add("filter", "Node:worker-3")
	query.Add("filter", "Storage class:gp3-encrypted")
	asked := askedFor(t, "?"+query.Encode())
	want := []string{"Node:worker-2", "Node:worker-3", "Storage class:gp3-encrypted"}
	if strings.Join(asked.Filters, "|") != strings.Join(want, "|") {
		t.Errorf("the reader was asked for %q, want %q", asked.Filters, want)
	}
	if got := askedFor(t, "?search=node:worker-2").Filters; len(got) != 0 {
		t.Errorf("a search alone became the filters %q; the box is plain text", got)
	}
}

// Bounded, like the search, and by the number the contract declares.
func TestTooManyFiltersAreRefused(t *testing.T) {
	source, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatalf("the contract is not readable: %v", err)
	}
	text := string(source)
	at := strings.Index(text, "        - name: filter\n")
	if at < 0 {
		t.Fatal("the filter parameter is gone from api/openapi.yaml")
	}
	block := text[at:]
	if end := strings.Index(block[1:], "        - "); end > 0 {
		block = block[:end+1]
	}
	if !strings.Contains(block, fmt.Sprintf("maxItems: %d", maxListFilters)) {
		t.Errorf("the contract's filter parameter does not declare maxItems: %d:\n%s", maxListFilters, block)
	}

	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)
	ask := func(query url.Values) int {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/c/prod/r/pods?"+query.Encode(), nil))
		return recorder.Code
	}

	many := url.Values{}
	for i := 0; i <= maxListFilters; i++ {
		many.Add("filter", fmt.Sprintf("Node:worker-%d", i))
	}
	if code := ask(many); code != http.StatusBadRequest {
		t.Errorf("%d filters answered %d, want 400", maxListFilters+1, code)
	}
	long := url.Values{"filter": {"Node:" + strings.Repeat("x", 512)}}
	if code := ask(long); code != http.StatusBadRequest {
		t.Errorf("a filter of 517 bytes answered %d, want 400", code)
	}
	if code := ask(url.Values{"filter": {"Node:worker-2"}}); code != http.StatusOK {
		t.Errorf("one filter answered %d, want 200", code)
	}
}
