package launch

// The Windows toasts, as far as they are decisions. Every
// platform runs these; `nativewindowtoast_windows.go` only stamps, shows and
// hands a click over. Which events notify, their text and when is
// `nativewindownotify_test.go`'s, and is not repeated: the Windows window asks
// the same `take`.

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/update"
)

// The AppUserModelID is the bundle identifier macOS already uses, and has the
// shape Windows asks for: at most 128 characters, no space, dotted.
func TestTheAppUserModelIDIsTheBundleIdentifier(t *testing.T) {
	if !strings.Contains(string(infoPlist("1.2.3")), "<string>"+appUserModelID+"</string>") {
		t.Errorf("the Info.plist does not carry %s: the Windows AppUserModelID and the macOS bundle "+
			"identifier have drifted apart", appUserModelID)
	}
	if len(appUserModelID) > 128 || strings.ContainsAny(appUserModelID, " \t") || !strings.Contains(appUserModelID, ".") {
		t.Errorf("%q is not an AppUserModelID Windows accepts", appUserModelID)
	}
}

// The shortcut the window stamps is the one the setup writes.
func TestTheStampedShortcutIsTheOneTheSetupWrites(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "packaging", "windows", "kubetower.nsi"))
	if err != nil {
		t.Fatalf("packaging/windows/kubetower.nsi is the Windows setup, and it is missing: %v", err)
	}
	want := `!define START_MENU_LINK "$SMPROGRAMS\` + shortcutName + `"`
	if !strings.Contains(string(raw), want) {
		t.Errorf("the setup no longer writes %s: the window would stamp a shortcut nobody made "+
			"(want the line %s)", shortcutName, want)
	}
}

