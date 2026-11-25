package launch

import "testing"

// fakeView records what an interrupt asked of it, running the dispatched
// function as the window's thread would.
type fakeView struct{ dispatched, terminated int }

func (f *fakeView) Dispatch(fn func()) { f.dispatched++; fn() }
func (f *fakeView) Terminate()         { f.terminated++ }

// An installed update stops the console through launch's stop channel; on
// Windows the window must close for the process to exit and the setup to run
// (#96). An interrupt closes the window held, on its own thread.
func TestAnInterruptClosesTheWindowOnItsThread(t *testing.T) {
	s := &stopWindow{}
	view := &fakeView{}
	release := s.hold(view)
	if view.terminated != 0 {
		t.Fatal("holding the window closed it")
	}
	s.interrupt()
	if view.dispatched != 1 || view.terminated != 1 {
		t.Fatalf("interrupt: dispatched %d, terminated %d; want the loop ended through Dispatch", view.dispatched, view.terminated)
	}
	release()
	s.interrupt()
	if view.terminated != 1 {
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
	if view.terminated != 1 {
		t.Fatalf("a window held after an interrupt was terminated %d times, want 1", view.terminated)
	}
}
