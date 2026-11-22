package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The `namespace` parameter is repeatable, and the handler has to hear all of it.
//
// `api/openapi.yaml` declares it `type: array` with `explode: true`, and says on
// the parameter itself why: *"the namespace selector is a
// multiple selection — 'pick another to add it' — and a list narrowed to four
// namespaces is how a cluster with thirty is actually read."* It also names the
// failure this test exists to prevent: *"a client that
// substitutes one for the other shows a fraction of a cluster with no sign that
// it did."*
//
// A handler reading `q.Get("namespace")` would return the **first** value and
// discard the rest. So `?namespace=a&namespace=b` would narrow to `a`, answer 200,
// and draw a table that looked complete. There is no observable difference
// between that and a cluster where `b` happens to be empty, which is exactly the
// empty-versus-failed collapse one layer down.
//
// Both spellings are asserted, because both reach one parser: `parseNamespaces`
// in `internal/web` splits on commas, so `?namespace=a,b` is a URL form that
// must keep working alongside the contract's.

// capturingReads records the query it was asked, and answers a bare page.
//
// It embeds `fakeReads` so that every other method keeps whatever that fake
// already answers — a second full fake here would be a second thing to keep in
// step with the contract, which is the defect `fakes_test.go` was written against.
type capturingReads struct {
	fakeReads
	asked *ListQuery
}

func (c capturingReads) ListResources(_ context.Context, q ListQuery) (ListPage, *Failure) {
	*c.asked = q
	return ListPage{Kind: "pods", Label: "Pods", Namespaced: true}, nil
}

func askedFor(t *testing.T, query string) ListQuery {
	t.Helper()
	var asked ListQuery
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: capturingReads{asked: &asked},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/c/prod/r/pods"+query, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/c/prod/r/pods%s answered %d: %s", query, recorder.Code, recorder.Body.String())
	}
	return asked
}

func TestEveryNamespaceOfARepeatedParameterReachesTheReader(t *testing.T) {
	// The contract's form. Two values, and the reader must be told both.
	asked := askedFor(t, "?namespace=shop-prod&namespace=argocd")
	if !strings.Contains(asked.Namespace, "shop-prod") || !strings.Contains(asked.Namespace, "argocd") {
		t.Errorf("the reader was asked for %q from ?namespace=shop-prod&namespace=argocd — "+
			"a selection of two narrowed to one, and the table that comes back looks "+
			"complete, which is the whole failure the parameter's description names",
			asked.Namespace)
	}

	// The comma-separated form, which the same parser handles. Both spellings must
	// keep working: a saved link carries this one.
	if got := askedFor(t, "?namespace=shop-prod,argocd").Namespace; got != "shop-prod,argocd" {
		t.Errorf("the reader was asked for %q from ?namespace=shop-prod,argocd", got)
	}

	// And the other direction, because a test that only ever adds cannot catch a
	// handler that invents. Absent means every namespace the caller may read,
	// which is deliberately not `default`.
	if got := askedFor(t, "").Namespace; got != "" {
		t.Errorf("no namespace was asked for and the reader was told %q; absent is not a selection", got)
	}
	if got := askedFor(t, "?search=api").Namespace; got != "" {
		t.Errorf("a search alone put %q in the namespace", got)
	}
}

// The declaration and the handler agree about the parameter being repeatable.
//
// Read out of the YAML at run time rather than restated here, which is the
// pattern `listmembers_test.go` establishes: a test carrying its own copy of the
// contract agrees with itself.
func TestTheListEndpointDeclaresARepeatableNamespace(t *testing.T) {
	source, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatalf("the contract is not readable: %v", err)
	}
	text := string(source)

	// The parameter's own block: from its key to the next key at the same indent.
	// Sliced by line rather than with strings.Index on "\n    ", which matches
	// every *deeper*-indented line too.
	lines := strings.Split(text, "\n")
	at := -1
	for i, line := range lines {
		if line == "    Namespace:" && i+1 < len(lines) && lines[i+1] == "      name: namespace" {
			at = i
			break
		}
	}
	if at < 0 {
		t.Fatal("the Namespace query parameter is gone from api/openapi.yaml")
	}
	end := len(lines)
	for i := at + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     ") &&
			strings.TrimSpace(line) != "" {
			end = i
			break
		}
	}
	block := strings.Join(lines[at:end], "\n")

	if !strings.Contains(block, "explode: true") || !strings.Contains(block, "type: array") {
		t.Errorf("the list's namespace parameter is no longer a repeatable array:\n%s\n\n"+
			"If that is deliberate, the selector it feeds has to lose the ability to "+
			"select several — a picker that can express a selection against a parameter "+
			"that cannot carry one silently narrows what the operator asked for.", block)
	}
}
