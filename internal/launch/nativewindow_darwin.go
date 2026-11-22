//go:build darwin

package launch

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"structs"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/thonicdev/kubetower/internal/update"
)

// A real window, drawn by this process, with no browser in the picture: the
// macOS half of what nativewindow_windows.go does with WebView2, embedding the
// HTML engine macOS already carries (appwindow.go has the argument).
//
// Pure Go: github.com/ebitengine/purego reaches the Objective-C runtime through
// dlopen and objc_msgSend with no cgo, as go-webview2 reaches COM on Windows, so
// the macOS binaries stay cross-compilable with CGO_ENABLED=0 and no C
// toolchain. A cgo binding would be better typed and better trodden, and would
// cost exactly that, plus a macOS CI runner.
//
// WKWebView is WebKit: no browser is launched or needed, but a browser engine
// renders the page.
//
// App Transport Security is not touched: plain HTTP on 127.0.0.1 loads without
// an exception, as a navigation delegate catching didFailProvisionalNavigation:
// shows (a failure would be a blank window with no message).

// Objective-C selectors and classes, shortened: they are on every other line
// below.
func sel(name string) objc.SEL { return objc.RegisterName(name) }

func class(name string) objc.ID { return objc.ID(objc.GetClass(name)) }

func nsString(s string) objc.ID {
	return class("NSString").Send(sel("stringWithUTF8String:"), s)
}

// Geometry, laid out as the C ABI expects. structs.HostLayout is required:
// without it Go promises no field order or padding, and the failure is a window
// at a nonsense size rather than a compile error.
type nsPoint struct {
	_    structs.HostLayout
	X, Y float64
}

type nsSize struct {
	_             structs.HostLayout
	Width, Height float64
}

type nsRect struct {
	_      structs.HostLayout
	Origin nsPoint
	Size   nsSize
}

func rectOf(w, h float64) nsRect {
	return nsRect{Size: nsSize{Width: w, Height: h}}
}

// The AppKit constants used below, from the framework headers. A wrong number
// is, say, a window silently without a close button.
const (
	activationPolicyRegular = 0

	styleMaskTitled          = 1 << 0
	styleMaskClosable        = 1 << 1
	styleMaskMiniaturizable  = 1 << 2
	styleMaskResizable       = 1 << 3
	backingStoreBuffered     = 2
	modifierFlagControl      = 1 << 18
	modifierFlagOption       = 1 << 19
	modifierFlagCommand      = 1 << 20
	terminateCancel          = 0
	alertStyleWarning        = 0
	windowCollectionFullScrn = 1 << 7
	eventTypeAppDefined      = 15

	// The quit Apple Event: kCoreEventClass 'aevt', kAEQuitApplication 'quit'.
	aeCoreEventClass  = 'a'<<24 | 'e'<<16 | 'v'<<8 | 't'
	aeQuitApplication = 'q'<<24 | 'u'<<16 | 'i'<<8 | 't'

	// The get-URL Apple Event a kubetower:// link arrives as: kInternetEventClass
	// and kAEGetURL are both 'GURL', with the address in keyDirectObject '----'.
	aeInternetEventClass = 'G'<<24 | 'U'<<16 | 'R'<<8 | 'L'
	aeGetURL             = 'G'<<24 | 'U'<<16 | 'R'<<8 | 'L'
	aeKeyDirectObject    = '-'<<24 | '-'<<16 | '-'<<8 | '-'

	// WKNavigationActionPolicy and WKNavigationResponsePolicy share the values.
	policyCancel   = 0
	policyAllow    = 1
	policyDownload = 2
)

// The window's opening size, the same as the Windows window's.
const (
	windowWidth  = 1440
	windowHeight = 900
)

// showSettingsScript and routeScript, which builds every native→web message,
// are in nativewindowfacts.go, untagged so they are tested on Linux.

// nativeWindowState is what the menu items and the delegates act on.
//
// Package-level rather than captured in closures: there is one window per
// process, and objc.NewIMP's function pointers are never deallocated, so state
// reachable from them outlives any scope anyway.
//
// Every field is touched on the main thread only (drawNativeWindow and the
// Objective-C callbacks, both run there by AppKit). What another thread needs,
// closing the window from a signal, is the two atomics below instead.
var nativeWindowState struct {
	app      objc.ID
	webView  objc.ID
	window   objc.ID
	origin   string // scheme://host of the console, to tell its links from others
	stopping bool

	// feed is where the window reads what the rest of the process holds (see
	// windowSources).
	feed windowSources

	// badge is the Dock badge as last set, and activity the App Nap guard's
	// token while held (retained; zero when not): what applyWindowFacts
	// compares against.
	badge    string
	activity objc.ID

	// asking is true while the ⌘Q sheet is up, so a second ⌘Q stacks no second
	// sheet; alert is that sheet's NSAlert, owned, released when the next is
	// made.
	asking bool
	alert  objc.ID

	// clusterScripts is each Cluster menu item's navigation script, a retained
	// NSString indexed by the item's tag, built with the menu so the callback
	// only reads.
	clusterScripts []objc.ID

	// downloads maps a WKDownload to where it is being saved, for the line
	// written when it finishes.
	downloads map[objc.ID]string

	// links is what a kubetower:// link has left to do: held until the first
	// page has loaded, then opened (nativewindowlink.go).
	links schemeLinks

	// delegate is the window and application delegate, which also answers the
	// notification center and the delayed look at the notices; kept so a
	// callback can schedule on it without the atomic below.
	delegate objc.ID

	// notify is the macOS notifications' state (see deliverNotices).
	notify struct {
		// available is true once setUpNotices has found a bundle and the
		// framework; nothing below is touched otherwise.
		available bool
		// center is the shared notification center, asked once and kept by
		// the framework for the process, so not retained here.
		center objc.ID
		// held is what was composed before macOS answered the permission
		// prompt, posted or dropped once it has. Bounded by noticeHeldMax.
		held []notice
		// deniedSaid is whether the refusal has been logged, so it is logged once.
		deniedSaid bool
	}
}

// The one door into the window from another thread: interruptNativeWindow.
//
// interruptTarget is the delegate that answers kubeTowerInterrupt:, stored once
// the event loop is about to run, zero before. interrupted records that an
// interrupt was asked for, so one arriving while the window is built is not lost
// (see drawNativeWindow for the order).
var (
	interruptTarget atomic.Uintptr
	interrupted     atomic.Bool
)

// noticeAuthorization is where macOS's answer to the permission prompt lands.
// Atomic: the answer arrives on the framework's queue, and the main thread reads
// it in settleNotices.
var noticeAuthorization atomic.Int32

const (
	noticeAuthUnasked int32 = iota
	noticeAuthAsking
	noticeAuthGranted
	noticeAuthDenied
)

// UNAuthorizationOptionAlert, `1 << 2` in UNUserNotificationCenter.h. Alert
// only: the Dock badge is already this application's own, and a sound for a
// lost tunnel would be a console making noise on a shared desk.
const unAuthorizationOptionAlert = 1 << 2

// noticeHeldMax bounds what waits for the permission answer: the prompt stays up
// until answered, and a dropping cluster must not grow a list meanwhile.
const noticeHeldMax = 4

// noticeLinkKey is the userInfo key the click route travels under.
const noticeLinkKey = "kubetowerLink"

// registerOnce guards the Objective-C classes this file creates:
// objc.RegisterClass fails on an existing name, which a second window would
// otherwise mistake for a missing WKWebView.
var registerOnce sync.Once

var (
	menuTargetClass  objc.Class
	delegateClass    objc.Class
	webDelegateClass objc.Class
	registerErr      error
)

// nativeWindow opens the console in a window this process owns, and blocks
// until that window is closed.
//
// The contract is openWindow's tier 1: true means the window was drawn and has
// since been closed, and Run takes the server down on it, since closing an
// application's window ends the application.
//
// It must run on the goroutine that owns the main OS thread, which init makes
// true (see init).
//
// Ways out: the red button, Window ▸ Close, ⌘W, ⌘Q, Quit from the Dock, logging
// out, and Ctrl-C or SIGTERM through interruptNativeWindow. Each ends in
// requestStop, so each returns here and Run shuts the server down in its own
// time. ⌘Q asks first while a terminal or port-forward is open
// (askBeforeQuitting).
func nativeWindow(url string, feed windowSources) bool {
	if err := loadFrameworks(); err != nil {
		log.Printf("[kubetower] the system UI frameworks could not be loaded (%v), so the "+
			"native window is not attempted. Falling back to a browser window; the console "+
			"itself is unaffected and is still at %s", err, loggableURL(url))
		return false
	}
	return guardWindow(url, func() bool { return drawNativeWindow(url, feed) })
}

// interruptNativeWindow asks the window to close, from any goroutine.
//
// Run reads SIGINT and SIGTERM on its own goroutine while the main goroutine is
// inside AppKit's event loop, and calls this; without it `kubetower --app` would
// ignore Ctrl-C.
//
// Nothing touches AppKit from the calling thread: kubeTowerInterrupt: is
// scheduled with performSelectorOnMainThread:, documented as safe from any
// thread. Safe before the window exists (drawNativeWindow reads interrupted just
// before the event loop), after it has closed, and more than once (requestStop
// is idempotent).
func interruptNativeWindow() {
	interrupted.Store(true)
	if target := interruptTarget.Load(); target != 0 {
		objc.ID(target).Send(hot.performOnMain, hot.interrupt, objc.ID(0), false)
	}
}

