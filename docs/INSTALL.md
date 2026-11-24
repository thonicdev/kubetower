# Installing KubeTower

The [README](../README.md#install) has the short version. This page has the
details: what each release file holds, the Windows setup from a script, the
Linux packages, updating from the console, how to check a download, what the
desktop listens on, and the one address shape the console can be served at.

> **The Windows and macOS builds are not code-signed yet.** Both systems warn
> before the first run, and that warning is expected; on Windows 11 with Smart
> App Control on, the program is blocked outright. The README's
> [Unsigned builds](../README.md#unsigned-builds) says what you will see and
> what to do, and [Check a download](#check-a-download) below is how you know a
> file is the one published.

---

## What a release carries

| File | What it is |
|---|---|
| `kubetower-<version>-setup.exe` | Windows setup wizard. It installs KubeTower for your user only, with no administrator rights: in a folder you choose (`%LOCALAPPDATA%\Programs\KubeTower` by default), with a Start-menu and a desktop shortcut if you want them, and one *Apps & features* entry that removes it. The uninstaller keeps your settings unless you tick the box that deletes them |
| `kubetower-<version>-win-x64.zip` | Windows, without installing: unzip it and run `kubetower.exe` |
| `kubetower-<version>-arm64.dmg`, `kubetower-<version>-x64.dmg` | macOS disk image, Apple silicon and Intel. Open it and drag `KubeTower.app` onto the `Applications` link beside it |
| `kubetower_<version>_darwin_arm64.tar.gz`, `kubetower_<version>_darwin_amd64.tar.gz` | macOS, Apple silicon and Intel, the binary alone |
| `kubetower-<version>-amd64.deb` | Debian and Ubuntu, x86-64 |
| `kubetower-<version>-x86_64.rpm` | Fedora and RHEL, x86-64 |
| `kubetower-<version>-x86_64.AppImage` | Any Linux, x86-64, without installing |
| `kubetower_<version>_linux_amd64.tar.gz`, `kubetower_<version>_linux_arm64.tar.gz` | Linux, the binary alone |
| `checksums.txt` | The SHA-256 of every file above |
| `checksums.txt.sig` | The Ed25519 signature of `checksums.txt` ([Check a download](#check-a-download)) |

Each archive holds the `kubetower` binary, `LICENSE`, `LICENSING.md` and
`THIRD_PARTY_NOTICES.txt`, which lists the third-party software built into the
binary with each license text. The binary carries the same file:
`kubetower --notices` prints it, and the desktop console serves it at
`/THIRD_PARTY_NOTICES.txt` without signing in. The Windows setup installs
`LICENSE`, `LICENSING.md` and `THIRD_PARTY_NOTICES.txt` beside the program. A
console in a cluster (`--incluster`) serves it only after sign-in, so that an
anonymous visitor does not learn the exact version of every dependency; the
image also holds the file under `/usr/share/licenses/kubetower/`.

---

## Windows

Use the setup if you want KubeTower installed like any other application, the
zip if you want to try it without installing anything. On Windows the setup is
the installer: `kubetower --install` refuses and names it, and
`kubetower --uninstall` starts the uninstaller of the registered install and
returns without waiting for it.

`kubetower.exe --version` typed in a terminal prints its answer after the
prompt has come back: it is a windowed program, and the shell does not wait for
one.

### The Windows setup from a script

- `kubetower-<version>-setup.exe /S` installs silently, and waits until it has.
  `/D=<folder>`, last on the line and unquoted, picks the folder. It exits 0
  on success and **2** when it changed nothing: KubeTower is running, or
  another program such as an antivirus scan holds `kubetower.exe`; or the
  folder already holds files and is not where KubeTower is installed.
- `uninstall.exe /S` in the install folder is what *Apps & features* runs, and
  **it returns at once, with 0, before anything is uninstalled**: it copies
  itself to the temporary folder and carries on from there. A script that needs
  the result runs it in place and waits, then removes what an uninstaller
  cannot remove while it runs - itself and its folder:

  ```powershell
  $dir = "$env:LOCALAPPDATA\Programs\KubeTower"
  $p = Start-Process "$dir\uninstall.exe" -ArgumentList '/S', "_?=$dir" -Wait -PassThru
  $p.ExitCode   # 0 uninstalled, 2 KubeTower was running and nothing changed
  # The folder goes only if it is empty: Delete refuses otherwise, where
  # Remove-Item on a folder with files left in it stops to ask.
  if ($p.ExitCode -eq 0) { Remove-Item "$dir\uninstall.exe"; [IO.Directory]::Delete($dir) }
  ```

- The uninstaller keeps `%USERPROFILE%\.kubetower` unless its box is ticked, and
  silently it is not. It never touches a state directory moved with
  `KT_STATE_DIR`.

---

## macOS

`KubeTower.app` in the disk images is sealed *ad hoc*: its files are hashed
into a signature that names no developer and is checked against no certificate.
That is not code signing, and Gatekeeper treats the app as unsigned; it only
lets macOS see that the bundle is intact, so it asks whether to open it rather
than calling the app damaged.

`kubetower --install`, run from the binary in a `.tar.gz`, installs it for your
user as an application in `~/Applications`; `--uninstall` removes it. That
path has not been tried on a real Mac yet.

---

## Linux

The `.deb` and the `.rpm` install the same binary as the Linux archive, at
`/usr/bin/kubetower`, with a KubeTower entry in the applications menu, its
icon, and the license files (`/usr/share/doc/kubetower/` from the `.deb`,
`/usr/share/licenses/kubetower/` from the `.rpm`); they depend on `xdg-utils`,
which opens the console in your browser. Install them with the package
manager, so that it brings that dependency and can remove KubeTower again:

```bash
sudo apt install ./kubetower-<version>-amd64.deb     # sudo apt remove kubetower
sudo dnf install ./kubetower-<version>-x86_64.rpm    # sudo dnf remove kubetower
```

They are unsigned, x86-64 only, and published in no apt or dnf repository: the
console installs a newer release itself ([Updating](#updating)), or a new
version is a new download. A pre-release such as `0.1.0-rc.1` is versioned
`0.1.0~rc.1` inside the package, so that the release `0.1.0` upgrades it.

The AppImage is the same binary in one file that runs where it is:

```bash
chmod +x kubetower-<version>-x86_64.AppImage
./kubetower-<version>-x86_64.AppImage --app
```

Where FUSE is missing (some containers and minimal systems), add
`--appimage-extract-and-run` before the other arguments. It carries the license
files under `usr/share/doc/kubetower/`, is unsigned and x86-64 only; the
console replaces it with a newer release ([Updating](#updating)).

From the `.tar.gz`, `kubetower --install` installs the binary in
`~/.local/bin` with a desktop entry, and `--uninstall` removes it. That path
has not been tried on a real Linux desktop yet.

---

## Updating

The desktop checks for a newer release at most once a day (`KT_UPDATE_CHECK=off` turns
the check off) and installs it from **Settings**, in two clicks:

1. **Download** fetches the release file for this installation from GitHub over
   HTTPS and keeps it only when `checksums.txt.sig` verifies against the key
   built into the binary and the file's SHA-256 matches its line in
   `checksums.txt`. Nothing unsigned or unmatched is ever installed, and never
   an older release.
2. **Restart to update** replaces KubeTower and starts it again. The terminals
   and port-forwards it ends are counted beside the button.

A badge on Settings (and on the Dock icon on macOS) says a newer release is
there. **Install updates**, in Settings › Infos, turns the download and the
install off; the check stays.

| Installation | How it is replaced |
|---|---|
| Windows setup | The new setup runs silently in the same folder, for your user, with no administrator rights |
| Windows zip | `kubetower.exe` is replaced beside itself |
| macOS `.app` | The new `KubeTower.app` from the disk image takes the old one's place |
| AppImage, `.tar.gz` | The file is replaced where it is |
| `.deb`, `.rpm` | The package manager installs it (`apt-get`, `dnf` or `zypper`, through `pkexec`, which asks for your password) |

Where KubeTower's folder is not writable (`C:\Program Files`, a system folder,
an app run from the disk image or Downloads), Settings says **Not writable**
and **Open folder** shows the verified file, to install by hand. A console in a
cluster or in a container never downloads or installs anything: its image tag
decides its version.

---

## Check a download

First check `checksums.txt` itself. It is signed with the project's Ed25519
key, published as `checksums.txt.sig` beside it. With OpenSSL 3 and
[`internal/releasesig/release-key.pem`](../internal/releasesig/release-key.pem)
from the repository:

```bash
openssl pkeyutl -verify -rawin -pubin -inkey release-key.pem \
  -in checksums.txt -sigfile checksums.txt.sig
```

It prints `Signature Verified Successfully`.

Then compare a file with its line in `checksums.txt`, from the directory you
downloaded both to, with the file's name in place of `<file>`.

Linux:

```bash
grep '<file>' checksums.txt | sha256sum --check
```

macOS:

```bash
grep '<file>' checksums.txt | shasum -a 256 --check
```

Both print the file name followed by `OK`.

Windows, in PowerShell:

```powershell
$file = '<file>'
$want = ((Select-String -Path checksums.txt -SimpleMatch $file).Line -split '\s+')[0]
(Get-FileHash $file -Algorithm SHA256).Hash -eq $want
```

It prints `True` when the file is the one published.

---

## What the desktop listens on

Run the binary and the console listens on `http://127.0.0.1:5823`, on loopback
only, and opens its window; open that address in a browser if no window opens.
There is no sign-in page on the loopback desktop: the window is handed a
one-time code, and a second launch (or `kubetower open`) opens a window on the
console already running.

Everything is configured with environment variables prefixed `KT_`, and
[`.env.example`](../.env.example) is the list. Two are worth knowing first:

| Variable | What | Default |
|---|---|---|
| `KT_LISTEN` | Address to listen on | loopback; the image sets `:5823` because a container is the boundary and Docker cannot route a published port to the container's own loopback |
| `KT_STATE_DIR` | The only directory this process writes | `~/.kubetower`; the image sets `/var/lib/kubetower` |

It never writes to your kubeconfig.

**The desktop answers only `localhost` and loopback addresses as its Host**,
in the image too: open a published container as `http://localhost:5823`. A
request naming the machine's LAN name or address is answered 421, which is what
refuses a DNS-rebound page. A console meant for other machines is the server
profile, `--incluster`. The probes - `/healthz`, `/readyz` and `/kt-healthz` -
answer any address, so a healthcheck or a load balancer can reach them by IP,
but off loopback they carry their status code and no reason: ask from this
machine to read why a probe is failing. The check refuses a browser, not a
client: anything that is not a browser can send `Host: localhost` and read the
full reason - and the whole console - so what keeps other machines off a
published container is the address its port is published on
(`-p 127.0.0.1:5823:5823`).

---

## Served at the root of its host only

The console is served at the root path of its host, whatever the profile:
`https://kubetower.example.com/`, never `https://example.com/kubetower/`. Every
address the page uses is absolute - its scripts under `/assets/`, its API under
`/api/`, its pages from `/` - so behind an ingress or a reverse proxy that
mounts it under a path prefix the page loads none of them and stays blank, with
no error from the console. In a cluster, give it a host of its own and leave the
chart's ingress path at `/`. Serving it under a prefix is not supported.
