package access

// What became of a reviewed request on its way out: whether it left this
// process, and whether what left was answered.
//
// A write that ends in DeadlineExceeded was either sent and unanswered
// (whether it happened is unknown) or never left: held by the rate limiter,
// no connection made, or the deadline already past. Only the second is safe
// to run again without a look, and neither the error nor its text tells them
// apart, so the client path records what it did on the review's stamp:
//
//   - the cluster's rate limiter (Limiter) marks that client-go began an
//     attempt: it asks for a token before every one, retries included;
//   - Guard marks each attempt it hands to the transport, and watches through
//     net/http's trace whether a connection was obtained for it. Until one
//     is, no byte can have been written; after, it counts as sent. Under a
//     transport whose connections cannot be watched, handing it over counts
//     as sent.
//
// Invariant: "not sent" is said only when nothing was sent. Every doubt
// resolves to "sent". When nothing on the path saw the request (a client
// without Guard or Limiter, only a test's fake here), nothing is said and
// the caller falls back to the error.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"

	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/client-go/util/flowcontrol"
)

// ErrNotSent is what a write that never left this process unwraps to. Its
// audit outcome is `held` (FailureWord): never `cancelled`, which says the
// request was made, nor `unsent`, which is Guard's for a request no review
// covered.
var ErrNotSent = errors.New("not sent")

// ErrUnanswered is what a write that left this process, and whose answer
// never came back, unwraps to: whether it happened is not known. Its audit
// outcome is `cancelled` when a context of ours cut it and `unanswered`
// otherwise (FailureWord).
var ErrUnanswered = errors.New("sent, not answered")

// ErrTimedOut is what a write the API server answered by timing out unwraps
// to: a 504 Timeout or 500 ServerTimeout, which may still be carried out, or
// any 5xx with Retry-After that client-go retried, whose first attempt may
// have been carried out whatever came back later. It was answered, so it is
// not ErrUnanswered, and it is never counted or worded as a refusal. Its
// audit outcome is `timed-out` (FailureWord).
var ErrTimedOut = errors.New("answered: the API server timed out, the change may still happen")

// Delivery is what the client path saw of the requests made on one review's
// stamp. The zero value is "nothing saw anything".
type Delivery struct {
	// Asked is set when client-go asked the cluster's rate limiter for a token
	// for an attempt (Limiter).
	Asked bool
	// Handed is the number of attempts Guard handed to the transport.
	Handed int
	// Left is the number of attempts that may have left the process: a
	// connection was obtained for each, or the transport is one whose
	// connections cannot be watched.
	Left int
	// Answered is the number of attempts a response came back for.
	Answered int
	// Statuses is each answer's status code, in the order they came.
	Statuses []int
	// TimedOut is the number of answers that left the request unfinished
	// (ErrTimedOut): a 504, or any 5xx with Retry-After, which client-go
	// retries, so a later answer can hide it. A proxy's 502 with Retry-After
	// then a 409 for a create is a create that may have been made.
	TimedOut int
	// TimedOutStatus is the first such answer's status, so that a sentence
	// can say which it was rather than call every one a timeout.
	TimedOutStatus int
	// Cut is set when the last answer's body ended before it was whole (a read
	// error that is not EOF). Watched only on answers of 400 and over: a 101
	// upgrade's body is the connection and is not touched.
	Cut bool
}

// Seen reports whether anything on the client path saw the request at all.
// When it did not, Delivery says nothing and the error is all there is.
func (d Delivery) Seen() bool { return d.Asked || d.Handed > 0 }

// NotSent reports that the client path saw the request and that no attempt of
// it left the process. Never true for one that was not seen.
func (d Delivery) NotSent() bool { return d.Seen() && d.Left == 0 }

// HeldBeforeTransport reports a request that never reached the transport:
// held at the rate limiter, or anything client-go does before handing it over.
func (d Delivery) HeldBeforeTransport() bool { return d.Asked && d.Handed == 0 }

// Unanswered reports whether an attempt left and no answer came back for it.
func (d Delivery) Unanswered() bool { return d.Left > d.Answered }

// AnsweredTimedOut reports whether an answer said the API server timed out on
// the request: whatever came after it, that attempt may still be carried out.
func (d Delivery) AnsweredTimedOut() bool { return d.TimedOut > 0 }

// LastStatus is the status of the last answer, and whether every attempt that
// left was answered - so that status is the last word the API server said,
// even where what followed it (the rest of the body, a retry's wait) was lost.
func (d Delivery) LastStatus() (int, bool) {
	if d.Left == 0 || d.Answered != d.Left || len(d.Statuses) == 0 {
		return 0, false
	}
	return d.Statuses[len(d.Statuses)-1], true
}

