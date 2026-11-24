package updater

// stopper ends this process the console's own way: internal/launch hands it
// the function that puts an interrupt on its shutdown channel, so shells,
// forwards and the window close as on Ctrl+C or a click on Quit.
var stopper func()

// SetStop is how internal/launch gives the installers that way to stop.
func SetStop(stop func()) { stopper = stop }
