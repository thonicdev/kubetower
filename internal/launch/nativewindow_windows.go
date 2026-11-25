//go:build windows

package launch

import (
	"errors"
	"fmt"
	"log"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/pkg/edge"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

// A real window, drawn by this process, with no browser in the picture.
//
// The console's interface is HTML the process serves itself, so a native
// application means embedding an HTML engine. Electron or CEF would ship a
// Chromium of its own (some 150 MB beside a one-file binary, plus a second update
// and signing channel); rewriting the UI natively would throw the project away.
// WebView2 is the engine Windows already carries, as an OS component: nothing
// is launched, no browser profile is touched, Chrome need not be installed. It
// is still Chromium's engine rendering the page.
//
// The binding is github.com/jchv/go-webview2, pure Go (COM through syscall, no
// cgo): every build is CGO_ENABLED=0 with no C compiler, so a cgo binding is
// unbuildable. It is imported only under this build tag.

// nativeWindow opens the console in a window this process owns, and blocks until
// that window is closed.
//
// It must run on the goroutine that owns the main thread, which is why Run
// calls it directly and must itself be called from main's own goroutine:
// WebView2 is COM, its message loop is bound to the thread that created the
// window, and Go would otherwise move the goroutine mid-loop.
// runtime.LockOSThread backs that up.
//
// Blocking is the right shape: closing a desktop application's window exits
// the application, unlike the browser fallback, where closing a tab leaves the
// server up.
//
// Of windowSources this window reads only the update check's answer, for the
// notifications; the badge, App Nap, the Cluster menu and ⌘Q are Mac answers.
func nativeWindow(url string, feed windowSources) bool {
	// Asked before anything is constructed, because the guard below cannot
	// catch the failure this avoids (see webView2Usable).
	if note, ok := webView2Usable(webviewloader.GetInstalledVersion()); !ok {
		log.Printf("[kubetower] %s Falling back to a browser window; the console itself is "+
			"unaffected and is still at %s", note, loggableURL(url))
		return false
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// Before the window exists: Windows reads the process's AppUserModelID
	// when its first window appears.
	toasts := prepareToasts()
	return guardWindow(url, func() bool { return drawNativeWindow(url, feed, toasts) })
}

// webView2Usable reads the runtime's own answer about itself and decides whether
// it is worth calling into the library at all.
//
// It exists because guardWindow's recover cannot cover everything: go-webview2
// reports most construction failures with log.Fatal, which is os.Exit(1). No
// deferred function runs, no recover sees it, no fallback happens, and a console
// serving terminals and log streams simply stops.
//
// This closes what the loader can see: no runtime, an unreadable registration,
// a version string that is not one. A runtime that reports a version and then
// fails to create a controller still reaches log.Fatal, which only a window in a
// child process could prevent; the library's message then goes through this
// process's logger, so kubetower.log holds it before the exit.
//
// It takes the loader's two return values so it can be tested: the real loader
// reads the registry of a host with a healthy runtime.
func webView2Usable(version string, err error) (note string, ok bool) {
	switch {
	case err != nil:
		return "the WebView2 runtime could not be read (" + err.Error() + "), so the native " +
			"window is not attempted: this binding reports a construction failure by exiting " +
			"the process, which no recover can catch. Repairing the Microsoft Edge WebView2 " +
			"Runtime from Settings > Apps is the fix.", false
	case strings.TrimSpace(version) == "":
		return "the WebView2 runtime is not installed, so the native window cannot open. To " +
			"get it, install the Microsoft Edge WebView2 Runtime - it is a free component " +
			"of Windows.", false
	}
	return "", true
}

// guardWindow runs draw and reports what it said, or false if it panicked.
//
// A panic in the window must not take the console down (openWindow's
// invariant). go-webview2 dereferences a nil pointer inside
// CreateCoreWebView2ControllerCompleted, a callback the runtime invokes after
// construction returned a usable-looking view: there is no error to check, and
// the panic happens on the main goroutine, where an unrecovered panic is the
// whole process. Auditing a third-party COM binding's every callback is the
// alternative, and being wrong costs a vanished console.
//
// The panic becomes false, which openWindow already handles by falling back to
// a browser window, so such a machine behaves like one with no WebView2.
//
// It covers a panic and nothing else: log.Fatal is not observable (see
// webView2Usable).
//
// A function taking a closure, not a defer inside nativeWindow, because
// nativeWindow cannot be called in a test: it would open a real window and
// block. Split out, the guarantee is testable.
func guardWindow(url string, draw func() bool) (opened bool) {
	defer func() {
		if r := recover(); r != nil {
			opened = false
			log.Printf("[kubetower] the native window failed inside the WebView2 runtime (%v), so "+
				"it could not be drawn. Falling back to a browser window; the console itself is "+
				"unaffected and is still at %s. This is usually a WebView2 installation that is "+
				"present but damaged — repairing the Microsoft Edge WebView2 Runtime from "+
				"Settings ▸ Apps is the fix", r, loggableURL(url))
		}
	}()
	return draw()
}

// drawNativeWindow is the window itself: construct, navigate, and run the
// message loop until the operator closes it.
//
// Called only by guardWindow, because it may panic.
func drawNativeWindow(url string, feed windowSources, toasts bool) bool {
	// The console's color behind the page from the first frame, and on the
	// title bar (windowframe.go).
	paintBeforeThePage()
	view := webview2.NewWithOptions(webview2.WebViewOptions{
		// Debug off: a right-click inspector on a page holding cluster
		// credentials is not a default worth having.
		Debug:     false,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  "KubeTower",
			Width:  1440,
			Height: 900,
			// IconId 1 is the icon compiled into this binary by the resource
			// object (internal/launch/winres/README.md), used for the window,
			// the taskbar button, Alt-Tab and Explorer.
			IconId: 1,
			Center: true,
		},
	})
	if view == nil {
		// The runtime is missing: an LTSC or stripped image may lack it, and
		// the answer must be a sentence and a fallback, not a nil
		// dereference.
		log.Printf("[kubetower] the WebView2 runtime is not installed, so the native window " +
			"cannot open. Falling back to a browser window. To get the native one, install " +
			"the Microsoft Edge WebView2 Runtime — it is a free component of Windows")
		return false
	}
	// Destroy behind its own recover: the guard's recover runs after this
	// deferred call, so a panic out of Run reaches Destroy while unwinding, and
	// tearing down a view whose controller never completed may fault again. A
	// panic in a deferred call during panicking replaces the original, and the
	// log would name the teardown instead of the cause.
	defer func() {
		defer func() { _ = recover() }()
		view.Destroy()
	}()

	// Before the first navigation: WebView2 applies a settings change from the
	// next navigation on, and the first is the one-time sign-in.
	colourTheFrame(view.Window())

	if err := holdTheWindow(view, url); err != nil {
		log.Printf("[kubetower] the native window could not be held to the console (%v), so it is "+
			"not used. Falling back to a browser window; the console itself is unaffected and is "+
			"still at %s", err, loggableURL(url))
		// Run processes the close Destroy posts; without it the window would
		// stay on screen, answering nothing, beside the browser window.
		view.Destroy()
		view.Run()
		return false
	}

	// The notifications, until the window closes: a click restores the window
	// and opens its page through the native→web door, on the window's thread
	// (Dispatch).
	toastsDone := make(chan struct{})
	defer close(toastsDone)
	if toasts {
		hwnd := uintptr(view.Window())
		startToasts(toastsDone, hwnd, feed.Update, func(path string) {
			view.Dispatch(func() {
				raiseWindow(hwnd)
				view.Eval(openPageScript(path))
			})
		})
	}

	// The tool windows (#65) open from here on, on this thread, and close
	// with this window.
	if feed.Tools != nil {
		feed.Tools.attach(windowsToolHost{console: view, tools: feed.Tools})
		defer func() {
			feed.Tools.attach(nil)
			closeAllToolWindows()
		}()
	}

	view.Navigate(url)
	log.Printf("[kubetower] opened %s in a native window. Closing it stops KubeTower", loggableURL(url))
	view.Run()
	return true
}

// holdTheWindow gives the window the macOS window's policy: the page cannot take
// the main frame off the console's origin, a request for a new window goes to
// the default browser, and the page cannot post messages into this process. The
// decisions are nativewindownav.go's.
//
// Web messages are off because there is no web→native channel: go-webview2
// opens one regardless, injecting window.external.invoke and routing every
// chrome.webview.postMessage into its dispatcher, so any page could write to
// kubetower.log. IsWebMessageEnabled false takes postMessage away.
//
// go-webview2 exposes neither the events nor its engine, so:
//
//   - the engine is read from the library's unexported `browser` field
//     (chromiumOf), the one dependency on its insides, failing with an error if
//     it moves;
//   - from there it is WebView2's own COM interfaces, fixed by Microsoft's ABI.
//     The vtables below name only the slots called, and a test checks their
//     offsets against the library's declarations.
//
// The setting is read back, and any failure is returned so drawNativeWindow
// falls back to a browser: a window without its policy is not drawn.
//
// Main thread only.
func holdTheWindow(view webview2.WebView, consoleURL string) error {
	chromium, err := chromiumOf(view)
	if err != nil {
		return err
	}
	controller := chromium.GetController()
	if controller == nil {
		return errors.New("the WebView2 controller does not exist")
	}
	ctl := (*coreWebView2Controller)(unsafe.Pointer(controller))
	var web *coreWebView2
	if r, _, _ := ctl.vtbl.getCoreWebView2.Call(uintptr(unsafe.Pointer(ctl)), uintptr(unsafe.Pointer(&web))); failed(r) || web == nil {
		return fmt.Errorf("the controller gave no web view (%s)", hresult(r))
	}
	// That reference is held for the life of the window, which is the life of
	// the process.

	if err := turnOffWebMessages(web); err != nil {
		return err
	}

	windowNavigation.origin = originOf(consoleURL)
	windowEventsOnce.Do(makeWindowEvents)
	var token int64
	if r, _, _ := web.vtbl.addNavigationStarting.Call(uintptr(unsafe.Pointer(web)),
		uintptr(unsafe.Pointer(windowEvents.navigationStarting)), uintptr(unsafe.Pointer(&token))); failed(r) {
		return fmt.Errorf("NavigationStarting could not be handled (%s)", hresult(r))
	}
	if r, _, _ := web.vtbl.addNewWindowRequested.Call(uintptr(unsafe.Pointer(web)),
		uintptr(unsafe.Pointer(windowEvents.newWindowRequested)), uintptr(unsafe.Pointer(&token))); failed(r) {
		return fmt.Errorf("NewWindowRequested could not be handled (%s)", hresult(r))
	}
	log.Printf("[kubetower] the window keeps to the console's origin and opens other addresses in " +
		"the default browser; web messages are off (IsWebMessageEnabled reads back false)")
	return nil
}

// turnOffWebMessages sets `IsWebMessageEnabled` to false and reads it back.
//
// Through this file's own vtable, not edge's ICoreWebViewSettings, whose getter
// reads the 4-byte BOOL into a 1-byte Go bool: a 3-byte overrun.
func turnOffWebMessages(web *coreWebView2) error {
	var settings *coreWebView2Settings
	if r, _, _ := web.vtbl.getSettings.Call(uintptr(unsafe.Pointer(web)), uintptr(unsafe.Pointer(&settings))); failed(r) || settings == nil {
		return fmt.Errorf("the web view's settings could not be read (%s)", hresult(r))
	}
	defer settings.vtbl.release.Call(uintptr(unsafe.Pointer(settings)))
	put, _, _ := settings.vtbl.putIsWebMessageEnabled.Call(uintptr(unsafe.Pointer(settings)), 0)
	var enabled int32 = 1
	if r, _, _ := settings.vtbl.getIsWebMessageEnabled.Call(uintptr(unsafe.Pointer(settings)), uintptr(unsafe.Pointer(&enabled))); failed(r) {
		return fmt.Errorf("IsWebMessageEnabled could not be read back (%s; setting it said %s)", hresult(r), hresult(put))
	}
	if enabled != 0 {
		return fmt.Errorf("IsWebMessageEnabled reads back true after being set to false (setting it said %s)", hresult(put))
	}
	return nil
}

// chromiumOf is the engine behind a go-webview2 window: the *edge.Chromium in
// its unexported `browser` field. Read through reflect and checked at each
// step, so a library update that renamed the field is an error (and a browser
// window), not a nil dereference in a callback.
func chromiumOf(view webview2.WebView) (*edge.Chromium, error) {
	v := reflect.ValueOf(view)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return nil, fmt.Errorf("go-webview2's window is a %T, not the struct its engine is read from", view)
	}
	field := v.Elem().FieldByName("browser")
	if !field.IsValid() || field.Kind() != reflect.Interface {
		return nil, errors.New("go-webview2's window has no browser field to read its engine from")
	}
	engine := reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Interface()
	chromium, ok := engine.(*edge.Chromium)
	if !ok || chromium == nil {
		return nil, fmt.Errorf("go-webview2's engine is a %T, not an *edge.Chromium", engine)
	}
	return chromium, nil
}

