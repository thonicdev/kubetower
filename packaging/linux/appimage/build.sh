#!/usr/bin/env bash
# Builds kubetower-<version>-x86_64.AppImage around the binary of a release's
# linux_amd64 archive, and reads the result back. appimage.yml runs it on a tag
# and on ci.yml's full tier; run by hand, it is the same build:
#
#   APPIMAGETOOL_VERSION=… APPIMAGETOOL_SHA256=… \
#   APPIMAGE_RUNTIME_VERSION=… APPIMAGE_RUNTIME_SHA256=… \
#     packaging/linux/appimage/build.sh kubetower_<v>_linux_amd64.tar.gz <v> <out-dir>
#
# The pins live in appimage.yml, where a reviewer sees them move; this script
# refuses to run without them.
#
# NOT A SECOND BUILD. The binary is the archive's, byte for byte, so the
# AppImage runs exactly what the tar.gz beside it on the release runs. The
# archive also gives the three license files, which go to
# usr/share/doc/kubetower/ as the archive carries them.
#
# THE TOOL is appimagetool, pinned by version and by the SHA-256 of its release
# asset, never the "continuous" release, whose file changes under one name.
# appimagetool is itself an AppImage, and run without --runtime-file it
# downloads the type-2 runtime it prepends from that same moving release; so
# the runtime is pinned and checked the same way and handed over by path.
# Both run with APPIMAGE_EXTRACT_AND_RUN=1, so no FUSE is needed on the host,
# and the result is read back with --appimage-extract-and-run for the same
# reason.
#
# NO UPDATE INFORMATION, NO ZSYNC, NO SIGNATURE: no -u, -g or -s is passed, and
# the read-back refuses an image that carries update information anyway.
#
# WHAT IS IN THE APPDIR, and nothing else:
#
#   AppRun                          packaging/linux/appimage/AppRun
#   kubetower.desktop               packaging/linux/kubetower.desktop, the one
#                                   desktop entry the Linux packages share
#   kubetower.png, .DirIcon         internal/web/static/pwa/icon-512.png, the
#                                   icon the console itself serves
#   usr/bin/kubetower               the archive's binary
#   usr/share/doc/kubetower/        LICENSE, LICENSING.md,
#                                   THIRD_PARTY_NOTICES.txt from the archive

set -euo pipefail

usage() {
  echo "usage: $0 <kubetower_<version>_linux_amd64.tar.gz> <version> <out-dir>" >&2
  exit 2
}
[ $# -eq 3 ] || usage
archive=$1 version=$2 outdir=$3

for v in APPIMAGETOOL_VERSION APPIMAGETOOL_SHA256 APPIMAGE_RUNTIME_VERSION APPIMAGE_RUNTIME_SHA256; do
  if [ -z "${!v:-}" ]; then
    echo "error: $v is not set; appimage.yml holds the pins" >&2
    exit 2
  fi
done
for v in APPIMAGETOOL_SHA256 APPIMAGE_RUNTIME_SHA256; do
  if ! [[ "${!v}" =~ ^[0-9a-f]{64}$ ]]; then
    echo "error: $v is '${!v}', not a SHA-256" >&2
    exit 2
  fi
done
if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then
  echo "error: '$version' is not a version (MAJOR.MINOR.PATCH[-pre])" >&2
  exit 2
fi
case "$(uname -m)" in
  x86_64) ;;
  *) echo "error: this builds and runs an x86_64 AppImage, and this host is $(uname -m)" >&2; exit 1 ;;
esac

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
archive=$(cd "$(dirname "$archive")" && pwd)/$(basename "$archive")
mkdir -p "$outdir"
outdir=$(cd "$outdir" && pwd)
image="$outdir/kubetower-${version}-x86_64.AppImage"

work=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/appimage.XXXXXX")
trap 'rm -rf "$work"' EXIT
# The type-2 runtime's extract-and-run leaves its extraction behind, under
# $TMPDIR, keyed by the image's digest. Every AppImage this script runs -
# appimagetool and the result - extracts under the work directory, so nothing
# is left on a runner that persists.
export TMPDIR="$work/tmp"
mkdir -p "$TMPDIR"

