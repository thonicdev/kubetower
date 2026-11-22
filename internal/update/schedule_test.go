package update

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// clock is a fake clock a test moves by hand. Nothing in this file sleeps or
// waits on real time: the schedule is driven tick by tick, so what is asserted
// is the windows, not a scheduler's luck.
type clock struct{ at atomic.Int64 }

func newClock() *clock {
	c := &clock{}
	c.at.Store(time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC).UnixNano())
	return c
}

func (c *clock) now() time.Time                       { return time.Unix(0, c.at.Load()).UTC() }
func (c *clock) set(start time.Time, d time.Duration) { c.at.Store(start.Add(d).UnixNano()) }

// schedule runs the watch loop on hand-sent ticks and stops it at the end of
// the test.
type schedule struct {
	t      *testing.T
	ticks  chan time.Time
	ticked chan struct{}
}

func watching(t *testing.T, checker *Checker) *schedule {
	t.Helper()
	s := &schedule{t: t, ticks: make(chan time.Time), ticked: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		checker.watch(ctx, s.ticks, s.ticked)
		close(stopped)
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
	return s
}

// tick sends n ticks and returns once each has been handled. The deadline is
// a bound on a broken loop, never something the assertions wait out.
func (s *schedule) tick(n int) {
	s.t.Helper()
	for range n {
		for _, step := range []func() bool{
			func() bool {
				select {
				case s.ticks <- time.Time{}:
					return true
				case <-time.After(5 * time.Second):
					return false
				}
			},
			func() bool {
				select {
				case <-s.ticked:
					return true
				case <-time.After(5 * time.Second):
					return false
				}
			},
		} {
			if !step() {
				s.t.Fatal("the watch loop did not handle a tick")
			}
		}
	}
}

// github answers like api.github.com: a release, or a status code when `fail`
// is set. It counts every request.
func github(t *testing.T, tag string, fail *atomic.Int32) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail != nil {
			if code := fail.Load(); code != 0 {
				w.WriteHeader(int(code))
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"tag_name": tag,
			"html_url": "https://example.invalid/releases/" + tag,
		})
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

// A console left running asks GitHub again once a day, and no sooner however
// many ticks arrive inside the day.
func TestTheScheduleAsksAgainOnceADayAndNoSooner(t *testing.T) {
	server, calls := github(t, "v0.3.0", nil)
	fake := newClock()
	start := fake.now()
	checker := New(Options{
		Version: "0.2.1", Enabled: true, Dir: t.TempDir(), Endpoint: server.URL, Now: fake.now,
	})
	checker.Check(context.Background())
	s := watching(t, checker)

	for _, at := range []time.Duration{time.Minute, time.Hour, 12 * time.Hour, goodFor - time.Second} {
		fake.set(start, at)
		s.tick(1)
	}
	s.tick(100)
	if got := calls.Load(); got != 1 {
		t.Fatalf("%d requests through 23 h 59 min 59 s, want 1", got)
	}

	fake.set(start, goodFor)
	s.tick(1)
	if got := calls.Load(); got != 2 {
		t.Fatalf("%d requests at 24 h, want 2: a day-old answer is asked again", got)
	}
	if got := checker.Report(); got.State != Newer || !got.CheckedAt.Equal(fake.now()) {
		t.Errorf("after the re-check the report is %+v, want newer, checked at %v", got, fake.now())
	}

	for h := 1; h < 24; h++ {
		fake.set(start, goodFor+time.Duration(h)*time.Hour)
		s.tick(1)
	}
	fake.set(start, 2*goodFor-time.Second)
	s.tick(100)
	if got := calls.Load(); got != 2 {
		t.Fatalf("%d requests in the day after the re-check, want 2", got)
	}

	fake.set(start, 2*goodFor)
	s.tick(1)
	if got := calls.Load(); got != 3 {
		t.Errorf("%d requests at 48 h, want 3", got)
	}
}

// A failed check is tried again after an hour, not on every tick, and once
// GitHub answers the day's window applies again.
func TestTheScheduleRetriesAFailureAfterAnHourWithoutHammering(t *testing.T) {
	var fail atomic.Int32
	fail.Store(http.StatusServiceUnavailable)
	server, calls := github(t, "v0.3.0", &fail)
	fake := newClock()
	start := fake.now()
	checker := New(Options{
		Version: "0.2.1", Enabled: true, Dir: t.TempDir(), Endpoint: server.URL, Now: fake.now,
	})
	checker.Check(context.Background())
	if got := checker.Report().State; got != Failed {
		t.Fatalf("state %q after a 503, want failed", got)
	}
	s := watching(t, checker)

	fake.set(start, retryAfter-time.Second)
	s.tick(100)
	if got := calls.Load(); got != 1 {
		t.Fatalf("%d requests through 59 min 59 s after a failure, want 1: no hammering", got)
	}

	fake.set(start, retryAfter)
	s.tick(1)
	if got := calls.Load(); got != 2 {
		t.Fatalf("%d requests at 1 h after a failure, want 2", got)
	}
	s.tick(100)
	if got := calls.Load(); got != 2 {
		t.Fatalf("%d requests after the retry failed again, want 2", got)
	}

	fail.Store(0)
	fake.set(start, 2*retryAfter)
	s.tick(1)
	if got := calls.Load(); got != 3 {
		t.Fatalf("%d requests at 2 h, want 3", got)
	}
	if got := checker.Report().State; got != Newer {
		t.Fatalf("state %q once GitHub answered, want newer", got)
	}

	fake.set(start, 2*retryAfter+goodFor-time.Second)
	s.tick(100)
	if got := calls.Load(); got != 3 {
		t.Errorf("%d requests in the day after the recovery, want 3", got)
	}
}

// Off, in a container or on a development build there is no schedule at all:
// Watch returns at once, and a tick sent to the loop asks nothing.
func TestTheScheduleIsNotKeptWhereTheCheckCannotAsk(t *testing.T) {
	cases := map[string]func(*Options){
		"off":         func(o *Options) { o.Enabled = false },
		"container":   func(o *Options) { o.InContainer = true },
		"development": func(o *Options) { o.Development = true },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			o := options(t)
			o.Client = &http.Client{Transport: unreachable{&calls}}
			o.Endpoint = "http://127.0.0.1:1/never"
			change(&o)
			checker := New(o)

			returned := make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				checker.Watch(ctx, time.Hour)
				close(returned)
			}()
			select {
			case <-returned:
			case <-time.After(5 * time.Second):
				t.Fatal("Watch kept a schedule for a check that can never ask")
			}

			watching(t, checker).tick(10)
			if got := calls.Load(); got != 0 {
				t.Errorf("ten ticks made %d requests, want none", got)
			}
		})
	}
}

