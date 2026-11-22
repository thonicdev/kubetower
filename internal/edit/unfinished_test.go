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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/audit"
)

// A dry run that did not complete is not a change that may still happen; a cut 409 to a create is AlreadyExists;
// every 5xx client-go retried leaves the attempt unfinished, and is named as
// what it was.

func settingsConfigMap(r evictRig) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": r.target.Name, "namespace": r.target.Namespace},
	}}
}

// withRetryAfter is status with a Retry-After and a body that is not a
// Status, as a proxy in front of the API server answers.
func withRetryAfter(status int) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, http.StatusText(status))
	}
}

// cut is status with a body that stops part-way.
func cut(status int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "400")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body[:len(body)/3])
	}
}

// Every create and every patch here runs a dry run first, and a dry run
// changes nothing: one the API server timed out on, or whose answer never came
// back, is not "may still happen" / "not known" nor `not_answered` - "do not
// run it again on your own" - since it cannot have changed anything. It is a
// check that did not complete, safe to run again (RefusedNotSent).
func TestADryRunThatDidNotCompleteChangedNothing(t *testing.T) {
	writes := []struct {
		name string
		do   func(context.Context, evictRig) error
	}{
		{"patch", func(ctx context.Context, r evictRig) error {
			_, _, err := Send(ctx, r.where, r.target, []byte(`{"data":{"a":"b"}}`), true)
			return err
		}},
		{"create", func(ctx context.Context, r evictRig) error {
			_, _, err := Create(ctx, r.where, r.target, settingsConfigMap(r), true)
			return err
		}},
	}
	outcomes := []struct {
		name string
		cfg  func(t *testing.T) *rest.Config
		word string
	}{
		{"held at the rate limit", func(t *testing.T) *rest.Config {
			return &rest.Config{Host: "https://prod.example", Transport: &fakeTransport{do: hang}, RateLimiter: starved()}
		}, access.Held},
		{"504 Timeout", func(t *testing.T) *rest.Config {
			srv, _ := budgetServer(t, apiTimeout)
			return &rest.Config{Host: srv.URL, RateLimiter: limited()}
		}, access.TimedOut},
		{"500 ServerTimeout", func(t *testing.T) *rest.Config {
			srv, _ := budgetServer(t, serverTimeoutFinal)
			return &rest.Config{Host: srv.URL, RateLimiter: limited()}
		}, access.TimedOut},
		{"a connection dropped after the write", func(t *testing.T) *rest.Config {
			srv, _ := budgetServer(t, dropConnection)
			return &rest.Config{Host: srv.URL, RateLimiter: limited()}
		}, access.Unanswered},
	}
	for _, write := range writes {
		for _, outcome := range outcomes {
			t.Run(write.name+"/"+outcome.name, func(t *testing.T) {
				rig := newEvictRig(t, outcome.cfg(t))
				ctx, cancel := within(time.Minute)
				defer cancel()

				err := write.do(ctx, rig)
				refusal, ok := AsRefusal(err)
				if !ok || refusal.Kind != RefusedNotSent {
					t.Fatalf("a dry run that did not complete answered %v, want RefusedNotSent", err)
				}
				for _, want := range []string{"dry run", "did not complete", "Nothing changed", "try again"} {
					if !strings.Contains(refusal.Reason, want) {
						t.Errorf("the sentence %q does not say %q", refusal.Reason, want)
					}
				}
				for _, not := range []string{"may still happen", "not known", "refused"} {
					if strings.Contains(refusal.Reason, not) {
						t.Errorf("the sentence %q says %q", refusal.Reason, not)
					}
				}
				// The record keeps what became of the request, beside the
				// dryRun that says nothing changed; a dry run writes no
				// `write:` line.
				got := rig.sink.records(t)
				if len(got) != 2 || !got[1].DryRun || got[1].Outcome != audit.Failed || got[1].Reason != outcome.word {
					t.Errorf("the record reads %+v, want a dry run failed/%s", got, outcome.word)
				}
				if len(*rig.logged) != 0 {
					t.Errorf("a dry run wrote %q", *rig.logged)
				}
			})
		}
	}
}

