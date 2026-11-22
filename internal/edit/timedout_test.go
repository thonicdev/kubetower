package edit

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"

	"github.com/thonicdev/kubetower/internal/access"
)

// What became of a write the API server did not refuse, said the same way by
// the audit record, the `write:` line and the sentence a person reads: a 504
// Timeout or a 500 ServerTimeout is an answer that the change may still
// happen, never "the cluster refused the change"; a patch, a create and a
// delete are read from the transport as an eviction is; and an answer whose
// body was cut is still that answer.

// apiTimeout is kube-apiserver's 504, as its timeout filter writes it: no
// Retry-After, so client-go hands it back at once.
func apiTimeout(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusGatewayTimeout)
	_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Timeout","code":504,`+
		`"message":"Timeout: request did not complete within requested timeout - context deadline exceeded"}`)
}

// apiTimeoutRetried is the same 504 with a Retry-After, which client-go honors
// and asks again after.
func apiTimeoutRetried(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	apiTimeout(w)
}

// serverTimeoutFinal is a 500 ServerTimeout client-go no longer retries: the
// last answer, once it has stopped (a Retry-After would make it ask again,
// and serverTimeout in evictwire_test.go is that one).
func serverTimeoutFinal(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"ServerTimeout",`+
		`"code":500,"message":"the server cannot complete the requested operation at this time, try again later"}`)
}

// proxyTimeout is a 504 from something in front of the API server, with no
// Status in its body.
func proxyTimeout(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusGatewayTimeout)
	_, _ = io.WriteString(w, "upstream request timeout")
}

func alreadyExists(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"AlreadyExists",`+
		`"code":409,"message":"configmaps \"settings\" already exists"}`)
}

const forbiddenBody = `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403,` +
	`"message":"configmaps \"settings\" is forbidden: User \"carol\" cannot patch resource \"configmaps\""}`

func apiForbidden(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(w, forbiddenBody)
}

// forbiddenCut is a 403 whose body stops part-way: the length promised, a
// third of it written, the connection closed.
func forbiddenCut(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", "400")
	w.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(w, forbiddenBody[:40])
}

// createdCut is a 201 whose body stops part-way: accepted, and nothing to say
// what was made.
func createdCut(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", "400")
	w.WriteHeader(http.StatusCreated)
	_, _ = io.WriteString(w, `{"kind":"Eviction",`)
}

func dropConnection(w http.ResponseWriter) {
	if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
		_ = conn.Close()
	}
}

func limited() flowcontrol.RateLimiter {
	return access.Limiter(flowcontrol.NewTokenBucketRateLimiter(50, 100))
}

// timedOutWith asserts err is an answer that the API server timed out, with
// status, and nothing it is not.
func timedOutWith(t *testing.T, err error, status int) {
	t.Helper()
	timedOut, ok := AsTimedOut(err)
	if !ok || !errors.Is(err, ErrTimedOut) || !errors.Is(err, access.ErrTimedOut) {
		t.Fatalf("answered %v, want the API server timing out", err)
	}
	if errors.Is(err, ErrNotSent) || errors.Is(err, ErrUnanswered) {
		t.Errorf("a timed-out answer answered %v: it was sent, and answered", err)
	}
	if timedOut.Status != status {
		t.Errorf("timed out with %d (%s), want %d", timedOut.Status, timedOut.Answer(), status)
	}
}

// On the Evict button's and the drain's path: a 504, and a 500 ServerTimeout
// client-go no longer retries, are neither "the cluster refused the change" nor
// counted under refused=. Both say the eviction may still happen.
func TestAnEvictionTheAPIServerTimedOutOnMayStillHappen(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []func(http.ResponseWriter)
		within  time.Duration
		status  int
		calls   int32
		// says is the sentence's own account of the answer: "timed out on"
		// only where the answer says so.
		says []string
	}{
		{"504 Timeout", []func(http.ResponseWriter){apiTimeout}, time.Minute, http.StatusGatewayTimeout, 1,
			[]string{"timed out on", "504 Timeout"}},
		{"500 ServerTimeout, no longer retried", []func(http.ResponseWriter){serverTimeoutFinal}, time.Minute,
			http.StatusInternalServerError, 1, []string{"timed out on", "500 ServerTimeout"}},
		{"504 from a proxy, no Status", []func(http.ResponseWriter){proxyTimeout}, time.Minute, http.StatusGatewayTimeout, 1,
			[]string{"timed out on", "504 Timeout"}},
		// client-go retried the ServerTimeout, its Retry-After, and the
		// deadline cut the retry's wait: no answer is left in the error, and
		// the transport's record still holds the first one - its status, and
		// not its reason, which client-go read and dropped.
		{"500 ServerTimeout retried, then the deadline", []func(http.ResponseWriter){serverTimeout}, 300 * time.Millisecond,
			http.StatusInternalServerError, 1, []string{"did not finish", "500 Internal Server Error with Retry-After"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, arrived := budgetServer(t, tc.answers...)
			rig := newEvictRig(t, &rest.Config{Host: srv.URL, RateLimiter: limited()})
			ctx, cancel := within(tc.within)
			defer cancel()

			err := rig.evict(ctx)
			if arrived.Load() != tc.calls {
				t.Fatalf("the API server received %d evictions, want %d", arrived.Load(), tc.calls)
			}
			timedOutWith(t, err, tc.status)
			rig.recorded(t, access.TimedOut, access.TimedOut)
			// The sentence for anything that is not an eviction says the same.
			refusal, _ := AsRefusal(apiRefusal(err))
			if refusal == nil || refusal.Kind != RefusedUnanswered ||
				!strings.Contains(refusal.Reason, "may still happen") || strings.Contains(refusal.Reason, "refused") {
				t.Fatalf("the sentence is %+v", refusal)
			}
			for _, want := range tc.says {
				if !strings.Contains(refusal.Reason, want) {
					t.Errorf("the sentence %q does not say %q", refusal.Reason, want)
				}
			}
		})
	}
}

