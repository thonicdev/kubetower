package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/releasesig"
)

func TestAssetNameIsTheReleasesOwn(t *testing.T) {
	for _, tc := range []struct {
		goos, goarch string
		kind         Kind
		want         string
	}{
		{"windows", "amd64", WindowsSetup, "kubetower-0.1.2-setup.exe"},
		{"windows", "amd64", WindowsZip, "kubetower-0.1.2-win-x64.zip"},
		{"darwin", "arm64", MacApp, "kubetower-0.1.2-arm64.dmg"},
		{"darwin", "amd64", MacApp, "kubetower-0.1.2-x64.dmg"},
		{"darwin", "arm64", MacTar, "kubetower_0.1.2_darwin_arm64.tar.gz"},
		{"linux", "amd64", LinuxAppImage, "kubetower-0.1.2-x86_64.AppImage"},
		{"linux", "amd64", LinuxDeb, "kubetower-0.1.2-amd64.deb"},
		{"linux", "amd64", LinuxRPM, "kubetower-0.1.2-x86_64.rpm"},
		{"linux", "arm64", LinuxTar, "kubetower_0.1.2_linux_arm64.tar.gz"},
		// What the release does not ship.
		{"linux", "arm64", LinuxAppImage, ""},
		{"linux", "arm64", LinuxDeb, ""},
		{"windows", "arm64", WindowsSetup, ""},
		{"linux", "amd64", Unknown, ""},
		{"darwin", "amd64", LinuxDeb, ""},
	} {
		got, ok := AssetName("0.1.2", tc.goos, tc.goarch, tc.kind)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("%s/%s %s: %q %v, want %q", tc.goos, tc.goarch, tc.kind, got, ok, tc.want)
		}
	}
}

