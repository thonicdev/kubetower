//go:build windows

package launch

import (
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"

	"github.com/thonicdev/kubetower/internal/statefile"
)

// The Windows tool windows (#65): toolwindow.go decides, this draws.
//
// Not go-webview2's window: its window procedure posts WM_QUIT when a window is
// destroyed, which would end the console's own message loop with the first
// tool window closed, and each window it creates spends one of the process's
// windows.NewCallback slots. So the window is this file's own class, made once,
// and the engine is go-webview2's edge.Chromium embedded in it, on the
// console's thread, whose message loop serves both.
//
// **A user data folder of its own** (`<state>/toolwindows-webview2`): WebView2
// keeps cookies, storage and cache per folder, and the console's window uses
// the default one, so nothing the console set is ever sent to the tool's
// origin. The folder is a second WebView2 environment in the same process,
// which WebView2 supports.
//
// The page gets the console window's policy, stricter (toolNavigation): web
// messages off, no other origin, no second window.

// windowsToolHost is toolWindowHost on Windows: each call is dispatched onto
// the console window's thread.
type windowsToolHost struct {
	console webview2.WebView
	tools   *toolWindows
}

// openToolWindow waits, a bounded time, for the window thread's answer: a
// window WebView2 could not be drawn in is a refusal the API says (#84). Past
// toolWindowBuildWait it is still being built, and reports itself.
func (h windowsToolHost) openToolWindow(id int, target toolWindowTarget) error {
	done := make(chan bool, 1)
	h.console.Dispatch(func() { done <- drawToolWindow(h.tools, id, target) })
	select {
	case drawn := <-done:
		if !drawn {
			return errToolWindowRefused
		}
	case <-time.After(toolWindowBuildWait):
	}
	return nil
}

// toolWindowBuildWait bounds how long opening a tool window waits for it.
const toolWindowBuildWait = 20 * time.Second

func (h windowsToolHost) closeToolWindow(id int) {
	h.console.Dispatch(func() {
		for hwnd, w := range toolWindowsOpen {
			if w.id == id {
				procDestroyWindow.Call(hwnd)
				return
			}
		}
	})
}

// toolWindow is one drawn window. Main thread only, as the map.
type toolWindow struct {
	id       int
	origin   string
	chromium *edge.Chromium
	web      *coreWebView2
	tools    *toolWindows
}

// toolWindowsOpen is every drawn tool window by its handle; toolWindowWebs the
// same by its web view, which is what an event handler is handed.
var (
	toolWindowsOpen = map[uintptr]*toolWindow{}
	toolWindowWebs  = map[uintptr]*toolWindow{}
)

var (
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procLoadImageW       = user32.NewProc("LoadImageW")
	procUpdateWindow     = user32.NewProc("UpdateWindow")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procPostThreadMsg    = user32.NewProc("PostThreadMessageW")
)

// wmApp is the thread message go-webview2's Dispatch posts and its Run reads.
const wmApp = 0x8000

const (
	wmDestroy          = 0x0002
	wmMove             = 0x0003
	wmSize             = 0x0005
	wmActivate         = 0x0006
	wmClose            = 0x0010
	wmMoving           = 0x0216
	waInactive         = 0
	wsOverlappedWindow = 0x00CF0000
	cwUseDefault       = 0x80000000
	swShow             = 5
	imageIcon          = 1
	lrDefaultSize      = 0x0040
	lrShared           = 0x8000
)

// toolWindowClass is the window class, registered once.
var (
	toolWindowClassOnce sync.Once
	toolWindowClass     *uint16
	toolWindowClassErr  error
)

// wndClassEx is WNDCLASSEXW.
type wndClassEx struct {
	size       uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   windows.Handle
	icon       windows.Handle
	cursor     windows.Handle
	background windows.Handle
	menuName   *uint16
	className  *uint16
	iconSm     windows.Handle
}

