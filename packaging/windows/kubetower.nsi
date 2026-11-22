; KubeTower's Windows setup wizard.
;
; It wraps the same kubetower.exe the Windows zip carries, and adds nothing to
; it: a license page, an install folder, two shortcuts, one uninstaller and one
; Apps & features entry. internal/launch/installsetup.go records why this is
; NSIS, what it replaced, and the operator's answers it follows.
;
; HOW IT IS BUILT. Through `go run ./cmd/ktbuild -os windows -arch amd64
; -setup`, never by hand: ktbuild runs makensis with `-NOCD`, from a staging
; folder that holds kubetower.exe, LICENSE, LICENSING.md and, in a release,
; THIRD_PARTY_NOTICES.txt - so every `File` below is relative to that folder -
; and passes the defines this script refuses to build without. Then it reads
; the result back: a PE file whose version resource says VERSION.
;
; Defines:
;   VERSION       the release version without the v, or `dev`
;   VIVERSION     the same as four numbers, which a version resource requires
;   OUTFILE       where the setup is written
;   ICON          KubeTower's icon (internal/launch/winres/icon.ico)
;   NOTICES       defined when the staging folder holds THIRD_PARTY_NOTICES.txt
;   LICENSE_FILE  the license page, LICENSE by default. A build that carries
;                 other parts than the core passes the file that describes
;                 what that binary carries. The extras are part of the core, so
;                 the default is the whole binary's.
;
; PER USER, NEVER FOR THE MACHINE. No elevation, nothing under HKLM and nothing
; in Program Files: this process holds administrator credentials for
; production clusters (install_windows.go says the rest), and a colleague can
; install it without an administrator. That argument holds for the default
; folder, which only its account can write. A folder chosen elsewhere - say
; C:\Tools\KubeTower - inherits that place's permissions, which on a drive's
; root let every signed-in account modify it.
;
; THE FOLDER IS NOT TRUSTED WHOLE. The install writes, and the uninstaller
; deletes, a fixed list of names and nothing else; `$INSTDIR` is never removed
; recursively, and a folder that already holds files and is not KubeTower's
; registered install is refused unless the person says otherwise (checkFolder).
; internal/launch's install_test.go holds these rules: no recursive removal
; but the state folder's, no wildcard delete, every prompt answered under /S.

Unicode true
ManifestDPIAware true
SetCompressor /SOLID lzma
RequestExecutionLevel user

!ifndef VERSION
  !error "VERSION is not defined: build this through `go run ./cmd/ktbuild -setup`"
!endif
!ifndef VIVERSION
  !error "VIVERSION is not defined: build this through `go run ./cmd/ktbuild -setup`"
!endif
!ifndef OUTFILE
  !error "OUTFILE is not defined: build this through `go run ./cmd/ktbuild -setup`"
!endif
!ifndef ICON
  !error "ICON is not defined: build this through `go run ./cmd/ktbuild -setup`"
!endif
!ifndef LICENSE_FILE
  !define LICENSE_FILE "LICENSE"
!endif

!define EXE "kubetower.exe"
!define UNINSTALLER "uninstall.exe"
; The key the binary's own `--install` wrote before this wizard existed. The
; same key, so an upgrade over that install replaces its Apps & features row
; instead of adding a second one. internal/launch holds the same string
; (installKey), and a test there holds the two together.
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\KubeTower"
; The same path as the shortcut `--install` wrote, so a shortcut somebody
; pinned from the Start menu keeps working across the upgrade.
!define START_MENU_LINK "$SMPROGRAMS\KubeTower.lnk"
!define DESKTOP_LINK "$DESKTOP\KubeTower.lnk"
; statefile.Dir() without KT_STATE_DIR: the password hash, the cluster names
; and colors, the layouts and the event archive.
!define STATE_DIR "$PROFILE\.kubetower"

Name "KubeTower"
OutFile "${OUTFILE}"
; The default is where Windows itself puts per-user installs. An existing
; install's folder wins over it, and `/D=` on the command line over both.
InstallDir "$LOCALAPPDATA\Programs\KubeTower"
InstallDirRegKey HKCU "${UNINSTALL_KEY}" "InstallLocation"
BrandingText "KubeTower ${VERSION}"

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "Sections.nsh"
!include "FileFunc.nsh"

