//go:build windows

package launch

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/thonicdev/kubetower/internal/update"
	"github.com/thonicdev/kubetower/safego"
)

// The Windows half of the window's notifications: what nativewindowtoast.go
// decided, said through COM and WinRT in pure Go through syscall, with no cgo
// (nativewindow_windows.go says why).
//
//   - prepareToasts, on the main thread before the window exists: the
//     Start-menu shortcut gets System.AppUserModel.ID through IPropertyStore on
//     its IShellLinkW, and the process says the same ID, which Windows wants
//     before the first window is created.
//   - runToasts, on its own thread in WinRT's multithreaded apartment: a
//     notifier for the ID, and a ToastNotification per notice from toastXML.
//   - A click is the toast's Activated event: its Arguments (the kubetower://
//     link) go through noticeClick, and the window opens the path through the
//     native→web door (openPageScript).
//
// The vtables name only the slots called, with `_` padding between them; a test
// holds each offset to the SDK headers' declaration order.

var (
	ole32   = windows.NewLazySystemDLL("ole32.dll")
	shell32 = windows.NewLazySystemDLL("shell32.dll")
	combase = windows.NewLazySystemDLL("combase.dll")

	procCoCreateInstance                        = ole32.NewProc("CoCreateInstance")
	procPropVariantClear                        = ole32.NewProc("PropVariantClear")
	procSetCurrentProcessExplicitAppUserModelID = shell32.NewProc("SetCurrentProcessExplicitAppUserModelID")
	procRoInitialize                            = combase.NewProc("RoInitialize")
	procRoUninitialize                          = combase.NewProc("RoUninitialize")
	procRoGetActivationFactory                  = combase.NewProc("RoGetActivationFactory")
	procRoActivateInstance                      = combase.NewProc("RoActivateInstance")
	procWindowsCreateString                     = combase.NewProc("WindowsCreateString")
	procWindowsDeleteString                     = combase.NewProc("WindowsDeleteString")
	procWindowsGetStringRawBuffer               = combase.NewProc("WindowsGetStringRawBuffer")
	procGetForegroundWindow                     = user32.NewProc("GetForegroundWindow")
	procGetAncestor                             = user32.NewProc("GetAncestor")
	procIsIconic                                = user32.NewProc("IsIconic")
	procSetForegroundWindow                     = user32.NewProc("SetForegroundWindow")
)

