package resources

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

// TestAListReadToBeNarrowedStopsAtItsBound: a person denied the list across
// every namespace must not cost the whole cluster's list, every page of it, on
// every refresh. Read to be narrowed, Paged stops once it holds
// access.NarrowedRows and the person is asked for a namespace.
func TestAListReadToBeNarrowedStopsAtItsBound(t *testing.T) {
	const pages = 100 // a cluster far past the bound; the fake ends here rather than never
	calls := 0
	list := func(_ context.Context, opts metav1.ListOptions) (*corev1.PodList, error) {
		calls++
		page := &corev1.PodList{Items: make([]corev1.Pod, PageSize)}
		for i := range page.Items {
			page.Items[i].Namespace = "shop"
		}
		if calls < pages {
			page.Continue = "more"
		}
		return page, nil
	}
	denied := (&accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) { return false, nil }}).Gate()
	r := Reader{Gate: denied, Cluster: "prod"}

	_, _, err := Listed(context.Background(), r, InNamespace("", "v1", "pods", ""), Cached(), list,
		func(l *corev1.PodList) []corev1.Pod { return l.Items })

	if want := access.NarrowedRows/PageSize + 1; calls > want {
		t.Errorf("%d pages were read for a list about to be narrowed, want at most %d", calls, want)
	}
	if err == nil || !strings.Contains(err.Error(), "choose a namespace") {
		t.Errorf("a list too large to narrow was not refused with a sentence: %v", err)
	}
}

// TestAWholeListStopsAtTheConsolesCeiling: a list the person may read whole
// must not follow the continue token with no bound, or one list of a large
// cluster is a memory event in a process every person shares. Paged stops
// once it holds MaxRows - no page past the one that crossed it - and
// refuses the list with a sentence rather than answering a short one.
func TestAWholeListStopsAtTheConsolesCeiling(t *testing.T) {
	const pages = 400 // a cluster far past the ceiling; the fake ends here rather than never
	calls := 0
	list := func(_ context.Context, opts metav1.ListOptions) (*corev1.PodList, error) {
		calls++
		page := &corev1.PodList{Items: make([]corev1.Pod, PageSize)}
		for i := range page.Items {
			page.Items[i].Namespace = "shop"
		}
		if calls < pages {
			page.Continue = "more"
		}
		return page, nil
	}
	r := Reader{Gate: accesstest.Allow(), Cluster: "prod"}

	items, _, err := Listed(context.Background(), r, InNamespace("", "v1", "pods", ""), Cached(), list,
		func(l *corev1.PodList) []corev1.Pod { return l.Items })

	if want := MaxRows/PageSize + 1; calls > want {
		t.Errorf("%d pages were read for one list, want at most %d", calls, want)
	}
	var tooLarge *TooLarge
	if !errors.As(err, &tooLarge) || !strings.Contains(err.Error(), "choose a namespace") {
		t.Errorf("a list past the ceiling answered %v, want TooLarge with a sentence", err)
	}
	if items != nil {
		t.Errorf("%d rows came back with the refusal; a short list would read as the whole one", len(items))
	}
}

// The control: a list of exactly MaxRows is read whole, so the ceiling above
// is a ceiling and not a lower cap on every list.
func TestAListAtTheCeilingIsReadWhole(t *testing.T) {
	calls := 0
	list := func(_ context.Context, opts metav1.ListOptions) (*corev1.PodList, error) {
		calls++
		page := &corev1.PodList{Items: make([]corev1.Pod, PageSize)}
		if calls < MaxRows/PageSize {
			page.Continue = "more"
		}
		return page, nil
	}
	items, err := Paged(context.Background(), Cached(), list, func(l *corev1.PodList) []corev1.Pod { return l.Items })
	if err != nil || len(items) != MaxRows {
		t.Fatalf("a list of exactly %d rows answered %d rows, %v", MaxRows, len(items), err)
	}
}
