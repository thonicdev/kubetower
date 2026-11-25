package launch

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeView records the closes an interrupt asked of it.
type fakeView struct{ closed int }

func (f *fakeView) Destroy() { f.closed++ }

// An installed update stops the console through launch's stop channel; on
// Windows the window must close for the process to exit and the setup to run
// (#96). An interrupt closes the window held, and again when asked again.
func TestAnInterruptClosesTheWindow(t *testing.T) {
	s := &stopWindow{}
	view := &fakeView{}
	release := s.hold(view)
	if view.closed != 0 {
		t.Fatal("holding the window closed it")
	}
	s.interrupt()
	s.interrupt()
	if view.closed != 2 {
		t.Fatalf("two interrupts closed the window %d times, want 2: a second install must be able to stop it", view.closed)
	}
	release()
	s.interrupt()
	if view.closed != 2 {
		t.Fatal("an interrupt after the window went reached it")
	}
}

// An interrupt before the window exists is kept: the window closes as soon as
// it is held, rather than staying up with the console asked to stop.
func TestAnInterruptBeforeTheWindowClosesItOnArrival(t *testing.T) {
	s := &stopWindow{}
	s.interrupt()
	view := &fakeView{}
	defer s.hold(view)()
	if view.closed != 1 {
		t.Fatalf("a window held after an interrupt was closed %d times, want 1", view.closed)
	}
}

// The Windows wiring is not run here: CI has no Windows runner, and the
// window cannot be opened in a test. This reads the source, so emptying
// interruptNativeWindow again, or dropping the hold, fails somewhere at least.
// It is a guard on the text, not a test of the behavior.
func TestTheWindowsWindowIsWiredToTheInterrupt(t *testing.T) {
	raw, err := os.ReadFile("nativewindow_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	for _, want := range []string{
		"func interruptNativeWindow() { windowStop.interrupt() }",
		"defer windowStop.hold(view)()",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("nativewindow_windows.go lacks %q", want)
		}
	}
}

// Every stop asks the window again, until it has closed: a second update's
// stop is not left in the channel behind a first that did not close it.
func TestEveryStopAsksTheWindowAgain(t *testing.T) {
	asks := make(chan struct{}, 4)
	was := interruptWindow
	interruptWindow = func() { asks <- struct{}{} }
	t.Cleanup(func() { interruptWindow = was })

	stop := make(chan os.Signal, 1)
	interrupted, windowDone, watcherDone := watchWindowStop(context.Background(), stop)
	for i := 0; i < 2; i++ {
		stop <- os.Interrupt
		select {
		case <-asks:
		case <-time.After(2 * time.Second):
			t.Fatalf("stop %d did not ask the window to close", i+1)
		}
	}
	select {
	case <-interrupted:
	default:
		t.Error("a stop was not remembered")
	}
	close(windowDone)
	<-watcherDone
}