// windowNavigation is what the two event handlers need to know. Main thread
// only: WebView2 raises both events on the thread that created it.
var windowNavigation struct {
	origin string // scheme://host of the console, to tell its links from others
}

// onNavigationStarting is ICoreWebView2NavigationStartingEventHandler::Invoke:
// a main-frame navigation that leaves the console is canceled and handed to
// the default browser.
func onNavigationStarting(_ *windowEvent, _ uintptr, args *navigationStartingArgs) uintptr {
	target, err := comString(args.vtbl.getURI, unsafe.Pointer(args))
	if err != nil {
		log.Printf("[kubetower] a navigation's address could not be read (%v), so it went ahead", err)
		return 0
	}
	if leavesConsole(windowNavigation.origin, target) {
		if r, _, _ := args.vtbl.putCancel.Call(uintptr(unsafe.Pointer(args)), 1); failed(r) {
			log.Printf("[kubetower] a navigation to %s could not be canceled (%s)", loggableURL(target), hresult(r))
		}
		openOutside(target)
	}
	return 0
}

// onNewWindowRequested is ICoreWebView2NewWindowRequestedEventHandler::Invoke:
// no second window, whatever the address. Handled with no NewWindow means
// nothing was opened, as the macOS UI delegate returning nil.
func onNewWindowRequested(_ *windowEvent, _ uintptr, args *newWindowRequestedArgs) uintptr {
	if r, _, _ := args.vtbl.putHandled.Call(uintptr(unsafe.Pointer(args)), 1); failed(r) {
		log.Printf("[kubetower] a request for a new window could not be refused (%s), so WebView2 "+
			"opens one of its own", hresult(r))
	}
	target, err := comString(args.vtbl.getURI, unsafe.Pointer(args))
	if err != nil {
		log.Printf("[kubetower] a request for a new window gave no address (%v), so nothing was opened", err)
		return 0
	}
	openOutside(target)
	return 0
}

