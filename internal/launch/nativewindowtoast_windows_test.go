//go:build windows

package launch

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The toasts, the parts only Windows can run: the vtable slots the
// toast code calls through, a real shortcut stamped and read back, and a real
// toast made (not shown) through WinRT. None of this runs in CI, which has no
// Windows runner; it runs with `go test ./internal/launch/` on a Windows
// machine.

// Each slot against the SDK header's declaration order, IUnknown's three (and
// IInspectable's three more for WinRT) first. A miscounted pad calls the wrong
// method and nothing says so before a toast misbehaves on somebody's machine.
func TestTheToastComSlotsAreWhereTheSDKPutsThem(t *testing.T) {
	ptr := unsafe.Sizeof(uintptr(0))
	for _, tc := range []struct {
		name string
		ours uintptr
		slot uintptr
	}{
		{"IShellLinkW::GetPath", unsafe.Offsetof(shellLinkVtbl{}.getPath), 3},
		{"IPersistFile::Load", unsafe.Offsetof(persistFileVtbl{}.load), 5},
		{"IPersistFile::Save", unsafe.Offsetof(persistFileVtbl{}.save), 6},
		{"IPropertyStore::GetValue", unsafe.Offsetof(propertyStoreVtbl{}.getValue), 5},
		{"IPropertyStore::SetValue", unsafe.Offsetof(propertyStoreVtbl{}.setValue), 6},
		{"IPropertyStore::Commit", unsafe.Offsetof(propertyStoreVtbl{}.commit), 7},
		{"IToastNotificationManagerStatics::CreateToastNotifierWithId", unsafe.Offsetof(toastManagerStaticsVtbl{}.createToastNotifierWithID), 7},
		{"IToastNotifier::Show", unsafe.Offsetof(toastNotifierVtbl{}.show), 6},
		{"IToastNotifier::get_Setting", unsafe.Offsetof(toastNotifierVtbl{}.getSetting), 8},
		{"IToastNotificationFactory::CreateToastNotification", unsafe.Offsetof(toastFactoryVtbl{}.createToastNotification), 6},
		{"IToastNotification::add_Activated", unsafe.Offsetof(toastNotificationVtbl{}.addActivated), 11},
		{"IToastNotification2::put_Tag", unsafe.Offsetof(toastNotification2Vtbl{}.putTag), 6},
		{"IToastNotification2::put_Group", unsafe.Offsetof(toastNotification2Vtbl{}.putGroup), 8},
		{"IXmlDocumentIO::LoadXml", unsafe.Offsetof(xmlDocumentIOVtbl{}.loadXML), 6},
		{"IToastActivatedEventArgs::get_Arguments", unsafe.Offsetof(toastActivatedArgsVtbl{}.getArguments), 6},
		{"the Activated handler's Invoke", unsafe.Offsetof(toastEventVtbl{}.invoke), 3},
	} {
		if tc.ours != tc.slot*ptr {
			t.Errorf("%s is at slot %d here, and the SDK declares it at slot %d", tc.name, tc.ours/ptr, tc.slot)
		}
	}
	if got := unsafe.Sizeof(propVariant{}); got != 24 && ptr == 8 {
		t.Errorf("PROPVARIANT is %d bytes here, 24 on 64-bit Windows", got)
	}
}

// onCOMThread runs f on a thread with COM initialized, as the window's main
// thread is.
func onCOMThread(t *testing.T, f func()) {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err != nil && err != windows.Errno(1) /* S_FALSE */ {
		t.Fatalf("CoInitializeEx: %v", err)
	}
	defer windows.CoUninitialize()
	f()
}

