package helm

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// **What a person cannot see must not move what they are told.** A release list
// that counted every release Secret the cluster sent towards its ceiling -
// including those in namespaces the person may not list, read only to be thrown
// away - would print a note whose number grows with another team's releases,
// and enough of them would use the ceiling up before the person's own were
// reached.

// selectedOnly is a person who may list Secrets in `argocd` and `cache`, one
// namespace at a time, and nowhere else: not across every namespace, and not in
// `accounts`, which belongs to somebody else - and which sorts first, as the
// API server returns a listing in key order, so its Secrets arrive before the
// person's own.
func selectedOnly(q accesstest.Question) (bool, error) {
	a := q.Attributes
	return a.Verb == "list" && a.Resource == "secrets" && (a.Namespace == "argocd" || a.Namespace == "cache"), nil
}

func readerFor(answer func(accesstest.Question) (bool, error), objects ...runtime.Object) (*Reader, *fake.Clientset) {
	api := fake.NewSimpleClientset(objects...)
	gate := access.New(access.Options{Reviews: (&accesstest.Reviews{Answer: answer}).For})
	return NewReader(stubClusters{client: &cluster.Client{Context: "test", Clients: api}}, gate), api
}

// releases is n current releases in one namespace, one revision each.
func releases(t *testing.T, namespace string, n int) []runtime.Object {
	t.Helper()
	out := make([]runtime.Object, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, revisionOf(t, fmt.Sprintf("app-%03d", i), namespace, 1, StatusDeployed,
			"1.0.0", "1.0.0", "", time.Now()))
	}
	return out
}

func TestAHiddenNamespaceDoesNotMoveTheCeilingsNumber(t *testing.T) {
	// The person's own namespaces hold more than the ceiling, so the page is
	// capped either way and says so: the question is only what number it says.
	mine := append(releases(t, "argocd", releaseLimit/2+5), releases(t, "cache", releaseLimit/2+5)...)

	quiet, _ := readerFor(selectedOnly, mine...)
	busy, _ := readerFor(selectedOnly, append(append([]runtime.Object{}, mine...),
		releases(t, "accounts", releaseLimit+50)...)...)

	selection := []string{"argocd", "cache"}
	without := quiet.Releases(t.Context(), "test", selection)
	with := busy.Releases(t.Context(), "test", selection)

	if without.State != StatePresent || with.State != StatePresent {
		t.Fatalf("states %v and %v, want both present: %s / %s", without.State, with.State, without.Detail, with.Detail)
	}
	if without.Note == "" {
		t.Fatal("the selection holds more than the ceiling and the page did not say it was capped")
	}
	if with.Note != without.Note {
		t.Errorf("a namespace this person cannot see changed what they are told:\n without: %q\n    with: %q",
			without.Note, with.Note)
	}
	if len(with.Releases) != len(without.Releases) {
		t.Errorf("%d rows with another team's releases in the cluster, %d without: their releases used up the ceiling",
			len(with.Releases), len(without.Releases))
	}
	for _, r := range with.Releases {
		if r.Namespace == "accounts" {
			t.Fatalf("a release in a namespace the person may not list is on the page: %s/%s", r.Namespace, r.Name)
		}
	}
}

// Below the ceiling, the other team's releases are not read at all: each
// selected namespace is listed on its own, already reviewed, so nothing is
// fetched only to be thrown away - and nothing is reviewed per namespace met.
func TestASelectionIsReadOneReviewedNamespaceAtATime(t *testing.T) {
	objects := append(releases(t, "argocd", 3), releases(t, "accounts", 40)...)
	reader, api := readerFor(selectedOnly, append(objects, releases(t, "cache", 2)...)...)

	report := reader.Releases(t.Context(), "test", []string{"argocd", "cache"})
	if report.State != StatePresent || report.Note != "" || len(report.Releases) != 5 {
		t.Fatalf("state %v, note %q, %d rows; want present, no note, the 5 selected", report.State, report.Note, len(report.Releases))
	}
	var listed []string
	for _, action := range api.Actions() {
		if action.Matches("list", "secrets") {
			listed = append(listed, action.GetNamespace())
		}
	}
	if fmt.Sprint(listed) != "[argocd cache]" {
		t.Errorf("listed Secrets in %q, want each selected namespace once and nothing across the cluster", listed)
	}
}

// The page a person is refused reads as sentences: the review's own words,
// which start in lower case, are capitalized where they follow a full stop.
func TestARefusedPageStartsEachSentenceInUpperCase(t *testing.T) {
	reader, _ := readerFor(selectedOnly)
	report := reader.Releases(t.Context(), "test", nil)
	if report.State != StateUnreadable {
		t.Fatalf("state = %v, want the refusal", report.State)
	}
	if strings.Contains(report.Detail, ". the ") {
		t.Errorf("a sentence starts in lower case: %q", report.Detail)
	}
	if !strings.Contains(report.Detail, ". The cluster says ") {
		t.Errorf("detail = %q, want the review's words as their own sentence", report.Detail)
	}
}
