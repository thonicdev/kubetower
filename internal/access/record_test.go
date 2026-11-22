package access_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/audit"
)

// sink is an audit sink a test can read, and break.
type sink struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	fail func(n int) error // n is the write's index, from 0
	n    int
}

func (s *sink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.n
	s.n++
	if s.fail != nil {
		if err := s.fail(n); err != nil {
			return 0, err
		}
	}
	return s.buf.Write(p)
}

// records decodes every line on the sink, failing on one that is not a JSON
// object - the property a collector relies on.
func (s *sink) records(t *testing.T) []audit.Record {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []audit.Record
	scanner := bufio.NewScanner(bytes.NewReader(s.buf.Bytes()))
	for scanner.Scan() {
		var r audit.Record
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&r); err != nil {
			t.Fatalf("a line on the audit sink is not a record: %v\n%s", err, scanner.Text())
		}
		out = append(out, r)
	}
	return out
}

func (s *sink) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

type processLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *processLog) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *processLog) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func recordingGate(r *accesstest.Reviews, s *sink, l *processLog) *access.Gate {
	return access.New(access.Options{Reviews: r.For, InCluster: true, Audit: audit.New(s), Logf: l.logf})
}

// **A failed write of the record refuses the action.** The attempt
// record cannot be written, so the request is not sent: the fake API server
// sees the review and nothing else, and the refusal says why.
func TestAFailedAuditWriteRefusesTheWriteBeforeItIsSent(t *testing.T) {
	server := newAPIServer(t)
	clients := clientsetFor(t, guarded(server, ""))
	full := errors.New("write /dev/stdout: no space left on device")
	s := &sink{fail: func(int) error { return full }}
	l := &processLog{}
	g := recordingGate(&accesstest.Reviews{Answer: rbac}, s, l)

	ran := false
	_, err := g.Perform(alice(), "prod", deletePods, func(ctx context.Context) error {
		ran = true
		return clients.CoreV1().Pods("shop").Delete(ctx, "api-1", metav1.DeleteOptions{})
	})
	if ran {
		t.Fatal("the write ran although its attempt record could not be written")
	}
	if got := server.requests(); len(got) != 0 {
		t.Fatalf("the cluster saw %v; nothing should have reached it", got)
	}
	if !errors.Is(err, access.ErrUnrecorded) || !errors.Is(err, full) {
		t.Fatalf("the refusal %v does not say the record could not be written", err)
	}
	if errors.Is(err, access.ErrDenied) || errors.Is(err, access.ErrUnreviewable) {
		t.Errorf("an unrecorded write reads as a review's answer: %v", err)
	}
	var unrecorded *access.Unrecorded
	if !errors.As(err, &unrecorded) || unrecorded.Status().Code != 503 {
		t.Errorf("the refusal is not a 503 Unrecorded: %#v", err)
	}
	if !strings.Contains(err.Error(), "did not delete pods api-1 in namespace shop") {
		t.Errorf("the sentence does not name the request: %q", err)
	}
	if !strings.Contains(l.all(), "unrecorded: cluster=\"prod\" act=write verb=delete") ||
		!strings.Contains(l.all(), `who="alice@example.com" via=sso`) {
		t.Errorf("the process log does not say which act was refused unrecorded, and whose:\n%s", l.all())
	}
}

