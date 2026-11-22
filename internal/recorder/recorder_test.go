package recorder

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/archive"
)

type fakeStore struct {
	mu      sync.Mutex
	on      bool
	batches [][]archive.Event
	// refuse is what RecordEvents hands back — a disk that will not take the
	// write. writable is what the store says about itself when asked, which is
	// the other half: internal/archive swallows its write failures and answers
	// (0, nil), so a poller that only watched return values would see nothing.
	refuse   error
	writable bool
	why      string
}

func (f *fakeStore) Enabled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.on
}

func (f *fakeStore) Writable() (bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writable, f.why
}

func (f *fakeStore) RecordEvents(_ string, events []archive.Event) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse != nil {
		return 0, f.refuse
	}
	f.batches = append(f.batches, events)
	return len(events), nil
}

// The archive is off by default and can be switched on from the settings page.
// A recorder that read the setting once at startup would need a restart to
// notice — which is the sort of thing nobody reports as a bug, they just decide
// the feature does not work.
func TestTheSettingIsReadEveryRoundAndNotOnceAtStartup(t *testing.T) {
	store := &fakeStore{on: false, writable: true}
	r := New(Options{Store: store})
	// No cluster manager: Round must return before it would need one.
	if got := r.Round(context.Background()); got != 0 {
		t.Fatalf("a disabled archive collected %d events", got)
	}
	if len(store.batches) != 0 {
		t.Error("something was recorded while the archive was off")
	}

	store.mu.Lock()
	store.on = true
	store.mu.Unlock()

	// Now it is on, and Round gets as far as needing a manager — which is the
	// proof it did not stop at the switch.
	r.clusters = nil
	r.Start(context.Background()) // returns immediately with no manager
}

// The newer Events API leaves the legacy timestamps empty. Without the fallback
// every archived event is dated 1970 and sorts before everything, forever.
func TestAnEventFromTheNewerAPIIsNotDated1970(t *testing.T) {
	when := time.Date(2026, 8, 17, 14, 30, 0, 0, time.UTC)

	got := convert("prod", []corev1.Event{{
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "api-1"},
		Reason:         "BackOff",
		EventTime:      metav1.NewMicroTime(when),
		// LastTimestamp and FirstTimestamp deliberately zero.
	}})

	if len(got) != 1 {
		t.Fatalf("got %d events", len(got))
	}
	if !got[0].Last.Equal(when) {
		t.Errorf("Last is %s, want %s — the legacy timestamps were empty and eventTime was not read",
			got[0].Last, when)
	}
	if !got[0].First.Equal(when) {
		t.Errorf("First is %s, want %s", got[0].First, when)
	}
}

func TestTheReporterIsFoundWhereverTheAPIPutIt(t *testing.T) {
	got := convert("prod", []corev1.Event{
		{InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "a"}, Source: corev1.EventSource{Component: "kubelet"}},
		{InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "b"}, ReportingController: "deployment-controller"},
	})

	if got[0].From != "kubelet" {
		t.Errorf("legacy source lost: %q", got[0].From)
	}
	if got[1].From != "deployment-controller" {
		t.Errorf("reportingController lost: %q", got[1].From)
	}
}

func TestEventsComeBackOldestFirst(t *testing.T) {
	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	got := convert("prod", []corev1.Event{
		{InvolvedObject: corev1.ObjectReference{Name: "late"}, LastTimestamp: metav1.NewTime(base.Add(time.Hour))},
		{InvolvedObject: corev1.ObjectReference{Name: "early"}, LastTimestamp: metav1.NewTime(base)},
	})

	if got[0].Object != "early" {
		t.Errorf("first is %q; a file read back should read forwards in time", got[0].Object)
	}
}

func TestTheClusterNameIsCarriedOntoEveryEvent(t *testing.T) {
	got := convert("SHOP-EKS-1", []corev1.Event{
		{InvolvedObject: corev1.ObjectReference{Name: "a"}},
		{InvolvedObject: corev1.ObjectReference{Name: "b"}},
	})
	for _, e := range got {
		if e.Cluster != "SHOP-EKS-1" {
			t.Errorf("an event arrived with cluster %q", e.Cluster)
		}
	}
}

// A very long message must not become the archive's size problem.
func TestALongMessageIsCut(t *testing.T) {
	long := make([]byte, 5000)
	for i := range long {
		long[i] = 'x'
	}
	got := convert("c", []corev1.Event{{
		InvolvedObject: corev1.ObjectReference{Name: "a"}, Message: string(long),
	}})
	if len(got[0].Message) > 2100 {
		t.Errorf("a %d-byte message was stored as %d bytes", len(long), len(got[0].Message))
	}
}

