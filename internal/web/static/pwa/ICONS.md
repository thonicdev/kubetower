# The application icons

Two drawings and three bitmaps. The drawings are the source; the bitmaps are
produced from them and are checked in, for the reason given below.

## What is here

| File | What | SHA-256 |
|---|---|---|
| `icon.svg` | the drawing | `26e54f673e51f8eec8fcbe81555c43e5d66ebc65f208e2b9e3d075f15ede7acc` |
| `icon-maskable.svg` | the same mark with the safe-zone padding Android crops to | `b9d3747895813119bd26eb508be3f710325c7e577e9d98a93d174d0fabce77d9` |
| `icon-192.png` | from `icon.svg` | `f1f762471609718de023f8990535ca9ee346344a563016999482c518de594e52` |
| `icon-512.png` | from `icon.svg` | `621f2474e4ed5f84585941dc74217afc6faeb971c307e5dae13629bc942b0120` |
| `icon-maskable-512.png` | from `icon-maskable.svg` | `209d5ec47e70d4a4073c234895c7c2ad37b2d61fc1e4a0f358d14335440e54c6` |

**The SVG hashes and the PNG hashes were not taken at the same edit.** The
bitmaps were rendered from drawings whose paths, transforms and colors are
byte-identical to the current ones; the drawings have since changed only in
their comments and their `aria-label`, which do not affect the rendering. The PNGs were
deliberately not re-rendered, because that would produce a diff nobody could
check against a drawing that did not change.

## How the bitmaps are made

```bash
docker run --rm -v "$PWD:/w" -w /w alpine:3.24 sh -c \
  'apk add --no-cache rsvg-convert \
   && rsvg-convert -w 192 -h 192 icon.svg          -o icon-192.png \
   && rsvg-convert -w 512 -h 512 icon.svg          -o icon-512.png \
   && rsvg-convert -w 512 -h 512 icon-maskable.svg -o icon-maskable-512.png'
```

The same three commands the `icons` stage of the `Dockerfile` runs, and the same
`rsvg-convert`, so the image and the tree cannot disagree about what the icon
looks like. Run it from **this directory**, and note that it must be a directory
on a local disk: Docker Desktop bind-mounts a directory from an SMB share but
exposes none of its files, so mounting the repository from such a share yields
an empty one.

Update the table above afterwards. `TestTheIconBitmapsFollowTheDrawings` fails
until you do.

## Why they are checked in

The `icons` stage rasterizes the drawings into the source tree immediately before
`go build`, so **only the image would have them** if they were not checked in —
not `go build ./...`, not `go run ./cmd/kubetower`, and not the release script.
Every downloadable binary would then serve a manifest naming three files it does
not carry, and a browser asking for them would get three 404s.

`internal/web/pwamanifest.go` also filters the manifest down to what the build
actually holds — a description that names a missing file is the one failure mode
a manifest has. But that alone would leave the SVG as the only icon offered, and
**the PWA is this project's desktop application**: it is what gives the console a
window and a taskbar icon without an Electron or Tauri shell. An install offer
that may not appear defeats that.

So the bitmaps are here. The cost is a generated artifact beside its source,
free to drift, and that cost is paid by the table above plus a test that
recomputes the **drawings'** hashes from the embedded filesystem. Change
`icon.svg` without re-running the command and the build fails, naming this file:
the check is in the same commit as the thing it checks.