// The GUIDs, from the Windows SDK headers (ShObjIdl_core.h, ObjIdl.h,
// PropKey.h, windows.ui.notifications.h, windows.data.xml.dom.h).
var (
	clsidShellLink    = windows.GUID{Data1: 0x00021401, Data2: 0, Data3: 0, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIShellLinkW    = windows.GUID{Data1: 0x000214F9, Data2: 0, Data3: 0, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIPersistFile   = windows.GUID{Data1: 0x0000010B, Data2: 0, Data3: 0, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIPropertyStore = windows.GUID{Data1: 0x886D8EEB, Data2: 0x8CF2, Data3: 0x4446,
		Data4: [8]byte{0x8D, 0x02, 0xCD, 0xBA, 0x1D, 0xBD, 0xCF, 0x99}}
	// PKEY_AppUserModel_ID: {9F4C2855-9F79-4B39-A8D0-E1D42DE1D5F3}, 5.
	pkeyAppUserModelID = propertyKey{
		fmtid: windows.GUID{Data1: 0x9F4C2855, Data2: 0x9F79, Data3: 0x4B39,
			Data4: [8]byte{0xA8, 0xD0, 0xE1, 0xD4, 0x2D, 0xE1, 0xD5, 0xF3}},
		pid: 5,
	}

	iidToastNotificationManagerStatics = windows.GUID{Data1: 0x50AC103F, Data2: 0xD235, Data3: 0x4598,
		Data4: [8]byte{0xBB, 0xEF, 0x98, 0xFE, 0x4D, 0x1A, 0x3A, 0xD4}}
	iidToastNotificationFactory = windows.GUID{Data1: 0x04124B20, Data2: 0x82C6, Data3: 0x4229,
		Data4: [8]byte{0xB1, 0x09, 0xFD, 0x9E, 0xD4, 0x66, 0x2B, 0x53}}
	iidToastNotification2 = windows.GUID{Data1: 0x9DFB9FD1, Data2: 0x143A, Data3: 0x490E,
		Data4: [8]byte{0x90, 0xBF, 0xB9, 0xFB, 0xA1, 0xF2, 0xE8, 0xA2}}
	iidToastActivatedEventArgs = windows.GUID{Data1: 0xE3BF92F3, Data2: 0xC197, Data3: 0x436F,
		Data4: [8]byte{0x82, 0x65, 0x06, 0x25, 0x82, 0x4F, 0x8D, 0xAC}}
	iidXmlDocument = windows.GUID{Data1: 0xF7F3A506, Data2: 0x1E87, Data3: 0x42D6,
		Data4: [8]byte{0xBC, 0xFB, 0xB8, 0xC8, 0x09, 0xFA, 0x54, 0x94}}
	iidXmlDocumentIO = windows.GUID{Data1: 0x6CD0E74E, Data2: 0xEE65, Data3: 0x4489,
		Data4: [8]byte{0x9E, 0xBF, 0xCA, 0x43, 0xE8, 0x7B, 0xA6, 0x37}}
	// TypedEventHandler<ToastNotification*, IInspectable*>, the type of
	// `ToastNotification.Activated`'s handler.
	iidToastActivatedHandler = windows.GUID{Data1: 0xAB54DE2D, Data2: 0x97D9, Data3: 0x5528,
		Data4: [8]byte{0xB6, 0xAD, 0x10, 0x5A, 0xFE, 0x15, 0x65, 0x30}}
	// IAgileObject: the handler may be called from any thread, which it can.
	iidIAgileObject = windows.GUID{Data1: 0x94EA2B94, Data2: 0xE9CC, Data3: 0x49E0,
		Data4: [8]byte{0xC0, 0xFF, 0xEE, 0x64, 0xCA, 0x8F, 0x5B, 0x90}}
)

const (
	clsctxInprocServer  = 0x1
	stgmReadWrite       = 0x2
	vtLPWSTR            = 31
	maxPath             = 260
	roInitMultithreaded = 1
	gaRoot              = 2
	swRestore           = 9
)

type propertyKey struct {
	fmtid windows.GUID
	pid   uint32
}

// propVariant is PROPVARIANT as far as a string goes: the type, three reserved
// words, and the value's union, 16 bytes on a 64-bit Windows.
type propVariant struct {
	vt                uint16
	reserved1, r2, r3 uint16
	val               *uint16
	_                 uintptr
}

// comCall calls slot fn of a COM object with this first, and returns the
// HRESULT.
func comCall(fn uintptr, this unsafe.Pointer, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(this)}, args...)...)
	return r
}

// unknownVtbl is IUnknown's three slots, first in every vtable below.
type unknownVtbl struct{ queryInterface, addRef, release uintptr }

type comObject struct{ vtbl *unknownVtbl }

func release(p unsafe.Pointer) {
	if p != nil {
		comCall((*comObject)(p).vtbl.release, p)
	}
}

func queryInterface(p unsafe.Pointer, iid *windows.GUID) (unsafe.Pointer, error) {
	var out unsafe.Pointer
	if r := comCall((*comObject)(p).vtbl.queryInterface, p, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out))); failed(r) || out == nil {
		return nil, errors.New(hresult(r))
	}
	return out, nil
}

// --- the shortcut ---------------------------------------------------------------

type shellLinkVtbl struct {
	unknownVtbl
	getPath uintptr // IShellLinkW::GetPath, the first after IUnknown
}

type persistFileVtbl struct {
	unknownVtbl
	_    [2]uintptr // IPersist::GetClassID, IPersistFile::IsDirty
	load uintptr
	save uintptr
}

type propertyStoreVtbl struct {
	unknownVtbl
	_        [2]uintptr // GetCount, GetAt
	getValue uintptr
	setValue uintptr
	commit   uintptr
}

