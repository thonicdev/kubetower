package updater

import "sync/atomic"

// stopper ends this process the console's own way: internal/launch hands it
// the function that puts an interrupt on its shutdown channel, so shells,
// forwards and the window close as on Ctrl+C or a click on Quit. Held
// atomically: set on launch's goroutine, read on an installer's (#154).
var stopper atomic.Pointer[func()]

// SetStop is how internal/launch gives the installers that way to stop.
func SetStop(stop func()) {
	if stop == nil {
		stopper.Store(nil)
		return
	}
	stopper.Store(&stop)
}

// stopFunc is the way to stop, or nil where none was given.
func stopFunc() func() {
	if p := stopper.Load(); p != nil {
		return *p
	}
	return nil
}