// openOutside hands an address to the default browser, if outsideAddress
// allows it. ShellExecute, not `cmd /c start`: the address comes from a page,
// and cmd would read `&` and `|` in it as syntax.
func openOutside(raw string) {
	if ok, refusal := outsideAddress(raw); !ok {
		if refusal != "" {
			log.Printf("[kubetower] %s", refusal)
		}
		return
	}
	target, err := windows.UTF16PtrFromString(raw)
	if err == nil {
		err = windows.ShellExecute(0, windows.StringToUTF16Ptr("open"), target, nil, nil, windows.SW_SHOWNORMAL)
	}
	if err != nil {
		log.Printf("[kubetower] could not open %s in the default browser (%v)", loggableURL(raw), err)
	}
}

// comString calls a COM getter that returns a string WebView2 allocated, and
// frees it.
func comString(getter edge.ComProc, this unsafe.Pointer) (string, error) {
	var text *uint16
	if r, _, _ := getter.Call(uintptr(this), uintptr(unsafe.Pointer(&text))); failed(r) {
		return "", errors.New(hresult(r))
	}
	if text == nil {
		return "", nil
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(text))
	return windows.UTF16PtrToString(text), nil
}

// failed is COM's FAILED: a negative HRESULT.
func failed(r uintptr) bool { return int32(uint32(r)) < 0 }

