package access

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/transport"
)

// An eviction held back before it left, whoever said so, is recorded `held`:
// its error carries the context's as its cause, and `cancelled` would record a
// request that was never made. Never `unsent`, which is Guard's alone - a
// request no review covered, the security signal - and which a rate limit or a
// dial must not set off.
func TestNeverSentIsHeldNotCancelledAndNotUnsent(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("%w: %w", ErrNotSent, context.DeadlineExceeded),
		fmt.Errorf("%w: %w", ErrNotSent, context.Canceled),
		fmt.Errorf("%w: client rate limiter Wait returned an error", ErrNotSent),
	} {
		if got := FailureWord(err); got != "held" {
			t.Errorf("FailureWord(%v) = %q, want held", err, got)
		}
	}
	if got := FailureWord(fmt.Errorf("x: %w", ErrNotReviewed)); got != "unsent" {
		t.Errorf("the guard's hold is %q, want unsent", got)
	}
	if got := FailureWord(context.DeadlineExceeded); got != "cancelled" {
		t.Errorf("a request cut after it left is %q, want canceled", got)
	}
}

// Sent and not answered: cut by our context, `cancelled`; a
// connection that ended under it, `unanswered` rather than `error`.
func TestSentNotAnsweredIsCancelledOrUnanswered(t *testing.T) {
	if got := FailureWord(fmt.Errorf("%w: %w", ErrUnanswered, context.DeadlineExceeded)); got != "cancelled" {
		t.Errorf("cut by the deadline: %q, want canceled", got)
	}
	if got := FailureWord(fmt.Errorf("%w: %w", ErrUnanswered, io.EOF)); got != "unanswered" {
		t.Errorf("a dropped connection: %q, want unanswered", got)
	}
	if got := FailureWord(io.EOF); got != "error" {
		t.Errorf("an error nobody classified: %q, want error", got)
	}
}

// The words are a contract - an alert may be keyed on `unsent`, a reader of
// failures counts everything but `held`, `timed-out`, `cancelled` and
// `unanswered` as the cluster's no - so they are pinned as text, not compared
// against the constants: a test that did would pass `const Held = "unsent"`,
// which sets the security signal off on every rate limit.
func TestTheFailureWordsAreTheirOwnText(t *testing.T) {
	for _, word := range []struct{ got, want string }{
		{Unsent, "unsent"}, {Held, "held"}, {Unanswered, "unanswered"}, {TimedOut, "timed-out"},
	} {
		if word.got != word.want {
			t.Errorf("a failure word reads %q, want %q", word.got, word.want)
		}
	}
}

// The API server timing out is `timed-out`, whoever said it: its own 504 or
// 500 ServerTimeout, or ErrTimedOut over whatever came back after it - a
// retry cut by our deadline is still that answer, not `cancelled`, and a 409
// for a create the timed-out attempt may have made is not `AlreadyExists`.
func TestTheAPIServerTimingOutIsTimedOut(t *testing.T) {
	for name, err := range map[string]error{
		"504 Timeout":       apierrors.NewTimeoutError("request did not complete within requested timeout", 0),
		"500 ServerTimeout": apierrors.NewServerTimeout(schema.GroupResource{Resource: "pods"}, "create", 0),
		"a retry cut":       fmt.Errorf("%w: %w", ErrTimedOut, context.DeadlineExceeded),
		"then a 409":        fmt.Errorf("%w: %w", ErrTimedOut, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "configmaps"}, "x")),
	} {
		if got := FailureWord(err); got != "timed-out" {
			t.Errorf("%s: %q, want timed-out", name, got)
		}
	}
	if got := FailureWord(apierrors.NewInternalError(io.EOF)); got != "InternalError" {
		t.Errorf("a 500 that is not a timeout: %q, want InternalError", got)
	}
}