// The backoff is the whole reason this package is not a log-flooding machine.
// An expired SSO session fails identically every two minutes for as long as the
// operator is away from their desk.
func TestAFailingClusterIsLeftAloneForLongerEachTime(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	r := New(Options{Store: &fakeStore{on: true, writable: true}})
	r.now = func() time.Time { return now }

	if !r.due("prod") {
		t.Fatal("a cluster never asked is not due")
	}

	r.failed("prod", errors.New("token has expired"))
	if r.due("prod") {
		t.Error("a cluster that just failed is due again immediately")
	}

	first := r.state["prod"].penalty
	if first != backoffStart {
		t.Errorf("first penalty is %s, want %s", first, backoffStart)
	}

	// Time passes, it is asked, it fails again.
	now = now.Add(first + time.Second)
	if !r.due("prod") {
		t.Fatal("a cluster past its backoff is not due")
	}
	r.failed("prod", errors.New("token has expired"))
	if r.state["prod"].penalty != first*2 {
		t.Errorf("second penalty is %s, want %s", r.state["prod"].penalty, first*2)
	}

	// And it cannot grow without bound.
	for range 20 {
		r.failed("prod", errors.New("token has expired"))
	}
	if r.state["prod"].penalty > backoffMax {
		t.Errorf("penalty grew to %s, past the %s ceiling", r.state["prod"].penalty, backoffMax)
	}
}

// The same sentence every two minutes forever is the same as no logging at all.
func TestTheSameFailureIsSaidOnceAndTheRecoveryIsSaidToo(t *testing.T) {
	var lines []string
	r := New(Options{Store: &fakeStore{on: true, writable: true}, Logf: func(f string, a ...any) {
		lines = append(lines, f)
	}})
	now := time.Now()
	r.now = func() time.Time { return now }

	for range 5 {
		r.failed("prod", errors.New("token has expired"))
	}
	if len(lines) != 1 {
		t.Errorf("five identical failures produced %d log lines, want 1", len(lines))
	}

	// A different reason is worth saying.
	r.failed("prod", errors.New("no such host"))
	if len(lines) != 2 {
		t.Errorf("a new reason produced %d lines in total, want 2", len(lines))
	}

	r.succeeded("prod")
	if len(lines) != 3 {
		t.Error("recovery was not announced; a cluster that came back should say so")
	}
	if !r.due("prod") {
		t.Error("a recovered cluster is still being backed off")
	}
}

func TestASucceedingClusterAccumulatesNoState(t *testing.T) {
	r := New(Options{Store: &fakeStore{on: true, writable: true}})
	r.succeeded("never-failed")
	if len(r.state) != 0 {
		t.Errorf("state was kept for a cluster that has not failed: %v", r.state)
	}
}

// What the poller is doing, which nothing else in this console shows.
//
// The backoff above is deliberately quiet — one log line per distinct failure,
// not one per round — and the cost of that quiet is that an archive which
// stopped collecting from most of the clusters overnight looks identical, from
// every screen, to one collecting from all of them.
func TestStatusNamesEveryClusterBeingBackedOff(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	r := New(Options{Store: &fakeStore{on: true, writable: true}})
	r.now = func() time.Time { return now }

	// A cluster that is answering keeps no state, so it must not appear.
	r.succeeded("healthy")

	r.failed("sso", errors.New("token has expired\nand a second line nobody needs"))
	// A second failure doubles the penalty, so this one has been dark longer.
	r.failed("long-gone", errors.New("no such host"))
	now = now.Add(backoffStart + time.Second)
	r.failed("long-gone", errors.New("no such host"))

	status := r.Status()
	if !status.Enabled {
		t.Error("the archive is on and Status says otherwise")
	}
	if status.Interval != defaultInterval {
		t.Errorf("interval is %s, want %s", status.Interval, defaultInterval)
	}
	if len(status.Failing) != 2 {
		t.Fatalf("Status reported %d failing clusters, want 2: %+v", len(status.Failing), status.Failing)
	}

	// Longest penalty first: that is the cluster missing from the archive longest.
	if status.Failing[0].Cluster != "long-gone" {
		t.Errorf("first row is %q; the worst-off cluster reads first", status.Failing[0].Cluster)
	}
	if status.Failing[0].Penalty != backoffStart*2 {
		t.Errorf("penalty is %s, want %s", status.Failing[0].Penalty, backoffStart*2)
	}
	if !status.Failing[0].NextAttempt.After(now) {
		t.Error("next attempt is in the past on a cluster being backed off")
	}

	sso := status.Failing[1]
	if sso.Cluster != "sso" {
		t.Fatalf("second row is %q", sso.Cluster)
	}
	if sso.Reason != "token has expired" {
		t.Errorf("reason is %q — it should be the one line that was logged, not the paragraph", sso.Reason)
	}
}

// A recorder that is collecting from everything says so with an empty list, and
// one whose archive is off says that instead — the two are different facts and
// the page shows them differently.
func TestStatusOnARecorderWithNothingWrong(t *testing.T) {
	store := &fakeStore{on: false, writable: true}
	r := New(Options{Store: store})

	if got := r.Status(); got.Enabled || len(got.Failing) != 0 {
		t.Errorf("a switched-off recorder reported %+v", got)
	}

	store.mu.Lock()
	store.on = true
	store.mu.Unlock()

	if got := r.Status(); !got.Enabled || len(got.Failing) != 0 {
		t.Errorf("a recorder with no failures reported %+v", got)
	}
}
