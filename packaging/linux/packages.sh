#!/usr/bin/env bash
# The Linux .deb and .rpm, around the binary of a release archive.
#
#   packaging/linux/packages.sh build VERSION ARCHIVE OUT
#       ARCHIVE is kubetower_<VERSION>_linux_amd64.tar.gz, as release.yml's
#       archives job makes it; OUT receives kubetower-<VERSION>-amd64.deb and
#       kubetower-<VERSION>-x86_64.rpm, the names the download page asks for.
#       Needs nfpm on PATH (NFPM may name it instead), tar and dpkg-deb.
#
#   packaging/linux/packages.sh try VERSION OUT
#       Installs each package where people will: the .deb with apt-get in a
#       debian:stable container, the .rpm with dnf in a fedora one, and asks
#       the installed command its version, which has to be VERSION. Needs
#       docker.
#
# Called by .github/workflows/linux-packages.yml, and run as it is from a
# developer's tree: the same script, not a copy of it.
#
# **The binary is the archive's, not a second build**: the bytes a person
# downloads in the .tar.gz are the bytes the packages install, with the
# license files beside them in the archive.
#
# Unsigned, amd64 only, and published in no apt or dnf repository: a person
# downloads the file and installs it, and checks it against the release's
# checksum file first.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)

# What a build and a try both hold the version to: a version's shape before it
# becomes part of a file name, as release.yml's verify job does with the tag.
version_ok() {
  if ! [[ "$1" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then
    echo "::error::'$1' is not a version (MAJOR.MINOR.PATCH[-pre])" >&2
    exit 1
  fi
}

# The installed command's answer, held to the shape internal/launch/version.go
# prints: "kubetower <version> (<built>, <revision>)".
says_version() {
  local line=$1 want=$2 where=$3
  case "$line" in
    "kubetower $want ("*) echo "ok   $where: $line" ;;
    *) echo "::error::$where: kubetower --version printed '$line', not version $want" >&2; return 1 ;;
  esac
}

build() {
  local version=$1 archive=$2 out=$3
  version_ok "$version"
  local nfpm=${NFPM:-nfpm}
  for tool in "$nfpm" tar dpkg-deb; do
    command -v "$tool" >/dev/null || { echo "::error::$tool is not on PATH" >&2; exit 1; }
  done
  mkdir -p "$out"
  out=$(cd "$out" && pwd)
  # Global, for the trap: the unpacked archive is removed however this ends.
  stage=$(mktemp -d)
  trap 'rm -rf "$stage"' EXIT

  # Each file by name, so an archive without one fails here and says which.
  tar -xzf "$archive" -C "$stage" ./kubetower ./LICENSE ./LICENSING.md ./THIRD_PARTY_NOTICES.txt
  says_version "$("$stage/kubetower" --version)" "$version" "the archive's binary"

  local deb="$out/kubetower-${version}-amd64.deb"
  local rpm="$out/kubetower-${version}-x86_64.rpm"
  # From the repository root: nfpm.yaml's committed sources are relative to it.
  (
    cd "$root"
    export KT_PKG_VERSION="$version" KT_PKG_STAGE="$stage"
    "$nfpm" package --config packaging/linux/nfpm.yaml --packager deb --target "$deb"
    "$nfpm" package --config packaging/linux/nfpm.yaml --packager rpm --target "$rpm"
  )

  # The .deb read back from the file itself. The .rpm is read back by `try`,
  # with rpm in a fedora container: a Debian or Ubuntu host has no rpm.
  local want_deb=${version/-/\~}
  # Captured whole, then searched: `| grep -q` closes the pipe early, which
  # fails under pipefail.
  local field info
  info=$(dpkg-deb --info "$deb")
  for field in "Package: kubetower" "Version: $want_deb" "Architecture: amd64" "Depends: xdg-utils"; do
    grep -qxF " $field" <<< "$info" || {
      echo "::error::$deb's control file has no '$field':" >&2
      echo "$info" >&2
      exit 1
    }
  done
  local listing path
  listing=$(dpkg-deb --contents "$deb")
  for path in /usr/bin/kubetower /usr/share/applications/kubetower.desktop \
    /usr/share/icons/hicolor/512x512/apps/kubetower.png /usr/share/doc/kubetower/LICENSE \
    /usr/share/doc/kubetower/LICENSING.md \
    /usr/share/doc/kubetower/THIRD_PARTY_NOTICES.txt; do
    grep -qE " \.${path}\$" <<< "$listing" || {
      echo "::error::$deb does not install $path:" >&2
      echo "$listing" >&2
      exit 1
    }
  done
  echo "ok   $(basename "$deb"): version $want_deb, depends on xdg-utils, installs the binary, the launcher, the icon and the license files"
  ls -l "$deb" "$rpm"
}