// Which answers leave a request unfinished, read from the status line and
// the headers: a 504, and any 5xx with Retry-After - every 5xx client-go
// retries, so every one a later error can hide (a proxy's 502 with
// Retry-After, then a 409 for a create, must not read "already exists"). A 5xx without Retry-After is the last word, and read as itself.
func TestWhichAnswersAreTimeouts(t *testing.T) {
	for _, tc := range []struct {
		status     int
		retryAfter string
		want       bool
	}{
		{http.StatusGatewayTimeout, "", true},
		{http.StatusInternalServerError, "1", true},
		{http.StatusInternalServerError, "", false},
		{http.StatusServiceUnavailable, "1", true},
		{http.StatusBadGateway, "1", true},
		{http.StatusBadGateway, "", false},
		{http.StatusServiceUnavailable, "", false},
		{http.StatusTooManyRequests, "10", false},
		{http.StatusForbidden, "", false},
	} {
		w := &wire{}
		_, _ = w.roundTrip(answering(tc.status, tc.retryAfter), false, &http.Request{})
		if got := w.snapshot().AnsweredTimedOut(); got != tc.want {
			t.Errorf("%d (Retry-After %q): AnsweredTimedOut=%v, want %v", tc.status, tc.retryAfter, got, tc.want)
		}
	}
}

// answering is a transport that answers status, with Retry-After when set.
func answering(status int, retryAfter string) http.RoundTripper {
	return roundTripper(func(*http.Request) (*http.Response, error) {
		header := http.Header{}
		if retryAfter != "" {
			header.Set("Retry-After", retryAfter)
		}
		return &http.Response{StatusCode: status, Header: header, Body: http.NoBody}, nil
	})
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// What Delivery says, and that it says nothing about a request nothing saw.
func TestWhatDeliverySays(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		d                                  Delivery
		notSent, held, unanswered, only429 bool
	}{
		{"nothing saw it", Delivery{}, false, false, false, false},
		{"held at the limiter", Delivery{Asked: true}, true, true, false, false},
		{"handed, no connection", Delivery{Asked: true, Handed: 1}, true, false, false, false},
		{"left, unanswered", Delivery{Asked: true, Handed: 1, Left: 1}, false, false, true, false},
		{"left, answered 429", Delivery{Asked: true, Handed: 1, Left: 1, Answered: 1, Statuses: []int{429}}, false, false, false, true},
		{"429, then 429", Delivery{Asked: true, Handed: 2, Left: 2, Answered: 2, Statuses: []int{429, 429}}, false, false, false, true},
		{"500, then 429", Delivery{Asked: true, Handed: 2, Left: 2, Answered: 2, Statuses: []int{500, 429}}, false, false, false, false},
		{"429, then 503", Delivery{Asked: true, Handed: 2, Left: 2, Answered: 2, Statuses: []int{429, 503}}, false, false, false, false},
		{"429, then no answer", Delivery{Asked: true, Handed: 2, Left: 2, Answered: 1, Statuses: []int{429}}, false, false, true, false},
	} {
		last, known := tc.d.LastStatus()
		if wantKnown := !tc.unanswered && tc.d.Left > 0; known != wantKnown ||
			(known && last != tc.d.Statuses[len(tc.d.Statuses)-1]) {
			t.Errorf("%s: LastStatus = %d, %v", tc.name, last, known)
		}
		d := tc.d
		if d.NotSent() != tc.notSent || d.HeldBeforeTransport() != tc.held || d.Unanswered() != tc.unanswered ||
			d.AnsweredOnly(429) != tc.only429 {
			t.Errorf("%s: NotSent=%v HeldBeforeTransport=%v Unanswered=%v AnsweredOnly(429)=%v", tc.name,
				d.NotSent(), d.HeldBeforeTransport(), d.Unanswered(), d.AnsweredOnly(429))
		}
	}
}

type opaque struct{}

func (opaque) RoundTrip(*http.Request) (*http.Response, error) { return nil, nil }

// Only a net/http Transport, reached through wrappers that say what they wrap,
// is watched for its connection; anything else counts as sent once handed.
func TestOnlyANetHTTPTransportIsWatched(t *testing.T) {
	base := &http.Transport{}
	if !watchable(base) {
		t.Error("a net/http Transport is not watched")
	}
	if !watchable(transport.NewUserAgentRoundTripper("kubetower", base)) {
		t.Error("a net/http Transport behind client-go's own wrapper is not watched")
	}
	if watchable(opaque{}) || watchable(transport.NewUserAgentRoundTripper("kubetower", opaque{})) {
		t.Error("an opaque transport is watched: an attempt handed to it could be called not sent")
	}
}
