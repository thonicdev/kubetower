package edit

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/audit"
)

// What became of an eviction on its way out is read from the transport, never
// from the error's text.
//
// Every test here sends a real eviction through a real dynamic client, on a
// config carrying the access guard and the marked rate limiter the cluster
// manager gives every client, so client-go's own wrapping and retrying is what
// is classified. Each asserts the invariant from both sides: "not sent" only
// where the transport, fake or real, saw nothing arrive, and never where it
// did.

type evictRig struct {
	where  Location
	target Target
	sink   *recordSink
	logged *[]string
}

// newEvictRig guards cfg as the cluster manager does and builds a door on it,
// recording to a sink.
func newEvictRig(t *testing.T, cfg *rest.Config) evictRig {
	t.Helper()
	access.Guard(cfg, "prod", nil)
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordSink{}
	var mu sync.Mutex
	logged := &[]string{}
	gate := access.New(access.Options{Reviews: (&accesstest.Reviews{}).For, Audit: audit.New(sink)})
	w := New(Options{
		Resolve: fakeResolver{dyn: dyn, cluster: "prod"},
		Access:  gate,
		Logf: func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			*logged = append(*logged, fmt.Sprintf(format, args...))
		},
	})
	target := Target{Cluster: "prod", Kind: "configmaps", Namespace: "shop", Name: "settings"}
	where, err := w.Locate(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	return evictRig{where: where, target: target, sink: sink, logged: logged}
}

func (r evictRig) evict(ctx context.Context) error {
	_, err := Evict(ctx, r.where, r.target, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "policy/v1", "kind": "Eviction",
		"metadata": map[string]any{"name": r.target.Name, "namespace": r.target.Namespace},
	}})
	return err
}

// recorded asserts the outcome the audit record and the `write:` line give.
func (r evictRig) recorded(t *testing.T, reason, line string) {
	t.Helper()
	got := r.sink.records(t)
	if len(got) != 2 || got[1].Outcome != audit.Failed || got[1].Reason != reason {
		t.Errorf("the audit record reads %+v, want failed/%s", got, reason)
	}
	if len(*r.logged) != 1 || !strings.Contains((*r.logged)[0], "outcome=failed reason="+line+" ") {
		t.Errorf("the write: line reads %q, want reason=%s", *r.logged, line)
	}
}

func notSentAt(t *testing.T, err error, at HeldAt) {
	t.Helper()
	held, ok := AsNotSent(err)
	if !ok || !errors.Is(err, ErrNotSent) || !errors.Is(err, access.ErrNotSent) {
		t.Fatalf("answered %v, want a request that was not sent", err)
	}
	if held.At != at {
		t.Errorf("held at %v (%q), want %v", held.At, held.At, at)
	}
}