!define MUI_ICON "${ICON}"
!define MUI_UNICON "${ICON}"

; The folder checkFolder last accepted, so the directory page and the install
; section do not ask twice about the same one.
Var FolderChecked
!define MUI_ABORTWARNING
!define MUI_UNABORTWARNING

; ---------------------------------------------------------------------------
; Pages
; ---------------------------------------------------------------------------

!define MUI_WELCOMEPAGE_TEXT "This installs KubeTower ${VERSION} for your account only. It needs no administrator rights.$\r$\n$\r$\nClick Next to continue."
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "${LICENSE_FILE}"
!define MUI_PAGE_CUSTOMFUNCTION_LEAVE checkFolder
!insertmacro MUI_PAGE_DIRECTORY
!define MUI_COMPONENTSPAGE_TEXT_TOP "Choose the shortcuts to create. Both open the KubeTower window."
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_INSTFILES
; One short line, and not the wall of text the binary's `--install` used to put
; in a message box: the window opens signed in, on this machine only.
!define MUI_FINISHPAGE_TEXT "KubeTower is installed. It opens already signed in, on this machine only."
!define MUI_FINISHPAGE_RUN "$INSTDIR\${EXE}"
!define MUI_FINISHPAGE_RUN_PARAMETERS "--app"
!define MUI_FINISHPAGE_RUN_TEXT "Launch KubeTower"
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!define MUI_COMPONENTSPAGE_TEXT_TOP "KubeTower is removed. Your settings are kept unless you tick the box below."
!insertmacro MUI_UNPAGE_COMPONENTS
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

; The setup's own version resource: Explorer's Properties dialog shows it, and
; ktbuild reads VERSION back out of it.
VIProductVersion "${VIVERSION}"
VIFileVersion "${VIVERSION}"
VIAddVersionKey /LANG=${LANG_ENGLISH} "ProductName" "KubeTower"
VIAddVersionKey /LANG=${LANG_ENGLISH} "ProductVersion" "${VERSION}"
VIAddVersionKey /LANG=${LANG_ENGLISH} "FileVersion" "${VERSION}"
VIAddVersionKey /LANG=${LANG_ENGLISH} "FileDescription" "KubeTower Setup"
VIAddVersionKey /LANG=${LANG_ENGLISH} "LegalCopyright" "GNU AGPL-3.0-only"

; ---------------------------------------------------------------------------
; KubeTower running
; ---------------------------------------------------------------------------

; The running executable cannot be replaced or deleted, so it is deleted first
; and the person is asked to close KubeTower when that fails, rather than the
; install failing half-way. The folder is $R9, so an upgrade that moves can
; ask about the old folder too. Silent (`/S`), the answer is Cancel: the
; install stops with exit code 2 and nothing changed, and nothing waits for a
; click.
;
; A delete that fails is the whole test, so something else holding the file -
; an antivirus scan, an indexer - reads as KubeTower running. The prompt says
; so, and Retry once it has let go is the answer.
!macro CLOSE_RUNNING UN
  Function ${UN}closeRunning
    retry:
    ClearErrors
    Delete "$R9\${EXE}"
    ${If} ${Errors}
      MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "$R9\${EXE} cannot be replaced: KubeTower is running, or another program (an antivirus scan, for instance) holds the file.$\r$\n$\r$\nClose KubeTower, then click Retry." /SD IDCANCEL IDRETRY retry
      Abort "KubeTower is still running. Nothing was changed."
    ${EndIf}
  FunctionEnd
!macroend
!insertmacro CLOSE_RUNNING ""
!insertmacro CLOSE_RUNNING "un."

; ---------------------------------------------------------------------------
; The install folder
; ---------------------------------------------------------------------------