// refreshNativeWindow asks the window to apply the facts watchWindowFacts last
// published, from any goroutine, through the same door as
// interruptNativeWindow. Before the window exists nothing is sent;
// drawNativeWindow applies what was published once it has published the target.
func refreshNativeWindow() {
	if target := interruptTarget.Load(); target != 0 {
		objc.ID(target).Send(hot.performOnMain, hot.refresh, objc.ID(0), false)
	}
}

// deliverNativeNotices asks the window to look at the waiting notifications
// (deliverNotices), from any goroutine (the forward set's, when a forward is
// lost). The same door as refreshNativeWindow.
func deliverNativeNotices() {
	if target := interruptTarget.Load(); target != 0 {
		objc.ID(target).Send(hot.performOnMain, hot.notices, objc.ID(0), false)
	}
}

// init pins the main goroutine to the thread the process started on.
//
// It cannot be done later: AppKit must be driven from the process's first
// thread, not merely a thread that stays put. Locking inside nativeWindow would
// pin whatever thread the goroutine is on after the whole start-up, and the
// first AppKit call would fail an assertion. Every Go binding for Cocoa does
// this in init.
//
// It runs on every darwin build, the server profile included; the cost is that
// the main goroutine does not migrate between OS threads.
func init() {
	runtime.LockOSThread()
}

// loadFrameworks brings Cocoa and WebKit into the process.
//
// Two dlopens: Cocoa is AppKit and Foundation, WebKit is neither. With Cocoa
// alone, GetClass("WKWebView") is zero, a nil receiver that silently does
// nothing: a titled window with no content and no error.
func loadFrameworks() error {
	for _, framework := range []string{
		"/System/Library/Frameworks/Cocoa.framework/Cocoa",
		"/System/Library/Frameworks/WebKit.framework/WebKit",
	} {
		if _, err := purego.Dlopen(framework, purego.RTLD_GLOBAL|purego.RTLD_LAZY); err != nil {
			return err
		}
	}
	return nil
}

// guardWindow runs draw and reports what it said, or false if it panicked.
//
// The same name, signature and guarantee as the Windows one (openWindow's
// invariant), but narrower: this binding has no log.Fatal. It catches a panic on
// the Go side of the binding, purego's own (a signature it cannot encode, a
// block it cannot build) or this file's use of a value AppKit returned.
//
// It does not catch a message to a nil receiver, which returns zero rather than
// panicking (defended by checking for zero where it matters), nor an
// Objective-C exception (an unknown selector, a nil inserted into a
// collection), which unwinds through frames Go cannot see and ends the process
// (defended only by never sending what AppKit would refuse).
//
// A function taking a closure so the guarantee is testable: nativeWindow opens a
// real window and blocks.
func guardWindow(url string, draw func() bool) (opened bool) {
	defer func() {
		if r := recover(); r != nil {
			opened = false
			log.Printf("[kubetower] the native window failed inside AppKit (%v), so it could "+
				"not be drawn. Falling back to a browser window; the console itself is "+
				"unaffected and is still at %s", r, loggableURL(url))
		}
	}()
	return draw()
}

// drawNativeWindow is the window itself: construct, navigate, and run the event
// loop until the operator closes it.
//
// Called only by guardWindow, because it may panic.
func drawNativeWindow(url string, feed windowSources) bool {
	// Before warmHotPath, which resolves the framework's classes: not loaded,
	// they are zero and notifications stay off.
	noticeFramework := loadNoticeFramework()
	registerOnce.Do(registerClasses)
	warmHotPath()
	if registerErr != nil {
		log.Printf("[kubetower] the window's Objective-C classes could not be registered "+
			"(%v), so the native window cannot open. Falling back to a browser window; the "+
			"console is at %s", registerErr, loggableURL(url))
		return false
	}

	state := &nativeWindowState
	state.origin = originOf(url)
	state.downloads = map[objc.ID]string{}
	state.feed = feed

	state.app = class("NSApplication").Send(sel("sharedApplication"))
	// Regular, not Accessory: a Dock icon, a menu bar and a place in ⌘-Tab, the
	// shape of an application rather than a status-bar utility.
	state.app.Send(sel("setActivationPolicy:"), activationPolicyRegular)

	if class("WKWebView") == 0 {
		log.Printf("[kubetower] WKWebView is not available on this system, so the native " +
			"window cannot open. Falling back to a browser window")
		return false
	}

	configuration := class("WKWebViewConfiguration").Send(sel("alloc")).Send(sel("init"))
	state.webView = class("WKWebView").Send(sel("alloc")).
		Send(sel("initWithFrame:configuration:"), rectOf(windowWidth, windowHeight), configuration)
	// Without these two delegates a WKWebView cannot leave itself: every
	// `target="_blank"` link (a port-forward, an Ingress host) is dropped, and a
	// download link saves nothing. See registerClasses for the answers.
	webDelegate := objc.ID(webDelegateClass).Send(sel("alloc")).Send(sel("init"))
	state.webView.Send(sel("setUIDelegate:"), webDelegate)
	state.webView.Send(sel("setNavigationDelegate:"), webDelegate)
	// The trackpad as in Safari: a two-finger swipe goes back and forward, a
	// pinch magnifies. The swipe walks the SPA's pushState history, so it is a
	// popstate the router answers and the dock survives. A pinch is
	// magnification of the page as drawn, not Zoom In (pageZoom, which lays the
	// page out again); View ▸ Actual Size resets both.
	state.webView.Send(sel("setAllowsBackForwardNavigationGestures:"), true)
	state.webView.Send(sel("setAllowsMagnification:"), true)
	enableWebInspector(state.webView)
	// The window's title follows the page's (followPageTitle), through
	// key-value observation, WebKit's documented way. The page only sets
	// document.title; no web→native channel is added, and the string WebKit
	// parsed is only displayed. The observer is the web delegate, which lives
	// for the process, removed once the event loop returns. Options 0: the
	// callback reads the title itself.
	state.webView.Send(sel("addObserver:forKeyPath:options:context:"), webDelegate, nsString("title"), uint(0), uintptr(0))
	defer state.webView.Send(sel("removeObserver:forKeyPath:"), webDelegate, nsString("title"))

	state.window = class("NSWindow").Send(sel("alloc")).
		Send(sel("initWithContentRect:styleMask:backing:defer:"),
			rectOf(windowWidth, windowHeight),
			styleMaskTitled|styleMaskClosable|styleMaskMiniaturizable|styleMaskResizable,
			backingStoreBuffered,
			false)
	state.window.Send(sel("setTitle:"), nsString(windowTitleFallback))
	// AppKit resizes the contentView with the window, so the web view needs no
	// autoresizing mask.
	state.window.Send(sel("setContentView:"), state.webView)
	state.window.Send(sel("setCollectionBehavior:"), windowCollectionFullScrn)
	// Position and size are remembered between launches, as in every macOS
	// application. Center first, then name the frame: naming it restores a
	// saved frame, and centering afterwards would move it back to the middle.
	state.window.Send(sel("center"))
	state.window.Send(sel("setFrameAutosaveName:"), nsString("KubeTowerMainWindow"))
	// Dark, explicitly: the console is dark only (theme.css has `color-scheme:
	// dark` and no light palette), so following the system would put a light
	// title bar above a #0d1117 page. One line, so that it goes the day the
	// console gets a light theme, and the window follows the system again.
	state.window.Send(sel("setAppearance:"),
		class("NSAppearance").Send(sel("appearanceNamed:"), nsString("NSAppearanceNameDarkAqua")))

	delegate := objc.ID(delegateClass).Send(sel("alloc")).Send(sel("init"))
	state.window.Send(sel("setDelegate:"), delegate)
	state.app.Send(sel("setDelegate:"), delegate)
	state.delegate = delegate
	// Before the event loop runs: a click on a notification that starts the
	// application reaches only a delegate set by then. Asks for nothing.
	setUpNotices(noticeFramework)

	buildMenuBar(objc.ID(menuTargetClass).Send(sel("alloc")).Send(sel("init")), readClusterMenu(feed.Clusters))

	// After sleep the page is told (see wakeEvent), through NSWorkspace's own
	// notification center, where NSWorkspaceDidWakeNotification is posted; the
	// default center never sees it. The name is the constant's string value.
	// The delegate lives for the process, so the observer is never removed.
	class("NSWorkspace").Send(sel("sharedWorkspace")).Send(sel("notificationCenter")).
		Send(sel("addObserver:selector:name:object:"), delegate, sel("kubeTowerDidWake:"),
			nsString("NSWorkspaceDidWakeNotification"), objc.ID(0))

	nsurl := class("NSURL").Send(sel("URLWithString:"), nsString(url))
	state.webView.Send(sel("loadRequest:"), class("NSURLRequest").Send(sel("requestWithURL:"), nsurl))
	state.window.Send(sel("makeKeyAndOrderFront:"), objc.ID(0))
	state.app.Send(sel("activateIgnoringOtherApps:"), true)

	// Publish the interrupt target, then read the flag, in that order:
	// interruptNativeWindow writes the flag then reads the target, so however
	// the threads interleave one sees the other's write. In the other order a
	// signal between the two would be lost.
	interruptTarget.Store(uintptr(delegate))
	if interrupted.Load() {
		log.Printf("[kubetower] asked to stop while the window was opening, so it was not shown")
		return true
	}
	// Same order for the facts: from here a refresh is delivered, and whatever
	// was published before is applied now.
	applyWindowFacts()

	log.Printf("[kubetower] opened %s in a native window. Closing it stops KubeTower", loggableURL(url))
	state.app.Send(sel("run"))
	interruptTarget.Store(0)
	notices.enabled.Store(false)
	endActivity()
	return true
}