// shortcutRead is what one shortcut says about itself.
type shortcutRead struct {
	found  bool
	target string
	aumid  string
}

// stampShortcut opens the shortcut at path, decides with `shortcutVerdict`,
// and writes `appUserModelID` into it when it should. It reports whether the
// shortcut is this executable's (and so carries the ID now) and the line to
// log when it is not. COM must be initialized on the calling thread.
func stampShortcut(path, exe string) (ours bool, line string, err error) {
	if _, statErr := os.Stat(path); statErr != nil {
		_, _, line = shortcutVerdict(false, "", "", exe)
		return false, line, nil
	}
	var link unsafe.Pointer
	if r, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidShellLink)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIShellLinkW)), uintptr(unsafe.Pointer(&link))); failed(r) || link == nil {
		return false, "", fmt.Errorf("CoCreateInstance(ShellLink): %s", hresult(r))
	}
	defer release(link)
	file, err := queryInterface(link, &iidIPersistFile)
	if err != nil {
		return false, "", fmt.Errorf("IPersistFile: %w", err)
	}
	defer release(file)
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, "", err
	}
	persist := (*struct{ vtbl *persistFileVtbl })(file)
	if r := comCall(persist.vtbl.load, file, uintptr(unsafe.Pointer(name)), stgmReadWrite); failed(r) {
		return false, "", fmt.Errorf("loading %s: %s", path, hresult(r))
	}

	read := shortcutRead{found: true}
	buf := make([]uint16, maxPath+1)
	shell := (*struct{ vtbl *shellLinkVtbl })(link)
	if r := comCall(shell.vtbl.getPath, link, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0); failed(r) {
		return false, "", fmt.Errorf("reading the shortcut's target: %s", hresult(r))
	}
	read.target = windows.UTF16ToString(buf)

	storeP, err := queryInterface(link, &iidIPropertyStore)
	if err != nil {
		return false, "", fmt.Errorf("IPropertyStore: %w", err)
	}
	defer release(storeP)
	store := (*struct{ vtbl *propertyStoreVtbl })(storeP)
	var value propVariant
	if r := comCall(store.vtbl.getValue, storeP, uintptr(unsafe.Pointer(&pkeyAppUserModelID)), uintptr(unsafe.Pointer(&value))); failed(r) {
		return false, "", fmt.Errorf("reading System.AppUserModel.ID: %s", hresult(r))
	}
	if value.vt == vtLPWSTR && value.val != nil {
		read.aumid = windows.UTF16PtrToString(value.val)
	}
	procPropVariantClear.Call(uintptr(unsafe.Pointer(&value)))

	stamp, ours, line := shortcutVerdict(read.found, read.target, read.aumid, exe)
	if !stamp {
		return ours, line, nil
	}
	id, err := windows.UTF16PtrFromString(appUserModelID)
	if err != nil {
		return false, "", err
	}
	// The string stays Go's: SetValue copies it, and this PROPVARIANT is never
	// cleared, so PropVariantClear never frees memory COM did not allocate.
	set := propVariant{vt: vtLPWSTR, val: id}
	if r := comCall(store.vtbl.setValue, storeP, uintptr(unsafe.Pointer(&pkeyAppUserModelID)), uintptr(unsafe.Pointer(&set))); failed(r) {
		return false, "", fmt.Errorf("setting System.AppUserModel.ID: %s", hresult(r))
	}
	runtime.KeepAlive(id)
	if r := comCall(store.vtbl.commit, storeP); failed(r) {
		return false, "", fmt.Errorf("committing System.AppUserModel.ID: %s", hresult(r))
	}
	// Save(NULL, TRUE): to the file it was loaded from.
	if r := comCall(persist.vtbl.save, file, 0, 1); failed(r) {
		return false, "", fmt.Errorf("saving %s: %s", path, hresult(r))
	}
	log.Printf("[kubetower] the shortcut %s now carries the AppUserModelID %s, which Windows attributes "+
		"KubeTower's notifications to", path, appUserModelID)
	return true, "", nil
}