; A folder is accepted when it is KubeTower's registered install (an upgrade),
; or does not exist yet, or is empty. Anything else - Downloads, a profile, a
; tools folder typed by hand or passed with /D= - is somebody's, and the files
; named like KubeTower's in it would be overwritten now and deleted on
; uninstall. So the person is asked, with No as the default; silent, the
; answer is No and the install stops with exit code 2.
Function checkFolder
  ${If} $FolderChecked == $INSTDIR
    Return
  ${EndIf}
  ReadRegStr $0 HKCU "${UNINSTALL_KEY}" "InstallLocation"
  ${If} $0 != ""
  ${AndIf} $0 == $INSTDIR
    StrCpy $FolderChecked $INSTDIR
    Return
  ${EndIf}
  StrCpy $1 "empty"
  ClearErrors
  FindFirst $2 $3 "$INSTDIR\*"
  ${DoUntil} ${Errors}
    ${If} $3 != "."
    ${AndIf} $3 != ".."
      StrCpy $1 "used"
      ${Break}
    ${EndIf}
    FindNext $2 $3
  ${Loop}
  FindClose $2
  ${If} $1 == "empty"
    StrCpy $FolderChecked $INSTDIR
    Return
  ${EndIf}
  MessageBox MB_YESNO|MB_ICONEXCLAMATION|MB_DEFBUTTON2 "$INSTDIR already holds files, and it is not where KubeTower is installed.$\r$\n$\r$\nInstall there anyway? Files in it named like KubeTower's (kubetower.exe, uninstall.exe, LICENSE, LICENSING.md, THIRD_PARTY_NOTICES.txt) would be replaced, and removed on uninstall. A folder of its own keeps them apart." /SD IDNO IDYES accept
  Abort
  accept:
  StrCpy $FolderChecked $INSTDIR
FunctionEnd

; ---------------------------------------------------------------------------
; Install
; ---------------------------------------------------------------------------

Section "KubeTower" SecApp
  SectionIn RO
  ; Again here, for /S, which shows no directory page.
  Call checkFolder
  StrCpy $R9 $INSTDIR
  Call closeRunning

  ; An upgrade that moves (/D=, or another folder chosen): the registered
  ; install is taken out of the old folder, by the same names the uninstaller
  ; uses, so it is neither orphaned without an uninstaller nor left running.
  ; The Apps & features row and the shortcuts are rewritten below.
  ReadRegStr $0 HKCU "${UNINSTALL_KEY}" "InstallLocation"
  ${If} $0 != ""
  ${AndIf} $0 != $INSTDIR
  ${AndIf} ${FileExists} "$0\${EXE}"
    StrCpy $R9 $0
    Call closeRunning
    Delete "$0\${EXE}.old"
    ; LICENSE and the rest were installed only by a setup, which leaves its
    ; uninstaller beside them; `--install` copied the executable alone.
    ${If} ${FileExists} "$0\${UNINSTALLER}"
      Delete "$0\LICENSE"
      Delete "$0\LICENSING.md"
      Delete "$0\THIRD_PARTY_NOTICES.txt"
      Delete "$0\${UNINSTALLER}"
    ${EndIf}
    RMDir "$0"
  ${EndIf}

  SetOutPath "$INSTDIR"
  ; What the binary's own install left when it replaced a running copy.
  Delete "$INSTDIR\${EXE}.old"

  File "${EXE}"
  File "LICENSE"
  File "LICENSING.md"
  !ifdef NOTICES
    File "THIRD_PARTY_NOTICES.txt"
  !endif
  WriteUninstaller "$INSTDIR\${UNINSTALLER}"

  ; One Apps & features row, under the current user. Every value is written
  ; again on an upgrade, so the row a `--install` made now points at this
  ; uninstaller.
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayName" "KubeTower"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "Publisher" "KubeTower"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayIcon" "$INSTDIR\${EXE}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "UninstallString" '"$INSTDIR\${UNINSTALLER}"'
  ; What Apps & features and winget run to uninstall silently, as they expect
  ; it. **It returns before anything is uninstalled**: an NSIS uninstaller
  ; started without `_?=` copies itself to the temporary folder, starts the
  ; copy and exits 0, so it can delete itself. A script that needs the
  ; uninstall's own exit code runs `"<folder>\uninstall.exe" /S _?=<folder>`
  ; and waits on that - the uninstaller then cannot delete itself, so the
  ; script removes uninstall.exe and the folder afterwards (README, Install).
  WriteRegStr HKCU "${UNINSTALL_KEY}" "QuietUninstallString" '"$INSTDIR\${UNINSTALLER}" /S'
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoRepair" 1
  ; The files this section installs, as the compiler measured them - not a
  ; walk of the folder, which would count whatever else is in it.
  SectionGetSize ${SecApp} $0
  IntFmt $0 "0x%08X" $0
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "EstimatedSize" "$0"
SectionEnd

