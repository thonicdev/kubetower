## These builds are not code-signed yet

Signing is coming. Until then Windows and macOS warn before the first run, and
**the warning is expected**. Check what you downloaded against
`checksums.txt` before you run it.

| System | What you will see | What to do |
|---|---|---|
| Windows, SmartScreen | "Windows protected your PC", with the publisher shown as *Unknown publisher* | Click **More info**, then **Run anyway** |
| Windows 11 with **Smart App Control** on | The program is blocked, with **no *Run anyway* and no per-program override** | The zip holds the same unsigned program, so do not count on it either. The console in a cluster is for the team only in this release: its image is private. Turning Smart App Control off works, but on most Windows 11 versions it cannot be turned back on without resetting Windows |
| macOS | "KubeTower" cannot be opened because Apple cannot check it for malicious software | Open **System Settings → Privacy & Security**, click **Open Anyway** beside the message about it, and confirm |
| Linux | Nothing from the archives or the AppImage. `dnf` installing the `.rpm` says it skipped OpenPGP checks, since the package is not signed | Check the package against `checksums.txt` before you install it |

Check a download from the folder holding it and `checksums.txt`, with the
file's name in place of `<file>`:

- macOS: `grep '<file>' checksums.txt | shasum -a 256 --check` prints the file
  name followed by `OK`.
- Linux: `grep '<file>' checksums.txt | sha256sum --check` prints the same.
- Windows, in PowerShell, prints `True`:

  ```powershell
  $file = '<file>'
  $want = ((Select-String -Path checksums.txt -SimpleMatch $file).Line -split '\s+')[0]
  (Get-FileHash $file -Algorithm SHA256).Hash -eq $want
  ```

The README's [Unsigned builds](https://github.com/thonicdev/kubetower#unsigned-builds)
says more, including how to clear the download mark before the first run.