# --- The pinned tool and runtime, checked before either is run --------------
tools="$work/tools"
mkdir -p "$tools"
curl -fsSL -o "$tools/appimagetool" \
  "https://github.com/AppImage/appimagetool/releases/download/${APPIMAGETOOL_VERSION}/appimagetool-x86_64.AppImage"
echo "$APPIMAGETOOL_SHA256  $tools/appimagetool" | sha256sum --check --strict
curl -fsSL -o "$tools/runtime" \
  "https://github.com/AppImage/type2-runtime/releases/download/${APPIMAGE_RUNTIME_VERSION}/runtime-x86_64"
echo "$APPIMAGE_RUNTIME_SHA256  $tools/runtime" | sha256sum --check --strict
chmod 755 "$tools/appimagetool"
echo "appimagetool: $(APPIMAGE_EXTRACT_AND_RUN=1 "$tools/appimagetool" --version 2>&1 | head -1)"

# --- The archive ------------------------------------------------------------
mkdir -p "$work/archive"
tar -xzf "$archive" -C "$work/archive"
for f in kubetower LICENSE LICENSING.md THIRD_PARTY_NOTICES.txt; do
  if [ ! -s "$work/archive/$f" ]; then
    echo "error: $(basename "$archive") carries no $f" >&2
    exit 1
  fi
done
kind=$(file -b "$work/archive/kubetower")
case "$kind" in
  "ELF 64-bit LSB executable, x86-64"*) echo "ok   the archive's binary: $kind" ;;
  *) echo "error: the archive's kubetower is '$kind', not an x86-64 ELF executable" >&2; exit 1 ;;
esac

# --- The AppDir -------------------------------------------------------------
app="$work/KubeTower.AppDir"
mkdir -p "$app/usr/bin" "$app/usr/share/doc/kubetower"
install -m 755 "$here/AppRun" "$app/AppRun"
install -m 644 "$root/packaging/linux/kubetower.desktop" "$app/kubetower.desktop"
install -m 644 "$root/internal/web/static/pwa/icon-512.png" "$app/kubetower.png"
ln -s kubetower.png "$app/.DirIcon"
install -m 755 "$work/archive/kubetower" "$app/usr/bin/kubetower"
for f in LICENSE LICENSING.md THIRD_PARTY_NOTICES.txt; do
  install -m 644 "$work/archive/$f" "$app/usr/share/doc/kubetower/$f"
done

# --- Pack -------------------------------------------------------------------
# -n: no AppStream metadata is shipped, so there is nothing to check. ARCH is
# named rather than guessed from the binaries. The destination is named, so
# appimagetool does not derive a file name from the environment.
rm -f "$image"
ARCH=x86_64 APPIMAGE_EXTRACT_AND_RUN=1 "$tools/appimagetool" -n \
  --runtime-file "$tools/runtime" "$app" "$image"
chmod 755 "$image"

# --- Read back, from the image itself ---------------------------------------
# (The runtime is not compared byte for byte with the image's head:
# appimagetool writes the image's MD5 digest into the runtime's own sections.)
update=$("$image" --appimage-updateinformation 2>&1)
if [ -n "$update" ]; then
  echo "error: $image carries update information: '$update'" >&2
  exit 1
fi
echo "ok   no update information"

# Unpacked by the image's own runtime, entry by entry.
(cd "$work" && "$image" --appimage-extract >/dev/null)
x="$work/squashfs-root"
for f in AppRun kubetower.desktop kubetower.png .DirIcon usr/bin/kubetower \
  usr/share/doc/kubetower/LICENSE usr/share/doc/kubetower/LICENSING.md \
  usr/share/doc/kubetower/THIRD_PARTY_NOTICES.txt; do
  if [ ! -e "$x/$f" ]; then
    echo "error: $image holds no $f" >&2
    exit 1
  fi
done
if ! cmp -s "$x/usr/bin/kubetower" "$work/archive/kubetower"; then
  echo "error: the image's usr/bin/kubetower is not the archive's binary" >&2
  exit 1
fi
echo "ok   AppRun, the desktop entry, the icon, the archive's binary and its license files"

# And run, as a person without FUSE would run it.
got=$("$image" --appimage-extract-and-run --version 2>&1)
echo "$got"
if ! grep -qF "$version" <<< "$got"; then
  echo "error: $image --version printed '$got', which does not name $version" >&2
  exit 1
fi
echo "ok   $(basename "$image") --version names $version"