// requestStop ends the event loop so that drawNativeWindow returns and Run
// shuts the server down in its own time.
//
// stop:, not terminate:, which is why an application delegate exists here:
// terminate: calls exit, running no deferred function and cutting terminals and
// log streams mid-write. stop: sets a flag run reads after the current event,
// returning to Go with the stack intact.
//
// An event is then posted, because there may be no current event: a signal's
// kubeTowerInterrupt: runs from a run-loop source, and run would wait in
// nextEvent until the mouse moved. Posting for every caller keeps one path.
//
// Main thread only: every caller is an Objective-C callback.
func requestStop() {
	if nativeWindowState.stopping {
		return
	}
	nativeWindowState.stopping = true
	app := nativeWindowState.app
	app.Send(hot.stop, objc.ID(0))
	wake := objc.ID(hot.nsEvent).Send(hot.otherEvent,
		uint(eventTypeAppDefined), nsPoint{}, uint(0), float64(0), 0, objc.ID(0), int16(0), 0, 0)
	app.Send(hot.postEvent, wake, true)
}

// registerClasses creates the three Objective-C classes this window needs.
//
// One class is the delegate of both the window and the application: both
// answer "the operator closed the console", and two classes would give two
// paths into requestStop.
//
// Every callback reaches AppKit only through hot (see hot).
func registerClasses() {
	nsObject := objc.GetClass("NSObject")

	delegateClass, registerErr = objc.RegisterClass(
		"KubeTowerWindowDelegate", nsObject, nil, nil, []objc.MethodDef{
			// The operator closed the window. On macOS that ends the
			// application, tier 1's contract.
			{Cmd: sel("windowWillClose:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
				requestStop()
			}},
			// ⌘Q, and Quit from the application menu.
			//
			// Cancel is not a refusal to quit: this process stops itself
			// properly through requestStop instead of letting AppKit exit past
			// every deferred cleanup. That is safe only because requests from
			// outside (logout, restart, Quit from the Dock) do not arrive here
			// (see kubeTowerQuitEvent:withReply:). While a terminal or a
			// port-forward is open it asks first (askBeforeQuitting).
			{Cmd: sel("applicationShouldTerminate:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) uint {
				askBeforeQuitting()
				return terminateCancel
			}},
			// The get-URL Apple Event, a kubetower:// link, which opens a console
			// page and never acts.
			//
			// Installed before AppKit finishes launching, not with the quit
			// handler: a link that starts the application is queued for the new
			// process and dispatched during launch, so a later handler would miss
			// it and a cold start by link would drop the page. The handler is
			// this delegate, which lives for the process.
			{Cmd: sel("applicationWillFinishLaunching:"), Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) {
				objc.ID(hot.nsAppleEventManager).Send(hot.sharedAppleEventManager).
					Send(hot.setEventHandler, self, hot.getURLEvent, uint32(aeInternetEventClass), uint32(aeGetURL))
			}},
			// A kubetower:// link: the event's direct object, handed to
			// receiveLink, which validates it first. No reply: a link is answered
			// by the page it opens, and the sender is told nothing.
			{Cmd: sel("kubeTowerGetURL:withReply:"), Fn: func(_ objc.ID, _ objc.SEL, event, _ objc.ID) {
				receiveLink(goString(event.Send(hot.paramDescriptorForKeyword, uint32(aeKeyDirectObject)).
					Send(hot.stringValue)))
			}},
			// AppKit installs its own Apple Event handlers while finishing
			// launching, so this is the first moment ours can replace the quit
			// handler and stay.
			{Cmd: sel("applicationDidFinishLaunching:"), Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) {
				objc.ID(hot.nsAppleEventManager).Send(hot.sharedAppleEventManager).
					Send(hot.setEventHandler, self, hot.quitEvent, uint32(aeCoreEventClass), uint32(aeQuitApplication))
			}},
			// The quit Apple Event: logout, restart, shutdown, Quit from the Dock,
			// `osascript … to quit`.
			//
			// Answered by stopping and accepting. AppKit's handler would ask
			// applicationShouldTerminate:, where both answers are wrong here:
			// Cancel tells macOS the logout was refused (User canceled, -128),
			// and Later waits for a reply inside terminate:, above run on the main
			// stack, so Run could never return to give it. Returning without an
			// error accepts; the process exits once Run has shut the server down.
			{Cmd: sel("kubeTowerQuitEvent:withReply:"), Fn: func(_ objc.ID, _ objc.SEL, _, _ objc.ID) {
				requestStop()
			}},
			// A signal, relayed to the main thread by interruptNativeWindow.
			{Cmd: sel("kubeTowerInterrupt:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
				requestStop()
			}},
			// New facts from watchWindowFacts, relayed by refreshNativeWindow. A
			// newer release is one, so the notices are looked at too.
			{Cmd: sel("kubeTowerRefresh:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
				applyWindowFacts()
				deliverNotices()
			}},
			// A lost forward (deliverNativeNotices), the end of a settle or gap
			// (scheduled by deliverNotices), or the permission answer.
			{Cmd: sel("kubeTowerNotices:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
				deliverNotices()
			}},
			// The operator went to another application: a newer release held
			// while the window was in front can be said now.
			{Cmd: sel("applicationDidResignActive:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
				deliverNotices()
			}},
			// UNUserNotificationCenterDelegate: a notification was clicked. The
			// route in userInfo is handed to the main thread, where it is
			// validated like any link; then the framework's completion handler
			// is called. The arriving thread is unspecified, so nothing here
			// touches the window; performSelectorOnMainThread: retains the
			// string until delivered.
			{
				Cmd: sel("userNotificationCenter:didReceiveNotificationResponse:withCompletionHandler:"),
				Fn: func(self objc.ID, _ objc.SEL, _, response, done objc.ID) {
					link := response.Send(hot.notification).Send(hot.request).Send(hot.content).
						Send(hot.userInfo).Send(hot.objectForKey, hot.noticeLinkKey)
					self.Send(hot.performOnMain, hot.noticeClicked, link, false)
					// `void (^)(void)`: the argument is not read.
					callBlock(done, 0)
				},
			},
			{Cmd: sel("kubeTowerNoticeClicked:"), Fn: func(_ objc.ID, _ objc.SEL, link objc.ID) {
				receiveNoticeClick(goString(link))
			}},
			// NSWorkspaceDidWakeNotification (see wakeEvent).
			{Cmd: sel("kubeTowerDidWake:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
				tellThePageTheMacWoke()
			}},
			// A click on the Dock icon while running (see bringWindowBack).
			// Answering No after doing the work: Yes hands the click to
			// AppKit's document-application default, which treats a minimized
			// window as not visible.
			{
				Cmd: sel("applicationShouldHandleReopen:hasVisibleWindows:"),
				Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID, _ bool) bool {
					bringWindowBack()
					return false
				},
			},
		})
	if registerErr != nil {
		return
	}

	// The web view's UI, navigation and download delegate, as one class, so
	// the policy reads in one place:
	//
	//   - A link asking for a new window (`target="_blank"`, window.open) goes
	//     to the default browser: the window has no tabs, and such addresses (a
	//     port-forward, an Ingress host, a repository) are somebody else's pages.
	//   - A whole-page navigation to another origin goes there too, and the
	//     console stays: following it would replace the console in a window with
	//     no back button.
	//   - A download (a download link, the log pane's blob, a response the engine
	//     cannot show) is saved to Downloads under the suggested name, never over
	//     an existing file.
	//   - Everything else is allowed.
	//
	// The address rules are nativewindownav.go's, shared with Windows. It also
	// observes the web view's title (followPageTitle).
	webDelegateClass, registerErr = objc.RegisterClass(
		"KubeTowerWebDelegate", nsObject, nil, nil, []objc.MethodDef{
			// WKUIDelegate. Returning nil means "no web view was created", so
			// WebKit drops the navigation once it has been handed out.
			{
				Cmd: sel("webView:createWebViewWithConfiguration:forNavigationAction:windowFeatures:"),
				Fn: func(_ objc.ID, _ objc.SEL, _, _, action, _ objc.ID) objc.ID {
					openOutside(actionURL(action))
					return 0
				},
			},
			// WKNavigationDelegate, request side.
			{
				Cmd: sel("webView:decidePolicyForNavigationAction:decisionHandler:"),
				Fn: func(_ objc.ID, _ objc.SEL, _, action, decide objc.ID) {
					callBlock(decide, decideNavigation(action))
				},
			},
			// WKNavigationDelegate, response side: what the engine cannot show
			// is saved. Main frame only; a subresource is the page's business.
			{
				Cmd: sel("webView:decidePolicyForNavigationResponse:decisionHandler:"),
				Fn: func(_ objc.ID, _ objc.SEL, _, response, decide objc.ID) {
					policy := uintptr(policyAllow)
					if objc.Send[bool](response, hot.isForMainFrame) && !objc.Send[bool](response, hot.canShowMIMEType) {
						policy = policyDownload
					}
					callBlock(decide, policy)
				},
			},
			// A Download decision arrives here as a WKDownload, which saves
			// nothing until it has a delegate.
			{
				Cmd: sel("webView:navigationAction:didBecomeDownload:"),
				Fn: func(self objc.ID, _ objc.SEL, _, _, download objc.ID) {
					download.Send(hot.setDelegate, self)
				},
			},
			{
				Cmd: sel("webView:navigationResponse:didBecomeDownload:"),
				Fn: func(self objc.ID, _ objc.SEL, _, _, download objc.ID) {
					download.Send(hot.setDelegate, self)
				},
			},
			// WKDownloadDelegate.
			{
				Cmd: sel("download:decideDestinationUsingResponse:suggestedFilename:completionHandler:"),
				Fn: func(_ objc.ID, _ objc.SEL, download, _, suggested, decide objc.ID) {
					path, err := downloadDestination(goString(suggested))
					if err != nil {
						log.Printf("[kubetower] a download was not saved: %v", err)
						callBlock(decide, 0) // nil cancels the download
						return
					}
					nativeWindowState.downloads[download] = path
					callBlock(decide, uintptr(objc.ID(hot.nsURL).Send(hot.fileURLWithPath, nsStringHot(path))))
				},
			},
			{
				Cmd: sel("downloadDidFinish:"),
				Fn: func(_ objc.ID, _ objc.SEL, download objc.ID) {
					log.Printf("[kubetower] saved %s", nativeWindowState.downloads[download])
					delete(nativeWindowState.downloads, download)
				},
			},
			{
				Cmd: sel("download:didFailWithError:resumeData:"),
				Fn: func(_ objc.ID, _ objc.SEL, download, failure, _ objc.ID) {
					log.Printf("[kubetower] a download to %s failed: %s",
						nativeWindowState.downloads[download],
						goString(failure.Send(hot.localizedDescription)))
					delete(nativeWindowState.downloads, download)
				},
			},
			// WKNavigationDelegate: a main-frame navigation finished. The first
			// is the one-time sign-in arriving, when a link held since launch can
			// open (windowLoaded).
			{
				Cmd: sel("webView:didFinishNavigation:"),
				Fn: func(_ objc.ID, _ objc.SEL, _, _ objc.ID) {
					windowLoaded()
				},
			},
			// Key-value observation of the web view's title, registered in
			// drawNativeWindow, the only key path observed, so only the object is
			// read.
			{
				Cmd: sel("observeValueForKeyPath:ofObject:change:context:"),
				Fn: func(_ objc.ID, _ objc.SEL, _, object, _ objc.ID, _ uintptr) {
					followPageTitle(object)
				},
			},
		})
	if registerErr != nil {
		return
	}

	menuTargetClass, registerErr = objc.RegisterClass(
		"KubeTowerMenuTarget", nsObject, nil, nil, []objc.MethodDef{
			// The native→web bridge, deliberately the only one: a script run in
			// the page. Every item that has to reach the console uses this shape,
			// never a second mechanism such as a script message handler, or the
			// menu becomes half-native, half-scripted. A test refuses any script
			// message handler or user script under internal/launch.
			{Cmd: sel("kubeTowerShowSettings:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
				nativeWindowState.webView.Send(hot.evaluateJavaScript, hot.showSettings, objc.ID(0))
			}},
			{Cmd: sel("kubeTowerReload:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
				nativeWindowState.webView.Send(hot.reload)
			}},
			// Both scales back to 1: the menu's zoom and the trackpad's pinch
			// (see drawNativeWindow).
			{Cmd: sel("kubeTowerActualSize:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
				setPageZoom(1)
				nativeWindowState.webView.Send(hot.setMagnification, float64(1))
			}},
			{Cmd: sel("kubeTowerZoomIn:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
				setPageZoom(currentPageZoom() * 1.1)
			}},
			{Cmd: sel("kubeTowerZoomOut:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
				setPageZoom(currentPageZoom() / 1.1)
			}},
			// History ▸ Back and Forward: the web view's history is the SPA's
			// pushState entries, so going back is a popstate the router
			// answers and the dock survives. With nothing behind or ahead,
			// goBack and goForward do nothing.
			{Cmd: sel("kubeTowerGoBack:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
				nativeWindowState.webView.Send(hot.goBack)
			}},
			{Cmd: sel("kubeTowerGoForward:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
				nativeWindowState.webView.Send(hot.goForward)
			}},
			// About KubeTower, with the dictionary warmHotPath built (see
			// aboutPanel).
			{Cmd: sel("kubeTowerShowAbout:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
				nativeWindowState.app.Send(hot.orderFrontAboutWithOptions, hot.aboutOptions)
			}},
			// Help ▸ KubeTower Help: the public documentation, in the browser,
			// through the door every outside address takes.
			{Cmd: sel("kubeTowerShowHelp:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
				openOutside(helpURL)
			}},
			// Cluster ▸ <a cluster>, and ⌘1…⌘9: the route change of ⌘, to
			// /c/<cluster>. The tag indexes clusterScripts, built with the menu,
			// so nothing is composed or resolved here.
			{Cmd: sel("kubeTowerShowCluster:"), Fn: func(_ objc.ID, _ objc.SEL, sender objc.ID) {
				showCluster(objc.Send[int](sender, hot.tag))
			}},
		})
}