// unreachable is a transport with no network behind it.
type unreachable struct{ calls *atomic.Int32 }

func (u unreachable) RoundTrip(*http.Request) (*http.Response, error) {
	u.calls.Add(1)
	return nil, errors.New("dial tcp: no route to host")
}

// "Check now" ignores a fresh cached answer, and a failure is reported as a
// failure: never as "latest", never carrying the release the cache held.
func TestCheckNowReportsAFailureAsAFailure(t *testing.T) {
	cases := map[string]func(t *testing.T, o *Options) *atomic.Int32{
		"refused": func(t *testing.T, o *Options) *atomic.Int32 {
			var fail atomic.Int32
			fail.Store(http.StatusServiceUnavailable)
			server, calls := github(t, "v0.2.1", &fail)
			o.Endpoint = server.URL
			return calls
		},
		"unreachable": func(t *testing.T, o *Options) *atomic.Int32 {
			var calls atomic.Int32
			o.Client = &http.Client{Transport: unreachable{&calls}}
			o.Endpoint = "https://api.github.invalid/releases/latest"
			return &calls
		},
	}
	for name, failing := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newClock()
			dir := t.TempDir()

			good, _ := github(t, "v0.2.1", nil)
			seeded := New(Options{Version: "0.2.1", Enabled: true, Dir: dir, Endpoint: good.URL, Now: fake.now})
			seeded.Check(context.Background())
			if got := seeded.Report().State; got != Current {
				t.Fatalf("seeding answered %q, want current", got)
			}

			fake.set(fake.now(), time.Minute)
			o := Options{Version: "0.2.1", Enabled: true, Dir: dir, Now: fake.now}
			calls := failing(t, &o)
			checker := New(o)
			checker.Check(context.Background())
			if got := checker.Report().State; got != Current || calls.Load() != 0 {
				t.Fatalf("an ordinary check inside the window answered %q with %d requests, "+
					"want the cached current and none", got, calls.Load())
			}

			report := checker.CheckNow(context.Background())
			if got := calls.Load(); got != 1 {
				t.Fatalf("Check now made %d requests, want 1: it asks past a fresh answer", got)
			}
			if report.State != Failed {
				t.Errorf("Check now that could not find out answered %q, want failed", report.State)
			}
			if report.Latest != "" || report.ReleaseURL != "" {
				t.Errorf("a failure carries the cached release %q %q; it established nothing",
					report.Latest, report.ReleaseURL)
			}
			if report.Note == "" || strings.Contains(strings.ToLower(report.Note), "latest release") {
				t.Errorf("a failure says %q; it must say it could not check, never \"latest\"",
					report.Note)
			}

			next := New(Options{
				Version: "0.2.1", Enabled: true, Dir: dir, Now: fake.now, Client: refusing(t),
				Endpoint: "http://127.0.0.1:1/never",
			})
			next.Check(context.Background())
			if got := next.Report().State; got != Failed {
				t.Errorf("the next start reads %q, want failed: the failure replaced the cache", got)
			}
		})
	}
}