// hresult is an HRESULT as the log prints it.
func hresult(r uintptr) string { return fmt.Sprintf("HRESULT 0x%08X", uint32(r)) }

// The COM objects this file calls into, as far as the slots it calls. A COM
// interface pointer points at a pointer to its vtable, ordered as declared in
// WebView2.idl, starting with IUnknown's three; `_` fields are the slots in
// between, named in comments.

type coreWebView2Controller struct{ vtbl *coreWebView2ControllerVtbl }

type coreWebView2ControllerVtbl struct {
	queryInterface, addRef, release edge.ComProc
	_                               [21]edge.ComProc // get_IsVisible ... remove_AcceleratorKeyPressed
	close                           edge.ComProc
	getCoreWebView2                 edge.ComProc
}

type coreWebView2 struct{ vtbl *coreWebView2Vtbl }

type coreWebView2Vtbl struct {
	queryInterface, addRef, release edge.ComProc
	getSettings                     edge.ComProc
	_                               [3]edge.ComProc // get_Source, Navigate, NavigateToString
	addNavigationStarting           edge.ComProc
	_                               [36]edge.ComProc // remove_NavigationStarting ... Stop
	addNewWindowRequested           edge.ComProc
}

type coreWebView2Settings struct{ vtbl *coreWebView2SettingsVtbl }