// followPageTitle puts the page's title on the window, which the title bar, the
// Window menu, ⌘` and Mission Control read. Called from the observation
// callback, so it uses hot only.
func followPageTitle(webView objc.ID) {
	window := nativeWindowState.window
	if window == 0 || nativeWindowState.stopping {
		return
	}
	window.Send(hot.setTitle, nsStringHot(windowTitle(goString(webView.Send(hot.title)))))
}

// receiveLink is a kubetower:// link from the get-URL Apple Event: validated,
// then opened, held or refused by schemeLinks.arrive, and logged. Main thread
// only. A refusal does nothing else: the window is not brought forward and a
// held link is kept (macOS's own activation is not this process's to undo).
func receiveLink(raw string) {
	open, line := nativeWindowState.links.arrive(raw)
	log.Printf("[kubetower] %s", line)
	if open != "" {
		openLink(open)
	}
}

// windowLoaded is told that a main-frame navigation finished, and opens the
// link held since the launch, once. Main thread only.
func windowLoaded() {
	open, line := nativeWindowState.links.windowReady()
	if line != "" {
		log.Printf("[kubetower] %s", line)
	}
	if open != "" {
		openLink(open)
	}
}

// openLink shows the console page at path, which consolePathFromURL returned:
// the window is brought back and the route changes through openPageScript, the
// door every menu item takes, so a link can do nothing a menu item could not.
// The script is built here, in plain Go, and converted through nsStringHot.
func openLink(path string) {
	state := &nativeWindowState
	if state.webView == 0 || state.stopping {
		return
	}
	bringWindowBack()
	state.webView.Send(hot.evaluateJavaScript, nsStringHot(openPageScript(path)), objc.ID(0))
}

// bringWindowBack puts the window in front again: out of the Dock if
// minimized, and key. Minimized is the only way it can be out of sight while
// running: closing it ends KubeTower, and AppKit unhides a hidden application
// as part of the activating click.
func bringWindowBack() {
	window := nativeWindowState.window
	if window == 0 || nativeWindowState.stopping {
		return
	}
	if objc.Send[bool](window, hot.isMiniaturized) {
		window.Send(hot.deminiaturize, objc.ID(0))
	}
	window.Send(hot.makeKeyAndOrderFront, objc.ID(0))
}

// askBeforeQuitting is ⌘Q and Quit KubeTower: stop now when nothing would be
// lost, otherwise ask in a sheet with quitQuestion's count.
//
// A sheet, not runModal: runModal would run a second event loop inside this
// callback, on AppKit's stack below a C→Go transition, reached through
// performKeyEquivalent:, the path hot records overflowing. A sheet returns, and
// the answer arrives later in answerQuitQuestion at the ordinary depth.
//
// Only ⌘Q and the menu's Quit ask. Not:
//
//   - logging out, restarting, Quit from the Dock: the quit Apple Event, which
//     kubeTowerQuitEvent:withReply: answers without asking, or macOS would
//     report that KubeTower canceled log out;
//   - the red button and ⌘W: no Mac application asks on closing its window,
//     and this application is one window;
//   - a signal, sent on purpose.
//
// The window is brought back first: a sheet on a minimized window is a question
// nobody sees.
func askBeforeQuitting() {
	state := &nativeWindowState
	if state.stopping || state.asking {
		return
	}
	message, detail, ask := quitQuestion(state.feed.open())
	if !ask || state.window == 0 {
		requestStop()
		return
	}
	bringWindowBack()
	if state.alert != 0 {
		state.alert.Send(hot.release)
	}
	alert := objc.ID(hot.nsAlert).Send(hot.alloc).Send(hot.init)
	alert.Send(hot.setAlertStyle, uint(alertStyleWarning))
	alert.Send(hot.setMessageText, nsStringHot(message))
	alert.Send(hot.setInformativeText, nsStringHot(detail))
	alert.Send(hot.addButtonWithTitle, nsStringHot(quitButton))
	alert.Send(hot.addButtonWithTitle, nsStringHot(cancelButton))
	state.alert, state.asking = alert, true
	alert.Send(hot.beginSheet, state.window, hot.quitAnswer)
}

