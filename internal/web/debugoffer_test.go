package web

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/detail"
)

// Debug on a pod's page: reviewed like Shell under the server profile, but on
// both questions the debug socket asks, and refused with its sentence on a
// read-only cluster on both profiles.

var debugPod = api.ObjectRef{Cluster: "prod", Kind: "pods", Namespace: "shop", Name: "api-1"}

// debugServer is the server profile over reviews answering through answer
// for the person and console for the console's own self review.
func debugServer(t *testing.T, answer func(access.Attributes) (bool, error), console func(access.Attributes) bool) (*Server, *accesstest.Reviews) {
	t.Helper()
	reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		if q.Self {
			return console(q.Attributes), nil
		}
		return answer(q.Attributes)
	}}
	s := serverFor(t, true)
	s.gate = access.New(access.Options{Reviews: reviews.For, InCluster: true})
	return s, reviews
}

func TestTheDebugOfferIsReviewedOnBothQuestions(t *testing.T) {
	patch := debugAttributes("shop", "api-1")
	exec := execAttributes("shop", "api-1")

	t.Run("allowed", func(t *testing.T) {
		s, reviews := debugServer(t, func(access.Attributes) (bool, error) { return true, nil }, consoleMayAll)
		offer := s.debugFor(as(alice), debugPod, true, "")
		if offer == nil || !offer.Permitted || offer.Reason != nil {
			t.Errorf("Debug is %+v, want offered", offer)
		}
		var askedPatch, askedExec bool
		for _, q := range reviews.Asked() {
			if q.Self {
				continue
			}
			askedPatch = askedPatch || q.Attributes == patch
			askedExec = askedExec || q.Attributes == exec
		}
		if !askedPatch || !askedExec {
			t.Errorf("the person was not asked both %+v and %+v: %+v", patch, exec, reviews.Asked())
		}
	})

	// Refused either question, the person gets no offer at all.
	for _, refused := range []access.Attributes{patch, exec} {
		t.Run("denied "+refused.Subresource, func(t *testing.T) {
			s, _ := debugServer(t, func(a access.Attributes) (bool, error) { return a != refused, nil }, consoleMayAll)
			if offer := s.debugFor(as(alice), debugPod, true, ""); offer != nil {
				t.Errorf("a person refused %s is offered Debug %+v", refused.Subresource, offer)
			}
		})
	}

	t.Run("console denied", func(t *testing.T) {
		s, _ := debugServer(t, func(access.Attributes) (bool, error) { return true, nil },
			func(a access.Attributes) bool { return a != patch })
		offer := s.debugFor(as(alice), debugPod, true, "")
		if offer == nil || offer.Permitted || offer.Reason == nil || offer.Reason.Code != api.CodeVerbDenied ||
			!strings.Contains(offer.Reason.Message, "pods/ephemeralcontainers") {
			t.Errorf("Debug is %+v, want disabled naming the console's missing grant", offer)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		s, _ := debugServer(t, func(access.Attributes) (bool, error) {
			return false, errors.New("the authorization API is not served")
		}, consoleMayAll)
		offer := s.debugFor(as(alice), debugPod, true, "")
		if offer == nil || offer.Permitted || offer.Reason == nil || offer.Reason.Code != api.CodeAccessUnreviewable {
			t.Errorf("Debug is %+v, want disabled as access_unreviewable", offer)
		}
	})
}

// A read-only cluster refuses Debug with the guardrail's sentence on both
// profiles, and asks nobody: adding a container is a change.
func TestTheDebugOfferIsRefusedOnAReadOnlyCluster(t *testing.T) {
	for _, inCluster := range []bool{true, false} {
		reviews := &accesstest.Reviews{}
		s := serverFor(t, inCluster)
		s.gate = access.New(access.Options{Reviews: reviews.For, InCluster: inCluster})
		s.readOnly = func(string) bool { return true }
		writable, note := s.writable("prod")
		if writable {
			t.Fatal("precondition: the cluster is writable")
		}
		offer := s.debugFor(as(alice), debugPod, writable, note)
		if offer == nil || offer.Permitted || offer.Reason == nil || offer.Reason.Code != api.CodeVerbDenied ||
			!strings.Contains(offer.Reason.Message, "KT_READONLY_CLUSTERS") {
			t.Errorf("inCluster=%t: Debug is %+v, want refused naming the setting", inCluster, offer)
		}
		if n := len(reviews.Asked()); n != 0 {
			t.Errorf("inCluster=%t: %d reviews asked on a read-only cluster", inCluster, n)
		}
	}
}

// On the desktop Debug is offered as Shell is, without a review; on a kind
// other than a pod, or a build that serves no debug socket, it is absent.
func TestTheDesktopOffersDebugAndOtherKindsDoNot(t *testing.T) {
	reviews := &accesstest.Reviews{}
	s := serverFor(t, false)
	s.gate = reviews.Gate()
	if offer := s.debugFor(context.Background(), debugPod, true, ""); offer == nil || !offer.Permitted {
		t.Errorf("the desktop's Debug is %+v, want offered", offer)
	}
	if n := len(reviews.Asked()); n != 0 {
		t.Errorf("the desktop asked %d reviews for Debug", n)
	}
	deployment := api.ObjectRef{Cluster: "prod", Kind: "deployments", Namespace: "shop", Name: "api"}
	if offer := s.debugFor(context.Background(), deployment, true, ""); offer != nil {
		t.Errorf("a Deployment's page offers Debug %+v", offer)
	}
	s.claude = nil
	if offer := s.debugFor(context.Background(), debugPod, true, ""); offer != nil {
		t.Errorf("a build with no debug socket offers Debug %+v", offer)
	}
}

// The wire carries a debug container marked and with its target.
func TestContainerViewsCarryEphemeralAndTarget(t *testing.T) {
	views := containerViews([]detail.Container{
		{Name: "api", Image: "api:1"},
		{Name: "kt-debug-ab12c", Image: "busybox:1.36", Ephemeral: true, Target: "api"},
	})
	if len(views) != 2 || views[0].Ephemeral || views[0].Target != "" {
		t.Fatalf("views %+v", views)
	}
	if !views[1].Ephemeral || views[1].Target != "api" || views[1].Init {
		t.Errorf("the debug container is carried as %+v", views[1])
	}
}