func registerToolWindowClass() {
	var instance windows.Handle
	if err := windows.GetModuleHandleEx(0, nil, &instance); err != nil {
		toolWindowClassErr = fmt.Errorf("the module handle could not be read (%w)", err)
		return
	}
	// IconId 1, the binary's own icon, as the console's window.
	icon, _, _ := procLoadImageW.Call(uintptr(instance), 1, imageIcon, 0, 0, lrDefaultSize|lrShared)
	toolWindowClass = windows.StringToUTF16Ptr("KubeTowerToolWindow")
	class := wndClassEx{
		wndProc:   windows.NewCallback(toolWindowProc),
		instance:  instance,
		icon:      windows.Handle(icon),
		iconSm:    windows.Handle(icon),
		className: toolWindowClass,
	}
	class.size = uint32(unsafe.Sizeof(class))
	if atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class))); atom == 0 {
		toolWindowClassErr = fmt.Errorf("the window class could not be registered (%v)", err)
	}
}

// drawToolWindow draws one window. Main thread only. Whatever fails, the
// window is reported closed, so the bookkeeping and the tunnel follow.
func drawToolWindow(tools *toolWindows, id int, target toolWindowTarget) (drawn bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[kubetower] the tool window for %s failed inside the WebView2 runtime (%v)", loggableURL(target.URL), r)
			drawn = false
		}
		if !drawn {
			// Refused, not closed: nothing was shown, so its tunnel stays.
			tools.refused(id)
		}
	}()
	toolWindowClassOnce.Do(registerToolWindowClass)
	if toolWindowClassErr != nil {
		log.Printf("[kubetower] the tool window for %s was not drawn: %v", loggableURL(target.URL), toolWindowClassErr)
		return
	}
	b := &windowsToolBuilder{tools: tools, id: id, target: target}
	if _, drawn = buildToolWindow(b, toolWindowsBuilding); drawn {
		log.Printf("[kubetower] opened %s in a tool window, %q", loggableURL(target.URL), target.Title)
	}
	return drawn
}

// toolWindowsBuilding is every tool window whose engine is being embedded:
// toolWindowProc ignores a close request for it (buildingWindows).
var toolWindowsBuilding = buildingWindows{}

// windowsToolBuilder is toolWindowBuilder on Windows, for one window.
type windowsToolBuilder struct {
	tools    *toolWindows
	id       int
	target   toolWindowTarget
	chromium *edge.Chromium
	web      *coreWebView2
}

// create makes the window, hidden: it is shown once the engine is in it.
func (b *windowsToolBuilder) create() (uintptr, bool) {
	var instance windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &instance)
	hwnd, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(toolWindowClass)),
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(b.target.Title))), wsOverlappedWindow,
		cwUseDefault, cwUseDefault, toolWindowWidth, toolWindowHeight, 0, 0, uintptr(instance), 0)
	if hwnd == 0 {
		log.Printf("[kubetower] the tool window for %s was not drawn: CreateWindowEx failed (%v)", loggableURL(b.target.URL), err)
		return 0, false
	}
	colourTheFrameOf(hwnd)
	return hwnd, true
}

func (b *windowsToolBuilder) embed(hwnd uintptr) bool {
	b.chromium = edge.NewChromium()
	b.chromium.DataPath = filepath.Join(statefile.Dir(), "toolwindows-webview2")
	// No message callback: web messages are turned off in hold, and the
	// engine drops a message that has no callback.
	if !embedToolWindow(b.chromium, hwnd) {
		why := "WebView2 could not be embedded"
		if err := b.chromium.Error(); err != nil {
			why = "WebView2 could not be embedded: " + err.Error()
		}
		log.Printf("[kubetower] the tool window for %s was not drawn: %s", loggableURL(b.target.URL), why)
		return false
	}
	return true
}

func (b *windowsToolBuilder) hold(uintptr) bool {
	web, err := holdTheToolWindow(b.chromium)
	if err != nil {
		log.Printf("[kubetower] the tool window for %s was not drawn: %v", loggableURL(b.target.URL), err)
		return false
	}
	b.web = web
	return true
}

// show registers the window, so its procedure serves it, then shows it and
// loads the tool.
func (b *windowsToolBuilder) show(hwnd uintptr) {
	w := &toolWindow{id: b.id, origin: b.target.Origin, chromium: b.chromium, web: b.web, tools: b.tools}
	toolWindowsOpen[hwnd] = w
	toolWindowWebs[uintptr(unsafe.Pointer(b.web))] = w
	procShowWindow.Call(hwnd, swShow)
	procUpdateWindow.Call(hwnd)
	b.chromium.Resize()
	b.chromium.Navigate(b.target.URL)
}

