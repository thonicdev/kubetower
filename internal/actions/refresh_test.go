package actions

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/edit"
)

// RefreshApplication is the narrow write door an extension is handed: one
// annotation on one Application, through the same guarded path as every verb.

func argoApplication(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": name, "namespace": namespace, "uid": "uid-" + name, "resourceVersion": "31"},
		"spec":       map[string]any{"project": "default"},
	}}
}

func TestARefreshSetsOneAnnotationAndNothingElse(t *testing.T) {
	h := newHarness(t, nil, argoApplication("argocd", "guestbook"))

	if err := h.runner.RefreshApplication(context.Background(), "prod", "argocd", "guestbook"); err != nil {
		t.Fatal(err)
	}
	writes := h.writes()
	// A dry run, then the write, both patches of that one Application.
	if len(writes) != 2 || !writes[0].DryRun || writes[1].DryRun {
		t.Fatalf("expected a dry run then the write, got %+v", writes)
	}
	for _, w := range writes {
		if w.Verb != "patch" || w.Resource != "applications" || w.Name != "guestbook" || w.Namespace != "argocd" {
			t.Errorf("unexpected request %+v", w)
		}
		want := `{"metadata":{"annotations":{"argocd.argoproj.io/refresh":"normal"},"resourceVersion":"31","uid":"uid-guestbook"}}`
		if w.Body != want {
			t.Errorf("the patch is %s, want %s", w.Body, want)
		}
	}
	// The `write:` line says who, what and the outcome.
	var line string
	for _, l := range h.logged {
		if strings.HasPrefix(l, "write:") && strings.Contains(l, "action=refresh") {
			line = l
		}
	}
	if !strings.Contains(line, "name=guestbook") || !strings.Contains(line, "outcome=applied") {
		t.Errorf("no `write:` line for the refresh in %q", h.logged)
	}
	// And the gate's own record carries the patch.
	if records := h.records.all(t); len(records) == 0 {
		t.Error("the gate wrote no audit record")
	}
}

func TestARefreshThePersonMayNotMakeWritesNothing(t *testing.T) {
	h := newHarness(t, nil, argoApplication("argocd", "guestbook"))
	h.reviews.Answer = func(q accesstest.Question) (bool, error) { return q.Attributes.Verb != "patch", nil }

	err := h.runner.RefreshApplication(context.Background(), "prod", "argocd", "guestbook")
	if out := refusal(t, err); out.Kind != edit.RefusedByAccess {
		t.Fatalf("expected an access refusal, got %v: %s", out.Kind, out.Reason)
	}
	h.noWrites()
}

func TestARefreshOnAReadOnlyClusterWritesNothing(t *testing.T) {
	h := newHarness(t, func(string) bool { return true }, argoApplication("argocd", "guestbook"))
	if out := refusal(t, h.runner.RefreshApplication(context.Background(), "prod", "argocd", "guestbook")); out.Kind != edit.RefusedReadOnly {
		t.Fatalf("got %v: %s", out.Kind, out.Reason)
	}
	h.noWrites()
}

func TestARefreshOfAnApplicationThatMovedIsRefusedAsAConflict(t *testing.T) {
	h := newHarness(t, nil, argoApplication("argocd", "guestbook"))
	// What the API server answers when the resourceVersion or uid in the patch
	// is no longer the object's.
	h.dyn.PrependReactor("patch", "applications", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(applicationGVR.GroupResource(), "guestbook", errors.New("the object has been modified"))
	})
	err := h.runner.RefreshApplication(context.Background(), "prod", "argocd", "guestbook")
	if out := refusal(t, err); out.Kind != edit.RefusedConflict {
		t.Fatalf("expected a conflict, got %v: %s", out.Kind, out.Reason)
	}
	for _, w := range h.writes() {
		if !w.DryRun {
			t.Errorf("the write was sent after the dry run conflicted: %+v", w)
		}
	}
}

func TestARefreshOfAMissingApplicationSaysSo(t *testing.T) {
	h := newHarness(t, nil)
	err := h.runner.RefreshApplication(context.Background(), "prod", "argocd", "gone")
	if out := refusal(t, err); !strings.Contains(out.Reason, "no longer exists") {
		t.Fatalf("got %v: %s", out.Kind, out.Reason)
	}
	h.noWrites()
}
