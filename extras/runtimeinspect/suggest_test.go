package runtimeinspect

import (
	"errors"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// The banner's evidence: the probable Java pods, counted with their
// namespaces, from one pod list over every namespace.
func TestASuggestionCountsTheJavaPodsAndTheirNamespaces(t *testing.T) {
	h := shop(t)
	got, err := New().Suggest(httptest.NewRequest("GET", "/", nil), h, "kind")
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if want := "2 probable Java pods in 2 namespaces"; got != want {
		t.Errorf("Suggest = %q, want %q", got, want)
	}
	if len(h.queries) != 1 || h.queries[0].Namespace != "" || h.queries[0].FieldSelector != "" {
		t.Errorf("queries = %+v, want one list of every pod", h.queries)
	}
}

// One pod in one namespace is said in the singular.
func TestASuggestionOfOnePodIsSingular(t *testing.T) {
	h := newHost(t, pod("data", "zk-0", corev1.Container{Name: "zk", Image: "zookeeper:3.9"}))
	if got, _ := New().Suggest(httptest.NewRequest("GET", "/", nil), h, "kind"); got != "1 probable Java pod in 1 namespace" {
		t.Errorf("Suggest = %q", got)
	}
}

// A cluster where nothing in any spec says Java is suggested nothing; a
// finished pod is not counted.
func TestNoJavaSuggestsNothing(t *testing.T) {
	done := pod("batch", "report-1", corev1.Container{Image: "eclipse-temurin:21-jre"})
	done.Status.Phase = corev1.PodFailed
	h := newHost(t, pod("web", "nginx-0", corev1.Container{Image: "nginx:1.27"}), done)
	if got, err := New().Suggest(httptest.NewRequest("GET", "/", nil), h, "kind"); err != nil || got != "" {
		t.Errorf("Suggest = %q, %v; want nothing", got, err)
	}
}

// A listing that fails is an error, never "nothing found".
func TestASuggestionThatCouldNotLookIsAnError(t *testing.T) {
	h := shop(t)
	h.listErr = errors.New("pods is forbidden")
	if got, err := New().Suggest(httptest.NewRequest("GET", "/", nil), h, "kind"); err == nil {
		t.Errorf("Suggest = %q with no error, after the listing failed", got)
	}
}

// A narrowed listing gives a floor, said as one.
func TestANarrowedListingIsCountedAsAFloor(t *testing.T) {
	h := shop(t)
	h.narrowed = true
	got, err := New().Suggest(httptest.NewRequest("GET", "/", nil), h, "kind")
	if err != nil || got != "At least 2 probable Java pods in 2 namespaces" {
		t.Errorf("Suggest = %q, %v", got, err)
	}
}
