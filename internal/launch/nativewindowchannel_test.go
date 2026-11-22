package launch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// There is no web→native channel, on either platform, and the source is read to
// keep it so.
//
// Nothing the window does needs one. So `evaluateJavaScript` is the only door
// between the page and the
// window, and it opens native→web only: the menu, ⌘,, waking from sleep and a
// notification's click all reach the page through it, and nothing in the page
// reaches native code. `nativewindow_darwin.go` says so twice, at the title's
// observer and at the menu target's bridge.
//
// **A safe state that is an absence needs something that checks it.** A comment
// cannot refuse anything, and the door this keeps shut is one line: one
// `addScriptMessageHandler:name:` on the web view's user content controller,
// and every script that runs in the window can post into the process. So the
// rule is read out of the source, as TestNoWindowCallbackResolvesAnything reads
// its own, which is also why it runs on every platform: it parses the files
// rather than compiling them, so a Linux runner checks the darwin and the
// windows file alike.
//
// **What it covers**, in every non-test Go file under `internal/launch`,
// whatever its build tag:
//
//   - **WebKit's doors**, in `scriptDoors`: a string literal or an identifier
//     that names `addScriptMessageHandler`, `addScriptMessageHandlerWithReply`,
//     `addUserScript` or `WKUserScript`, in any case. A string, because purego
//     reaches Objective-C by name (`sel("addUserScript:")`,
//     `class("WKUserScript")`); an identifier, because a typed binding would
//     spell the same door as a method (`AddScriptMessageHandler`). A user script
//     is not a channel on its own, and is here because it is the other half of
//     every bridge WebKit has: it is how a page is handed the function it posts
//     through.
//   - **WebView2's doors**, in `webView2Doors`, in a file that imports
//     `go-webview2`: the identifiers `Bind` (the library's RPC binding, a Go
//     function the page calls by name), `MessageCallback` (the hook every
//     `chrome.webview.postMessage` reaches), `MessageReceived` and
//     `AddWebMessageReceived` (the COM event under both). Exact names, and
//     only where the library is imported, because `Bind` is too common a word
//     to refuse everywhere.
//
// **What it does not**: a test file - it ships nothing, and this one has to name
// every door to forbid it; a comment, which is not in the tree a parse without
// `parser.ParseComments` returns, so the two that state the rule do not trip
// it; a name assembled at run time (`"addScript" + "MessageHandler:name:"`);
// and `go-webview2`'s own `window.external.invoke`, which the library injects
// whether this process binds anything or not. That one is shut a different
// way: the Windows window turns WebView2's web messages off,
// so the function has nothing to post through, and
// TestTheWindowsWindowTurnsWebMessagesOff below keeps that call in the source.
// A change that does any of those should be read by hand.
//
// **The web half is a lint rule**: `webkit` and `chrome` are restricted globals
// and properties in `web/eslint.config.js`, so the page cannot reach for the
// other end of a door that does not exist.
func TestNoWebToNativeChannel(t *testing.T) {
	var violations []string
	var darwin, windows *ast.File
	fset := token.NewFileSet()
	files := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		files++
		switch path {
		case "nativewindow_darwin.go":
			darwin = file
		case "nativewindow_windows.go":
			windows = file
		}
		violations = append(violations, scriptDoors(fset, file, forbiddenScriptDoors)...)
		violations = append(violations, webView2Doors(fset, file, forbiddenWebView2Doors)...)
		return nil
	})
	if err != nil {
		t.Fatalf("reading internal/launch: %v", err)
	}

	// The walk has to have read the two files the rule is about, and each scan
	// has to be shown finding a door that does exist. Without these, a walk
	// rooted in the wrong place, or a scan that stopped recognizing how the
	// windows are written, would find no forbidden door and pass - an empty
	// answer read as a clean one.
	if darwin == nil || windows == nil {
		t.Fatalf("read %d files and not both nativewindow_darwin.go and nativewindow_windows.go: "+
			"the walk is not reading internal/launch", files)
	}
	// `evaluateJavaScript:completionHandler:` is the one door there is, named
	// the way every forbidden one would be.
	if len(scriptDoors(fset, darwin, []string{"evaluateJavaScript"})) == 0 {
		t.Fatal("found no evaluateJavaScript in nativewindow_darwin.go: the scan no longer recognizes " +
			"how the macOS window names a selector")
	}
	// `Navigate` is a `go-webview2` call the Windows window makes, so finding it
	// shows the import is recognized and its selectors are read.
	if len(webView2Doors(fset, windows, []string{"Navigate"})) == 0 {
		t.Fatal("found no Navigate in nativewindow_windows.go: the scan no longer recognizes " +
			"the go-webview2 import or how the Windows window calls it")
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Error(v + ": there is no web→native channel; " +
			"evaluateJavaScript, native→web, is the only door")
	}
}