func (b *windowsToolBuilder) destroy(hwnd uintptr) {
	if b.chromium != nil {
		closeController(b.chromium)
	}
	procDestroyWindow.Call(hwnd)
}

// embedToolWindow runs go-webview2's Embed and puts back what its own message
// loop took from the console's (afterEmbed): the Dispatch wake-up, always,
// and WM_QUIT when the loop stopped on it. It reports whether the engine is
// usable.
func embedToolWindow(chromium *edge.Chromium, hwnd uintptr) bool {
	returned, panicked := false, false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
				log.Printf("[kubetower] a tool window's engine failed while it was built (%v)", r)
			}
		}()
		returned = chromium.Embed(hwnd)
	}()
	outcome := afterEmbed(returned, panicked, chromium.GetController() != nil)
	if outcome.repostQuit {
		log.Printf("[kubetower] the console's window closed while a tool window was built; KubeTower stops")
		procPostQuitMessage.Call(0)
	}
	if outcome.wakeDispatch {
		procPostThreadMsg.Call(uintptr(windows.GetCurrentThreadId()), wmApp, 0, 0)
	}
	return outcome.usable
}

// holdTheToolWindow gives the tool's web view its policy: settings, web
// messages off and read back, and the two navigation handlers.
func holdTheToolWindow(chromium *edge.Chromium) (*coreWebView2, error) {
	if settings, err := chromium.GetSettings(); err == nil {
		// As the console's window: no inspector, no engine context menu.
		_ = settings.PutAreDevToolsEnabled(false)
		_ = settings.PutAreDefaultContextMenusEnabled(false)
	}
	controller := chromium.GetController()
	if controller == nil {
		return nil, fmt.Errorf("the WebView2 controller does not exist")
	}
	ctl := (*coreWebView2Controller)(unsafe.Pointer(controller))
	var web *coreWebView2
	if r, _, _ := ctl.vtbl.getCoreWebView2.Call(uintptr(unsafe.Pointer(ctl)), uintptr(unsafe.Pointer(&web))); failed(r) || web == nil {
		return nil, fmt.Errorf("the controller gave no web view (%s)", hresult(r))
	}
	if err := turnOffWebMessages(web); err != nil {
		return nil, err
	}
	toolEventsOnce.Do(makeToolEvents)
	var token int64
	if r, _, _ := web.vtbl.addNavigationStarting.Call(uintptr(unsafe.Pointer(web)),
		uintptr(unsafe.Pointer(toolEvents.navigationStarting)), uintptr(unsafe.Pointer(&token))); failed(r) {
		return nil, fmt.Errorf("NavigationStarting could not be handled (%s)", hresult(r))
	}
	if r, _, _ := web.vtbl.addNewWindowRequested.Call(uintptr(unsafe.Pointer(web)),
		uintptr(unsafe.Pointer(toolEvents.newWindowRequested)), uintptr(unsafe.Pointer(&token))); failed(r) {
		return nil, fmt.Errorf("NewWindowRequested could not be handled (%s)", hresult(r))
	}
	return web, nil
}

// closeController is ICoreWebView2Controller::Close: the engine lets go of the
// window and its processes end.
func closeController(chromium *edge.Chromium) {
	controller := chromium.GetController()
	if controller == nil {
		return
	}
	ctl := (*coreWebView2Controller)(unsafe.Pointer(controller))
	ctl.vtbl.close.Call(uintptr(unsafe.Pointer(ctl)))
}

// releaseToolWindow closes the engine and releases the references this
// process holds: the web view this file read (get_CoreWebView2 adds one), and
// the AddRef go-webview2 makes on the web view, the controller and the
// environment once each is created. go-webview2's own out-parameter
// reference on the web view is its, never released here: one too many would
// free an object the engine still uses, one too few only leaks it.
func releaseToolWindow(w *toolWindow) {
	closeController(w.chromium)
	release(unsafe.Pointer(w.web))
	release(unsafe.Pointer(w.web))
	release(unsafe.Pointer(w.chromium.GetController()))
	release(unsafe.Pointer(w.chromium.Environment()))
	w.web = nil
}