// The control for the test above: the same write, with a sink that works,
// reaches the cluster - so the refusal above is the sink's doing.
func TestAWorkingSinkLetsTheWriteThrough(t *testing.T) {
	server := newAPIServer(t)
	clients := clientsetFor(t, guarded(server, ""))
	s := &sink{}
	g := recordingGate(&accesstest.Reviews{Answer: rbac}, s, &processLog{})

	_, err := g.Perform(alice(), "prod", deletePods, func(ctx context.Context) error {
		// The attempt is on the record before the request leaves.
		if got := s.records(t); len(got) != 1 || got[0].Phase != audit.Attempt {
			t.Errorf("when the request was sent the record held %+v, want the attempt alone", got)
		}
		return clients.CoreV1().Pods("shop").Delete(ctx, "api-1", metav1.DeleteOptions{})
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := server.requests(); len(got) != 1 || got[0] != "DELETE /api/v1/namespaces/shop/pods/api-1" {
		t.Fatalf("the cluster saw %v", got)
	}
}

// A run over the outcomes: every act is one attempt and one outcome, sharing
// an id no other act has, each carrying who, the question and the decision -
// and none carrying what the API server said, which can quote a value.
func TestEveryActIsOneAttemptAndOneOutcomeJoinedByAnID(t *testing.T) {
	s := &sink{}
	g := recordingGate(&accesstest.Reviews{Answer: rbac}, s, &processLog{})
	secret := errors.New(`admission webhook denied: password "hunter2" is too short`)

	_, _ = g.Perform(alice(), "prod", deletePods, func(context.Context) error { return nil })
	_, _ = g.Perform(bob(), "prod", deletePods, func(context.Context) error { return nil })
	_, _ = g.Perform(alice(), "prod", deletePods, func(context.Context) error { return secret })
	exec := access.Attributes{Verb: "create", Version: "v1", Resource: "pods", Subresource: "exec", Namespace: "shop", Name: "api-1"}
	_, _ = g.Perform(access.DryRun(alice()), "prod", exec, func(context.Context) error { return nil })

	got := s.records(t)
	want := []struct {
		phase, act, decision, outcome, reason, who string
		dry                                        bool
	}{
		{audit.Attempt, audit.Write, "allowed", "", "", "alice@example.com", false},
		{audit.Outcome, audit.Write, "allowed", audit.Applied, "", "alice@example.com", false},
		{audit.Attempt, audit.Write, "denied", "", "", "bob@example.com", false},
		{audit.Outcome, audit.Write, "denied", audit.Refused, "", "bob@example.com", false},
		{audit.Attempt, audit.Write, "allowed", "", "", "alice@example.com", false},
		{audit.Outcome, audit.Write, "allowed", audit.Failed, "error", "alice@example.com", false},
		{audit.Attempt, audit.Exec, "allowed", "", "", "alice@example.com", true},
		{audit.Outcome, audit.Exec, "allowed", audit.Applied, "", "alice@example.com", true},
	}
	if len(got) != len(want) {
		t.Fatalf("four acts wrote %d records, want %d:\n%s", len(got), len(want), s.text())
	}
	ids := map[string]bool{}
	for i, w := range want {
		r := got[i]
		if r.Phase != w.phase || r.Act != w.act || r.Decision != w.decision || r.Outcome != w.outcome ||
			r.Reason != w.reason || r.Who != w.who || r.Via != "sso" || r.DryRun != w.dry {
			t.Errorf("record %d is %+v, want %+v", i, r, w)
		}
		if r.Cluster != "prod" || r.Resource != "pods" || r.Namespace != "shop" || r.Name != "api-1" || r.Time == "" {
			t.Errorf("record %d does not say what and where: %+v", i, r)
		}
		if w.phase == audit.Attempt {
			if r.ID == "" || ids[r.ID] {
				t.Errorf("record %d's id %q is empty or another act's", i, r.ID)
			}
			ids[r.ID] = true
		} else if r.ID != got[i-1].ID {
			t.Errorf("record %d's id %q is not its attempt's %q", i, r.ID, got[i-1].ID)
		}
	}
	if strings.Contains(s.text(), "hunter2") || strings.Contains(s.text(), "webhook") {
		t.Errorf("a record carries the API server's message:\n%s", s.text())
	}
}

// An outcome that cannot be written changes nothing about the act - it has
// been made - and is said in the process log under the act's id.
func TestAFailedOutcomeWriteIsSaidAndChangesNothing(t *testing.T) {
	s := &sink{fail: func(n int) error {
		if n == 1 {
			return errors.New("broken pipe")
		}
		return nil
	}}
	l := &processLog{}
	g := recordingGate(&accesstest.Reviews{Answer: rbac}, s, l)

	ran := false
	if _, err := g.Perform(alice(), "prod", deletePods, func(context.Context) error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("the write ran %t and answered %v; a lost outcome record must not undo it", ran, err)
	}
	got := s.records(t)
	if len(got) != 1 || got[0].Phase != audit.Attempt {
		t.Fatalf("the sink holds %+v, want the attempt alone", got)
	}
	if !strings.Contains(l.all(), "the outcome record of act "+got[0].ID) || !strings.Contains(l.all(), "broken pipe") {
		t.Errorf("the process log does not say the outcome was lost:\n%s", l.all())
	}
}

// The server profile does not act without a log - a server wired without one
// stops acting rather than acting unrecorded. The desktop keeps none and acts.
func TestOnlyTheDesktopActsWithoutALog(t *testing.T) {
	r := &accesstest.Reviews{Answer: rbac}
	ran := false
	do := func(context.Context) error { ran = true; return nil }

	server := access.New(access.Options{Reviews: r.For, InCluster: true})
	if _, err := server.Perform(alice(), "prod", deletePods, do); ran || !errors.Is(err, access.ErrUnrecorded) {
		t.Errorf("a server-profile gate with no audit log ran %t and answered %v", ran, err)
	}
	desktop := access.New(access.Options{Reviews: r.For})
	if _, err := desktop.Perform(alice(), "prod", deletePods, do); !ran || err != nil {
		t.Errorf("a desktop gate with no audit log ran %t and answered %v", ran, err)
	}
}

// A request Guard holds back is recorded as `failed`, reason `unsent` -
// the cluster never saw it - however the closure's error came back: here it
// is replaced by one that no longer unwraps to the guard's, as
// internal/edit's own refusal does, and the outcome still says so.
func TestARequestTheGuardHeldBackIsRecordedAsUnsent(t *testing.T) {
	server := newAPIServer(t)
	clients := clientsetFor(t, guarded(server, ""))
	s := &sink{}
	g := recordingGate(&accesstest.Reviews{Answer: rbac}, s, &processLog{})

	_, _ = g.Perform(alice(), "prod", deletePods, func(ctx context.Context) error {
		// Reviewed for api-1, sent for api-2: the mismatch Guard refuses.
		if err := clients.CoreV1().Pods("shop").Delete(ctx, "api-2", metav1.DeleteOptions{}); err != nil {
			return errors.New("the panel's own sentence, which no longer wraps the guard's")
		}
		return nil
	})
	if got := server.requests(); len(got) != 0 {
		t.Fatalf("the cluster saw %v", got)
	}
	got := s.records(t)
	if len(got) != 2 || got[1].Outcome != audit.Failed || got[1].Reason != "unsent" {
		t.Fatalf("a held-back request was recorded as %+v, want failed/unsent", got)
	}
}

// An act the console makes for itself is reviewed as the console (a self
// review) and recorded as the console's, with the person whose request caused
// it as onBehalfOf - never as who.
func TestAConsoleActIsRecordedAsTheConsolesOnBehalfOfThePerson(t *testing.T) {
	// The ServiceAccount may open its own tunnel; alice may not.
	r := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) { return q.Self, nil }}
	s := &sink{}
	g := recordingGate(r, s, &processLog{})
	tunnel := access.Attributes{Verb: "create", Version: "v1", Resource: "pods", Subresource: "portforward",
		Namespace: "monitoring", Name: "prometheus-0"}
	if _, err := g.Perform(access.AsConsole(alice()), "prod", tunnel, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("the console's own tunnel was refused: %v", err)
	}
	if asked := r.Asked(); len(asked) != 1 || !asked[0].Self {
		t.Fatalf("the console's act was not a self review: %+v", asked)
	}
	for i, rec := range s.records(t) {
		if rec.Who != "" || rec.Via != audit.Console || rec.OnBehalfOf != "alice@example.com" || rec.Act != audit.PortForward {
			t.Errorf("record %d reads who=%q via=%q onBehalfOf=%q act=%q", i, rec.Who, rec.Via, rec.OnBehalfOf, rec.Act)
		}
	}
}