// `go-webview2` routes every `chrome.webview.postMessage` into its RPC
// dispatcher, bound or not, so the Windows window's half of "no web→native
// channel" is a setting rather than an absence: `IsWebMessageEnabled` false,
// set before the first navigation. **A safe state that
// needs a call to hold it needs something that keeps the call**, so the source
// is read for it, the same way as above and on every platform: `holdTheWindow`
// puts 0 into `putIsWebMessageEnabled`, and `drawNativeWindow` calls
// `holdTheWindow` before it calls `Navigate`.
//
// What it does not establish is that WebView2 honors the setting; that is
// read back at run time, and a window whose read-back is not false is not
// drawn.
func TestTheWindowsWindowTurnsWebMessagesOff(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "nativewindow_windows.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing nativewindow_windows.go: %v", err)
	}
	funcs := map[string]*ast.FuncDecl{}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
			funcs[fn.Name.Name] = fn
		}
	}

	// `<x>.putIsWebMessageEnabled.Call(this, 0)`, reached from holdTheWindow.
	turnedOff := false
	for _, name := range []string{"holdTheWindow", "turnOffWebMessages"} {
		fn := funcs[name]
		if fn == nil {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			method, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || method.Sel.Name != "Call" {
				return true
			}
			slot, ok := method.X.(*ast.SelectorExpr)
			if !ok || slot.Sel.Name != "putIsWebMessageEnabled" {
				return true
			}
			if lit, ok := call.Args[1].(*ast.BasicLit); ok && lit.Kind == token.INT && lit.Value == "0" {
				turnedOff = true
			}
			return true
		})
	}
	if funcs["holdTheWindow"] == nil {
		t.Fatal("nativewindow_windows.go has no holdTheWindow: the Windows window's navigation " +
			"policy and its web-message setting are gone or renamed")
	}
	if !turnedOff {
		t.Error("nativewindow_windows.go never puts 0 into putIsWebMessageEnabled: go-webview2's " +
			"postMessage dispatcher is open to every page")
	}

	// And it happens before the window navigates anywhere, because WebView2
	// applies a settings change from the next navigation on.
	draw := funcs["drawNativeWindow"]
	if draw == nil {
		t.Fatal("nativewindow_windows.go has no drawNativeWindow")
	}
	var held, navigated token.Pos
	ast.Inspect(draw, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			if fn.Name == "holdTheWindow" && held == token.NoPos {
				held = call.Pos()
			}
		case *ast.SelectorExpr:
			if fn.Sel.Name == "Navigate" && navigated == token.NoPos {
				navigated = call.Pos()
			}
		}
		return true
	})
	switch {
	case held == token.NoPos:
		t.Error("drawNativeWindow never calls holdTheWindow: the window has no navigation policy " +
			"and its web messages are on")
	case navigated == token.NoPos:
		t.Error("drawNativeWindow never calls Navigate: this test no longer reads the window it is about")
	case held > navigated:
		t.Errorf("drawNativeWindow calls holdTheWindow at %s, after Navigate at %s: the first page "+
			"would load with web messages on", fset.Position(held), fset.Position(navigated))
	}
}

// forbiddenScriptDoors are WebKit's, in the order they are reported: the longer
// name first, so that `addScriptMessageHandlerWithReply:...` is named as itself
// rather than as the shorter name it contains.
var forbiddenScriptDoors = []string{
	"addScriptMessageHandlerWithReply",
	"addScriptMessageHandler",
	"addUserScript",
	"WKUserScript",
}

// forbiddenWebView2Doors are `go-webview2`'s, at the pinned version: `Bind` on
// the WebView (`webview.go`), `MessageCallback` and `MessageReceived` on
// `edge.Chromium`, and the COM vtable's `AddWebMessageReceived` they sit on
// (`pkg/edge/chromium.go`).
var forbiddenWebView2Doors = []string{"Bind", "MessageCallback", "MessageReceived", "AddWebMessageReceived"}

