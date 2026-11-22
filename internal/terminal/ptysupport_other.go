//go:build !windows

package terminal

// ptySupported is true everywhere creack/pty has an implementation, which is
// every platform this project builds for except Windows. A constant here, since
// the implementation is compiled in; a measured variable on Windows, where it
// depends on an export the system may lack.
const ptySupported = true