// Each write - a patch, a create, a delete - says what its audit record says,
// on its `write:` line and in its sentence: `held`, `timed-out`, `unanswered`,
// never `refused-by-the-cluster` and "the cluster refused the change" for a
// request the cluster did not refuse. The line's word
// for those is the record's own, from the same function. And a refusal is
// still one.
func TestEveryWriteSaysWhatItsAuditRecordSays(t *testing.T) {
	writes := []struct {
		name string
		do   func(context.Context, evictRig) error
		// sentence is what a person reads: Send and Create answer it
		// themselves; a delete is worded by its caller through APIRefusal.
		sentence func(error) error
	}{
		{"patch", func(ctx context.Context, r evictRig) error {
			_, _, err := Send(ctx, r.where, r.target, []byte(`{"data":{"a":"b"}}`), false)
			return err
		}, func(err error) error { return err }},
		{"create", func(ctx context.Context, r evictRig) error {
			_, _, err := Create(ctx, r.where, r.target, &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "v1", "kind": "ConfigMap",
				"metadata": map[string]any{"name": r.target.Name, "namespace": r.target.Namespace},
			}}, false)
			return err
		}, func(err error) error { return err }},
		{"delete", func(ctx context.Context, r evictRig) error {
			_, err := Delete(ctx, r.where, r.target, metav1.DeleteOptions{})
			return err
		}, APIRefusal},
	}
	outcomes := []struct {
		name    string
		cfg     func(t *testing.T) *rest.Config
		within  time.Duration
		kind    RefusalKind
		word    string // the audit record's reason
		line    string // the `write:` line's
		says    []string
		notSays string
	}{
		{"held at the rate limit", func(t *testing.T) *rest.Config {
			return &rest.Config{Host: "https://prod.example", Transport: &fakeTransport{do: hang}, RateLimiter: starved()}
		}, time.Minute, RefusedNotSent, access.Held, access.Held, []string{"was not sent", "Nothing changed"}, "refused"},
		{"504 Timeout", func(t *testing.T) *rest.Config {
			srv, _ := budgetServer(t, apiTimeout)
			return &rest.Config{Host: srv.URL, RateLimiter: limited()}
		}, time.Minute, RefusedUnanswered, access.TimedOut, access.TimedOut, []string{"timed out", "504 Timeout", "may still happen"}, "refused"},
		{"500 ServerTimeout", func(t *testing.T) *rest.Config {
			srv, _ := budgetServer(t, serverTimeoutFinal)
			return &rest.Config{Host: srv.URL, RateLimiter: limited()}
		}, time.Minute, RefusedUnanswered, access.TimedOut, access.TimedOut, []string{"timed out", "500 ServerTimeout", "may still happen"}, "refused"},
		{"a connection dropped after the write", func(t *testing.T) *rest.Config {
			srv, _ := budgetServer(t, dropConnection)
			return &rest.Config{Host: srv.URL, RateLimiter: limited()}
		}, time.Minute, RefusedUnanswered, access.Unanswered, access.Unanswered, []string{"no answer came back", "not known"}, "refused"},
		{"refused, 403", func(t *testing.T) *rest.Config {
			srv, _ := budgetServer(t, apiForbidden)
			return &rest.Config{Host: srv.URL, RateLimiter: limited()}
		}, time.Minute, RefusedByAPI, "Forbidden", "refused-by-the-cluster", []string{"the cluster refused the change", "forbidden"}, "not known"},
	}
	for _, write := range writes {
		for _, outcome := range outcomes {
			t.Run(write.name+"/"+outcome.name, func(t *testing.T) {
				rig := newEvictRig(t, outcome.cfg(t))
				ctx, cancel := within(outcome.within)
				defer cancel()

				err := write.do(ctx, rig)
				if err == nil {
					t.Fatal("the write answered nothing")
				}
				line := outcome.line
				if outcome.kind == RefusedByAPI && write.name == "delete" {
					// A delete's error comes back unclassified, as client-go
					// sent it, and its line names the API server's
					// reason; a patch's and a create's, classified on the way
					// back, the class. Either is a refusal, which is the point.
					line = outcome.word
				}
				rig.recorded(t, outcome.word, line)
				refusal, ok := AsRefusal(write.sentence(err))
				if !ok || refusal.Kind != outcome.kind {
					t.Fatalf("the sentence is %v (%+v), want kind %v", write.sentence(err), refusal, outcome.kind)
				}
				for _, want := range outcome.says {
					if !strings.Contains(refusal.Reason, want) {
						t.Errorf("the sentence %q does not say %q", refusal.Reason, want)
					}
				}
				if strings.Contains(refusal.Reason, outcome.notSays) {
					t.Errorf("the sentence %q says %q", refusal.Reason, outcome.notSays)
				}
			})
		}
	}
}