# The images the packages are tried in. Tags, not digests: a container that only
# installs a package and asks it a question holds nothing and ships nothing, and
# a tag follows the distribution's security updates where a digest would need
# moving by hand. debian:stable is the current Debian; fedora:44 a release still
# supported, named so that a new Fedora is a change somebody reads.
DEBIAN_IMAGE=${DEBIAN_IMAGE:-debian:stable}
FEDORA_IMAGE=${FEDORA_IMAGE:-fedora:44}

try() {
  local version=$1 out=$2
  version_ok "$version"
  command -v docker >/dev/null || { echo "::error::docker is not on PATH" >&2; exit 1; }
  out=$(cd "$out" && pwd)
  local deb="kubetower-${version}-amd64.deb" rpm="kubetower-${version}-x86_64.rpm"
  test -f "$out/$deb" && test -f "$out/$rpm" || {
    echo "::error::$out holds no $deb and $rpm" >&2
    exit 1
  }

  # The package goes in on the container's stdin, not through a bind mount: a
  # mount names a path on the docker daemon's host, and a runner whose daemon
  # does not share its filesystem (a snap docker, a runner in a container)
  # mounts an empty directory there instead of failing.
  #
  # The last line of each script is the installed command's answer; everything
  # before it is printed for the log. The image's dpkg leaves /usr/share/doc
  # out (path-exclude), so the license files are read from the package's own
  # file list, which is what was installed elsewhere.
  local log
  log=$(docker run --rm -i -e DEB="$deb" "$DEBIAN_IMAGE" sh -euc '
    mkdir /pkg && cat > "/pkg/$DEB"
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    apt-get install -y -qq --no-install-recommends "/pkg/$DEB" >/dev/null
    dpkg-query -W -f "\${Package} \${Version} depends: \${Depends}\n" kubetower
    dpkg -L kubetower | grep -E "^/usr/(bin|share/(applications|icons/hicolor/512x512/apps|doc/kubetower))/[^/]+$"
    test -x /usr/bin/kubetower && test -s /usr/share/applications/kubetower.desktop
    test -s /usr/share/icons/hicolor/512x512/apps/kubetower.png
    command -v xdg-open
    dpkg -L kubetower | grep -qx /usr/share/doc/kubetower/THIRD_PARTY_NOTICES.txt
    kubetower --version' < "$out/$deb")
  echo "$log"
  says_version "${log##*$'\n'}" "$version" "$DEBIAN_IMAGE, apt-get install ./$deb"

  log=$(docker run --rm -i -e RPM="$rpm" "$FEDORA_IMAGE" sh -euc '
    mkdir /pkg && cat > "/pkg/$RPM"
    dnf install -y -q "/pkg/$RPM" >/dev/null
    rpm -q --qf "%{NAME} %{VERSION}-%{RELEASE} %{ARCH}\n" kubetower
    rpm -q --requires kubetower | grep -x "xdg-utils"
    rpm -ql kubetower
    test -x /usr/bin/kubetower && test -s /usr/share/applications/kubetower.desktop
    test -s /usr/share/icons/hicolor/512x512/apps/kubetower.png
    test -s /usr/share/licenses/kubetower/LICENSE && test -s /usr/share/licenses/kubetower/LICENSING.md
    test -s /usr/share/licenses/kubetower/THIRD_PARTY_NOTICES.txt
    command -v xdg-open
    kubetower --version' < "$out/$rpm")
  echo "$log"
  says_version "${log##*$'\n'}" "$version" "$FEDORA_IMAGE, dnf install ./$rpm"
}

case "${1:-}" in
  build) [ $# = 4 ] || { echo "usage: $0 build VERSION ARCHIVE OUT" >&2; exit 2; }; build "$2" "$3" "$4" ;;
  try) [ $# = 3 ] || { echo "usage: $0 try VERSION OUT" >&2; exit 2; }; try "$2" "$3" ;;
  *) echo "usage: $0 build VERSION ARCHIVE OUT | try VERSION OUT" >&2; exit 2 ;;
esac
