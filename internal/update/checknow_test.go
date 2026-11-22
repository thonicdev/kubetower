package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// counted is a stand-in for api.github.com that counts requests safely across
// goroutines and, when `hold` is non-nil, answers only once it is closed.
func counted(t *testing.T, tag string, hold chan struct{}) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if hold != nil {
			<-hold
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

// "Check now" asks GitHub even inside the window, which is the whole of what
// the button is for: an answer from this morning is
// not the answer to "ask now".
func TestCheckNowAsksPastAFreshAnswer(t *testing.T) {
	server, calls := counted(t, "v0.3.0", nil)
	checker := New(Options{Version: "0.2.1", Enabled: true, Dir: t.TempDir(), Endpoint: server.URL})

	checker.Check(context.Background())
	checker.Check(context.Background())
	if got := calls.Load(); got != 1 {
		t.Fatalf("two ordinary checks inside the window made %d requests, want 1", got)
	}

	report := checker.CheckNow(context.Background())
	if got := calls.Load(); got != 2 {
		t.Errorf("Check now made %d requests in all, want 2: it must ask past the cache", got)
	}
	if report.State != Newer || report.Latest != "0.3.0" {
		t.Errorf("Check now answered %+v, want newer 0.3.0", report)
	}
	if checker.Checking() {
		t.Error("a finished check still says it is in flight")
	}
}

// The button is never the way round the off switch, the container or a
// development build: each is answered before any socket, exactly as Check is.
func TestCheckNowMakesNoRequestWhereTheCheckDoesNot(t *testing.T) {
	cases := map[string]func(*Options){
		"off":         func(o *Options) { o.Enabled = false },
		"container":   func(o *Options) { o.InContainer = true },
		"development": func(o *Options) { o.Development = true },
	}
	want := map[string]State{"off": Off, "container": NotApplicable, "development": NotApplicable}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			o := options(t)
			o.Client = refusing(t)
			o.Endpoint = "http://127.0.0.1:1/never"
			change(&o)
			if got := New(o).CheckNow(context.Background()).State; got != want[name] {
				t.Errorf("state %q, want %q", got, want[name])
			}
		})
	}
}

// While the request is out, Checking says so; the Infos tab draws it as a
// running check rather than the previous answer alone.
func TestCheckingIsTrueOnlyWhileTheRequestIsOut(t *testing.T) {
	hold := make(chan struct{})
	server, _ := counted(t, "v0.3.0", hold)
	checker := New(Options{Version: "0.2.1", Enabled: true, Dir: t.TempDir(), Endpoint: server.URL})
	if checker.Checking() {
		t.Fatal("a checker that has asked nothing says it is checking")
	}

	done := make(chan Report, 1)
	go func() { done <- checker.CheckNow(context.Background()) }()

	deadline := time.Now().Add(5 * time.Second)
	for !checker.Checking() {
		if time.Now().After(deadline) {
			t.Fatal("Checking never became true while the request was held")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(hold)
	if got := (<-done).State; got != Newer {
		t.Errorf("state %q after the held answer, want newer", got)
	}
	if checker.Checking() {
		t.Error("Checking is still true after the answer arrived")
	}
}

// The startup check and a press of the button may overlap. Serialized, the
// second ordinary check reads what the first wrote instead of asking again.
func TestOverlappingChecksMakeOneRequest(t *testing.T) {
	hold := make(chan struct{})
	server, calls := counted(t, "v0.3.0", hold)
	checker := New(Options{Version: "0.2.1", Enabled: true, Dir: t.TempDir(), Endpoint: server.URL})

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			checker.Check(context.Background())
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no request arrived")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(hold)
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Errorf("two overlapping checks made %d requests, want 1", got)
	}
}

// A console left open asks again once its answer has aged past the window, and
// not before however often the tick comes.
func TestWatchAsksAgainOnlyOnceTheAnswerHasAged(t *testing.T) {
	server, calls := counted(t, "v0.3.0", nil)
	var now atomic.Int64
	now.Store(time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC).UnixNano())
	clock := func() time.Time { return time.Unix(0, now.Load()).UTC() }
	checker := New(Options{
		Version: "0.2.1", Enabled: true, Dir: t.TempDir(), Endpoint: server.URL, Now: clock,
	})
	checker.Check(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		checker.Watch(ctx, 2*time.Millisecond)
		close(stopped)
	}()

	time.Sleep(50 * time.Millisecond) // many ticks, all inside the window
	if got := calls.Load(); got != 1 {
		t.Fatalf("%d requests inside the window, want 1: a tick must not bypass the cache", got)
	}

	now.Add(int64(goodFor + time.Minute))
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("no request once the answer was older than the window")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 2 {
		t.Errorf("%d requests after one aged answer, want 2", got)
	}

	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch did not return once its context was done")
	}
}