// toolWindowProc is the class's window procedure. A message for a window not
// (or no longer) in the map, including those sent while it is being created,
// goes to DefWindowProc, but for a close request while it is built, which is
// ignored (buildingWindows).
func toolWindowProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	if msg == wmClose && !toolWindowsBuilding.closeHonoured(hwnd) {
		return 0
	}
	if w, ok := toolWindowsOpen[hwnd]; ok {
		switch msg {
		case wmSize:
			w.chromium.Resize()
		case wmMove, wmMoving:
			_ = w.chromium.NotifyParentWindowPositionChanged()
		case wmActivate:
			if wParam&0xffff != waInactive {
				w.chromium.Focus()
			}
		case wmDestroy:
			delete(toolWindowsOpen, hwnd)
			delete(toolWindowWebs, uintptr(unsafe.Pointer(w.web)))
			releaseToolWindow(w)
			w.tools.closed(w.id)
			return 0
		}
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
}

// closeAllToolWindows destroys every tool window: the console's window has
// closed. Main thread, after its message loop returned.
func closeAllToolWindows() {
	for hwnd := range toolWindowsOpen {
		procDestroyWindow.Call(hwnd)
	}
}

// toolEvents are the tool windows' two handlers, made once (each takes a
// windows.NewCallback slot); the web view they are handed says which window.
var (
	toolEventsOnce sync.Once
	toolEvents     struct {
		navigationStarting, newWindowRequested *windowEvent
	}
)

func makeToolEvents() {
	queryInterface := edge.NewComProc(windowEventQueryInterface)
	count := edge.NewComProc(windowEventCount)
	toolEvents.navigationStarting = &windowEvent{&windowEventVtbl{
		queryInterface, count, count, edge.NewComProc(onToolNavigationStarting)}}
	toolEvents.newWindowRequested = &windowEvent{&windowEventVtbl{
		queryInterface, count, count, edge.NewComProc(onToolNewWindowRequested)}}
}

// onToolNavigationStarting keeps a tool window on its tunnel. An address that
// cannot be read, or a web view not in the map, is canceled: a tool's page is
// not this console's to trust.
func onToolNavigationStarting(_ *windowEvent, sender uintptr, args *navigationStartingArgs) uintptr {
	target, err := comString(args.vtbl.getURI, unsafe.Pointer(args))
	w, known := toolWindowWebs[sender]
	allow, handOut := false, false
	if err == nil && known {
		allow, handOut = toolNavigation(w.origin, target)
	}
	if allow {
		return 0
	}
	if r, _, _ := args.vtbl.putCancel.Call(uintptr(unsafe.Pointer(args)), 1); failed(r) {
		log.Printf("[kubetower] a tool window's navigation to %s could not be canceled (%s)", loggableURL(target), hresult(r))
	}
	if handOut {
		handOutOfToolWindow(target, comBool(args.vtbl.getIsUserInitiated, unsafe.Pointer(args)))
	}
	return 0
}

// handOutOfToolWindow opens target in the default browser if toolHandOuts
// allows it, and logs why not otherwise.
func handOutOfToolWindow(target string, userInitiated bool) {
	if ok, why := toolHandOuts.allow(target, userInitiated, time.Now()); !ok {
		log.Printf("[kubetower] a tool window's link was not opened in the browser: %s", why)
		return
	}
	openOutside(target)
}

// comBool calls a COM getter of a BOOL, false when it fails.
func comBool(getter edge.ComProc, this unsafe.Pointer) bool {
	var value int32
	if r, _, _ := getter.Call(uintptr(this), uintptr(unsafe.Pointer(&value))); failed(r) {
		return false
	}
	return value != 0
}

// onToolNewWindowRequested: no second window from a tool's page; an http or
// https address goes to the default browser.
func onToolNewWindowRequested(_ *windowEvent, _ uintptr, args *newWindowRequestedArgs) uintptr {
	if r, _, _ := args.vtbl.putHandled.Call(uintptr(unsafe.Pointer(args)), 1); failed(r) {
		log.Printf("[kubetower] a tool window's request for a new window could not be refused (%s)", hresult(r))
	}
	if target, err := comString(args.vtbl.getURI, unsafe.Pointer(args)); err == nil {
		handOutOfToolWindow(target, comBool(args.vtbl.getIsUserInitiated, unsafe.Pointer(args)))
	}
	return 0
}