// answerQuitQuestion is the ⌘Q sheet's completion handler: Quit stops, Cancel
// (or Escape) leaves everything open. Made into a block once by warmHotPath and
// reached from AppKit, so it is a callback in hot's sense, and a test walks it.
func answerQuitQuestion(_ objc.Block, response int) {
	nativeWindowState.asking = false
	if response == alertFirstButtonReturn {
		requestStop()
	}
}

// applyWindowFacts puts what watchWindowFacts last published on the Dock and on
// the process: the update badge, and the App Nap guard. Main thread only,
// called from callbacks, so it reads hot and resolves nothing.
//
// The server runs in this process, so when macOS naps the application (window
// hidden, occluded or minimized) the server's timers are coalesced and its I/O
// throttled, degrading a shell's echo, a port-forward or a log follow. While any
// is open, an activity is held with NSActivityUserInitiatedAllowingIdleSystemSleep,
// ended when the last closes. Whether App Nap actually slows them is not
// measured, and the guard is kept either way; `pmset -g assertions` shows the
// activity under activityReason.
func applyWindowFacts() {
	state := &nativeWindowState
	facts := windowFactsNow.Load()
	if facts == nil || state.app == 0 || state.stopping {
		return
	}
	if facts.Badge != state.badge {
		label := objc.ID(0) // nil clears the badge
		if facts.Badge != "" {
			label = nsStringHot(facts.Badge)
		}
		state.app.Send(hot.dockTile).Send(hot.setBadgeLabel, label)
		state.badge = facts.Badge
		if facts.Badge != "" {
			log.Printf("[kubetower] a newer release exists, so the Dock icon carries a badge; " +
				"Settings ▸ Infos says which and where to get it")
		}
	}
	switch {
	case facts.Busy && state.activity == 0:
		info := objc.ID(hot.nsProcessInfo).Send(hot.processInfo)
		// Retained: the token comes back autoreleased, and draining the
		// event's pool would silently end the activity.
		state.activity = info.Send(hot.beginActivity, activityUserInitiatedAllowingIdleSystemSleep, hot.activityReason).
			Send(hot.retain)
		log.Printf("[kubetower] a terminal, a port-forward or a log stream is open, so App Nap is held off until the last one closes")
	case !facts.Busy && state.activity != 0:
		endActivity()
		log.Printf("[kubetower] nothing is open any more, so App Nap is allowed again")
	}
}

// endActivity ends the App Nap guard if it is held. Main thread only.
func endActivity() {
	state := &nativeWindowState
	if state.activity == 0 {
		return
	}
	objc.ID(hot.nsProcessInfo).Send(hot.processInfo).Send(hot.endActivity, state.activity)
	state.activity.Send(hot.release)
	state.activity = 0
}

// tellThePageTheMacWoke dispatches wakeEvent in the page and logs it (see
// wakeEvent).
func tellThePageTheMacWoke() {
	state := &nativeWindowState
	if state.webView == 0 || state.stopping {
		return
	}
	log.Printf("[kubetower] the Mac woke from sleep; the page is told (%s)", wakeEvent)
	state.webView.Send(hot.evaluateJavaScript, hot.wakeScript, objc.ID(0))
}

// --- notifications ---------------------------------------------------------------
//
// What is said, when, and where a click goes are decided in
// nativewindownotify.go. Here is the part only a Mac can do: asking for
// permission, posting, and taking a click back to the main thread. All of it
// runs on the main thread except the two completion blocks and the click
// callback, which say so.

// loadNoticeFramework brings UserNotifications into the process. Not one of
// loadFrameworks: a failure turns notifications off rather than sending the
// console to a browser. Loading it outside a bundle is harmless; asking for the
// center is not (noticesAvailable).
func loadNoticeFramework() error {
	_, err := purego.Dlopen("/System/Library/Frameworks/UserNotifications.framework/UserNotifications",
		purego.RTLD_GLOBAL|purego.RTLD_LAZY)
	return err
}

// setUpNotices decides once whether this window can notify, and when it can,
// makes the window's delegate the notification center's.
//
// Permission is asked at the first notification (askForNotices), so a launch
// with nothing to say shows no prompt. The delegate is set now because a click
// that starts the application reaches only a delegate set before the launch
// finishes.
//
// Outside a bundle the center is never asked for (see noticesAvailable for the
// abort that would follow).
func setUpNotices(framework error) {
	state := &nativeWindowState
	bundle := objc.ID(hot.nsBundle).Send(hot.mainBundle)
	ok, line := noticesAvailable(goString(bundle.Send(hot.bundleIdentifier)), goString(bundle.Send(hot.bundlePath)))
	switch {
	case !ok:
		log.Printf("[kubetower] %s", line)
		return
	case framework != nil || hot.unCenter == 0 || hot.unContent == 0 || hot.unRequest == 0:
		log.Printf("[kubetower] the UserNotifications framework could not be loaded (%v), so KubeTower "+
			"sends no macOS notifications; the console is unaffected", framework)
		return
	}
	state.notify.available = true
	noticeCenter().Send(hot.setDelegate, state.delegate)
	notices.enabled.Store(true)
}

// noticeCenter is the notification center, asked once, and only after
// setUpNotices has found a bundle.
func noticeCenter() objc.ID {
	state := &nativeWindowState
	if state.notify.center == 0 {
		state.notify.center = objc.ID(hot.unCenter).Send(hot.currentCenter)
	}
	return state.notify.center
}

// deliverNotices asks windowNotices.take what to post now, posts it, and
// schedules the next look when something waits for a settle or a gap. Reached
// from callbacks, so it reads hot and resolves nothing. "Not active" is
// -[NSApplication isActive], read on the main thread at the moment of decision.
func deliverNotices() {
	state := &nativeWindowState
	if !state.notify.available || state.app == 0 || state.stopping {
		return
	}
	settleNotices()
	var report update.Report
	if state.feed.Update != nil {
		report = state.feed.Update()
	}
	out, lines, again := notices.take(time.Now(), objc.Send[bool](state.app, hot.isActive), report)
	for _, line := range lines {
		log.Printf("[kubetower] %s", line)
	}
	for _, note := range out {
		postNotice(note)
	}
	if again > 0 && state.delegate != 0 {
		// One pending look at a time: an earlier one is replaced, not added to.
		objc.ID(hot.nsObject).Send(hot.cancelPrevious, state.delegate, hot.notices, objc.ID(0))
		state.delegate.Send(hot.performAfterDelay, hot.notices, objc.ID(0), again.Seconds())
	}
}

// postNotice posts one notification, or holds it while macOS has not answered
// the permission prompt, which is asked for here at the first one.
func postNotice(note notice) {
	state := &nativeWindowState
	switch noticeAuthorization.Load() {
	case noticeAuthGranted:
		addNotice(note)
	case noticeAuthDenied:
		settleNotices()
		log.Printf("[kubetower] not notified, macOS does not allow it: %s", note.Title)
	default:
		if len(state.notify.held) < noticeHeldMax {
			state.notify.held = append(state.notify.held, note)
			log.Printf("[kubetower] notification held until macOS answers the permission request: %s", note.Title)
		} else {
			log.Printf("[kubetower] notification dropped, %d already wait for the permission answer: %s",
				noticeHeldMax, note.Title)
		}
		if noticeAuthorization.CompareAndSwap(noticeAuthUnasked, noticeAuthAsking) {
			askForNotices()
		}
	}
}

// askForNotices is the permission request. macOS prompts the first time and
// answers from the recorded choice after, so asking at the first notification
// of every launch shows the prompt once, when there is something to say.
func askForNotices() {
	log.Printf("[kubetower] asking macOS whether KubeTower may show notifications; macOS prompts once, " +
		"and the answer is kept in System Settings > Notifications")
	noticeCenter().Send(hot.requestAuthorization, uint(unAuthorizationOptionAlert), hot.noticeAuthorized)
}

// settleNotices acts on macOS's answer once it has arrived: what was held is
// posted, or dropped with one line saying notifications are off.
func settleNotices() {
	state := &nativeWindowState
	switch noticeAuthorization.Load() {
	case noticeAuthGranted:
		held := state.notify.held
		state.notify.held = nil
		for _, note := range held {
			addNotice(note)
		}
	case noticeAuthDenied:
		state.notify.held = nil
		if !state.notify.deniedSaid {
			state.notify.deniedSaid = true
			log.Printf("[kubetower] macOS does not allow KubeTower's notifications (System Settings > " +
				"Notifications > KubeTower), so a lost port-forward or a newer release is said in this log only")
		}
	}
}

// addNotice hands one notification to the center: a title, a body and the click
// route under noticeLinkKey, delivered now (no trigger).
//
// The content is alloc/init and released once the request has copied it; the
// request and strings are autoreleased and outlive the center's taking them.
//
// No nil reaches a collection: dictionaryWithObject: with nil raises, and an
// Objective-C exception ends the process. stringWithUTF8String: answers nil for
// invalid UTF-8, so every string is made first and checked. The text is valid
// UTF-8 by construction (clean, bound); this bounds a defect.
func addNotice(note notice) {
	title, body, link, id := nsStringHot(note.Title), nsStringHot(note.Body), nsStringHot(note.Link), nsStringHot(note.ID)
	if title == 0 || body == 0 || link == 0 || id == 0 {
		log.Printf("[kubetower] a notification was not posted: its text is not valid UTF-8")
		return
	}
	content := objc.ID(hot.unContent).Send(hot.alloc).Send(hot.init)
	content.Send(hot.setTitle, title)
	content.Send(hot.setBody, body)
	content.Send(hot.setUserInfo, objc.ID(hot.nsDictionary).Send(hot.dictionaryWithObject, link, hot.noticeLinkKey))
	request := objc.ID(hot.unRequest).Send(hot.requestWithIdentifier, id, content, objc.ID(0))
	content.Send(hot.release)
	noticeCenter().Send(hot.addRequest, request, hot.noticePosted)
	log.Printf("[kubetower] notification posted: %s", note.Title)
}