// prepareToasts stamps the shortcuts and sets the process's AppUserModelID,
// and reports whether toasts can be attempted. Main thread, before the window
// is created: Windows reads the process's ID when its first window appears. COM
// is already initialized there (go-webview2's edge package does it in init, in
// a single-threaded apartment).
//
// Never fatal: a failure is one line, and the window opens as before.
func prepareToasts() (ok bool) {
	err := safego.Call("preparing the Windows notifications", func() error {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("this executable's path could not be read: %w", err)
		}
		programs, err := windows.KnownFolderPath(windows.FOLDERID_Programs, 0)
		if err != nil {
			return fmt.Errorf("the Start menu's Programs folder could not be found: %w", err)
		}
		ours, line, err := stampShortcut(filepath.Join(programs, shortcutName), exe)
		if err != nil {
			return fmt.Errorf("the Start-menu shortcut could not be read or written (%w)", err)
		}
		if !ours {
			log.Printf("[kubetower] %s", line)
			return nil
		}
		// The taskbar pin, if any (see nativewindowtoast.go).
		if roaming, err := windows.KnownFolderPath(windows.FOLDERID_RoamingAppData, 0); err == nil {
			pinned := filepath.Join(roaming, `Microsoft\Internet Explorer\Quick Launch\User Pinned\TaskBar`, shortcutName)
			if _, err := os.Stat(pinned); err == nil {
				if _, _, err := stampShortcut(pinned, exe); err != nil {
					log.Printf("[kubetower] the taskbar's KubeTower shortcut could not be given the "+
						"AppUserModelID (%v); the window may show a second taskbar button beside it", err)
				}
			}
		}
		id, err := windows.UTF16PtrFromString(appUserModelID)
		if err != nil {
			return err
		}
		if r, _, _ := procSetCurrentProcessExplicitAppUserModelID.Call(uintptr(unsafe.Pointer(id))); failed(r) {
			return fmt.Errorf("SetCurrentProcessExplicitAppUserModelID: %s", hresult(r))
		}
		ok = true
		return nil
	})
	if err != nil {
		log.Printf("[kubetower] %v, so KubeTower sends no Windows notifications; the console is unaffected", err)
		return false
	}
	return ok
}

// --- WinRT ----------------------------------------------------------------------

// hstring is an HSTRING made from a Go string; free it with deleteHString.
func hstring(s string) (uintptr, error) {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return 0, err
	}
	var h uintptr
	if r, _, _ := procWindowsCreateString.Call(uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&h))); failed(r) {
		return 0, errors.New(hresult(r))
	}
	return h, nil
}

func deleteHString(h uintptr) {
	if h != 0 {
		procWindowsDeleteString.Call(h)
	}
}

func goHString(h uintptr) string {
	if h == 0 {
		return ""
	}
	var n uint32
	p, _, _ := procWindowsGetStringRawBuffer.Call(h, uintptr(unsafe.Pointer(&n)))
	if p == 0 || n == 0 {
		return ""
	}
	// p is WinRT's buffer, valid while h lives; read through its address so no
	// uintptr becomes a pointer by conversion.
	return windows.UTF16ToString(unsafe.Slice(*(**uint16)(unsafe.Pointer(&p)), n))
}

func activationFactory(class string, iid *windows.GUID) (unsafe.Pointer, error) {
	h, err := hstring(class)
	if err != nil {
		return nil, err
	}
	defer deleteHString(h)
	var out unsafe.Pointer
	if r, _, _ := procRoGetActivationFactory.Call(h, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out))); failed(r) || out == nil {
		return nil, fmt.Errorf("RoGetActivationFactory(%s): %s", class, hresult(r))
	}
	return out, nil
}

// inspectableVtbl is IInspectable's six slots, first in every WinRT vtable.
type inspectableVtbl struct {
	unknownVtbl
	_ [3]uintptr // GetIids, GetRuntimeClassName, GetTrustLevel
}

type toastManagerStaticsVtbl struct {
	inspectableVtbl
	_                         uintptr // CreateToastNotifier
	createToastNotifierWithID uintptr
}

type toastNotifierVtbl struct {
	inspectableVtbl
	show       uintptr
	_          uintptr // Hide
	getSetting uintptr
}

