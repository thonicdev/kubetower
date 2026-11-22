package web

import (
	"errors"
	"testing"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

// Host.MayReadLogs is the console's own `get pods/log` review, handed to an
// extension: asked for the person, in the namespace named,
// and answered as the contract's two sentinels - never by reading anything.

// hostLogsServer is a console under the server profile whose reviews answer
// the pods/log question with logs(q) and allow everything else, and the
// reviews it was asked.
func hostLogsServer(t *testing.T, logs func(accesstest.Question) (bool, error)) (*Server, *accesstest.Reviews, *surfaceServer) {
	t.Helper()
	manager, fake := surfaceManager(t)
	reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		if q.Attributes.Resource == "pods" && q.Attributes.Subresource == "log" {
			return logs(q)
		}
		return true, nil
	}}
	s, err := New(Options{Clusters: manager, InCluster: true,
		Access: access.New(access.Options{Reviews: reviews.For, InCluster: true})})
	if err != nil {
		t.Fatal(err)
	}
	return s, reviews, fake
}

func TestMayReadLogsAsksGetPodsLogForThePersonInTheNamespace(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(accesstest.Question) (bool, error)
		want   error
	}{
		{"allowed", func(accesstest.Question) (bool, error) { return true, nil }, nil},
		{"denied", func(accesstest.Question) (bool, error) { return false, nil }, extension.ErrLogsDenied},
		{"not reviewed", func(accesstest.Question) (bool, error) {
			return false, errors.New("the authorizer did not answer")
		}, extension.ErrLogsUnreviewed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, reviews, fake := hostLogsServer(t, tc.answer)
			err := s.hostFor("kubecron").MayReadLogs(asGrace(), "prod", "shop")
			switch {
			case tc.want == nil && err != nil:
				t.Fatalf("an allowed person was refused: %v", err)
			case tc.want != nil && !errors.Is(err, tc.want):
				t.Fatalf("answered %v, want %v", err, tc.want)
			}
			if tc.want == extension.ErrLogsDenied && errors.Is(err, extension.ErrLogsUnreviewed) {
				t.Error("a denial also reads as unreviewed")
			}
			asked := reviews.Asked()
			if len(asked) != 1 {
				t.Fatalf("asked %d reviews, want one: %+v", len(asked), asked)
			}
			q := asked[0]
			if q.Self || q.User != "grace@example.com" || q.Attributes.Verb != "get" ||
				q.Attributes.Resource != "pods" || q.Attributes.Subresource != "log" || q.Attributes.Namespace != "shop" {
				t.Errorf("the review asked %+v, want get pods/log in shop for grace", q)
			}
			for _, path := range fake.asked() {
				t.Errorf("the cluster was read (%s): the answer is a review, nothing else", path)
			}
		})
	}
}

func TestMayReadLogsAsksNothingAboutNoNamespaceOrAnUnknownCluster(t *testing.T) {
	s, reviews, _ := hostLogsServer(t, func(accesstest.Question) (bool, error) { return true, nil })
	h := s.hostFor("kubecron")
	for _, namespace := range []string{"", "shop/../kube-system", ".."} {
		if err := h.MayReadLogs(asGrace(), "prod", namespace); err == nil {
			t.Errorf("namespace %q was allowed", namespace)
		}
	}
	if err := h.MayReadLogs(asGrace(), "nowhere", "shop"); !errors.Is(err, extension.ErrUnknownCluster) {
		t.Errorf("an unknown cluster answered %v, want ErrUnknownCluster", err)
	}
	if asked := reviews.Asked(); len(asked) != 0 {
		t.Errorf("a review was made for a question that is not one: %+v", asked)
	}
}
