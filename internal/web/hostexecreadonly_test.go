package web

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/audit"
)

// A cluster named in KT_READONLY_CLUSTERS refuses a command an extension would
// run in a pod, as the console's own pod shell refuses one: before anything is
// counted, reviewed, recorded or sent.

// readOnlyExecServer is hostExecServer with the cluster `prod` marked
// read-only when marked is true; reviewed reports whether an exec review was
// asked.
func readOnlyExecServer(t *testing.T, marked bool, sink *revealSink) (*Server, *surfaceServer, *bool) {
	t.Helper()
	manager, fake := surfaceManager(t)
	reviewed := false
	reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		if q.Attributes.Subresource == "exec" {
			reviewed = true
		}
		return true, nil
	}}
	s, err := New(Options{Clusters: manager, InCluster: true,
		ReadOnly: func(name string) bool { return marked && name == "prod" },
		Access:   access.New(access.Options{Reviews: reviews.For, InCluster: true, Audit: audit.New(sink)})})
	if err != nil {
		t.Fatal(err)
	}
	return s, fake, &reviewed
}

func TestAnExecOnAReadOnlyClusterIsRefusedBeforeItIsReviewedOrSent(t *testing.T) {
	sink := &revealSink{}
	s, fake, reviewed := readOnlyExecServer(t, true, sink)
	_, err := s.hostFor(execExt).Exec(asGrace(), "prod", jcmdQuery())
	if !errors.Is(err, extension.ErrDenied) {
		t.Fatalf("an exec on a read-only cluster answered %v, want ErrDenied", err)
	}
	if !strings.Contains(err.Error(), "KT_READONLY_CLUSTERS") {
		t.Errorf("the refusal does not name the setting: %v", err)
	}
	if *reviewed || askedToExec(fake) {
		t.Errorf("an exec on a read-only cluster was reviewed (%v) or reached the cluster: %v", *reviewed, fake.asked())
	}
	if got := sink.records(t); len(got) != 0 {
		t.Errorf("an exec refused by the guardrail wrote %+v, want nothing", got)
	}
}

func TestAStreamOnAReadOnlyClusterIsRefusedBeforeItIsReviewedOrSent(t *testing.T) {
	sink := &revealSink{}
	s, fake, reviewed := readOnlyExecServer(t, true, sink)
	var out bytes.Buffer
	_, err := streamer(t, s).ExecStream(asGrace(), "prod", catQuery(), &out, 1<<20)
	if !errors.Is(err, extension.ErrDenied) {
		t.Fatalf("a stream on a read-only cluster answered %v, want ErrDenied", err)
	}
	if *reviewed || askedToExec(fake) || out.Len() != 0 {
		t.Errorf("a stream on a read-only cluster was reviewed (%v), reached the cluster (%v) or wrote %d bytes",
			*reviewed, fake.asked(), out.Len())
	}
}

// The guardrail is per cluster: an unmarked one still runs the command.
func TestAnExecOnAClusterNotMarkedReadOnlyStillReachesTheCluster(t *testing.T) {
	sink := &revealSink{}
	s, fake, reviewed := readOnlyExecServer(t, false, sink)
	_, _ = s.hostFor(execExt).Exec(asGrace(), "prod", jcmdQuery())
	if !*reviewed || !askedToExec(fake) {
		t.Errorf("an exec on an unmarked cluster was not reviewed (%v) or never reached the cluster: %v",
			*reviewed, fake.asked())
	}
}