// The reason on a record is from the API server's own closed list. A
// webhook's Status may carry any text as its reason, and it does not reach
// the record.
func TestAFailureReasonIsOnlyOneTheAPIServerWrites(t *testing.T) {
	webhook := &apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Code: 400,
		Reason: metav1.StatusReason("password hunter2 is too short\n{\"who\":\"ceo\"}")}}
	if got := access.FailureWord(webhook); got != "error" {
		t.Errorf("a webhook's free-text reason became %q", got)
	}
	if got := access.FailureWord(apierrors.NewConflict(schema.GroupResource{Resource: "pods"}, "api-1", errors.New("moved"))); got != "Conflict" {
		t.Errorf("a 409 became %q, want Conflict", got)
	}

	s := &sink{}
	g := recordingGate(&accesstest.Reviews{Answer: rbac}, s, &processLog{})
	_, _ = g.Perform(alice(), "prod", deletePods, func(context.Context) error { return webhook })
	if strings.Contains(s.text(), "hunter2") || strings.Contains(s.text(), "ceo") {
		t.Errorf("a webhook's reason reached the record:\n%s", s.text())
	}
}

// Perform hands the record's id back on the answer, for the human line.
func TestPerformAnswersTheRecordsID(t *testing.T) {
	s := &sink{}
	g := recordingGate(&accesstest.Reviews{Answer: rbac}, s, &processLog{})
	answer, _ := g.Perform(alice(), "prod", deletePods, func(context.Context) error { return nil })
	if got := s.records(t); answer.Record == "" || got[0].ID != answer.Record {
		t.Errorf("the answer names record %q; the records are %+v", answer.Record, got)
	}
	if !g.Audited() || access.New(access.Options{Reviews: (&accesstest.Reviews{}).For}).Audited() {
		t.Error("Audited does not say whether the gate has a log")
	}
}