type toastFactoryVtbl struct {
	inspectableVtbl
	createToastNotification uintptr
}

type toastNotificationVtbl struct {
	inspectableVtbl
	_            [5]uintptr // get_Content, put_ExpirationTime, get_ExpirationTime, add_Dismissed, remove_Dismissed
	addActivated uintptr
}

type toastNotification2Vtbl struct {
	inspectableVtbl
	putTag   uintptr
	_        uintptr // get_Tag
	putGroup uintptr
}

type xmlDocumentIOVtbl struct {
	inspectableVtbl
	loadXML uintptr
}

type toastActivatedArgsVtbl struct {
	inspectableVtbl
	getArguments uintptr
}

// toaster is the WinRT side, owned by the toast thread.
type toaster struct {
	notifier unsafe.Pointer
	factory  unsafe.Pointer
	shown    []unsafe.Pointer // the last `toastKeep` toasts, oldest first
	said     map[string]bool  // the setting lines already logged
}

func newToaster() (*toaster, error) {
	statics, err := activationFactory("Windows.UI.Notifications.ToastNotificationManager", &iidToastNotificationManagerStatics)
	if err != nil {
		return nil, err
	}
	defer release(statics)
	id, err := hstring(appUserModelID)
	if err != nil {
		return nil, err
	}
	defer deleteHString(id)
	t := &toaster{said: map[string]bool{}}
	vt := (*struct{ vtbl *toastManagerStaticsVtbl })(statics).vtbl
	if r := comCall(vt.createToastNotifierWithID, statics, id, uintptr(unsafe.Pointer(&t.notifier))); failed(r) || t.notifier == nil {
		return nil, fmt.Errorf("CreateToastNotifier(%s): %s", appUserModelID, hresult(r))
	}
	if t.factory, err = activationFactory("Windows.UI.Notifications.ToastNotification", &iidToastNotificationFactory); err != nil {
		release(t.notifier)
		return nil, err
	}
	return t, nil
}

func (t *toaster) close() {
	for _, p := range t.shown {
		release(p)
	}
	t.shown = nil
	release(t.factory)
	release(t.notifier)
}

// show posts one notice, after asking Windows whether it would be displayed.
func (t *toaster) show(note notice) {
	var setting uint32
	nv := (*struct{ vtbl *toastNotifierVtbl })(t.notifier).vtbl
	if r := comCall(nv.getSetting, t.notifier, uintptr(unsafe.Pointer(&setting))); failed(r) {
		log.Printf("[kubetower] Windows did not say whether it shows KubeTower's notifications (%s); "+
			"posting anyway: %s", hresult(r), note.Title)
	} else if ok, line := toastSetting(setting); !ok {
		if !t.said[line] {
			t.said[line] = true
			log.Printf("[kubetower] %s", line)
		}
		log.Printf("[kubetower] not notified: %s", note.Title)
		return
	}
	toast, err := t.make(note)
	if err != nil {
		log.Printf("[kubetower] a notification could not be made (%v): %s", err, note.Title)
		return
	}
	if r := comCall(nv.show, t.notifier, uintptr(toast)); failed(r) {
		release(toast)
		log.Printf("[kubetower] Windows refused a notification (%s): %s", hresult(r), note.Title)
		return
	}
	t.shown = append(t.shown, toast)
	if len(t.shown) > toastKeep {
		release(t.shown[0])
		t.shown = t.shown[1:]
	}
	log.Printf("[kubetower] notification posted: %s", note.Title)
}

