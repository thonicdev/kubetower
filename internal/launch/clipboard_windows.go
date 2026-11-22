//go:build windows

package launch

import (
	"runtime"
	"syscall"
	"unsafe"
)

// The drawn password, on the clipboard: a Windows console does not select with
// the mouse unless QuickEdit is on, and Ctrl+C in a message box copies the whole
// box. As with a password manager, it stays there until something else is
// copied, and the box says so. Plain syscalls, no dependency.

var (
	procOpenClipboard    = user32.NewProc("OpenClipboard")
	procCloseClipboard   = user32.NewProc("CloseClipboard")
	procEmptyClipboard   = user32.NewProc("EmptyClipboard")
	procSetClipboardData = user32.NewProc("SetClipboardData")

	procGlobalAlloc  = kernel32.NewProc("GlobalAlloc")
	procGlobalLock   = kernel32.NewProc("GlobalLock")
	procGlobalUnlock = kernel32.NewProc("GlobalUnlock")
	procGlobalFree   = kernel32.NewProc("GlobalFree")

	procRtlMoveMemory = kernel32.NewProc("RtlMoveMemory")
)

const (
	cfUnicodeText = 13
	// GMEM_MOVEABLE is required: SetClipboardData takes ownership only of a
	// moveable handle.
	gmemMoveable = 0x0002
)

// toClipboard puts one string on the Windows clipboard and reports whether it
// got there.
//
// Every failure is reported: the caller tells the operator the password is on
// the clipboard, and one who pastes nothing has lost it.
func toClipboard(text string) bool {
	encoded, err := syscall.UTF16FromString(text)
	if err != nil {
		return false
	}

	// The clipboard is system-wide and any process may hold it briefly, so a
	// few attempts are made.
	opened := false
	for attempt := 0; attempt < 8; attempt++ {
		if ret, _, _ := procOpenClipboard.Call(0); ret != 0 {
			opened = true
			break
		}
	}
	if !opened {
		return false
	}
	defer procCloseClipboard.Call()

	if ret, _, _ := procEmptyClipboard.Call(); ret == 0 {
		return false
	}

	size := uintptr(len(encoded) * 2) // UTF-16, including the terminating NUL
	handle, _, _ := procGlobalAlloc.Call(gmemMoveable, size)
	if handle == 0 {
		return false
	}

	target, _, _ := procGlobalLock.Call(handle)
	if target == 0 {
		procGlobalFree.Call(handle)
		return false
	}

	// Copied by RtlMoveMemory, not by writing through a pointer made from
	// `target`: converting a syscall's uintptr to unsafe.Pointer is the misuse
	// go vet reports, since the collector may move an object it cannot see a
	// reference to. Passed as an integer it stays one; the source pointer
	// converts inside the call argument, and KeepAlive covers it, because
	// LazyProc.Call is not the syscall shape the compiler recognizes.
	procRtlMoveMemory.Call(target, uintptr(unsafe.Pointer(&encoded[0])), size)
	runtime.KeepAlive(encoded)

	procGlobalUnlock.Call(handle)

	if ret, _, _ := procSetClipboardData.Call(cfUnicodeText, handle); ret == 0 {
		// Ownership did not pass, so this side frees the block; on success it
		// must not.
		procGlobalFree.Call(handle)
		return false
	}
	return true
}