// A create the API server timed out on, retried by client-go on the 504's
// Retry-After, and answered 409 AlreadyExists the second time: the first
// attempt is what made it exist. It must not read "the cluster refused the
// change" - nor the record `AlreadyExists` - about a create that happened.
func TestACreateThatTimedOutThenAlreadyExistsMayHaveHappened(t *testing.T) {
	srv, arrived := budgetServer(t, apiTimeoutRetried, alreadyExists)
	rig := newEvictRig(t, &rest.Config{Host: srv.URL, RateLimiter: limited()})
	ctx, cancel := within(time.Minute)
	defer cancel()

	_, _, err := Create(ctx, rig.where, rig.target, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": rig.target.Name, "namespace": rig.target.Namespace},
	}}, false)
	if arrived.Load() != 2 {
		t.Fatalf("the API server received %d creates, want the first and its retry", arrived.Load())
	}
	refusal, ok := AsRefusal(err)
	if !ok || refusal.Kind != RefusedUnanswered || !errors.Is(err, ErrTimedOut) ||
		!strings.Contains(refusal.Reason, "may still happen") || strings.Contains(refusal.Reason, "refused") {
		t.Fatalf("504 then 409 answered %v (%+v), want the change that may have happened", err, refusal)
	}
	rig.recorded(t, access.TimedOut, access.TimedOut)
}

// A refusal whose body was cut mid-read is not "no answer came back": the
// transport's record holds the 403. The status is the answer.
func TestARefusalWhoseBodyWasCutIsStillTheRefusal(t *testing.T) {
	srv, arrived := budgetServer(t, forbiddenCut)
	rig := newEvictRig(t, &rest.Config{Host: srv.URL, RateLimiter: limited()})
	ctx, cancel := within(time.Minute)
	defer cancel()

	err := rig.evict(ctx)
	if arrived.Load() != 1 {
		t.Fatalf("the API server received %d evictions, want 1", arrived.Load())
	}
	if !apierrors.IsForbidden(err) || errors.Is(err, ErrUnanswered) || errors.Is(err, ErrNotSent) ||
		errors.Is(err, ErrTimedOut) {
		t.Fatalf("a 403 cut mid-read answered %v, want the 403", err)
	}
	if !strings.Contains(err.Error(), "answered 403 Forbidden") {
		t.Errorf("the answer reads %q, want it to name the 403", err)
	}
	rig.recorded(t, "Forbidden", "Forbidden")
	refusal, _ := AsRefusal(apiRefusal(err))
	if refusal == nil || refusal.Kind != RefusedByAPI || strings.Contains(refusal.Reason, "no answer came back") {
		t.Errorf("the sentence is %+v", refusal)
	}
}

// But an acceptance whose body was cut stays not known: what was made cannot
// be read, and "applied" would be a claim with nothing under it.
func TestAnAcceptanceWhoseBodyWasCutIsNotKnown(t *testing.T) {
	srv, _ := budgetServer(t, createdCut)
	rig := newEvictRig(t, &rest.Config{Host: srv.URL, RateLimiter: limited()})
	ctx, cancel := within(time.Minute)
	defer cancel()

	err := rig.evict(ctx)
	if !errors.Is(err, ErrUnanswered) || isAPIStatus(err) {
		t.Fatalf("a 201 cut mid-read answered %v, want not known", err)
	}
	rig.recorded(t, access.Unanswered, access.Unanswered)
}