func TestOnlyThisExecutablesShortcutIsStamped(t *testing.T) {
	const exe = `C:\Users\me\AppData\Local\Programs\KubeTower\kubetower.exe`
	for _, tc := range []struct {
		name               string
		found              bool
		target, aumid      string
		wantStamp, wantOur bool
	}{
		{"no shortcut", false, "", "", false, false},
		{"another copy's shortcut", true, `C:\Tools\KubeTower\kubetower.exe`, "", false, false},
		{"another copy's, already stamped", true, `C:\Tools\KubeTower\kubetower.exe`, appUserModelID, false, false},
		{"ours, never stamped (a fresh install)", true, exe, "", true, true},
		{"ours, with somebody else's ID", true, exe, "Other.App", true, true},
		{"ours, already stamped", true, exe, appUserModelID, false, true},
		{"ours, spelt in another case", true, strings.ToUpper(exe), appUserModelID, false, true},
		{"ours, with forward slashes", true, strings.ReplaceAll(exe, `\`, "/"), "", true, true},
		{"an empty target", true, "", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stamp, ours, line := shortcutVerdict(tc.found, tc.target, tc.aumid, exe)
			if stamp != tc.wantStamp || ours != tc.wantOur {
				t.Errorf("stamp, ours = %v, %v; want %v, %v", stamp, ours, tc.wantStamp, tc.wantOur)
			}
			if ours != (line == "") {
				t.Errorf("ours = %v and the line is %q: a shortcut that is not ours is always said", ours, line)
			}
		})
	}
}

func TestWindowsPathsCompareAsWindowsDoes(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		same bool
	}{
		{`C:\A\kubetower.exe`, `c:\a\KUBETOWER.EXE`, true},
		{`C:\A\B\..\kubetower.exe`, `C:\A\kubetower.exe`, true},
		{`C:\A\kubetower.exe`, `C:/A/kubetower.exe`, true},
		{`C:\A\kubetower.exe`, `C:\B\kubetower.exe`, false},
		{`C:\A\kubetower.exe`, `C:\A\kubetower.exe.bak`, false},
		{"", "", false},
	} {
		if got := sameWindowsPath(tc.a, tc.b); got != tc.same {
			t.Errorf("sameWindowsPath(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.same)
		}
	}
}

// toastShape is what the toast XML says, decoded the way any XML reader would.
type toastShape struct {
	XMLName xml.Name `xml:"toast"`
	Launch  string   `xml:"launch,attr"`
	Texts   []string `xml:"visual>binding>text"`
	Audio   struct {
		Silent string `xml:"silent,attr"`
	} `xml:"audio"`
}

func decodeToast(t *testing.T, raw string) toastShape {
	t.Helper()
	var shape toastShape
	if err := xml.Unmarshal([]byte(raw), &shape); err != nil {
		t.Fatalf("the toast XML does not parse (%v): %s", err, raw)
	}
	return shape
}

// The text reaches the toast as text, whatever it holds: the decoded XML says
// exactly the notice's title, body and link, and nothing became markup.
func TestTheToastSaysTheNoticeAndNothingElse(t *testing.T) {
	note := notice{
		ID:    noticeForwardIDPrefix + "1",
		Title: `Port-forward lost: service/<b>web</b>`,
		Body:  `local port 1 to 2, namespace "a&b" on prod's <cluster>.`,
		Link:  "kubetower://c/prod/services/shop/web",
	}
	raw := toastXML(note)
	shape := decodeToast(t, raw)
	if shape.Launch != note.Link {
		t.Errorf("launch = %q, want %q", shape.Launch, note.Link)
	}
	if len(shape.Texts) != 2 || shape.Texts[0] != note.Title || shape.Texts[1] != note.Body {
		t.Errorf("texts = %q, want the title and the body", shape.Texts)
	}
	if shape.Audio.Silent != "true" {
		t.Errorf("the toast makes a sound (audio silent = %q)", shape.Audio.Silent)
	}
	if strings.Contains(raw, "<b>") || strings.Contains(raw, "<cluster>") {
		t.Errorf("a name became markup: %s", raw)
	}
}

// A click goes where the macOS click goes: every notice `take` can compose,
// written into a toast and read back from it as Windows hands it over, opens
// the page its link names.
func TestAToastClickOpensTheNoticesPage(t *testing.T) {
	n := enabledNotices()
	n.forwardLost(webTunnel(), noticeEpoch)
	out, _, _ := n.take(noticeEpoch.Add(noticeSettle), false, update.Report{State: update.Newer, Latest: "9.9.9"})
	if len(out) != 2 {
		t.Fatalf("want the update and the forward, got %+v", out)
	}
	for _, note := range out {
		open, line := noticeClick(decodeToast(t, toastXML(note)).Launch)
		if want := pathOf(t, note); open != want {
			t.Errorf("a click on %q opens %q, want %q (%s)", note.Title, open, want, line)
		}
		if !strings.HasPrefix(line, "a notification was clicked") {
			t.Errorf("the log does not say a notification was clicked: %q", line)
		}
	}
}

// What Windows hands back is not trusted: an argument the validator refuses
// opens nothing, and the log says why.
func TestAToastClickWithABadLinkOpensNothing(t *testing.T) {
	for _, raw := range []string{"", "https://example.com/", "kubetower://api/forwards", "kubetower://c/prod?x=1"} {
		open, line := noticeClick(raw)
		if open != "" {
			t.Errorf("noticeClick(%q) opens %q", raw, open)
		}
		if !strings.Contains(line, "refused") {
			t.Errorf("noticeClick(%q) does not say it refused: %q", raw, line)
		}
	}
}

// The update toast replaces the update toast; forward toasts stand side by
// side; every tag fits the 16 characters older Windows 10 allows.
func TestToastTags(t *testing.T) {
	if got := toastTag(noticeUpdateID); got != "update" {
		t.Errorf("the update's tag is %q, want update", got)
	}
	a, b := toastTag(noticeForwardIDPrefix+"1"), toastTag(noticeForwardIDPrefix+"2")
	if a == b || a == "update" {
		t.Errorf("forward tags %q and %q would replace each other or the update", a, b)
	}
	if got := toastTag(noticeForwardIDPrefix + "123456789012345"); len(got) > 16 {
		t.Errorf("tag %q is longer than 16", got)
	}
}

// Enabled shows; every other answer is said in the log and shows nothing,
// including one this build does not know.
func TestToastSetting(t *testing.T) {
	for setting := uint32(0); setting <= 5; setting++ {
		show, line := toastSetting(setting)
		if show != (setting == 0) {
			t.Errorf("setting %d: show = %v", setting, show)
		}
		if show != (line == "") {
			t.Errorf("setting %d: show = %v with line %q", setting, show, line)
		}
	}
}

// The loop looks at once, when woken, when asked to look again, and stops when
// the window closes.
func TestTheNoticeLoopLooksWhenItShould(t *testing.T) {
	done, wake := make(chan struct{}), make(chan struct{}, 1)
	var looks atomic.Int32
	asked := make(chan int32, 16)
	stopped := make(chan struct{})
	go func() {
		noticeLoop(done, wake, time.Hour, func() time.Duration {
			n := looks.Add(1)
			asked <- n
			if n == 2 {
				return 10 * time.Millisecond // a settle: look again by itself
			}
			return 0
		})
		close(stopped)
	}()
	wait := func(want int32, why string) {
		t.Helper()
		select {
		case n := <-asked:
			if n != want {
				t.Fatalf("look %d, want %d (%s)", n, want, why)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no look %d (%s)", want, why)
		}
	}
	wait(1, "at the start")
	wake <- struct{}{}
	wait(2, "when woken")
	wait(3, "when the last look asked to be asked again")
	select {
	case n := <-asked:
		t.Fatalf("look %d with nothing to look for", n)
	case <-time.After(50 * time.Millisecond):
	}
	close(done)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not stop when the window closed")
	}
}
