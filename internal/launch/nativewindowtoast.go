package launch

import (
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// The Windows window's toasts, as far as they are decisions: the same two
// events as macOS, composed by the same code (windowNotices.take, updateNotice,
// lostNotice). No build tag, so they are tested on Linux; the Windows file
// stamps the shortcut, sets the AppUserModelID, shows what take returns and
// hands a click to the kubetower:// validation.
//
// Windows shows a toast from an unpackaged application only when a Start-menu
// shortcut carries its AppUserModelID and the process says the same ID. NSIS's
// CreateShortcut cannot set it, so the window stamps the setup's KubeTower.lnk
// at start, if it points at this very executable. A copy run from the zip has
// no such shortcut: it sets no ID and sends no toasts, and the log says so.
//
// A taskbar pin is a copy of the shortcut made before the stamp, and Windows
// groups a window with a pinned button by ID, so a mismatch gives a second
// button. The pinned copy is stamped too, under the same condition.

// appUserModelID is the Windows AppUserModelID: the macOS bundle identifier,
// already in the `Company.Product` shape, under 128 characters, with no space.
const appUserModelID = "dev.thonicdev.kubetower"

// shortcutName is the file the setup writes in the Programs folder
// (`START_MENU_LINK` in `kubetower.nsi`), and what a pin to the taskbar copies.
const shortcutName = "KubeTower.lnk"

// shortcutVerdict decides what to do with one shortcut, from what was read of
// it: whether it exists, the executable it starts, the AppUserModelID it
// carries ("" for none), and this process's own executable.
//
//   - stamp: write `appUserModelID` into it;
//   - ours: it is this executable's, so its ID is the one toasts are
//     attributed to (after the stamp, when there is one);
//   - line: what the log says when it is not ours, "" when it is.
//
// A shortcut that starts another executable is never written: an installed
// copy and a zip run from Downloads would otherwise take the shortcut from each
// other on every launch.
func shortcutVerdict(found bool, target, aumid, exe string) (stamp, ours bool, line string) {
	switch {
	case !found:
		return false, false, "there is no KubeTower shortcut in the Start menu, so Windows has nothing to " +
			"attribute a notification to and this copy sends none: a lost port-forward or a newer " +
			"release is said in this log. The setup (kubetower-<version>-setup.exe) writes the shortcut"
	case !sameWindowsPath(target, exe):
		return false, false, "the Start-menu shortcut starts another copy of KubeTower, so this one leaves " +
			"it alone and sends no notifications: a lost port-forward or a newer release is said in this log"
	}
	return aumid != appUserModelID, true, ""
}

// sameWindowsPath compares two Windows paths as Windows does: without regard
// to case, with either slash, and cleaned. Written for any GOOS, so it is
// tested on Linux.
func sameWindowsPath(a, b string) bool {
	norm := func(p string) string {
		p = strings.TrimSpace(strings.ReplaceAll(p, `\`, "/"))
		if p == "" {
			return ""
		}
		return filepath.ToSlash(filepath.Clean(filepath.FromSlash(p)))
	}
	na, nb := norm(a), norm(b)
	return na != "" && strings.EqualFold(na, nb)
}

// toastXML is one notice as toast XML: a title and a body in the generic
// template, the click route in `launch`, and no sound, as on macOS (a console
// making noise on a shared desk).
//
// The text is escaped, never trusted: a context name may hold a `<`. `launch`
// comes back on a click as the toast's Arguments and goes through
// consolePathFromURL like any link.
func toastXML(n notice) string {
	return `<toast launch="` + xmlText(n.Link) + `"><visual><binding template="ToastGeneric">` +
		`<text>` + xmlText(n.Title) + `</text><text>` + xmlText(n.Body) + `</text>` +
		`</binding></visual><audio silent="true"/></toast>`
}

func xmlText(s string) string {
	var b strings.Builder
	// EscapeText writes to a strings.Builder, which never fails.
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// toastGroup and toastTag place a notice in the Action Center: one group, and a
// tag that is the notice's ID without the bundle prefix ("update",
// "forward.3"). A toast with the tag and group of an earlier one replaces it.
// The tag is kept to 16 characters, the limit older Windows 10 builds enforced.
const toastGroup = "kubetower"

func toastTag(id string) string {
	tag := strings.TrimPrefix(id, "dev.thonicdev.kubetower.")
	if len(tag) > 16 {
		tag = tag[len(tag)-16:]
	}
	return tag
}

// toastSetting is what IToastNotifier::get_Setting answers: whether a toast
// shown now would be displayed, and the line to log when not. Show succeeds
// either way, so this is asked before each one.
func toastSetting(setting uint32) (show bool, line string) {
	switch setting {
	case 0: // Enabled
		return true, ""
	case 1: // DisabledForApplication
		return false, "Windows does not allow KubeTower's notifications (Settings > System > " +
			"Notifications > KubeTower), so a lost port-forward or a newer release is said in this log only"
	case 2: // DisabledForUser
		return false, "notifications are turned off for this Windows account (Settings > System > " +
			"Notifications), so a lost port-forward or a newer release is said in this log only"
	case 3: // DisabledByGroupPolicy
		return false, "a group policy turns notifications off on this machine, so a lost port-forward or a " +
			"newer release is said in this log only"
	case 4: // DisabledByManifest
		return false, "Windows does not yet know KubeTower's shortcut as a notifier (" + appUserModelID +
			"), so this notification is only in the log; Windows reads the Start-menu shortcut again " +
			"by itself, and the next launch should notify"
	}
	return false, fmt.Sprintf("Windows answered %d about KubeTower's notifications, which this build does "+
		"not know, so this notification is only in the log", setting)
}

// noticeClick is a click on a toast: its Arguments, validated as any
// kubetower:// link (a toast is posted only once the window has signed in). It
// returns the console path to open, "" for a refusal, and the line to log.
func noticeClick(arguments string) (open, line string) {
	open, line = (&schemeLinks{ready: true}).arrive(arguments)
	return open, "a notification was clicked; " + line
}

// noticeLoop looks at the notices waiting until done is closed: at the start,
// when wake says something arrived, every `every`, and when look asks to be
// asked again.
//
// `every` exists because the Windows window gets no event when it loses focus
// (macOS releases a held update notice on applicationDidResignActive:). A look
// costs a mutex and GetForegroundWindow, and also picks up an update found by
// any later check.
func noticeLoop(done, wake <-chan struct{}, every time.Duration, look func() time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		again := look()
		var fire <-chan time.Time
		if timer != nil {
			timer.Stop()
			timer = nil
		}
		if again > 0 {
			timer = time.NewTimer(again)
			fire = timer.C
		}
		select {
		case <-done:
			return
		case <-wake:
		case <-ticker.C:
		case <-fire:
		}
	}
}

// toastKeep is how many shown toasts are held: a toast's Activated event
// reaches this process only while the object lives, so the last few are kept
// for a click from the Action Center.
const toastKeep = 8
