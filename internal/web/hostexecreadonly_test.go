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
// run in a pod, as the console's own pod shell refuses one, and a tunnel to one
// of its ports: before anything is counted, reviewed or sent, and recorded as a
// refusal only.

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
	if !errors.Is(err, extension.ErrReadOnly) {
		t.Errorf("the refusal is not classifiable as the marking's: %v", err)
	}
	onlyARefusal(t, sink.records(t), audit.Exec, execExt)
}

// onlyARefusal asserts the audit holds the refusal of act and nothing else: an
// attempt and its outcome, both read_only, the outcome refused, naming the
// extension that asked.
func onlyARefusal(t *testing.T, got []audit.Record, act, ext string) {
	t.Helper()
	if len(got) != 2 || got[0].Phase != audit.Attempt || got[1].Phase != audit.Outcome || got[1].Outcome != audit.Refused {
		t.Fatalf("the guardrail recorded %+v, want one refused attempt", got)
	}
	for _, r := range got {
		if r.Act != act || r.Decision != string(access.ReadOnly) || r.Extension != ext || r.Cluster != "prod" ||
			r.Who != "grace@example.com" {
			t.Errorf("the %s record reads %+v", r.Phase, r)
		}
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
	onlyARefusal(t, sink.records(t), audit.Exec, execExt)
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

// A tunnel to a pod's port is refused the same way: nothing reviewed, nothing
// dug, no slot held, the refusal alone recorded.
func TestAPodTunnelOnAReadOnlyClusterIsRefusedBeforeItIsReviewedOrDug(t *testing.T) {
	reviews := &accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) { return true, nil }}
	sink := &revealSink{}
	gate := access.New(access.Options{Reviews: reviews.For, InCluster: true, Audit: audit.New(sink)})
	dig := &reviewedPodTunnels{reviewedTunnels{gate: gate}}
	s, err := New(Options{Clusters: navFixture(t, "prod").clusters, Tunnels: dig, InCluster: true, Access: gate,
		ReadOnly: func(name string) bool { return name == "prod" }})
	if err != nil {
		t.Fatal(err)
	}
	pods := s.hostFor("runtime").(extension.PodForwarder)
	_, err = pods.ForwardPod(podRequest(grace()), "prod", "shop", "api-0", 6060)
	if !errors.Is(err, extension.ErrReadOnly) || !errors.Is(err, extension.ErrDenied) {
		t.Fatalf("a pod tunnel on a read-only cluster answered %v, want ErrReadOnly", err)
	}
	if !strings.Contains(err.Error(), "KT_READONLY_CLUSTERS") {
		t.Errorf("the refusal does not name the setting: %v", err)
	}
	if dig.count() != 0 || len(portForwardQuestions(reviews.Asked())) != 0 || s.tunnels.(*countedTunnels).live() != 0 {
		t.Errorf("a refused pod tunnel dug %d, was reviewed (%v) or held a slot", dig.count(), reviews.Asked())
	}
	got := sink.records(t)
	onlyARefusal(t, got, audit.PortForward, "runtime")
	if got[0].Namespace != "shop" || got[0].Name != "api-0" || got[0].Subresource != "portforward" {
		t.Errorf("the refusal names %+v, want shop/api-0's portforward", got[0])
	}
}