func sentNotAnswered(t *testing.T, err error) {
	t.Helper()
	if errors.Is(err, ErrNotSent) {
		t.Fatalf("a request the transport received answered %v: never \"not sent\"", err)
	}
	if !errors.Is(err, ErrUnanswered) {
		t.Errorf("a request sent and not answered answered %v, want ErrUnanswered", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) || isAPIStatus(err) {
		t.Errorf("a request sent and not answered answered %v, want the deadline and no answer", err)
	}
}

// fakeTransport records every request that arrives at it - the transport the
// guard hands requests to - and answers with do.
type fakeTransport struct {
	arrived atomic.Int32
	do      func(*http.Request) (*http.Response, error)
}

func (f *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.arrived.Add(1)
	return f.do(req)
}

// hang answers nothing until the request's context runs out.
func hang(req *http.Request) (*http.Response, error) {
	<-req.Context().Done()
	return nil, req.Context().Err()
}

// starved is a token bucket with its one token spent: the next one is a
// thousand seconds away, so a wait under any deadline here would overrun it,
// and x/time/rate says so at once - "rate: Wait(n=1) would exceed context
// deadline", which client-go wraps as "client rate limiter Wait returned an
// error". The shape is the cluster's own (internal/cluster/limits.go:42-43,
// 50/s, burst 100), emptied.
func starved() flowcontrol.RateLimiter {
	bucket := flowcontrol.NewTokenBucketRateLimiter(0.001, 1)
	bucket.TryAccept()
	return access.Limiter(bucket)
}

// waitsOut is a limiter that never has a token and answers its wait with the
// bare ctx.Err() when the deadline passes - client-go passes that through
// unwrapped, so it reads exactly like a request sent and unanswered.
type waitsOut struct{}

func (waitsOut) TryAccept() bool { return false }
func (waitsOut) Accept()         {}
func (waitsOut) Stop()           {}
func (waitsOut) QPS() float32    { return 1 }
func (waitsOut) Wait(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func within(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(carol(), d)
}

// The per-cluster bucket starved: an eviction refused by the limiter before it
// left must not read as an answer - "refused", counted under `refused=` - which
// client-go's wrapping invites, because it matches neither context error.
func TestAnEvictionTheRateLimiterRefusedIsNotSentNotRefused(t *testing.T) {
	transport := &fakeTransport{do: hang}
	rig := newEvictRig(t, &rest.Config{Host: "https://prod.example", Transport: transport, RateLimiter: starved()})
	ctx, cancel := within(time.Minute)
	defer cancel()

	err := rig.evict(ctx)
	notSentAt(t, err, HeldBeforeTransport)
	if errors.Is(err, context.DeadlineExceeded) || isAPIStatus(err) {
		t.Errorf("the limiter's refusal answered %v; it is neither the deadline nor the API server", err)
	}
	if n := transport.arrived.Load(); n != 0 {
		t.Errorf("%d requests reached the transport", n)
	}
	rig.recorded(t, access.Held, "held")
}

// The reverse misreading: the limiter answering its wait with a bare ctx.Err()
// read as "sent, not answered".
func TestAnEvictionThatWaitedOutTheRateLimiterIsNotSent(t *testing.T) {
	transport := &fakeTransport{do: hang}
	rig := newEvictRig(t, &rest.Config{Host: "https://prod.example", Transport: transport, RateLimiter: access.Limiter(waitsOut{})})
	ctx, cancel := within(50 * time.Millisecond)
	defer cancel()

	err := rig.evict(ctx)
	notSentAt(t, err, HeldBeforeTransport)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("answered %v, want the deadline as its cause", err)
	}
	if n := transport.arrived.Load(); n != 0 {
		t.Errorf("%d requests reached the transport", n)
	}
	rig.recorded(t, access.Held, "held")
}

// **The invariant.** An eviction the transport received is never "not sent",
// however it ends - here under a transport whose connections cannot be
// watched, where arriving is all there is to go on.
func TestAnEvictionThatReachedTheTransportIsNeverNotSent(t *testing.T) {
	transport := &fakeTransport{do: hang}
	rig := newEvictRig(t, &rest.Config{Host: "https://prod.example", Transport: transport,
		RateLimiter: access.Limiter(flowcontrol.NewTokenBucketRateLimiter(50, 100))})
	ctx, cancel := within(50 * time.Millisecond)
	defer cancel()

	err := rig.evict(ctx)
	if n := transport.arrived.Load(); n != 1 {
		t.Fatalf("%d requests reached the transport, want 1", n)
	}
	sentNotAnswered(t, err)
	rig.recorded(t, "cancelled", "cancelled")
}

// The other reverse misreading: a dial that never completes read as "sent,
// not answered". Through net/http's own Transport, whose trace says no
// connection was obtained, so not a byte was written.
func TestAnEvictionWithNoConnectionIsNotSent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		dial     func(ctx context.Context) (net.Conn, error)
		deadline bool
	}{
		{"a dial that times out", func(ctx context.Context) (net.Conn, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
				return nil, errors.New("the test's dial gave up")
			}
		}, true},
		{"a dial refused", func(context.Context) (net.Conn, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dials atomic.Int32
			rig := newEvictRig(t, &rest.Config{
				Host: "http://192.0.2.10:6443",
				Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
					dials.Add(1)
					return tc.dial(ctx)
				},
				RateLimiter: access.Limiter(flowcontrol.NewTokenBucketRateLimiter(50, 100)),
			})
			ctx, cancel := within(100 * time.Millisecond)
			defer cancel()

			err := rig.evict(ctx)
			notSentAt(t, err, HeldBeforeConnection)
			if dials.Load() == 0 {
				t.Error("nothing was dialled: the request never reached the transport, and this tests the connection")
			}
			if tc.deadline != errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("answered %v", err)
			}
			rig.recorded(t, access.Held, "held")
		})
	}
}

// An API server that received the eviction and had not answered by the
// deadline: sent, not answered - through net/http's Transport, over HTTP/1.1
// and over HTTP/2, so the connection the trace reports is the real one.
func TestAnEvictionSentAndNotAnsweredIsNeverNotSent(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "http/1.1", true: "http/2"}[h2], func(t *testing.T) {
			var arrived atomic.Int32
			var proto atomic.Int32
			release := make(chan struct{})
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				arrived.Add(1)
				proto.Store(int32(r.ProtoMajor))
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			cfg := &rest.Config{RateLimiter: access.Limiter(flowcontrol.NewTokenBucketRateLimiter(50, 100))}
			if h2 {
				srv.EnableHTTP2 = true
				srv.StartTLS()
				cfg.TLSClientConfig.CAData = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
			} else {
				srv.Start()
			}
			defer srv.Close()
			defer close(release)
			cfg.Host = srv.URL
			rig := newEvictRig(t, cfg)
			ctx, cancel := within(300 * time.Millisecond)
			defer cancel()

			err := rig.evict(ctx)
			if arrived.Load() != 1 {
				t.Fatalf("the API server received %d evictions, want 1", arrived.Load())
			}
			if want := map[bool]int32{false: 1, true: 2}[h2]; proto.Load() != want {
				t.Fatalf("the eviction arrived over HTTP/%d, want HTTP/%d", proto.Load(), want)
			}
			sentNotAnswered(t, err)
			rig.recorded(t, "cancelled", "cancelled")
		})
	}
}

