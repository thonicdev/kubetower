# The Windows resources compiled into the binary

`rsrc_windows_amd64.syso` is picked up automatically by `go build` on
windows/amd64 and linked into `kubetower.exe`. It carries three things:

| Resource | What it does |
|---|---|
| `RT_GROUP_ICON` **#1** | the logo, on the file in Explorer, on the taskbar button, in Alt-Tab, and on the native window — `nativewindow_windows.go` asks for `IconId: 1`, so the numeric ID is load-bearing and **not** a name like `APP` |
| `RT_MANIFEST` | `per monitor v2` DPI awareness, so the window is sharp on a scaled display instead of bitmap-stretched, and `as invoker`, so Windows never offers to elevate a process holding cluster credentials |
| `RT_VERSION` | what the Properties dialog shows: KubeTower, and the version — `dev` as committed, the tag's version in a release build |

**It lives beside the window code in `internal/launch`, not in `cmd/kubetower`.**
Go links a package's `.syso` into every binary that imports the package, so
every `main` that starts the console through `app.Run` — `cmd/kubetower`, and any
other built on it — gets the icon the window asks for and the manifest,
without a second copy that could drift. A `main` must not carry a `.syso` of
its own beside it.

## The version, and regenerating

**The committed object says `dev`**, which is what `--version` says of a build
nobody stamped. A release's version is never committed: `cmd/ktbuild -version X`
regenerates the object from the tag before it links windows/amd64, reads `X`
back out of the `.exe` it produced, and puts the committed object back, so
Properties and `--version` always name the same version.
`winres_test.go` holds the committed object and `winres.json` to `dev`.

After changing the icon or the manifest:

```bash
cd internal/launch
go run github.com/tc-hib/go-winres@v0.3.3 make --in winres/winres.json \
    --arch amd64 --product-version dev --file-version dev
```

`go run pkg@version` deliberately, and pinned: it is a build-time tool, so it
leaves nothing in `go.mod`; the webview binding and its loader there are
runtime imports.

`icon.ico` here is the same drawing as the PWA's, wrapped around the 256px
bitmap `internal/web/static/pwa/ICONS.md` records. One drawing, one source of
truth: if the mark changes, regenerate both from `icon.svg` and the test in
`internal/web/pwaicons_test.go` will tell you if you forgot the PWA half.

## Why the .syso is checked in

The same reason the PWA bitmaps are, and the same objection applies: it is a
generated artifact free to drift from its source. It is here because the
alternative is a build that needs the network and a tool download to produce a
binary with an icon, and an unstamped `task build:api` must work offline. The
guard is that the icon it wraps is the one hashed in `ICONS.md`, so the drawing
cannot move without that test failing.

**It is amd64-only.** Windows arm64 is not built today — `release.yml`'s header
says why — and adding it means a second `--arch` run and a second `.syso`.