// A turned-off check stays silent under Watch as well: no request, ever.
func TestWatchOfATurnedOffCheckMakesNoRequest(t *testing.T) {
	o := options(t)
	o.Enabled = false
	o.Client = refusing(t)
	o.Endpoint = "http://127.0.0.1:1/never"
	checker := New(o)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	checker.Watch(ctx, 2*time.Millisecond)
	if got := checker.Report().State; got != Off {
		t.Errorf("state %q, want off", got)
	}
}

// A Check now whose caller went away - the Infos page reloaded mid-check -
// found nothing out. It must leave the last answer where it was, in memory and
// in update.json, rather than record "could not reach GitHub" over a good
// `newer` for the hour a failure is kept.
func TestACancelledCheckNowLeavesTheLastAnswer(t *testing.T) {
	good, _ := counted(t, "v0.3.0", nil)
	dir := t.TempDir()
	checker := New(Options{Version: "0.2.1", Enabled: true, Dir: dir, Endpoint: good.URL})
	checker.Check(context.Background())
	before := checker.Report()
	if before.State != Newer || before.Latest != "0.3.0" {
		t.Fatalf("seeding answered %+v, want newer 0.3.0", before)
	}

	// A GitHub that answers only once the test is over, so the request is out
	// when it is canceled.
	arrived := make(chan struct{}, 1)
	hold := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		select {
		case <-hold:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(hold) })
	checker.opts.Endpoint = slow.URL

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Report, 1)
	go func() { done <- checker.CheckNow(ctx) }()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the request never reached the server")
	}
	cancel()
	var answered Report
	select {
	case answered = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a canceled Check now did not return")
	}

	if got := checker.Report(); got != before {
		t.Errorf("after a canceled Check now the report is %+v, want it unchanged: %+v", got, before)
	}
	if answered != before {
		t.Errorf("a canceled Check now answered %+v, want the last answer %+v", answered, before)
	}
	if checker.Checking() {
		t.Error("a canceled check still says it is in flight")
	}
	next := New(Options{Version: "0.2.1", Enabled: true, Dir: dir, Client: refusing(t),
		Endpoint: "http://127.0.0.1:1/never"})
	next.Check(context.Background())
	if got := next.Report(); got.State != Newer || got.Latest != "0.3.0" {
		t.Errorf("the next start reads %+v from update.json, want the newer 0.3.0 kept", got)
	}
}

// A reader that reads Checking and then Report, as the settings read does,
// never sees the previous answer marked as settled: the answer is stored
// before the flag clears. Read the other way round, a page polling only while
// a check runs could stop on the old answer.
func TestNotCheckingMeansTheReportIsTheSettledOne(t *testing.T) {
	failing, _ := github(t, "v0.3.0", func() *atomic.Int32 {
		var fail atomic.Int32
		fail.Store(http.StatusServiceUnavailable)
		return &fail
	}())
	checker := New(Options{Version: "0.2.1", Enabled: true, Dir: t.TempDir(), Endpoint: failing.URL})
	checker.Check(context.Background())
	if got := checker.Report().State; got != Failed {
		t.Fatalf("seeding answered %q, want failed", got)
	}

	for range 50 {
		hold := make(chan struct{})
		server, _ := counted(t, "v0.3.0", hold)
		checker.opts.Endpoint = server.URL
		checker.set(Report{State: Failed, Note: "before"})
		done := make(chan struct{})
		go func() {
			checker.CheckNow(context.Background())
			close(done)
		}()
		deadline := time.Now().Add(5 * time.Second)
		for !checker.Checking() {
			if time.Now().After(deadline) {
				t.Fatal("Checking never became true while the request was held")
			}
			time.Sleep(time.Millisecond)
		}
		close(hold)
		for {
			checking := checker.Checking()
			report := checker.Report()
			if !checking {
				if report.State != Newer {
					t.Fatalf("not checking, and the report is still %+v", report)
				}
				break
			}
		}
		<-done
	}
}