// scriptDoors returns where file names one of names, in any case, as a string
// literal or inside an identifier.
func scriptDoors(fset *token.FileSet, file *ast.File, names []string) []string {
	var found []string
	match := func(text string) string {
		lower := strings.ToLower(text)
		for _, name := range names {
			if strings.Contains(lower, strings.ToLower(name)) {
				return name
			}
		}
		return ""
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				return true
			}
			text, err := strconv.Unquote(n.Value)
			if err != nil {
				text = n.Value
			}
			if name := match(text); name != "" {
				found = append(found, fset.Position(n.Pos()).String()+" names "+name+" in "+n.Value)
			}
		case *ast.Ident:
			if name := match(n.Name); name != "" {
				found = append(found, fset.Position(n.Pos()).String()+" names "+name+" as "+n.Name)
			}
		}
		return true
	})
	return found
}

// webView2Doors returns where file, if it imports `go-webview2` or one of its
// packages, uses an identifier spelled exactly as one of names.
func webView2Doors(fset *token.FileSet, file *ast.File, names []string) []string {
	imported := false
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err == nil && (path == "github.com/jchv/go-webview2" || strings.HasPrefix(path, "github.com/jchv/go-webview2/")) {
			imported = true
		}
	}
	if !imported {
		return nil
	}
	forbidden := map[string]bool{}
	for _, name := range names {
		forbidden[name] = true
	}
	var found []string
	ast.Inspect(file, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && forbidden[id.Name] {
			found = append(found, fset.Position(id.Pos()).String()+" uses go-webview2's "+id.Name)
		}
		return true
	})
	return found
}

// The guard above passes on the tree as it is, and a guard that has only ever
// passed has not been shown to look. Each door, written the way this package
// would write it, has to be found; and what the guard deliberately lets
// through - the one door there is, a door named in a comment, `Bind` in a file
// that is not WebView2's - has to stay let through, or the guard would be
// refusing the code that states the rule.
func TestTheWebToNativeGuardFindsEachDoor(t *testing.T) {
	for _, tc := range []struct {
		name  string
		src   string
		doors int
	}{
		{
			name:  "a script message handler, by selector",
			src:   `package launch; func f() { webView.Send(sel("addScriptMessageHandler:name:"), h, nsString("kt")) }`,
			doors: 1,
		},
		{
			name:  "a script message handler with a reply, by selector, named as itself",
			src:   `package launch; func f() { c.Send(sel("addScriptMessageHandlerWithReply:contentWorld:name:"), h, w, n) }`,
			doors: 1,
		},
		{
			name:  "a user script, by selector",
			src:   `package launch; func f() { c.Send(sel("addUserScript:"), s) }`,
			doors: 1,
		},
		{
			name:  "a user script, by class, through purego directly",
			src:   `package launch; import "github.com/ebitengine/purego/objc"; var c = objc.GetClass("WKUserScript")`,
			doors: 1,
		},
		{
			name:  "a script message handler, through a typed binding",
			src:   `package launch; func f() { controller.AddScriptMessageHandler(h, "kt") }`,
			doors: 1,
		},
		{
			name:  "a selector held in a raw string",
			src:   "package launch; const s = `addUserScript:`",
			doors: 1,
		},
		{
			name: "a binding on the WebView2 window",
			src: `package launch; import "github.com/jchv/go-webview2"
				func f(v webview2.WebView) { _ = v.Bind("kt", func() {}) }`,
			doors: 1,
		},
		{
			name: "the web-message hook, one package down",
			src: `package launch; import "github.com/jchv/go-webview2/pkg/edge"
				func f(c *edge.Chromium) { c.MessageCallback = func(string) {} }`,
			doors: 1,
		},
		{
			name: "the one door there is",
			src: `package launch; import "github.com/jchv/go-webview2"
				func f(v webview2.WebView) { v.Eval("x"); v.Navigate("u"); w.Send(sel("evaluateJavaScript:completionHandler:"), s, 0) }`,
		},
		{
			name: "a door named in a comment, as nativewindow_darwin.go names it",
			src: `package launch
				// no script message handler, so no addScriptMessageHandler and no WKUserScript
				func f() {}`,
		},
		{
			name: "Bind in a file that does not import go-webview2",
			src:  `package launch; import "syscall"; func f() { _ = syscall.Bind }`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "fixture.go", tc.src, 0)
			if err != nil {
				t.Fatalf("parsing the fixture: %v", err)
			}
			found := append(scriptDoors(fset, file, forbiddenScriptDoors),
				webView2Doors(fset, file, forbiddenWebView2Doors)...)
			if len(found) != tc.doors {
				t.Errorf("found %d doors, want %d: %v", len(found), tc.doors, found)
			}
		})
	}
}
