package launch

import "sync"

// closer is what an interrupt needs of a native window: a way to close it as
// its close box does. go-webview2's Destroy posts WM_CLOSE to the window, from
// any thread: the window goes at once, the message survives a modal move or
// size loop (a thread message posted through Dispatch can be lost in one), and
// its WM_DESTROY then ends the message loop (#96, #101).
type closer interface {
	Destroy()
}

// stopWindow is the window an interrupt closes, and whether an interrupt came
// before the window was there. One per process: there is one main window.
type stopWindow struct {
	mu    sync.Mutex
	view  closer
	asked bool
}

// windowStop is the Windows window's (nativewindow_windows.go). Untagged, so
// the rule below is tested on every platform.
var windowStop = &stopWindow{}

// interrupt closes the window held, from any goroutine. With no window yet,
// the request is kept for hold. Asked again, it closes again: a close is
// idempotent, and a second install must be able to stop the console.
func (s *stopWindow) interrupt() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = true
	if s.view != nil {
		s.view.Destroy()
	}
}

// hold makes view the window interrupt closes, and closes it at once when an
// interrupt already came. The function it returns lets the window go.
func (s *stopWindow) hold(view closer) func() {
	s.mu.Lock()
	s.view = view
	if s.asked {
		view.Destroy()
	}
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.view = nil
		s.mu.Unlock()
	}
}