; `--app`, as the Start-menu shortcut has always run it: a bare launch from a
; shortcut would infer the window, and a shortcut says so instead.
Section "Start-menu shortcut" SecStartMenu
  CreateShortcut "${START_MENU_LINK}" "$INSTDIR\${EXE}" "--app" "$INSTDIR\${EXE}" 0 SW_SHOWNORMAL "" "A Kubernetes console that runs on your own machine"
SectionEnd

Section "Desktop shortcut" SecDesktop
  CreateShortcut "${DESKTOP_LINK}" "$INSTDIR\${EXE}" "--app" "$INSTDIR\${EXE}" 0 SW_SHOWNORMAL "" "A Kubernetes console that runs on your own machine"
SectionEnd

; A shortcut left unticked on an upgrade is one the person does not want, so an
; earlier one is removed rather than left pointing at the new install.
Section "-shortcuts not asked for"
  ${IfNot} ${SectionIsSelected} ${SecStartMenu}
    Delete "${START_MENU_LINK}"
  ${EndIf}
  ${IfNot} ${SectionIsSelected} ${SecDesktop}
    Delete "${DESKTOP_LINK}"
  ${EndIf}
SectionEnd

; ---------------------------------------------------------------------------
; Uninstall
; ---------------------------------------------------------------------------

; NSIS runs this from a copy in the temporary folder, which is what lets it
; delete kubetower.exe and itself: the binary's own `--uninstall` could only
; apologize that Windows never deletes a running program.
Section "un.KubeTower" UnSecApp
  SectionIn RO
  StrCpy $R9 $INSTDIR
  Call un.closeRunning
  ; The names the install wrote, and only those. The notices file only where
  ; this build installed one: a development build carries none, and a file of
  ; that name in the folder is then not this install's.
  Delete "$INSTDIR\${EXE}.old"
  Delete "$INSTDIR\LICENSE"
  Delete "$INSTDIR\LICENSING.md"
  !ifdef NOTICES
    Delete "$INSTDIR\THIRD_PARTY_NOTICES.txt"
  !endif
  Delete "$INSTDIR\${UNINSTALLER}"
  ; Only when empty: anything else in the folder is the person's.
  RMDir "$INSTDIR"
  Delete "${START_MENU_LINK}"
  Delete "${DESKTOP_LINK}"
  DeleteRegKey HKCU "${UNINSTALL_KEY}"
SectionEnd

; Unticked by default. The state directory is the one thing here that cannot
; be regenerated - the event archive above all - and a reinstall next week
; picks all of it up again.
Section /o "un.Also delete my KubeTower settings" UnSecState
  RMDir /r "${STATE_DIR}"
SectionEnd

; ---------------------------------------------------------------------------
; What each checkbox does, under the list
; ---------------------------------------------------------------------------

!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${SecApp} "The program, its uninstaller and its Apps & features entry."
  !insertmacro MUI_DESCRIPTION_TEXT ${SecStartMenu} "KubeTower in the Start menu."
  !insertmacro MUI_DESCRIPTION_TEXT ${SecDesktop} "KubeTower on the desktop."
!insertmacro MUI_FUNCTION_DESCRIPTION_END

!insertmacro MUI_UNFUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${UnSecApp} "The program, its shortcuts and its Apps & features entry."
  !insertmacro MUI_DESCRIPTION_TEXT ${UnSecState} "Deletes ${STATE_DIR}: the password hash, the cluster names and colors, the layouts and the event archive. Left alone, reinstalling picks all of it up again."
!insertmacro MUI_UNFUNCTION_DESCRIPTION_END
