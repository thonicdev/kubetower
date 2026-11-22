package safego

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// watchLog swaps the package's log for one the test reads.
func watchLog(t *testing.T) *[]string {
	t.Helper()
	var mu sync.Mutex
	var lines []string
	old := logf
	logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	t.Cleanup(func() { logf = old })
	return &lines
}

func TestCallTurnsAPanicIntoThatTasksError(t *testing.T) {
	lines := watchLog(t)
	err := Call("reading a thing", func() error { panic("odd object") })
	var p *Panic
	if !errors.As(err, &p) {
		t.Fatalf("Call answered %v, want a *Panic", err)
	}
	if p.What != "reading a thing" || p.Value != "odd object" {
		t.Errorf("the panic was recorded as %+v", p)
	}
	if !strings.Contains(err.Error(), "reading a thing") || !strings.Contains(err.Error(), "odd object") {
		t.Errorf("the error says %q, which names neither the work nor the panic", err)
	}
	if len(*lines) != 1 {
		t.Fatalf("logged %d lines, want exactly one: %q", len(*lines), *lines)
	}
	if !strings.Contains((*lines)[0], "odd object") || !strings.Contains((*lines)[0], "goroutine ") {
		t.Errorf("the log line carries no panic value or no stack: %q", (*lines)[0])
	}
}

func TestCallPassesTheTasksOwnAnswerThrough(t *testing.T) {
	lines := watchLog(t)
	if err := Call("ok", func() error { return nil }); err != nil {
		t.Errorf("a task that returned nil answered %v", err)
	}
	want := errors.New("refused")
	if err := Call("refused", func() error { return want }); err != want {
		t.Errorf("a task's own error came back as %v", err)
	}
	if len(*lines) != 0 {
		t.Errorf("logged %q for tasks that did not panic", *lines)
	}
}

// TestGoKeepsTheProcessAlive: a panicking task on a goroutine of its own yields
// an error for that task, and the process - this test binary - lives to read
// it. Without the recover the binary dies with "panic: odd object" and every
// test in the package fails with it.
func TestGoKeepsTheProcessAlive(t *testing.T) {
	watchLog(t)
	got := make(chan error, 2)
	Go("a panicking task", func() error { panic("odd object") }, func(err error) { got <- err })
	Go("a quiet task", func() error { return nil }, func(err error) { got <- err })
	var panicked, quiet int
	for range 2 {
		err := <-got
		var p *Panic
		switch {
		case errors.As(err, &p):
			panicked++
		case err == nil:
			quiet++
		default:
			t.Errorf("unexpected answer %v", err)
		}
	}
	if panicked != 1 || quiet != 1 {
		t.Errorf("answers: %d panicked and %d quiet, want one of each", panicked, quiet)
	}
}

// TestGoCallsDoneAfterTheErrorIsKnown pins the order the call sites rely on:
// done runs once the panic has been recovered, so a WaitGroup released in it
// never lets a caller read a result before the error that replaces it.
func TestGoCallsDoneAfterTheErrorIsKnown(t *testing.T) {
	watchLog(t)
	var wg sync.WaitGroup
	var result error
	wg.Add(1)
	Go("a panicking read", func() error { panic("odd") }, func(err error) { result = err; wg.Done() })
	wg.Wait()
	if result == nil {
		t.Fatal("wg.Wait returned before the panic's error was set")
	}
}

func TestGoWithoutDone(t *testing.T) {
	said := make(chan string, 1)
	old := logf
	logf = func(format string, args ...any) { said <- fmt.Sprintf(format, args...) }
	t.Cleanup(func() { logf = old })
	Go("fire and forget", func() error { panic("odd") }, nil)
	if line := <-said; !strings.Contains(line, "fire and forget") {
		t.Errorf("the panic was logged as %q", line)
	}
}

// TestAfterFuncKeepsTheProcessAlive: a timer whose function panics is
// recovered and logged once, and the test binary lives to read the line.
func TestAfterFuncKeepsTheProcessAlive(t *testing.T) {
	said := make(chan string, 2)
	old := logf
	logf = func(format string, args ...any) { said <- fmt.Sprintf(format, args...) }
	t.Cleanup(func() { logf = old })
	AfterFunc("an idle timer", time.Millisecond, func() { panic("odd") })
	select {
	case line := <-said:
		if !strings.Contains(line, "an idle timer") || !strings.Contains(line, "odd") {
			t.Errorf("the panic was logged as %q", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the timer's panic was never logged")
	}
}

// TestAfterFuncTimerStops: the returned timer is the real one, so a caller
// that stops it before it fires keeps f from running at all.
func TestAfterFuncTimerStops(t *testing.T) {
	ran := make(chan struct{}, 1)
	timer := AfterFunc("a stopped timer", time.Hour, func() { ran <- struct{}{} })
	if !timer.Stop() {
		t.Fatal("Stop reported the timer already fired or stopped")
	}
	fired := make(chan struct{}, 1)
	AfterFunc("a fired timer", time.Millisecond, func() { fired <- struct{}{} })
	select {
	case <-fired:
	case <-time.After(10 * time.Second):
		t.Fatal("a timer that was not stopped never ran")
	}
	select {
	case <-ran:
		t.Error("a stopped timer ran")
	default:
	}
}