// answerNoticeAuthorization is the permission request's completion handler,
// `void (^)(BOOL granted, NSError *error)`. Not on the main thread: the
// framework calls it on its own queue (purego runs Go on a foreign thread), so
// it only records the answer and asks the main thread to act.
func answerNoticeAuthorization(_ objc.Block, granted bool, failure objc.ID) {
	if granted {
		noticeAuthorization.Store(noticeAuthGranted)
	} else {
		noticeAuthorization.Store(noticeAuthDenied)
	}
	if failure != 0 {
		log.Printf("[kubetower] macOS answered the notification permission request with an error: %s",
			goString(failure.Send(hot.localizedDescription)))
	}
	if target := interruptTarget.Load(); target != 0 {
		objc.ID(target).Send(hot.performOnMain, hot.notices, objc.ID(0), false)
	}
}

// answerNoticePosted is addNotificationRequest:'s completion handler,
// `void (^)(NSError *error)`, on the framework's queue: it only logs a refusal.
func answerNoticePosted(_ objc.Block, failure objc.ID) {
	if failure != 0 {
		log.Printf("[kubetower] macOS did not take a notification: %s", goString(failure.Send(hot.localizedDescription)))
	}
}

// receiveNoticeClick is a click on a notification, back on the main thread: the
// window comes back and the route goes through receiveLink, validated and held
// exactly as a kubetower:// link.
func receiveNoticeClick(link string) {
	log.Printf("[kubetower] a notification was clicked")
	bringWindowBack()
	receiveLink(link)
}

// showCluster runs the navigation script of the Cluster menu item tagged index;
// an index out of range is a disabled line's.
func showCluster(index int) {
	state := &nativeWindowState
	if index < 0 || index >= len(state.clusterScripts) || state.webView == 0 {
		return
	}
	state.webView.Send(hot.evaluateJavaScript, state.clusterScripts[index], objc.ID(0))
}

// enableWebInspector turns the Web Inspector on, on a development build only
// (webInspectorAllowed).
//
// setInspectable: exists from macOS 13.3; on an older system it would be an
// unknown selector, an uncatchable Objective-C exception, so it is asked about
// first. The value is read back and logged, since a developer acts on that
// line.
func enableWebInspector(webView objc.ID) {
	if !webInspectorAllowed() {
		return
	}
	if !objc.Send[bool](webView, sel("respondsToSelector:"), sel("setInspectable:")) {
		log.Printf("[kubetower] development build, but this macOS has no setInspectable: (it " +
			"arrived in 13.3), so the window has no Web Inspector")
		return
	}
	webView.Send(sel("setInspectable:"), true)
	log.Printf("[kubetower] development build: the window's Web Inspector is on (isInspectable "+
		"reads back %t); open it from Safari ▸ Develop. A release build never turns it on",
		objc.Send[bool](webView, sel("isInspectable")))
}

// decideNavigation is the request-side policy registerClasses describes.
func decideNavigation(action objc.ID) uintptr {
	// Asked first: a download link targets the current frame and would
	// otherwise be judged as a navigation of it.
	if objc.Send[bool](action, hot.shouldPerformDownload) {
		return policyDownload
	}
	frame := action.Send(hot.targetFrame)
	if frame == 0 || !objc.Send[bool](frame, hot.isMainFrame) {
		return policyAllow
	}
	if target := actionURL(action); leavesConsole(nativeWindowState.origin, target) {
		openOutside(target)
		return policyCancel
	}
	return policyAllow
}

// openOutside hands an address to the default browser, if outsideAddress
// allows it.
func openOutside(raw string) {
	if ok, refusal := outsideAddress(raw); !ok {
		if refusal != "" {
			log.Printf("[kubetower] %s", refusal)
		}
		return
	}
	nsurl := objc.ID(hot.nsURL).Send(hot.urlWithString, nsStringHot(raw))
	workspace := objc.ID(hot.nsWorkspace).Send(hot.sharedWorkspace)
	if !objc.Send[bool](workspace, hot.openURL, nsurl) {
		log.Printf("[kubetower] could not open %s in the default browser", loggableURL(raw))
	}
}

// actionURL is the address a WKNavigationAction is going to.
func actionURL(action objc.ID) string {
	return goString(action.Send(hot.request).Send(hot.url).Send(hot.absoluteString))
}

// downloadDestination is where a download suggested as name is saved: in
// Downloads, under that name or `name (2)`, `name (3)`…: WKDownload refuses an
// existing destination rather than overwriting it.
//
// The name comes from the page, so only its last element is kept: a suggestion
// of `../../.zshrc` lands as `.zshrc` in Downloads.
func downloadDestination(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "Downloads")
	name = filepath.Base(filepath.Clean("/" + name))
	if name == "/" || name == "." {
		name = "download"
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for n := 1; n < 1000; n++ {
		candidate := filepath.Join(dir, name)
		if n > 1 {
			candidate = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, n, ext))
		}
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s and 998 numbered copies of it already exist in %s", name, dir)
}

// callBlock calls an Objective-C block that AppKit or WebKit handed this file (a
// decision or completion handler) with one argument.
//
// purego's Block.Invoke only calls blocks purego made. So this reads the invoke
// pointer from the block's header, fixed by the Blocks ABI, and calls it with
// the block first, as a C caller does. The header is read through the local
// variable's address, not by converting the uintptr ID to a pointer, which go
// vet rightly refuses.
func callBlock(block objc.ID, arg uintptr) {
	if block == 0 {
		return
	}
	header := *(**blockHeader)(unsafe.Pointer(&block))
	purego.SyscallN(header.invoke, uintptr(block), arg)
}

// blockHeader is the start of every Objective-C block: the Blocks ABI's
// Block_literal, as far as the invoke pointer.
type blockHeader struct {
	_        structs.HostLayout
	isa      uintptr
	flags    int32
	reserved int32
	invoke   uintptr
}

// hot holds everything an Objective-C callback touches, resolved once, before
// any callback can run.
//
// This is the most important constraint of this file, not an optimization:
// objc.RegisterName is implemented with reflect over a SyscallN, a Go→C
// transition. Inside an IMP, already a C→Go transition on AppKit's stack, the
// two nest, and resolving a name there crashes with SIGSEGV a few bytes below
// the stack pointer (a stack overflow) on the path of every keystroke. Some
// callbacks are reached from shallower stacks, but that is not a property; ⌘,
// arrives through performKeyEquivalent:, the path that crashes.
//
// So the rule is structural: a callback reads hot and resolves nothing. sel,
// class and nsString appear in no callback nor any function one reaches, which a
// test reads out of this file on every platform.
var hot struct {
	stop, postEvent, otherEvent                         objc.SEL
	performOnMain, interrupt                            objc.SEL
	stringWithUTF8String, utf8String                    objc.SEL
	evaluateJavaScript, reload                          objc.SEL
	pageZoom, setPageZoom                               objc.SEL
	request, url, absoluteString                        objc.SEL
	targetFrame, isMainFrame                            objc.SEL
	shouldPerformDownload                               objc.SEL
	isForMainFrame, canShowMIMEType                     objc.SEL
	setDelegate, localizedDescription                   objc.SEL
	urlWithString, fileURLWithPath                      objc.SEL
	sharedWorkspace, openURL                            objc.SEL
	sharedAppleEventManager, setEventHandler, quitEvent objc.SEL
	getURLEvent, paramDescriptorForKeyword, stringValue objc.SEL
	goBack, goForward, setMagnification                 objc.SEL
	title, setTitle                                     objc.SEL
	isMiniaturized, deminiaturize, makeKeyAndOrderFront objc.SEL
	orderFrontAboutWithOptions                          objc.SEL
	refresh, tag, alloc, init, retain, release          objc.SEL
	dockTile, setBadgeLabel                             objc.SEL
	processInfo, beginActivity, endActivity             objc.SEL
	setAlertStyle, setMessageText, setInformativeText   objc.SEL
	addButtonWithTitle, beginSheet                      objc.SEL
	notices, noticeClicked, isActive                    objc.SEL
	performAfterDelay, cancelPrevious                   objc.SEL
	mainBundle, bundleIdentifier, bundlePath            objc.SEL
	currentCenter, requestAuthorization, addRequest     objc.SEL
	setBody, setUserInfo, dictionaryWithObject          objc.SEL
	requestWithIdentifier, notification, content        objc.SEL
	userInfo, objectForKey                              objc.SEL
	nsString, nsURL, nsWorkspace, nsEvent               objc.Class
	nsAppleEventManager, nsAlert, nsProcessInfo         objc.Class
	nsBundle, nsDictionary, nsObject                    objc.Class
	unCenter, unContent, unRequest                      objc.Class
	noticeLinkKey                                       objc.ID // noticeLinkKey, as an NSString
	noticeAuthorized, noticePosted                      objc.Block
	showSettings                                        objc.ID // showSettingsScript, as an NSString
	wakeScript                                          objc.ID // wakeScript, as an NSString
	activityReason                                      objc.ID // activityReason, as an NSString
	aboutOptions                                        objc.ID // aboutPanel(), as the panel's NSDictionary
	quitAnswer                                          objc.Block
}