// make builds the ToastNotification: the XML loaded into an XmlDocument, the
// tag and group, and the click handler.
func (t *toaster) make(note notice) (unsafe.Pointer, error) {
	class, err := hstring("Windows.Data.Xml.Dom.XmlDocument")
	if err != nil {
		return nil, err
	}
	defer deleteHString(class)
	var doc unsafe.Pointer
	if r, _, _ := procRoActivateInstance.Call(class, uintptr(unsafe.Pointer(&doc))); failed(r) || doc == nil {
		return nil, fmt.Errorf("XmlDocument: %s", hresult(r))
	}
	defer release(doc)
	io, err := queryInterface(doc, &iidXmlDocumentIO)
	if err != nil {
		return nil, fmt.Errorf("IXmlDocumentIO: %w", err)
	}
	defer release(io)
	text, err := hstring(toastXML(note))
	if err != nil {
		return nil, err
	}
	defer deleteHString(text)
	if r := comCall((*struct{ vtbl *xmlDocumentIOVtbl })(io).vtbl.loadXML, io, text); failed(r) {
		return nil, fmt.Errorf("LoadXml: %s", hresult(r))
	}
	xmlDoc, err := queryInterface(doc, &iidXmlDocument)
	if err != nil {
		return nil, fmt.Errorf("IXmlDocument: %w", err)
	}
	defer release(xmlDoc)

	var toast unsafe.Pointer
	fv := (*struct{ vtbl *toastFactoryVtbl })(t.factory).vtbl
	if r := comCall(fv.createToastNotification, t.factory, uintptr(xmlDoc), uintptr(unsafe.Pointer(&toast))); failed(r) || toast == nil {
		return nil, fmt.Errorf("CreateToastNotification: %s", hresult(r))
	}

	// The tag and group make a later update notice replace the earlier one;
	// lacking them is no reason to drop the notice.
	if second, err := queryInterface(toast, &iidToastNotification2); err == nil {
		v := (*struct{ vtbl *toastNotification2Vtbl })(second).vtbl
		if tag, err := hstring(toastTag(note.ID)); err == nil {
			comCall(v.putTag, second, tag)
			deleteHString(tag)
		}
		if group, err := hstring(toastGroup); err == nil {
			comCall(v.putGroup, second, group)
			deleteHString(group)
		}
		release(second)
	}

	toastEventsOnce.Do(makeToastEvents)
	var token int64
	tv := (*struct{ vtbl *toastNotificationVtbl })(toast).vtbl
	if r := comCall(tv.addActivated, toast, uintptr(unsafe.Pointer(toastEvents.activated)), uintptr(unsafe.Pointer(&token))); failed(r) {
		// Shown anyway: an unclickable notification still says what happened.
		log.Printf("[kubetower] a click on this notification will not reach the window (%s): %s", hresult(r), note.Title)
	}
	return toast, nil
}

// --- the click -------------------------------------------------------------------

// toastEvent is the Activated handler: a COM object with IUnknown and one
// Invoke, living for the process, as windowEvent does for WebView2.
type toastEvent struct{ vtbl *toastEventVtbl }

type toastEventVtbl struct{ queryInterface, addRef, release, invoke uintptr }

var (
	toastEventsOnce sync.Once
	toastEvents     struct{ activated *toastEvent }
	// toastOpen is what a click does with the path it validated: set by the
	// window, read on whatever thread WinRT raises the event on.
	toastOpen   func(path string)
	toastOpenMu sync.Mutex
)

func makeToastEvents() {
	toastEvents.activated = &toastEvent{&toastEventVtbl{
		queryInterface: windows.NewCallback(toastEventQueryInterface),
		addRef:         windows.NewCallback(toastEventCount),
		release:        windows.NewCallback(toastEventCount),
		invoke:         windows.NewCallback(onToastActivated),
	}}
}

// toastEventQueryInterface answers IUnknown, IAgileObject and the handler's
// own interface with itself, anything else with E_NOINTERFACE.
func toastEventQueryInterface(this *toastEvent, iid *windows.GUID, object *unsafe.Pointer) uintptr {
	if object == nil {
		return 0x80004003 // E_POINTER
	}
	if iid != nil && (*iid == iidIUnknown || *iid == iidIAgileObject || *iid == iidToastActivatedHandler) {
		*object = unsafe.Pointer(this)
		return 0
	}
	*object = nil
	return 0x80004002 // E_NOINTERFACE
}

func toastEventCount(*toastEvent) uintptr { return 1 }