// writeShortcut makes a shortcut to target at path, through IShellLinkW's
// SetPath (slot 20) and IPersistFile::Save.
func writeShortcut(t *testing.T, path, target string) {
	t.Helper()
	var link unsafe.Pointer
	if r, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidShellLink)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIShellLinkW)), uintptr(unsafe.Pointer(&link))); failed(r) {
		t.Fatalf("CoCreateInstance: %s", hresult(r))
	}
	defer release(link)
	type setPathVtbl struct {
		unknownVtbl
		_       [17]uintptr
		setPath uintptr
	}
	to, _ := windows.UTF16PtrFromString(target)
	if r := comCall((*struct{ vtbl *setPathVtbl })(link).vtbl.setPath, link, uintptr(unsafe.Pointer(to))); failed(r) {
		t.Fatalf("SetPath: %s", hresult(r))
	}
	file, err := queryInterface(link, &iidIPersistFile)
	if err != nil {
		t.Fatal(err)
	}
	defer release(file)
	name, _ := windows.UTF16PtrFromString(path)
	if r := comCall((*struct{ vtbl *persistFileVtbl })(file).vtbl.save, file, uintptr(unsafe.Pointer(name)), 1); failed(r) {
		t.Fatalf("Save: %s", hresult(r))
	}
}

// A shortcut to this executable is stamped once, then left alone; a shortcut
// to another executable is never written.
func TestAShortcutIsStampedOnlyWhenItIsOurs(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	onCOMThread(t, func() {
		ours := filepath.Join(dir, "ours.lnk")
		writeShortcut(t, ours, exe)
		logged := captureLog(t)
		ok, line, err := stampShortcut(ours, exe)
		if err != nil || !ok || line != "" {
			t.Fatalf("stamping our shortcut: ok %v, line %q, err %v", ok, line, err)
		}
		if !strings.Contains(logged.String(), "now carries the AppUserModelID") {
			t.Errorf("the first stamp was not logged: %q", logged.String())
		}
		logged.Reset()
		if ok, _, err := stampShortcut(ours, exe); err != nil || !ok {
			t.Fatalf("reading our stamped shortcut back: ok %v, err %v", ok, err)
		}
		if strings.Contains(logged.String(), "now carries") {
			t.Errorf("a shortcut already carrying the ID was written again: %q", logged.String())
		}

		theirs := filepath.Join(dir, "theirs.lnk")
		writeShortcut(t, theirs, `C:\Windows\System32\notepad.exe`)
		before, _ := os.ReadFile(theirs)
		ok, line, err = stampShortcut(theirs, exe)
		if err != nil || ok || line == "" {
			t.Errorf("another executable's shortcut: ok %v, line %q, err %v", ok, line, err)
		}
		if after, _ := os.ReadFile(theirs); !bytes.Equal(before, after) {
			t.Error("another executable's shortcut was written")
		}

		ok, line, err = stampShortcut(filepath.Join(dir, "missing.lnk"), exe)
		if err != nil || ok || line == "" {
			t.Errorf("a missing shortcut: ok %v, line %q, err %v", ok, line, err)
		}
	})
}

// A toast is made through WinRT from what `toastXML` writes, with its tag and
// its click handler. Not shown: a test does not put a notification on the
// screen of the machine that runs it.
func TestAToastIsMadeThroughWinRT(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if r, _, _ := procRoInitialize.Call(roInitMultithreaded); failed(r) {
		t.Fatalf("RoInitialize: %s", hresult(r))
	}
	defer procRoUninitialize.Call()
	factory, err := activationFactory("Windows.UI.Notifications.ToastNotification", &iidToastNotificationFactory)
	if err != nil {
		t.Fatal(err)
	}
	tt := &toaster{factory: factory, said: map[string]bool{}}
	defer release(factory)
	toast, err := tt.make(updateNotice("9.9.9"))
	if err != nil {
		t.Fatalf("making a toast: %v", err)
	}
	release(toast)
	statics, err := activationFactory("Windows.UI.Notifications.ToastNotificationManager", &iidToastNotificationManagerStatics)
	if err != nil {
		t.Fatal(err)
	}
	release(statics)
}