type coreWebView2SettingsVtbl struct {
	queryInterface, addRef, release edge.ComProc
	_                               [2]edge.ComProc // get_IsScriptEnabled, put_IsScriptEnabled
	getIsWebMessageEnabled          edge.ComProc
	putIsWebMessageEnabled          edge.ComProc
}

type navigationStartingArgs struct{ vtbl *navigationStartingArgsVtbl }

type navigationStartingArgsVtbl struct {
	queryInterface, addRef, release edge.ComProc
	getURI                          edge.ComProc
	getIsUserInitiated              edge.ComProc
	_                               [3]edge.ComProc // get_IsRedirected, get_RequestHeaders, get_Cancel
	putCancel                       edge.ComProc
}

type newWindowRequestedArgs struct{ vtbl *newWindowRequestedArgsVtbl }

type newWindowRequestedArgsVtbl struct {
	queryInterface, addRef, release edge.ComProc
	getURI                          edge.ComProc
	_                               [2]edge.ComProc // put_NewWindow, get_NewWindow
	putHandled                      edge.ComProc
	_                               [1]edge.ComProc // get_Handled
	getIsUserInitiated              edge.ComProc
}

// windowEvent is an event handler handed to WebView2: a COM object with IUnknown
// and one Invoke. Each lives for the process, so AddRef and Release count
// nothing and the collector never frees it.
type windowEvent struct{ vtbl *windowEventVtbl }

type windowEventVtbl struct {
	queryInterface, addRef, release, invoke edge.ComProc
}

// windowEvents are the two handlers, made once: each callback takes one of the
// process's limited windows.NewCallback slots, so none are made by a process
// that never draws the window.
var (
	windowEventsOnce sync.Once
	windowEvents     struct {
		navigationStarting, newWindowRequested *windowEvent
	}
)

func makeWindowEvents() {
	queryInterface := edge.NewComProc(windowEventQueryInterface)
	count := edge.NewComProc(windowEventCount) // AddRef and Release alike
	windowEvents.navigationStarting = &windowEvent{&windowEventVtbl{
		queryInterface, count, count, edge.NewComProc(onNavigationStarting)}}
	windowEvents.newWindowRequested = &windowEvent{&windowEventVtbl{
		queryInterface, count, count, edge.NewComProc(onNewWindowRequested)}}
}

// iidIUnknown is IUnknown's interface ID, {00000000-0000-0000-C000-000000000046}.
var iidIUnknown = windows.GUID{Data1: 0, Data2: 0, Data3: 0, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}

// windowEventQueryInterface answers IUnknown with the handler itself and
// anything else with E_NOINTERFACE, COM's contract. Should WebView2 ever ask for
// the handler's own interface, registration fails and holdTheWindow falls back
// to a browser: a refusal, not a crash.
func windowEventQueryInterface(this *windowEvent, iid *windows.GUID, object *unsafe.Pointer) uintptr {
	if object == nil {
		return 0x80004003 // E_POINTER
	}
	if iid != nil && *iid == iidIUnknown {
		*object = unsafe.Pointer(this)
		return 0
	}
	*object = nil
	return 0x80004002 // E_NOINTERFACE
}

// windowEventCount is AddRef and Release: the handlers are never freed, so the
// count is a constant.
func windowEventCount(*windowEvent) uintptr { return 1 }

// interruptNativeWindow does nothing on Windows.
//
// The binary is linked -H windowsgui: there is no console to press Ctrl-C in,
// and Windows sends no SIGTERM, so no signal can arrive to close the window.
func interruptNativeWindow() {}

// refreshNativeWindow has nothing to refresh on Windows; see `nativeWindow`.
func refreshNativeWindow() {}
