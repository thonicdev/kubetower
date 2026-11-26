# go-webview2, patched for KubeTower

A copy of github.com/jchv/go-webview2 at v0.0.0-20260205173254-56598839c808
(MIT, see LICENSE), used through a `replace` in KubeTower's go.mod (#84).

Changed: no failure of the WebView2 runtime ends the process any more.

- `pkg/edge/chromium.go`: a failed environment or controller creation is
  recorded instead of `log.Fatalf`; `Embed` returns false and `Error()` says
  why. `Eval`, `WebResourceRequested` and `AddWebResourceRequestedFilter` log
  instead of `log.Fatal`. `Embed`'s loop posts WM_QUIT again when it takes
  it, so the caller's loop still ends.
- `pkg/edge`: an HRESULT is tested as the signed 32-bit value it is
  (`hresultFailed`, `int32(res) < 0`): upstream's `int64(res) < 0` on a
  zero-extended `uintptr` never saw a failure. An environment, controller or
  web view that comes back nil with a success code (a session with no
  desktop) is a failure too, not a nil dereference.
- `webview.go`: a failed `Embed`, or a settings read or write that fails,
  destroys the window at once (`discard`, without posting WM_QUIT) and
  `NewWithOptions` returns nil.
- `cmd/` (the demo) and `.github/` (upstream's own CI) are left out, and every
  file is gofmt'd (old `// +build` lines gain `//go:build`), as KubeTower's CI
  asks of the whole tree.

Remove this copy, and the `replace` lines in `go.mod` and `boundary/go.mod`,
once jchv/go-webview2#87, or a change that does the same, is merged and
released; check the two points above against it first.
