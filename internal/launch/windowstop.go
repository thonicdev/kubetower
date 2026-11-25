package launch

import "sync"

// quitter is what an interrupt needs of a native window: a way onto its own
// thread, and a way to end its message loop from there. go-webview2's view is
// one.
type quitter interface {
	Dispatch(func())
	Terminate()
}

// stopWindow is the window an interrupt closes, and whether an interrupt came
// before the window was there. One per process: there is one main window.
type stopWindow struct {
	mu    sync.Mutex
	view  quitter
	asked bool
}

// windowStop is the Windows window's (nativewindow_windows.go). Untagged, so
// the rule below is tested on every platform.
var windowStop = &stopWindow{}

// interrupt closes the window held, from any goroutine, on its own thread
// (Terminate posts WM_QUIT to the calling thread's queue). With no window yet,
// the request is kept for hold.
func (s *stopWindow) interrupt() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = true
	if s.view != nil {
		s.view.Dispatch(s.view.Terminate)
	}
}

// hold makes view the window interrupt closes, and closes it at once when an
// interrupt already came. The function it returns lets the window go.
func (s *stopWindow) hold(view quitter) func() {
	s.mu.Lock()
	s.view = view
	if s.asked {
		view.Dispatch(view.Terminate)
	}
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.view = nil
		s.mu.Unlock()
	}
}