func TestDetectReadsHowItWasInstalled(t *testing.T) {
	exists := func(paths ...string) func(string) bool {
		return func(p string) bool {
			for _, want := range paths {
				if p == want {
					return true
				}
			}
			return false
		}
	}
	env := func(k, v string) func(string) string {
		return func(key string) string {
			if key == k {
				return v
			}
			return ""
		}
	}
	envs := func(pairs ...string) func(string) string {
		return func(key string) string {
			for i := 0; i+1 < len(pairs); i += 2 {
				if pairs[i] == key {
					return pairs[i+1]
				}
			}
			return ""
		}
	}
	for _, tc := range []struct {
		name, goos, exe string
		probe           Probe
		want            Installation
	}{
		{"the per-user setup", "windows", `C:\Users\a\AppData\Local\Programs\KubeTower\kubetower.exe`,
			Probe{Exists: exists(filepath.Join(filepath.Dir(`C:\Users\a\AppData\Local\Programs\KubeTower\kubetower.exe`), "uninstall.exe"))},
			Installation{WindowsSetup, `C:\Users\a\AppData\Local\Programs\KubeTower\kubetower.exe`}},
		{"the zip", "windows", `D:\tools\kubetower.exe`, Probe{Exists: exists()}, Installation{WindowsZip, `D:\tools\kubetower.exe`}},
		{"an app bundle", "darwin", "/Applications/KubeTower.app/Contents/MacOS/kubetower", Probe{},
			Installation{MacApp, "/Applications/KubeTower.app"}},
		{"a macOS binary", "darwin", "/usr/local/bin/kubetower", Probe{}, Installation{MacTar, "/usr/local/bin/kubetower"}},
		{"another app's executable", "darwin", "/Applications/Other.app/Contents/MacOS/other", Probe{}, Installation{}},
		{"an AppImage", "linux", "/tmp/.mount_x/usr/bin/kubetower",
			Probe{Getenv: envs("APPIMAGE", "/home/a/KubeTower.AppImage", "APPDIR", "/tmp/.mount_x")},
			Installation{LinuxAppImage, "/home/a/KubeTower.AppImage"}},
		{"a relative APPIMAGE is not one", "linux", "/tmp/.mount_x/usr/bin/kubetower",
			Probe{Getenv: envs("APPIMAGE", "x.AppImage", "APPDIR", "/tmp/.mount_x")},
			Installation{LinuxTar, "/tmp/.mount_x/usr/bin/kubetower"}},
		// Started from a terminal or an IDE that is itself an AppImage: both
		// variables are inherited, and they name that other application.
		{"a tar.gz under another AppImage's variables", "linux", "/home/a/bin/kubetower",
			Probe{Getenv: envs("APPIMAGE", "/home/a/Editor.AppImage", "APPDIR", "/tmp/.mount_editor")},
			Installation{LinuxTar, "/home/a/bin/kubetower"}},
		{"a .deb under another AppImage's variables", "linux", "/usr/bin/kubetower",
			Probe{Getenv: envs("APPIMAGE", "/home/a/Editor.AppImage", "APPDIR", "/tmp/.mount_editor"),
				Owner: func(string) string { return "deb" }},
			Installation{LinuxDeb, "/usr/bin/kubetower"}},
		{"a mount whose name only starts like APPDIR", "linux", "/tmp/.mount_editor2/usr/bin/kubetower",
			Probe{Getenv: envs("APPIMAGE", "/home/a/Editor.AppImage", "APPDIR", "/tmp/.mount_editor")},
			Installation{LinuxTar, "/tmp/.mount_editor2/usr/bin/kubetower"}},
		{"APPIMAGE without APPDIR", "linux", "/tmp/.mount_x/usr/bin/kubetower",
			Probe{Getenv: env("APPIMAGE", "/home/a/KubeTower.AppImage")},
			Installation{LinuxTar, "/tmp/.mount_x/usr/bin/kubetower"}},
		{"a .deb", "linux", "/usr/bin/kubetower", Probe{Owner: func(string) string { return "deb" }}, Installation{LinuxDeb, "/usr/bin/kubetower"}},
		{"an .rpm", "linux", "/usr/bin/kubetower", Probe{Owner: func(string) string { return "rpm" }}, Installation{LinuxRPM, "/usr/bin/kubetower"}},
		{"a tar.gz", "linux", "/home/a/bin/kubetower", Probe{}, Installation{LinuxTar, "/home/a/bin/kubetower"}},
		{"no executable", "linux", "", Probe{}, Installation{}},
		{"another system", "freebsd", "/usr/local/bin/kubetower", Probe{}, Installation{}},
	} {
		if got := Detect(tc.goos, tc.exe, tc.probe); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// release is a fake GitHub: the latest release, its assets, its signed sums.
type fake struct {
	t       *testing.T
	srv     *httptest.Server
	key     ed25519.PublicKey
	priv    ed25519.PrivateKey
	tag     string
	asset   string
	body    []byte
	listed  string // the hash checksums.txt gives the asset
	noSig   bool
	badSig  bool
	served  []byte // what is served for the asset, when not body
	foreign string // an asset URL outside the project's prefix
	redirTo string // where the asset redirects

	mu   sync.Mutex
	hits []string
}

func newFake(t *testing.T) *fake {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &fake{t: t, key: pub, priv: priv, tag: "v0.1.3", asset: "kubetower_0.1.3_linux_amd64.tar.gz", body: []byte("the new kubetower")}
	sum := sha256.Sum256(f.body)
	f.listed = hex.EncodeToString(sum[:])
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) sums() []byte {
	return []byte(fmt.Sprintf("%s  ./%s\n%s  ./checksums-other\n", f.listed, f.asset, strings.Repeat("0", 64)))
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits = append(f.hits, r.URL.Path)
	f.mu.Unlock()
	dl := f.srv.URL + "/dl/"
	switch r.URL.Path {
	case "/latest":
		assetURL := dl + f.asset
		if f.foreign != "" {
			assetURL = f.foreign
		}
		assets := []map[string]any{
			{"name": "checksums.txt", "browser_download_url": dl + "checksums.txt", "size": len(f.sums())},
			{"name": f.asset, "browser_download_url": assetURL, "size": len(f.body)},
		}
		if !f.noSig {
			assets = append(assets, map[string]any{"name": "checksums.txt.sig", "browser_download_url": dl + "checksums.txt.sig", "size": 64})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": f.tag, "assets": assets})
	case "/dl/checksums.txt":
		_, _ = w.Write(f.sums())
	case "/dl/checksums.txt.sig":
		sig := ed25519.Sign(f.priv, f.sums())
		if f.badSig {
			sig[0] ^= 1
		}
		_, _ = w.Write(sig)
	case "/dl/" + f.asset:
		if f.redirTo != "" {
			http.Redirect(w, r, f.redirTo, http.StatusFound)
			return
		}
		if f.served != nil {
			_, _ = w.Write(f.served)
			return
		}
		_, _ = w.Write(f.body)
	default:
		http.NotFound(w, r)
	}
}

type recorder struct {
	mu    sync.Mutex
	plans []Plan
	err   error
}

func (r *recorder) install(_ context.Context, p Plan) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plans = append(r.plans, p)
	return r.err
}

func (f *fake) updater(dir string, rec *recorder, over func(*Options)) *Updater {
	host, _ := url.Parse(f.srv.URL)
	o := Options{
		Version:      "0.1.2",
		GOOS:         "linux",
		GOARCH:       "amd64",
		Installation: Installation{Kind: LinuxTar, Path: "/opt/kt/kubetower"},
		Dir:          dir,
		Endpoint:     f.srv.URL + "/latest",
		AssetPrefix:  f.srv.URL + "/dl/",
		Hosts:        map[string]bool{host.Host: true},
		Verify: func(sums, sig []byte) (releasesig.Sums, error) {
			return releasesig.Verify(f.key, sums, sig)
		},
		Install: rec.install,
	}
	if over != nil {
		over(&o)
	}
	return New(o)
}

func settle(t *testing.T, u *Updater) Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := u.Status()
		if s.Phase != Downloading && s.Phase != Verifying {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the download did not settle")
	return Status{}
}

func left(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "update"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestADownloadVerifiedThenInstalledInTwoSteps(t *testing.T) {
	f := newFake(t)
	dir := t.TempDir()
	rec := &recorder{}
	u := f.updater(dir, rec, nil)
	if s := u.Status(); s.Phase != Idle || !s.Enabled || !s.Supported {
		t.Fatalf("before: %+v", s)
	}
	if _, err := u.Download(); err != nil {
		t.Fatal(err)
	}
	s := settle(t, u)
	if s.Phase != Ready || s.Version != "0.1.3" || s.Asset != f.asset || s.Done != int64(len(f.body)) || s.Total != int64(len(f.body)) {
		t.Fatalf("after the download: %+v", s)
	}
	// The download installs nothing by itself: that is the second click.
	if len(rec.plans) != 0 {
		t.Fatal("the download ran the installer")
	}
	if _, err := u.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		rec.mu.Lock()
		n := len(rec.plans)
		rec.mu.Unlock()
		if n == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.plans) != 1 {
		t.Fatalf("installed %d times", len(rec.plans))
	}
	p := rec.plans[0]
	got, err := os.ReadFile(p.File)
	if err != nil || string(got) != string(f.body) || p.SHA256 != f.listed || p.Version != "0.1.3" {
		t.Fatalf("plan %+v: %q %v", p, got, err)
	}
}

func TestNothingUnverifiedIsKeptOrInstalled(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(f *fake)
		reason string
	}{
		{"a wrong hash", func(f *fake) { f.listed = strings.Repeat("a", 64) }, "does not match its signed sha256"},
		{"a bad signature", func(f *fake) { f.badSig = true }, "not signed by the release key"},
		{"no signature", func(f *fake) { f.noSig = true }, "not signed"},
		{"an asset served from elsewhere", func(f *fake) { f.foreign = "https://example.com/x" }, "not served from this project's releases"},
		{"a redirect off GitHub", func(f *fake) { f.redirTo = "http://example.com/evil" }, "not a GitHub release host"},
		{"more bytes than declared", func(f *fake) { f.served = append([]byte("x"), f.body...) }, "the release declares"},
		{"fewer bytes than declared", func(f *fake) { f.served = f.body[:3] }, "the release declares"},
		{"an older release", func(f *fake) { f.tag = "v0.1.1" }, "not newer"},
		{"the same release", func(f *fake) { f.tag = "v0.1.2" }, "not newer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			tc.setup(f)
			dir := t.TempDir()
			rec := &recorder{}
			u := f.updater(dir, rec, nil)
			if _, err := u.Download(); err != nil {
				t.Fatal(err)
			}
			s := settle(t, u)
			if s.Phase != Failed || !strings.Contains(s.Reason, tc.reason) {
				t.Fatalf("%+v, want failed with %q", s, tc.reason)
			}
			if names := left(t, dir); len(names) != 0 {
				t.Errorf("left on disk: %v", names)
			}
			if _, err := u.Install(context.Background()); !errors.Is(err, ErrNotReady) {
				t.Errorf("install after a failure: %v, want ErrNotReady", err)
			}
			if len(rec.plans) != 0 {
				t.Error("the installer ran")
			}
		})
	}
}