// The tick is shorter than the failure window. An attempt is dated a moment
// after the tick that made it, so with a tick equal to the window the next tick
// found a failure just under an hour old and the retry landed at two hours.
func TestAFailureIsRetriedWithinATickOfItsWindow(t *testing.T) {
	if RecheckEvery >= retryAfter {
		t.Fatalf("the tick (%s) is not shorter than the failure window (%s)", RecheckEvery, retryAfter)
	}
	var fail atomic.Int32
	fail.Store(http.StatusServiceUnavailable)
	server, calls := github(t, "v0.3.0", &fail)
	fake := newClock()
	start := fake.now()
	checker := New(Options{
		Version: "0.2.1", Enabled: true, Dir: t.TempDir(), Endpoint: server.URL, Now: fake.now,
	})
	s := watching(t, checker)

	// The first tick fails, and its attempt is dated a second after the tick.
	fake.set(start, time.Second)
	s.tick(1)
	if got := calls.Load(); got != 1 {
		t.Fatalf("%d requests after the first tick, want 1", got)
	}

	// Ticks every RecheckEvery from there; the retry must come by the first
	// tick at or past the window, not one window later.
	var retriedAt time.Duration
	for at := RecheckEvery; at <= 2*retryAfter; at += RecheckEvery {
		fake.set(start, at)
		s.tick(1)
		if calls.Load() == 2 {
			retriedAt = at
			break
		}
	}
	if retriedAt == 0 {
		t.Fatalf("no retry within two hours of a failure")
	}
	if retriedAt < retryAfter || retriedAt > retryAfter+RecheckEvery {
		t.Errorf("a failure was retried %s after it, want between %s and %s",
			retriedAt, retryAfter, retryAfter+RecheckEvery)
	}
}

// A loop whose `ticked` nobody reads any more still stops when its context
// ends, so a test that gave up at its deadline does not hang in cleanup.
func TestTheWatchLoopStopsWhileItsTickIsUnread(t *testing.T) {
	server, _ := github(t, "v0.3.0", nil)
	checker := New(Options{Version: "0.2.1", Enabled: true, Dir: t.TempDir(), Endpoint: server.URL})
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		checker.watch(ctx, ticks, make(chan struct{}))
		close(stopped)
	}()
	select {
	case ticks <- time.Time{}:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch loop did not take a tick")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch loop stayed parked on an unread tick after its context ended")
	}
}