// budgetServer answers each eviction in turn with answers[i], and hangs on any
// beyond them.
func budgetServer(t *testing.T, answers ...func(http.ResponseWriter)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var arrived atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		n := int(arrived.Add(1))
		if n <= len(answers) {
			answers[n-1](w)
			return
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	return srv, &arrived
}

// tooMany is a PodDisruptionBudget's answer as kube-apiserver writes it: 429,
// with the Retry-After that makes client-go try again.
func tooMany(retryAfter string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", retryAfter)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"TooManyRequests",`+
			`"code":429,"message":"Cannot evict pod as it would violate the pod's disruption budget.",`+
			`"details":{"retryAfterSeconds":`+retryAfter+`}}`)
	}
}

// A budget's 429 that client-go retried past the deadline. client-go sleeps
// the Retry-After before trying again, and the deadline cuts that sleep: it
// answers a bare DeadlineExceeded with the 429 gone, which would read "sent,
// not answered - whether the pod is going is not known". The budget had
// refused it, and nothing else had been sent: it is the budget's refusal.
func TestABudgetRefusalRetriedPastTheDeadlineIsTheBudgetsAnswer(t *testing.T) {
	srv, arrived := budgetServer(t, tooMany("5"))
	rig := newEvictRig(t, &rest.Config{Host: srv.URL, RateLimiter: access.Limiter(flowcontrol.NewTokenBucketRateLimiter(50, 100))})
	ctx, cancel := within(300 * time.Millisecond)
	defer cancel()

	err := rig.evict(ctx)
	if arrived.Load() != 1 {
		t.Fatalf("the API server received %d evictions, want 1", arrived.Load())
	}
	if !apierrors.IsTooManyRequests(err) || errors.Is(err, ErrNotSent) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a budget's refusal retried past the deadline answered %v, want the 429", err)
	}
	rig.recorded(t, "TooManyRequests", "TooManyRequests")
}

// But a retry that was sent and not answered leaves the outcome unknown: the
// second attempt may have been accepted.
func TestABudgetRefusalWhoseRetryIsUnansweredIsUnanswered(t *testing.T) {
	srv, arrived := budgetServer(t, tooMany("1"))
	rig := newEvictRig(t, &rest.Config{Host: srv.URL, RateLimiter: access.Limiter(flowcontrol.NewTokenBucketRateLimiter(50, 100))})
	ctx, cancel := within(1500 * time.Millisecond)
	defer cancel()

	err := rig.evict(ctx)
	if arrived.Load() != 2 {
		t.Fatalf("the API server received %d evictions, want the first and its retry", arrived.Load())
	}
	sentNotAnswered(t, err)
	if apierrors.IsTooManyRequests(err) {
		t.Errorf("an unanswered retry answered %v: the 429 is not the last word", err)
	}
}

// The audit record reads the transport for every write, not only an
// eviction: a patch the rate limiter held is recorded `held`, never
// `cancelled` or the cluster's error, and never `unsent`, the guard's
// security signal. The patch's own
// sentence says it was not sent (TestEveryWriteSaysWhatItsAuditRecordSays).
func TestAPatchTheRateLimiterHeldIsRecordedHeld(t *testing.T) {
	transport := &fakeTransport{do: hang}
	rig := newEvictRig(t, &rest.Config{Host: "https://prod.example", Transport: transport, RateLimiter: starved()})
	ctx, cancel := within(time.Minute)
	defer cancel()

	if _, _, err := Send(ctx, rig.where, rig.target, []byte(`{"data":{"a":"b"}}`), false); err == nil {
		t.Fatal("a patch the limiter held answered nothing")
	}
	if n := transport.arrived.Load(); n != 0 {
		t.Fatalf("%d requests reached the transport", n)
	}
	got := rig.sink.records(t)
	if len(got) != 2 || got[1].Outcome != audit.Failed || got[1].Reason != access.Held {
		t.Errorf("the audit record reads %+v, want failed/held", got)
	}
}

