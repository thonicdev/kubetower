//go:build windows

package launch

import (
	"syscall"
	"unsafe"
)

// The first-run password, in a box, because a GUI-linked Windows binary has no
// console to print it to; without the box a first run would ask for a password
// nothing displayed.
//
// A modal box rather than a page: the password must be read before the window
// opens, by somebody who is looking. It blocks the startup it interrupts, which
// is the point: nothing is useful until the operator can sign in.
//
// MessageBoxW through user32, no dependency. Ctrl+C in a message box copies its
// whole text, and the text says so.

var (
	user32         = syscall.NewLazyDLL("user32.dll")
	procMessageBox = user32.NewProc("MessageBoxW")
)

const (
	mbOK              = 0x00000000
	mbIconInformation = 0x00000040
	// Foreground and topmost together: during startup the process has no
	// window and no claim on the foreground, and a box behind the browser gets
	// dismissed unread.
	mbSetForeground = 0x00010000
	mbTopMost       = 0x00040000
)

// showFirstRunPassword puts the drawn password in front of the operator and
// reports whether it managed to.
//
// announce refuses to keep a password no channel delivered, so this must report
// truthfully.
func showFirstRunPassword(p drawnPassword) bool {
	// The box reports what happened to the clipboard, not what was intended:
	// an operator told it is there who pastes nothing has lost the password.
	clipboard := "The clipboard could not be reached, so this one has to be typed."
	if p.Copied {
		clipboard = "It is already on your clipboard: paste it straight into the login page,\n" +
			"then copy something else, because it stays there until you do."
	}

	// Likewise for the one-time file: name it only if it was written, and
	// otherwise say this is the only time the password is shown.
	second := "This is the only time it is shown."
	if p.Copy != "" {
		second = "It is also in\n" + p.Copy + "\nwhich KubeTower deletes the first time somebody logs in."
	}

	body := "KubeTower has drawn a password for you.\n\n" +
		"    user:      " + p.User + "\n" +
		"    password:  " + p.Password + "\n\n" +
		clipboard + "\n\n" +
		second + " Only its bcrypt hash is kept, in\n" +
		p.Kept + "\n\n" +
		"Later starts reuse it and this box appears once. To draw another,\n" +
		"run:  kubetower --reset-password"

	title, err := syscall.UTF16PtrFromString("KubeTower — first run")
	if err != nil {
		return false
	}
	text, err := syscall.UTF16PtrFromString(body)
	if err != nil {
		return false
	}

	ret, _, _ := procMessageBox.Call(
		0, // no owner window: there is not one yet
		uintptr(unsafe.Pointer(text)),
		uintptr(unsafe.Pointer(title)),
		uintptr(mbOK|mbIconInformation|mbSetForeground|mbTopMost),
	)
	// Zero only when no window could be created at all: a session with no
	// interactive desktop, such as a service.
	return ret != 0
}

// showMessage is the same box, for anything that is not the first-run password.
//
// A GUI-subsystem binary double-clicked has no stderr anybody reads, so
// `--install` reports through this. It shares the DLL handle and flags. The
// return is whether a window appeared.
func showMessage(title, body string) bool {
	titlePtr, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return false
	}
	textPtr, err := syscall.UTF16PtrFromString(body)
	if err != nil {
		return false
	}
	ret, _, _ := procMessageBox.Call(
		0,
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		uintptr(mbOK|mbIconInformation|mbSetForeground|mbTopMost),
	)
	return ret != 0
}