// AnsweredOnly reports whether at least one attempt left, every attempt that
// left was answered, and every answer was status. Not only the last: client-go
// retries a 5xx with Retry-After, POST included, and a 500 ServerTimeout
// first means the request may have been carried out.
func (d Delivery) AnsweredOnly(status int) bool {
	if d.Left == 0 || d.Answered != d.Left || len(d.Statuses) != d.Answered {
		return false
	}
	for _, got := range d.Statuses {
		if got != status {
			return false
		}
	}
	return true
}

// DeliveryOf is what the client path saw of the requests made on ctx's stamp
// so far: the zero Delivery when ctx carries no stamp from Perform. Read it
// after the request has returned; client-go's attempts are made in turn, so by
// then every one has been recorded.
func DeliveryOf(ctx context.Context) Delivery {
	s, ok := stampOf(ctx)
	if !ok || s.wire == nil {
		return Delivery{}
	}
	return s.wire.snapshot()
}

// wire is Delivery, as it is written to while the request is under way.
type wire struct {
	mu sync.Mutex
	d  Delivery
}

func (w *wire) snapshot() Delivery {
	w.mu.Lock()
	defer w.mu.Unlock()
	d := w.d
	d.Statuses = append([]int(nil), w.d.Statuses...)
	return d
}

func (w *wire) asked() {
	w.mu.Lock()
	w.d.Asked = true
	w.mu.Unlock()
}

// roundTrip hands one attempt to next and records what became of it.
// watchable is whether next is a net/http Transport, possibly behind wrappers
// that say what they wrap; only there does "no connection was obtained" mean
// "nothing was written".
func (w *wire) roundTrip(next http.RoundTripper, watchable bool, req *http.Request) (*http.Response, error) {
	w.mu.Lock()
	w.d.Handed++
	w.mu.Unlock()

	var connected atomic.Bool
	if watchable {
		// GotConn fires once a connection is the request's, HTTP/1 and HTTP/2
		// alike, before anything is written to it. A failed dial or handshake,
		// or a context that runs out while waiting, never reaches it.
		trace := &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { connected.Store(true) }}
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	}
	resp, err := next.RoundTrip(req)

	w.mu.Lock()
	defer w.mu.Unlock()
	if !watchable || connected.Load() || resp != nil {
		w.d.Left++
	}
	if err == nil && resp != nil {
		w.d.Answered++
		w.d.Statuses = append(w.d.Statuses, resp.StatusCode)
		if timedOut(resp) {
			if w.d.TimedOut == 0 {
				w.d.TimedOutStatus = resp.StatusCode
			}
			w.d.TimedOut++
		}
		w.d.Cut = false
		if resp.StatusCode >= http.StatusBadRequest && resp.Body != nil {
			resp.Body = &watchedBody{ReadCloser: resp.Body, w: w}
		}
	}
	return resp, err
}

// timedOut reports whether resp left the request unfinished (Delivery.TimedOut).
// Status line and headers only: the body is client-go's to read.
func timedOut(resp *http.Response) bool {
	if resp.StatusCode == http.StatusGatewayTimeout {
		return true
	}
	return resp.StatusCode >= http.StatusInternalServerError && resp.Header.Get("Retry-After") != ""
}

// watchedBody is an error answer's body, marking Delivery.Cut when a read of
// it fails with anything but EOF.
type watchedBody struct {
	io.ReadCloser
	w *wire
}

func (b *watchedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		b.w.mu.Lock()
		b.w.d.Cut = true
		b.w.mu.Unlock()
	}
	return n, err
}

// watchable reports whether rt is a net/http Transport, through any wrapper
// that names what it wraps (client-go's RoundTripperWrapper). A wrapper that
// does not say is opaque, and an attempt handed to it counts as sent.
func watchable(rt http.RoundTripper) bool {
	for range 16 {
		switch t := rt.(type) {
		case *http.Transport:
			return true
		case utilnet.RoundTripperWrapper:
			rt = t.WrappedRoundTripper()
		default:
			return false
		}
	}
	return false
}

// Limiter wraps the cluster's shared rate limiter to mark on a reviewed
// request's stamp that client-go asked it for a token: the first half of
// Delivery's "held before the transport".
func Limiter(limiter flowcontrol.RateLimiter) flowcontrol.RateLimiter {
	if limiter == nil {
		return nil
	}
	return &markingLimiter{RateLimiter: limiter}
}

type markingLimiter struct {
	flowcontrol.RateLimiter
}

// Wait is the only call client-go makes with the request's context, once per
// attempt, before the attempt is made.
func (l *markingLimiter) Wait(ctx context.Context) error {
	if s, ok := stampOf(ctx); ok && s.wire != nil {
		s.wire.asked()
	}
	return l.RateLimiter.Wait(ctx)
}