// onToastActivated is TypedEventHandler<ToastNotification, Object>::Invoke:
// the operator clicked a toast. Its Arguments are validated by noticeClick, and
// the window is asked to open the path.
func onToastActivated(_ *toastEvent, _ uintptr, args unsafe.Pointer) uintptr {
	_ = safego.Call("a click on a Windows notification", func() error {
		if args == nil {
			log.Printf("[kubetower] a notification was clicked and Windows gave no arguments, so nothing opened")
			return nil
		}
		activated, err := queryInterface(args, &iidToastActivatedEventArgs)
		if err != nil {
			log.Printf("[kubetower] a notification was clicked and its arguments could not be read (%v)", err)
			return nil
		}
		defer release(activated)
		var h uintptr
		if r := comCall((*struct{ vtbl *toastActivatedArgsVtbl })(activated).vtbl.getArguments, activated, uintptr(unsafe.Pointer(&h))); failed(r) {
			log.Printf("[kubetower] a notification was clicked and its link could not be read (%s)", hresult(r))
			return nil
		}
		raw := goHString(h)
		deleteHString(h)
		open, line := noticeClick(raw)
		log.Printf("[kubetower] %s", line)
		toastOpenMu.Lock()
		to := toastOpen
		toastOpenMu.Unlock()
		if open != "" && to != nil {
			to(open)
		}
		return nil
	})
	return 0
}

// raiseWindow restores the window if it is minimized and asks Windows to put
// it in front. Windows may refuse (it decides which process may take the
// foreground), and the log says so.
func raiseWindow(hwnd uintptr) {
	if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
		procShowWindow.Call(hwnd, swRestore)
	}
	if ok, _, _ := procSetForegroundWindow.Call(hwnd); ok == 0 {
		log.Printf("[kubetower] Windows did not let the window come to the front (Windows decides " +
			"which process may); the page was opened in it all the same")
	}
}

// windowActive is whether the window, or a window inside it (WebView2's), has
// the keyboard focus, read at the moment of the decision.
func windowActive(hwnd uintptr) bool {
	fg, _, _ := procGetForegroundWindow.Call()
	if fg == 0 {
		return false
	}
	root, _, _ := procGetAncestor.Call(fg, gaRoot)
	return fg == hwnd || root == hwnd
}

// --- the thread -------------------------------------------------------------------

// toastWake is deliverNativeNotices's door to the toast thread.
var toastWake = make(chan struct{}, 1)

// deliverNativeNotices asks the toast thread to look at what waits, from any
// goroutine; never blocks.
func deliverNativeNotices() {
	select {
	case toastWake <- struct{}{}:
	default:
	}
}

// startToasts runs the toast thread until done is closed, for the window hwnd;
// open shows a console path in it. Returns at once.
//
// Only with an update source, which only the first instance has: a second
// launch's window reads nothing of the instance that owns the forwards and the
// check.
func startToasts(done <-chan struct{}, hwnd uintptr, updates func() update.Report, open func(path string)) {
	if updates == nil {
		return
	}
	toastOpenMu.Lock()
	toastOpen = open
	toastOpenMu.Unlock()
	safego.Go("the Windows notifications", func() error { return runToasts(done, hwnd, updates) }, func(err error) {
		notices.enabled.Store(false)
		if err != nil {
			log.Printf("[kubetower] %v, so KubeTower sends no Windows notifications; the console is unaffected", err)
		}
	})
}

// runToasts is the toast thread: WinRT in the multithreaded apartment, on its
// own thread so nothing waits on the window's message loop or the reverse.
func runToasts(done <-chan struct{}, hwnd uintptr, updates func() update.Report) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if r, _, _ := procRoInitialize.Call(roInitMultithreaded); failed(r) {
		return fmt.Errorf("WinRT could not be initialized (%s)", hresult(r))
	}
	defer procRoUninitialize.Call()
	t, err := newToaster()
	if err != nil {
		return err
	}
	defer t.close()
	notices.enabled.Store(true)
	noticeLoop(done, toastWake, windowWatchEvery, func() time.Duration {
		out, lines, again := notices.take(time.Now(), windowActive(hwnd), updates())
		for _, line := range lines {
			log.Printf("[kubetower] %s", line)
		}
		for _, note := range out {
			t.show(note)
		}
		return again
	})
	return nil
}