// warmHotPath fills hot before the window is on screen, so before any callback
// can run.
func warmHotPath() {
	hot.stop = sel("stop:")
	hot.postEvent = sel("postEvent:atStart:")
	hot.otherEvent = sel("otherEventWithType:location:modifierFlags:timestamp:windowNumber:context:subtype:data1:data2:")
	hot.performOnMain = sel("performSelectorOnMainThread:withObject:waitUntilDone:")
	hot.interrupt = sel("kubeTowerInterrupt:")
	hot.stringWithUTF8String = sel("stringWithUTF8String:")
	hot.utf8String = sel("UTF8String")
	hot.evaluateJavaScript = sel("evaluateJavaScript:completionHandler:")
	hot.reload = sel("reload")
	hot.pageZoom = sel("pageZoom")
	hot.setPageZoom = sel("setPageZoom:")
	hot.request = sel("request")
	hot.url = sel("URL")
	hot.absoluteString = sel("absoluteString")
	hot.targetFrame = sel("targetFrame")
	hot.isMainFrame = sel("isMainFrame")
	hot.shouldPerformDownload = sel("shouldPerformDownload")
	hot.isForMainFrame = sel("isForMainFrame")
	hot.canShowMIMEType = sel("canShowMIMEType")
	hot.setDelegate = sel("setDelegate:")
	hot.localizedDescription = sel("localizedDescription")
	hot.urlWithString = sel("URLWithString:")
	hot.fileURLWithPath = sel("fileURLWithPath:")
	hot.sharedWorkspace = sel("sharedWorkspace")
	hot.openURL = sel("openURL:")
	hot.sharedAppleEventManager = sel("sharedAppleEventManager")
	hot.setEventHandler = sel("setEventHandler:andSelector:forEventClass:andEventID:")
	hot.quitEvent = sel("kubeTowerQuitEvent:withReply:")
	hot.getURLEvent = sel("kubeTowerGetURL:withReply:")
	hot.paramDescriptorForKeyword = sel("paramDescriptorForKeyword:")
	hot.stringValue = sel("stringValue")
	hot.goBack = sel("goBack")
	hot.goForward = sel("goForward")
	hot.setMagnification = sel("setMagnification:")
	hot.title = sel("title")
	hot.setTitle = sel("setTitle:")
	hot.isMiniaturized = sel("isMiniaturized")
	hot.deminiaturize = sel("deminiaturize:")
	hot.makeKeyAndOrderFront = sel("makeKeyAndOrderFront:")
	hot.orderFrontAboutWithOptions = sel("orderFrontStandardAboutPanelWithOptions:")
	hot.refresh = sel("kubeTowerRefresh:")
	hot.tag = sel("tag")
	hot.alloc = sel("alloc")
	hot.init = sel("init")
	hot.retain = sel("retain")
	hot.release = sel("release")
	hot.dockTile = sel("dockTile")
	hot.setBadgeLabel = sel("setBadgeLabel:")
	hot.processInfo = sel("processInfo")
	hot.beginActivity = sel("beginActivityWithOptions:reason:")
	hot.endActivity = sel("endActivity:")
	hot.setAlertStyle = sel("setAlertStyle:")
	hot.setMessageText = sel("setMessageText:")
	hot.setInformativeText = sel("setInformativeText:")
	hot.addButtonWithTitle = sel("addButtonWithTitle:")
	hot.beginSheet = sel("beginSheetModalForWindow:completionHandler:")
	hot.notices = sel("kubeTowerNotices:")
	hot.noticeClicked = sel("kubeTowerNoticeClicked:")
	hot.isActive = sel("isActive")
	hot.performAfterDelay = sel("performSelector:withObject:afterDelay:")
	hot.cancelPrevious = sel("cancelPreviousPerformRequestsWithTarget:selector:object:")
	hot.mainBundle = sel("mainBundle")
	hot.bundleIdentifier = sel("bundleIdentifier")
	hot.bundlePath = sel("bundlePath")
	hot.currentCenter = sel("currentNotificationCenter")
	hot.requestAuthorization = sel("requestAuthorizationWithOptions:completionHandler:")
	hot.addRequest = sel("addNotificationRequest:withCompletionHandler:")
	hot.setBody = sel("setBody:")
	hot.setUserInfo = sel("setUserInfo:")
	hot.dictionaryWithObject = sel("dictionaryWithObject:forKey:")
	hot.requestWithIdentifier = sel("requestWithIdentifier:content:trigger:")
	hot.notification = sel("notification")
	hot.content = sel("content")
	hot.userInfo = sel("userInfo")
	hot.objectForKey = sel("objectForKey:")
	hot.nsBundle = objc.GetClass("NSBundle")
	hot.nsDictionary = objc.GetClass("NSDictionary")
	hot.nsObject = objc.GetClass("NSObject")
	// Zero when UserNotifications did not load, which setUpNotices reads as no
	// notifications rather than sending to a nil class.
	hot.unCenter = objc.GetClass("UNUserNotificationCenter")
	hot.unContent = objc.GetClass("UNMutableNotificationContent")
	hot.unRequest = objc.GetClass("UNNotificationRequest")
	hot.nsAlert = objc.GetClass("NSAlert")
	hot.nsProcessInfo = objc.GetClass("NSProcessInfo")
	hot.nsAppleEventManager = objc.GetClass("NSAppleEventManager")
	hot.nsString = objc.GetClass("NSString")
	hot.nsURL = objc.GetClass("NSURL")
	hot.nsWorkspace = objc.GetClass("NSWorkspace")
	hot.nsEvent = objc.GetClass("NSEvent")
	// Retained: used for the life of the process, while stringWithUTF8String:
	// alone returns an object the next autorelease drain would free.
	hot.showSettings = nsString(showSettingsScript).Send(sel("retain"))
	hot.wakeScript = nsString(wakeScript).Send(sel("retain"))
	hot.activityReason = nsString(activityReason).Send(sel("retain"))
	hot.aboutOptions = aboutPanelOptions(aboutPanel())
	// Made once and never released: AppKit copies the block for each sheet.
	// Made here rather than in askBeforeQuitting because objc.NewBlock creates
	// a callback and encodes a signature, which a callback may not do.
	if hot.quitAnswer == 0 {
		hot.quitAnswer = objc.NewBlock(answerQuitQuestion)
	}
	// The notification center's two completion handlers, likewise.
	if hot.noticeAuthorized == 0 {
		hot.noticeAuthorized = objc.NewBlock(answerNoticeAuthorization)
	}
	if hot.noticePosted == 0 {
		hot.noticePosted = objc.NewBlock(answerNoticePosted)
	}
	if hot.noticeLinkKey == 0 {
		hot.noticeLinkKey = nsString(noticeLinkKey).Send(sel("retain"))
	}
}

// aboutPanelOptions is the dictionary orderFrontStandardAboutPanelWithOptions:
// takes, built once by warmHotPath, since building it resolves classes and
// selectors. Owned (alloc/init, never released): it lives for the process.
//
// The keys are the string values of AppKit's exported constants, spelt out
// rather than read with dlsym: NSAboutPanelOptionApplicationName is
// "ApplicationName", …ApplicationVersion "ApplicationVersion", …Version
// "Version", …Credits "Credits"; NSForegroundColorAttributeName "NSColor",
// NSFontAttributeName "NSFont", NSParagraphStyleAttributeName "NSParagraphStyle".
//
// The credits carry labelColor, which follows the panel's appearance: AppKit
// maps uncolored credits to dark only for binaries declaring a recent SDK, and
// setting the color keeps the credits readable whatever the toolchain writes.
func aboutPanelOptions(text aboutPanelText) objc.ID {
	style := class("NSMutableParagraphStyle").Send(sel("alloc")).Send(sel("init"))
	style.Send(sel("setAlignment:"), textAlignmentCenter(runtime.GOARCH))
	attributes := class("NSMutableDictionary").Send(sel("alloc")).Send(sel("init"))
	attributes.Send(sel("setObject:forKey:"), class("NSColor").Send(sel("labelColor")), nsString("NSColor"))
	attributes.Send(sel("setObject:forKey:"),
		class("NSFont").Send(sel("systemFontOfSize:"), objc.Send[float64](class("NSFont"), sel("smallSystemFontSize"))),
		nsString("NSFont"))
	attributes.Send(sel("setObject:forKey:"), style, nsString("NSParagraphStyle"))
	credits := class("NSAttributedString").Send(sel("alloc")).
		Send(sel("initWithString:attributes:"), nsString(text.Credits), attributes)

	options := class("NSMutableDictionary").Send(sel("alloc")).Send(sel("init"))
	options.Send(sel("setObject:forKey:"), nsString(text.Name), nsString("ApplicationName"))
	options.Send(sel("setObject:forKey:"), nsString(text.Version), nsString("ApplicationVersion"))
	options.Send(sel("setObject:forKey:"), nsString(text.Build), nsString("Version"))
	options.Send(sel("setObject:forKey:"), credits, nsString("Credits"))
	return options
}