func TestADevelopmentBuildIsNeverUpdated(t *testing.T) {
	f := newFake(t)
	u := f.updater(t.TempDir(), &recorder{}, func(o *Options) { o.Version = "dev" })
	if _, err := u.Download(); err != nil {
		t.Fatal(err)
	}
	if s := settle(t, u); s.Phase != Failed || !strings.Contains(s.Reason, "not newer") {
		t.Fatalf("%+v", s)
	}
}

// "Install updates automatically" (#122): on and on quit by default; off, a
// download by hand still works; both are kept for the next start; an unknown
// moment is refused and changes nothing.
func TestTheAutomaticSettingIsKeptAndOffStaysByHand(t *testing.T) {
	f := newFake(t)
	dir := t.TempDir()
	u := f.updater(dir, &recorder{}, nil)
	if s := u.Status(); !s.Enabled || s.When != WhenQuit || s.Password {
		t.Fatalf("defaults: %+v", s)
	}
	if s, err := u.Set(false, WhenNow); err != nil || s.Enabled || s.When != WhenNow {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err := u.Download(); err != nil {
		t.Fatalf("a download by hand with the setting off: %v", err)
	}
	if s := settle(t, u); s.Phase != Ready {
		t.Fatalf("%+v", s)
	}
	// Read back by a new process.
	if again := f.updater(dir, &recorder{}, nil).Status(); again.Enabled || again.When != WhenNow {
		t.Errorf("not kept across a restart: %+v", again)
	}
	// An empty moment keeps the one set; an unknown one is refused.
	if s, err := u.Set(true, ""); err != nil || !s.Enabled || s.When != WhenNow {
		t.Fatalf("%+v %v", s, err)
	}
	if s, err := u.Set(false, "later"); !errors.Is(err, ErrWhen) || !s.Enabled {
		t.Fatalf("an unknown moment: %+v %v", s, err)
	}
	if again := f.updater(dir, &recorder{}, nil).Status(); !again.Enabled || again.When != WhenNow {
		t.Errorf("the refused write was kept: %+v", again)
	}
	// The name the manual setting had: off before is off now.
	old := t.TempDir()
	if err := os.WriteFile(filepath.Join(old, settingsFile), []byte(`{"installUpdates":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if s := f.updater(old, &recorder{}, nil).Status(); s.Enabled || s.When != WhenQuit {
		t.Errorf("an older setting off: %+v", s)
	}
}

func TestAPackageAsksForAPassword(t *testing.T) {
	f := newFake(t)
	for kind, want := range map[Kind]bool{LinuxDeb: true, LinuxRPM: true, LinuxTar: false, LinuxAppImage: false, WindowsSetup: false} {
		if got := f.updater(t.TempDir(), &recorder{}, func(o *Options) { o.Installation.Kind = kind }).Status().Password; got != want {
			t.Errorf("%s: password %v", kind, got)
		}
	}
}

func TestAnInstallationWithNoAssetOffersNothing(t *testing.T) {
	f := newFake(t)
	u := f.updater(t.TempDir(), &recorder{}, func(o *Options) { o.GOARCH = "arm64"; o.Installation.Kind = LinuxDeb })
	if u.Status().Supported {
		t.Fatal("a .deb on arm64 is offered")
	}
	if _, err := u.Download(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("%v", err)
	}
}

func TestAFileChangedAfterItsCheckIsNotInstalled(t *testing.T) {
	f := newFake(t)
	rec := &recorder{}
	u := f.updater(t.TempDir(), rec, nil)
	if _, err := u.Download(); err != nil {
		t.Fatal(err)
	}
	s := settle(t, u)
	if s.Phase != Ready {
		t.Fatalf("%+v", s)
	}
	if err := os.WriteFile(u.planned.File, []byte("swapped"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Install(context.Background()); err == nil {
		t.Fatal("installed a file that changed")
	}
	if u.Status().Phase != Failed || len(rec.plans) != 0 {
		t.Fatalf("%+v, %d installs", u.Status(), len(rec.plans))
	}
}

func TestAFailingInstallerSaysWhy(t *testing.T) {
	f := newFake(t)
	rec := &recorder{err: errors.New("pkexec: cancelled\nmore")}
	u := f.updater(t.TempDir(), rec, nil)
	if _, err := u.Download(); err != nil {
		t.Fatal(err)
	}
	settle(t, u)
	if _, err := u.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for u.Status().Phase == Installing && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s := u.Status(); s.Phase != Failed || s.Reason != "pkexec: cancelled" {
		t.Fatalf("%+v", s)
	}
}

func TestTheRealHostsAreHTTPSOnGitHubOnly(t *testing.T) {
	u := New(Options{Dir: t.TempDir()})
	for raw, ok := range map[string]bool{
		"https://github.com/thonicdev/kubetower/releases/download/0.1.3/x": true,
		"https://objects.githubusercontent.com/x":                          true,
		"https://release-assets.githubusercontent.com/x":                   true,
		"http://github.com/x":                                              false,
		"https://github.com.evil.example/x":                                false,
		"https://example.com/x":                                            false,
	} {
		if err := u.allowed(raw); (err == nil) != ok {
			t.Errorf("%s: %v, want allowed=%v", raw, err, ok)
		}
	}
}

// The installer handed over and asked the console to stop, and the console
// did not stop - the Windows window that ignored the interrupt (#96). Within
// ExitWait the helper is stopped and the install goes back to ready with its
// reason, the verified file kept, rather than "Installing…" for ever; every
// step is in the log, with no URL in it.
func TestAConsoleThatDoesNotStopFailsTheInstallInBoundedTime(t *testing.T) {
	stopped := 0
	was := abandonHelper
	abandonHelper = func() error { stopped++; return nil }
	wasAfter := installsAfterExit
	installsAfterExit = true // the Windows helpers: the install happens after exit
	t.Cleanup(func() { abandonHelper, installsAfterExit = was, wasAfter })

	f := newFake(t)
	dir := t.TempDir()
	rec := &recorder{}
	u := f.updater(dir, rec, func(o *Options) { o.ExitWait = 50 * time.Millisecond })
	if _, err := u.Download(); err != nil {
		t.Fatal(err)
	}
	settle(t, u)
	if _, err := u.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for u.Status().Phase == Installing && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s := u.Status(); s.Phase != Ready || s.Reason != errNotStopped.Error() || s.Version != "0.1.3" {
		t.Fatalf("after ExitWait: %+v, want ready again with the reason: the verified file is kept", s)
	}
	if stopped != 1 {
		t.Errorf("the helper was stopped %d times, want 1: left waiting, it would install later, unasked", stopped)
	}
	rec.mu.Lock()
	logPath := rec.plans[0].Log
	rec.mu.Unlock()
	if logPath != filepath.Join(dir, installLog) {
		t.Errorf("the installer was handed the log %q", logPath)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(raw)
	for _, want := range []string{"install 0.1.3 from " + f.asset, "staged ", "installer handed over", "still running after 50ms", "back to ready, the verified file kept"} {
		if !strings.Contains(log, want) {
			t.Errorf("the log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "http") {
		t.Errorf("the log names a URL:\n%s", log)
	}

	// Install again, with no new download, and a new install starts a new log.
	if s, err := u.Install(context.Background()); err != nil || s.Reason != "" {
		t.Fatalf("install again: %+v %v", s, err)
	}
	raw, _ = os.ReadFile(logPath)
	if strings.Contains(string(raw), "still running") {
		t.Errorf("a new install kept the last one's log:\n%s", raw)
	} // Its own wait ends before the test does: abandonHelper is a global.
	deadline = time.Now().Add(3 * time.Second)
	for u.Status().Phase == Installing && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
}

// An installer that ended the process leaves nothing to fail: the wait only
// fails an install still in progress.
func TestAnInstallThatMovedOnIsNotFailedByTheWait(t *testing.T) {
	was := abandonHelper
	abandonHelper = func() error { t.Error("the helper was stopped"); return nil }
	t.Cleanup(func() { abandonHelper = was })
	f := newFake(t)
	rec := &recorder{err: ErrNotRestarted}
	u := f.updater(t.TempDir(), rec, func(o *Options) { o.ExitWait = 20 * time.Millisecond })
	if _, err := u.Download(); err != nil {
		t.Fatal(err)
	}
	settle(t, u)
	if _, err := u.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if s := u.Status(); s.Phase != Idle {
		t.Errorf("%+v, want idle: installed, not restarted", s)
	}
}

// An update installed and not started again says so in the install log
// (#178, C31.1): the line a person reads to know the next start runs it.
func TestInstalledNotRestartedIsLogged(t *testing.T) {
	was := abandonHelper
	abandonHelper = func() error { return nil }
	t.Cleanup(func() { abandonHelper = was })
	f := newFake(t)
	dir := t.TempDir()
	u := f.updater(dir, &recorder{err: fmt.Errorf("%w: no /bin/sh", ErrNotRestarted)}, nil)
	if _, err := u.Download(); err != nil {
		t.Fatal(err)
	}
	settle(t, u)
	if _, err := u.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, _ := os.ReadFile(filepath.Join(u.dir(), installLog))
		if strings.Contains(string(raw), "installed; not restarted: ") && strings.Contains(string(raw), "no /bin/sh") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the install log does not say it: %s", raw)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