// A 409 to a create whose body was cut is not a Conflict - "this object
// changed while you had it open" - but a name already taken. To a create it is
// AlreadyExists, to a patch or an eviction still a Conflict; a
// cut 422 keeps its reason and loses its causes, which were in the body.
func TestACutAnswerIsRebuiltForTheRequestItAnswers(t *testing.T) {
	const conflictBody = `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"AlreadyExists","code":409,` +
		`"message":"configmaps \"settings\" already exists"}`
	const invalidBody = `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Invalid","code":422,` +
		`"message":"ConfigMap \"settings\" is invalid: data.a: Invalid value"}`
	for _, tc := range []struct {
		name  string
		write func(context.Context, evictRig) error
		body  func(http.ResponseWriter)
		is    func(error) bool
		kind  RefusalKind
	}{
		{"a create's 409", func(ctx context.Context, r evictRig) error {
			_, _, err := Create(ctx, r.where, r.target, settingsConfigMap(r), false)
			return err
		}, cut(http.StatusConflict, conflictBody), apierrors.IsAlreadyExists, RefusedByAPI},
		{"a patch's 409", func(ctx context.Context, r evictRig) error {
			_, _, err := Send(ctx, r.where, r.target, []byte(`{"data":{"a":"b"}}`), false)
			return err
		}, cut(http.StatusConflict, conflictBody), apierrors.IsConflict, RefusedConflict},
		// An eviction is a POST and creates nothing that could already
		// exist: its cut 409 is a Conflict, worded as the Evict button's
		// callers word it.
		{"an eviction's 409", func(ctx context.Context, r evictRig) error {
			return apiRefusal(r.evict(ctx))
		}, cut(http.StatusConflict, conflictBody), func(err error) bool {
			return apierrors.IsConflict(err) && !apierrors.IsAlreadyExists(err)
		}, RefusedConflict},
		{"a patch's 422", func(ctx context.Context, r evictRig) error {
			_, _, err := Send(ctx, r.where, r.target, []byte(`{"data":{"a":"b"}}`), false)
			return err
		}, cut(http.StatusUnprocessableEntity, invalidBody), apierrors.IsInvalid, RefusedByAPI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := budgetServer(t, tc.body)
			rig := newEvictRig(t, &rest.Config{Host: srv.URL, RateLimiter: limited()})
			ctx, cancel := within(time.Minute)
			defer cancel()

			err := tc.write(ctx, rig)
			refusal, ok := AsRefusal(err)
			if !ok || refusal.Kind != tc.kind || !tc.is(err) {
				t.Fatalf("answered %v (%+v), want kind %v", err, refusal, tc.kind)
			}
			if tc.kind == RefusedByAPI && !strings.Contains(refusal.Reason, "cut off") {
				t.Errorf("the sentence %q does not say the answer was cut off", refusal.Reason)
			}
		})
	}
}

// client-go asks again after any 5xx carrying Retry-After, so a proxy's 502,
// then a 409, is a create the first attempt may have made, not "the cluster
// refused the change: ... already exists". And a 503 with Retry-After
// cut while client-go waited is unfinished too, never "nothing more of it
// came back" - its body did. Each is named as what it was, not as a timeout.
func TestEvery5xxClientGoAskedAgainAfterLeavesTheWriteUnfinished(t *testing.T) {
	t.Run("a proxy's 502, then 409", func(t *testing.T) {
		srv, arrived := budgetServer(t, withRetryAfter(http.StatusBadGateway), alreadyExists)
		rig := newEvictRig(t, &rest.Config{Host: srv.URL, RateLimiter: limited()})
		ctx, cancel := within(time.Minute)
		defer cancel()

		_, _, err := Create(ctx, rig.where, rig.target, settingsConfigMap(rig), false)
		if arrived.Load() != 2 {
			t.Fatalf("the API server received %d creates, want the first and its retry", arrived.Load())
		}
		refusal, ok := AsRefusal(err)
		if !ok || refusal.Kind != RefusedUnanswered || !errors.Is(err, ErrTimedOut) ||
			!strings.Contains(refusal.Reason, "did not finish") || !strings.Contains(refusal.Reason, "502 Bad Gateway with Retry-After") ||
			strings.Contains(refusal.Reason, "timed out") || strings.Contains(refusal.Reason, "already exists") {
			t.Fatalf("502 then 409 answered %v (%+v)", err, refusal)
		}
		rig.recorded(t, access.TimedOut, access.TimedOut)
	})
	t.Run("503, then the deadline", func(t *testing.T) {
		srv, arrived := budgetServer(t, withRetryAfter(http.StatusServiceUnavailable))
		rig := newEvictRig(t, &rest.Config{Host: srv.URL, RateLimiter: limited()})
		ctx, cancel := within(300 * time.Millisecond)
		defer cancel()

		err := rig.evict(ctx)
		if arrived.Load() != 1 {
			t.Fatalf("the API server received %d evictions, want 1", arrived.Load())
		}
		timedOutWith(t, err, http.StatusServiceUnavailable)
		if strings.Contains(err.Error(), "nothing more") || strings.Contains(err.Error(), "timed out") {
			t.Errorf("the answer reads %q", err)
		}
		rig.recorded(t, access.TimedOut, access.TimedOut)
	})
}