// serverTimeout is a 500 ServerTimeout as kube-apiserver writes one, with the
// Retry-After that makes client-go try again: the create may yet be carried
// out.
func serverTimeout(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "1")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"ServerTimeout",`+
		`"code":500,"message":"the server cannot complete the requested operation at this time, try again later",`+
		`"details":{"retryAfterSeconds":1}}`)
}

// A 500 ServerTimeout, then a 429, then the deadline. Read as "the last answer
// was 429" it would become the budget's refusal - "each of the 2 attempts ...
// with 429" - although the first attempt may have evicted the pod. Every answer
// has to be 429 for the 429 to be the answer.
//
// Nor is it "sent, not answered": the first answer said the API server timed
// out, so the eviction may still happen, and the record says `timed-out`.
func TestA5xxThenA429IsNotTheBudgetsRefusal(t *testing.T) {
	srv, arrived := budgetServer(t, serverTimeout, tooMany("5"))
	rig := newEvictRig(t, &rest.Config{Host: srv.URL, RateLimiter: access.Limiter(flowcontrol.NewTokenBucketRateLimiter(50, 100))})
	ctx, cancel := within(2500 * time.Millisecond)
	defer cancel()

	err := rig.evict(ctx)
	if arrived.Load() != 2 {
		t.Fatalf("the API server received %d evictions, want the first and its retry", arrived.Load())
	}
	if apierrors.IsTooManyRequests(err) {
		t.Fatalf("500 then 429 answered %v: not the budget's refusal, the first may have been carried out", err)
	}
	timedOutWith(t, err, http.StatusInternalServerError)
	rig.recorded(t, access.TimedOut, access.TimedOut)
}

// 429, then 429, then the deadline: every answer the budget's, so the refusal
// stands, and the sentence counts both.
func TestTwoBudgetRefusalsThenTheDeadlineAreTheBudgetsAnswer(t *testing.T) {
	srv, arrived := budgetServer(t, tooMany("1"), tooMany("5"))
	rig := newEvictRig(t, &rest.Config{Host: srv.URL, RateLimiter: access.Limiter(flowcontrol.NewTokenBucketRateLimiter(50, 100))})
	ctx, cancel := within(1500 * time.Millisecond)
	defer cancel()

	err := rig.evict(ctx)
	if arrived.Load() != 2 {
		t.Fatalf("the API server received %d evictions, want 2", arrived.Load())
	}
	if !apierrors.IsTooManyRequests(err) || errors.Is(err, ErrUnanswered) ||
		!strings.Contains(err.Error(), "each of the 2 attempts client-go made with 429") {
		t.Fatalf("429 then 429 answered %v, want the budget's refusal of both", err)
	}
	rig.recorded(t, "TooManyRequests", "TooManyRequests")
}

// A connection that ends after the eviction was written (EOF) is sent, not
// answered - never the API server's answer: not "the cluster refused the
// change", `refused=` in the drain, a 403 on the Evict button, or
// `failed/error` in the record.
func TestAnEvictionWhoseConnectionDroppedIsSentNotAnswered(t *testing.T) {
	var arrived atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		arrived.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer srv.Close()
	rig := newEvictRig(t, &rest.Config{Host: srv.URL, RateLimiter: access.Limiter(flowcontrol.NewTokenBucketRateLimiter(50, 100))})
	ctx, cancel := within(time.Minute)
	defer cancel()

	err := rig.evict(ctx)
	if arrived.Load() != 1 {
		t.Fatalf("the API server received %d evictions, want 1", arrived.Load())
	}
	if !errors.Is(err, ErrUnanswered) || errors.Is(err, ErrNotSent) || isAPIStatus(err) ||
		errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a dropped connection answered %v, want sent and not answered", err)
	}
	rig.recorded(t, access.Unanswered, access.Unanswered)
}

// The record reads the transport for every write here too: a patch whose
// connection ended after it was written is audited `unanswered`, not `error`,
// and says so (TestEveryWriteSaysWhatItsAuditRecordSays).
func TestAPatchWhoseConnectionDroppedIsRecordedUnanswered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
	}))
	defer srv.Close()
	rig := newEvictRig(t, &rest.Config{Host: srv.URL, RateLimiter: access.Limiter(flowcontrol.NewTokenBucketRateLimiter(50, 100))})
	ctx, cancel := within(time.Minute)
	defer cancel()

	if _, _, err := Send(ctx, rig.where, rig.target, []byte(`{"data":{"a":"b"}}`), false); err == nil {
		t.Fatal("a patch whose connection dropped answered nothing")
	}
	got := rig.sink.records(t)
	if len(got) != 2 || got[1].Outcome != audit.Failed || got[1].Reason != access.Unanswered {
		t.Errorf("the audit record reads %+v, want failed/unanswered", got)
	}
}