// nsStringHot and goString convert strings inside a callback, through hot.
func nsStringHot(s string) objc.ID {
	return objc.ID(hot.nsString).Send(hot.stringWithUTF8String, s)
}

func goString(ns objc.ID) string {
	if ns == 0 {
		return ""
	}
	return objc.Send[string](ns, hot.utf8String)
}

// currentPageZoom and setPageZoom drive WKWebView's own zoom: the engine's
// scaling, so the terminal's canvas and the measured column widths scale as in
// a browser, rather than being laid out at one size and painted at another.
func currentPageZoom() float64 {
	zoom := objc.Send[float64](nativeWindowState.webView, hot.pageZoom)
	if zoom <= 0 {
		return 1
	}
	return zoom
}

func setPageZoom(zoom float64) {
	// Bounded: the items repeat, and a tiny zoom leaves an unreadable console
	// whose way back is an item nobody can read.
	switch {
	case zoom < 0.5:
		zoom = 0.5
	case zoom > 3:
		zoom = 3
	}
	nativeWindowState.webView.Send(hot.setPageZoom, zoom)
}

// buildMenuBar is the menu bar at the top of the screen.
//
// The Edit menu is not decoration: in a WKWebView ⌘C, ⌘V, ⌘X and ⌘A are menu key
// equivalents AppKit routes down the responder chain, so without the menu they
// reach nothing, breaking the dock terminal and the object editor. Every Edit
// item has a nil target, meaning "up the responder chain", which lands on the
// web view.
//
// The application menu's title comes from the bundle's CFBundleName; the bare
// binary run from a terminal shows the executable name, macOS's rule.
func buildMenuBar(target objc.ID, clusters []clusterMenuItem) {
	mainMenu := class("NSMenu").Send(sel("alloc")).Send(sel("init"))

	appMenu := submenu(mainMenu, "KubeTower")
	// Not the standard orderFrontStandardAboutPanel: with a nil target, which
	// reads the version from the bundle: outside one it would name the process
	// and show no version.
	addItem(appMenu, "About KubeTower", "kubeTowerShowAbout:", "", 0, target)
	addSeparator(appMenu)
	addItem(appMenu, "Settings…", "kubeTowerShowSettings:", ",", 0, target)
	addSeparator(appMenu)
	addItem(appMenu, "Hide KubeTower", "hide:", "h", 0, 0)
	addItem(appMenu, "Hide Others", "hideOtherApplications:", "h", modifierFlagCommand|modifierFlagOption, 0)
	addItem(appMenu, "Show All", "unhideAllApplications:", "", 0, 0)
	addSeparator(appMenu)
	// terminate:, because ⌘Q must mean what it means everywhere.
	// applicationShouldTerminate: turns it into an orderly stop, after a
	// question while a terminal or port-forward is open.
	addItem(appMenu, "Quit KubeTower", "terminate:", "q", 0, 0)

	editMenu := submenu(mainMenu, "Edit")
	addItem(editMenu, "Undo", "undo:", "z", 0, 0)
	addItem(editMenu, "Redo", "redo:", "Z", 0, 0)
	addSeparator(editMenu)
	addItem(editMenu, "Cut", "cut:", "x", 0, 0)
	addItem(editMenu, "Copy", "copy:", "c", 0, 0)
	addItem(editMenu, "Paste", "paste:", "v", 0, 0)
	addItem(editMenu, "Select All", "selectAll:", "a", 0, 0)

	viewMenu := submenu(mainMenu, "View")
	addItem(viewMenu, "Reload", "kubeTowerReload:", "r", 0, target)
	addSeparator(viewMenu)
	addItem(viewMenu, "Actual Size", "kubeTowerActualSize:", "0", 0, target)
	// "=" not "+": a key equivalent is the character the key types, and "+"
	// needs Shift on a US layout, so ⌘= would do nothing. Every Mac application
	// binds Zoom In to "=".
	addItem(viewMenu, "Zoom In", "kubeTowerZoomIn:", "=", 0, target)
	addItem(viewMenu, "Zoom Out", "kubeTowerZoomOut:", "-", 0, target)
	addSeparator(viewMenu)
	// ⌃⌘F, the system's own shortcut.
	addItem(viewMenu, "Enter Full Screen", "toggleFullScreen:", "f", modifierFlagCommand|modifierFlagControl, 0)

	// Between View and Window, where Safari has it; ⌘[ and ⌘] are every Mac
	// browser's. AppKit localizes them to the keyboard layout (on a French one
	// they become `^` and `$`), as for Safari.
	//
	// The page has the first refusal of every ⌘ key (WKWebView offers it before
	// the menu), so an editor binding ⌘] keeps it while focused.
	//
	// Always enabled: graying Back would need validateMenuItem:, run on every
	// key equivalent and menu opening, the stack hot exists to protect, for an
	// item that already does nothing then.
	historyMenu := submenu(mainMenu, "History")
	addItem(historyMenu, "Back", "kubeTowerGoBack:", "[", 0, target)
	addItem(historyMenu, "Forward", "kubeTowerGoForward:", "]", 0, target)

	// Cluster, between History and Window, where Safari has Bookmarks.
	// clusterMenu says what is listed and in what order.
	//
	// ⌘1…⌘9 do not collide with the console's shortcuts (⌘K/Ctrl+K and `/` for
	// the menu filter, Ctrl+Shift+C/V in the terminal, no ⌘ digit), and the page
	// refuses them first. What xterm.js does with a ⌘ digit while a terminal has
	// focus is not established.
	//
	// Read once, when the window opens: a change on the rail shows at the next
	// launch. Rebuilding on every opening would need a menu delegate, a
	// callback on every key equivalent, for a list that rarely changes.
	//
	// Not auto-enabled: the line explaining an empty menu must stay disabled
	// whatever its action.
	clusterMenuBar := submenu(mainMenu, "Cluster")
	clusterMenuBar.Send(sel("setAutoenablesItems:"), false)
	for _, line := range clusters {
		if line.Separator {
			addSeparator(clusterMenuBar)
			continue
		}
		item := addItem(clusterMenuBar, line.Title, "kubeTowerShowCluster:", line.Key, 0, target)
		item.Send(sel("setEnabled:"), line.Enabled)
		if line.ToolTip != "" {
			item.Send(sel("setToolTip:"), nsString(line.ToolTip))
		}
		if !line.Enabled {
			item.Send(sel("setTag:"), -1) // `showCluster` does nothing for it
			continue
		}
		item.Send(sel("setTag:"), len(nativeWindowState.clusterScripts))
		nativeWindowState.clusterScripts = append(nativeWindowState.clusterScripts,
			nsString(navigateScript(line.Path)).Send(sel("retain")))
	}

	windowMenu := submenu(mainMenu, "Window")
	addItem(windowMenu, "Minimize", "performMiniaturize:", "m", 0, 0)
	addItem(windowMenu, "Zoom", "performZoom:", "", 0, 0)
	addSeparator(windowMenu)
	// ⌘W closes the window, and with it KubeTower, like the red button.
	// WKWebView passes on what it does not handle (also why ⌘M works), so no
	// NSWindow subclass overriding performKeyEquivalent: is needed; one
	// overflows the stack (see hot).
	addItem(windowMenu, "Close", "performClose:", "w", 0, 0)

	// ⌘? is the system's Help shortcut. Declaring the Help menu is what puts
	// the system's menu search field at its top.
	helpMenu := submenu(mainMenu, "Help")
	addItem(helpMenu, "KubeTower Help", "kubeTowerShowHelp:", "?", 0, target)

	nativeWindowState.app.Send(sel("setMainMenu:"), mainMenu)
	nativeWindowState.app.Send(sel("setHelpMenu:"), helpMenu)
	// Declaring the Window menu is what makes AppKit list the open windows in
	// it.
	nativeWindowState.app.Send(sel("setWindowsMenu:"), windowMenu)
}

// submenu adds a titled top-level entry and returns the menu hanging off it.
//
// The item carries the title as well as the menu: macOS reads the menu's title
// for the bar, and an item with none is a blank space.
func submenu(mainMenu objc.ID, title string) objc.ID {
	item := class("NSMenuItem").Send(sel("alloc")).Send(sel("init"))
	item.Send(sel("setTitle:"), nsString(title))
	mainMenu.Send(sel("addItem:"), item)

	menu := class("NSMenu").Send(sel("alloc")).Send(sel("initWithTitle:"), nsString(title))
	item.Send(sel("setSubmenu:"), menu)
	return menu
}

// addItem appends one menu item.
//
// A target of zero is a nil target, not "no action": AppKit sends the action up
// the responder chain until something implements it. Every standard item relies
// on that (copy: reaches the web view, performClose: the window, terminate: the
// application).
func addItem(menu objc.ID, title, action, key string, modifiers uint, target objc.ID) objc.ID {
	item := class("NSMenuItem").Send(sel("alloc")).
		Send(sel("initWithTitle:action:keyEquivalent:"),
			nsString(title), sel(action), nsString(key))
	if modifiers != 0 {
		item.Send(sel("setKeyEquivalentModifierMask:"), modifiers)
	}
	if target != 0 {
		item.Send(sel("setTarget:"), target)
	}
	menu.Send(sel("addItem:"), item)
	return item
}

func addSeparator(menu objc.ID) {
	menu.Send(sel("addItem:"), class("NSMenuItem").Send(sel("separatorItem")))
}
