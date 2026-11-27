package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func plans(rec *recorder) []Plan {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]Plan(nil), rec.plans...)
}

// waitPlans waits for the installer, which Install runs on its own goroutine.
func waitPlans(t *testing.T, rec *recorder, n int) []Plan {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(plans(rec)) < n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return plans(rec)
}

func nothingOpen() (int, int) { return 0, 0 }

// The automatic update downloads what the check found, once per release, and
// asks nothing while the setting is off or nothing newer is known (#122).
func TestTheAutomaticUpdateDownloadsTheNewerReleaseOnce(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	off := f.updater(t.TempDir(), &recorder{}, nil)
	if _, err := off.Set(false, ""); err != nil {
		t.Fatal(err)
	}
	off.autoStep(ctx, func() string { return "0.1.3" }, nothingOpen)
	if s := off.Status(); s.Phase != Idle {
		t.Fatalf("off, it downloaded: %+v", s)
	}

	u := f.updater(t.TempDir(), &recorder{}, nil)
	for _, latest := range []string{"", "0.1.2", "0.1.1"} {
		u.autoStep(ctx, func() string { return latest }, nothingOpen)
		if s := u.Status(); s.Phase != Idle {
			t.Fatalf("latest %q started %+v", latest, s)
		}
	}
	u.autoStep(ctx, func() string { return "0.1.3" }, nothingOpen)
	if s := settle(t, u); s.Phase != Ready || s.Version != "0.1.3" {
		t.Fatalf("%+v", s)
	}

	// A failed download is not repeated for the same release.
	f.badSig = true
	bad := f.updater(t.TempDir(), &recorder{}, nil)
	bad.autoStep(ctx, func() string { return "0.1.3" }, nothingOpen)
	if s := settle(t, bad); s.Phase != Failed {
		t.Fatalf("%+v", s)
	}
	f.mu.Lock()
	asked := len(f.hits)
	f.mu.Unlock()
	bad.autoStep(ctx, func() string { return "0.1.3" }, nothingOpen)
	f.mu.Lock()
	again := len(f.hits)
	f.mu.Unlock()
	if s := bad.Status(); s.Phase != Failed || again != asked {
		t.Fatalf("retried the same release: %+v, %d then %d requests", s, asked, again)
	}
}

// Now: installed once nothing a restart would close is open, and once per
// release; never with a password, and never on quit's setting.
func TestNowInstallsOnceNothingIsOpen(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	ready := func(rec *recorder, over func(*Options), when string) *Updater {
		u := f.updater(t.TempDir(), rec, over)
		if _, err := u.Set(true, when); err != nil {
			t.Fatal(err)
		}
		if _, err := u.Download(); err != nil {
			t.Fatal(err)
		}
		if s := settle(t, u); s.Phase != Ready {
			t.Fatalf("%+v", s)
		}
		return u
	}
	latest := func() string { return "0.1.3" }

	rec := &recorder{}
	u := ready(rec, nil, WhenNow)
	u.autoStep(ctx, latest, func() (int, int) { return 1, 0 })
	u.autoStep(ctx, latest, func() (int, int) { return 0, 2 })
	if n := len(plans(rec)); n != 0 {
		t.Fatalf("installed with a terminal or a forward open: %d", n)
	}
	u.autoStep(ctx, latest, nothingOpen)
	if got := waitPlans(t, rec, 1); len(got) != 1 || got[0].Quit {
		t.Fatalf("%+v", got)
	}
	// Back to ready (an install that did not finish): not tried again unasked.
	u.mu.Lock()
	u.status.Phase = Ready
	u.mu.Unlock()
	u.autoStep(ctx, latest, nothingOpen)
	time.Sleep(50 * time.Millisecond)
	if n := len(plans(rec)); n != 1 {
		t.Fatalf("installed %d times", n)
	}

	// A package's install asks for a password: drawn ready, never started.
	deb := f.updater(t.TempDir(), rec, func(o *Options) { o.Installation.Kind = LinuxDeb })
	if _, err := deb.Set(true, WhenNow); err != nil {
		t.Fatal(err)
	}
	deb.mu.Lock()
	deb.status.Phase, deb.status.Version = Ready, "0.1.3"
	deb.mu.Unlock()
	for _, u := range []*Updater{deb, ready(rec, nil, WhenQuit)} {
		u.autoStep(ctx, latest, nothingOpen)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(plans(rec)); n != 1 {
		t.Fatalf("installed with a password or on quit's setting: %d", n)
	}
}

// On quit: the verified update is installed with Quit set, synchronously, and
// only where the setting says on quit and no password is needed.
func TestOnQuitInstallsTheReadyUpdateAndStartsNothing(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	rec := &recorder{}
	u := f.updater(t.TempDir(), rec, nil)
	if u.InstallOnQuit(ctx) {
		t.Fatal("installed with nothing downloaded")
	}
	if _, err := u.Download(); err != nil {
		t.Fatal(err)
	}
	settle(t, u)
	if !u.InstallOnQuit(ctx) {
		t.Fatalf("not installed: %+v", u.Status())
	}
	got := plans(rec)
	if len(got) != 1 || !got[0].Quit || got[0].Version != "0.1.3" {
		t.Fatalf("%+v", got)
	}

	for name, set := range map[string]func(*Updater){
		"off": func(u *Updater) { _, _ = u.Set(false, "") },
		"now": func(u *Updater) { _, _ = u.Set(true, WhenNow) },
	} {
		other := &recorder{}
		v := f.updater(t.TempDir(), other, nil)
		if _, err := v.Download(); err != nil {
			t.Fatal(err)
		}
		settle(t, v)
		set(v)
		if v.InstallOnQuit(ctx) || len(plans(other)) != 0 {
			t.Errorf("%s: installed on quit", name)
		}
	}

	// A failing installer says why, and quitting goes on.
	failing := &recorder{err: errBackToReady}
	w := f.updater(t.TempDir(), failing, nil)
	if _, err := w.Download(); err != nil {
		t.Fatal(err)
	}
	settle(t, w)
	if w.InstallOnQuit(ctx) {
		t.Fatal("a failed install reported installed")
	}
	if s := w.Status(); s.Phase != Failed || s.Reason == "" {
		t.Fatalf("%+v", s)
	}
}

// readyOn downloads the fake's release into a fresh updater on rec.
func readyOn(t *testing.T, f *fake, dir string, rec *recorder, over func(*Options)) *Updater {
	t.Helper()
	u := f.updater(dir, rec, over)
	if _, err := u.Download(); err != nil {
		t.Fatal(err)
	}
	if s := settle(t, u); s.Phase != Ready {
		t.Fatalf("%+v", s)
	}
	return u
}

// B1 of #124's review: a folder that cannot be written is settled at the
// download, while KubeTower runs - the manual phase, Open folder - and on
// quit nothing is tried.
func TestANotWritableFolderIsTheManualPhaseBeforeAnyQuit(t *testing.T) {
	f := newFake(t)
	rec := &recorder{}
	u := f.updater(t.TempDir(), rec, func(o *Options) { o.Writable = func() bool { return false } })
	if _, err := u.Download(); err != nil {
		t.Fatal(err)
	}
	s := settle(t, u)
	if s.Phase != Manual || s.Folder == "" || s.Version != "0.1.3" {
		t.Fatalf("%+v", s)
	}
	if u.InstallOnQuit(context.Background()) || len(plans(rec)) != 0 {
		t.Fatal("tried on quit")
	}
}

// A failure at quit is kept: the next start says it, the automatic update
// neither downloads that release again nor tries it, and a person's download
// clears it.
func TestAFailedInstallOnQuitIsKeptAndNotRepeated(t *testing.T) {
	f := newFake(t)
	dir := t.TempDir()
	u := readyOn(t, f, dir, &recorder{err: errors.New("the setup refused")}, nil)
	if u.InstallOnQuit(context.Background()) {
		t.Fatal("reported installed")
	}
	next := f.updater(dir, &recorder{}, nil)
	if s := next.Status(); s.Phase != Failed || s.Version != "0.1.3" || !strings.Contains(s.Reason, "the setup refused") {
		t.Fatalf("the next start: %+v", s)
	}
	f.mu.Lock()
	asked := len(f.hits)
	f.mu.Unlock()
	next.autoStep(context.Background(), func() string { return "0.1.3" }, nothingOpen)
	f.mu.Lock()
	again := len(f.hits)
	f.mu.Unlock()
	if again != asked || next.Status().Phase != Failed {
		t.Fatalf("downloaded again: %d then %d requests, %+v", asked, again, next.Status())
	}
	// Retry, by a person: the failure is forgotten.
	if _, err := next.Download(); err != nil {
		t.Fatal(err)
	}
	settle(t, next)
	deadline := time.Now().Add(2 * time.Second)
	for f.updater(dir, &recorder{}, nil).Status().Phase == Failed && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s := f.updater(dir, &recorder{}, nil).Status(); s.Phase == Failed {
		t.Fatalf("still failed after a download: %+v", s)
	}
}

// pkexec is never started without a click, on quit either.
func TestOnQuitNeverInstallsAPackage(t *testing.T) {
	f := newFake(t)
	rec := &recorder{}
	// A real verified download, so only the password stands in the way.
	deb := readyOn(t, f, t.TempDir(), rec, nil)
	deb.mu.Lock()
	deb.status.Password = true
	deb.mu.Unlock()
	if deb.InstallOnQuit(context.Background()) || len(plans(rec)) != 0 {
		t.Fatal("a package installed on quit")
	}
	if s := deb.Status(); s.Phase != Ready {
		t.Fatalf("not left ready for Install: %+v", s)
	}
}

// The installer reads the copy staged for this install, never the verified
// path a swap could reach; and never a release that is not newer.
func TestOnQuitInstallsTheStagedCopyOfANewerRelease(t *testing.T) {
	f := newFake(t)
	rec := &recorder{}
	u := readyOn(t, f, t.TempDir(), rec, nil)
	checked := u.planned.File
	if !u.InstallOnQuit(context.Background()) {
		t.Fatal("not installed")
	}
	got := plans(rec)
	if len(got) != 1 || got[0].File == checked || !strings.HasPrefix(filepath.Base(filepath.Dir(got[0].File)), "install-") {
		t.Fatalf("installed %+v, checked %s", got, checked)
	}

	older := &recorder{}
	same := readyOn(t, f, t.TempDir(), older, nil)
	same.mu.Lock()
	same.planned.Version = "0.1.2"
	same.mu.Unlock()
	if same.InstallOnQuit(context.Background()) || len(plans(older)) != 0 {
		t.Fatal("installed a release that is not newer")
	}
	if s := same.Status(); s.Phase != Failed || !strings.Contains(s.Reason, "not newer") {
		t.Fatalf("%+v", s)
	}
}

// QuitWait bounds the whole install: quitting is not held by an installer
// that does not return.
func TestOnQuitIsBounded(t *testing.T) {
	f := newFake(t)
	was := QuitWait
	QuitWait = 50 * time.Millisecond
	t.Cleanup(func() { QuitWait = was })
	u := readyOn(t, f, t.TempDir(), &recorder{}, func(o *Options) {
		o.Install = func(ctx context.Context, _ Plan) error { <-ctx.Done(); time.Sleep(time.Second); return nil }
	})
	start := time.Now()
	if u.InstallOnQuit(context.Background()) {
		t.Fatal("reported installed")
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("held quitting for %s", took)
	}
}

// A switch to off, or to on quit, just before the automatic install starts
// is honored: the conditions are checked again under the lock that starts it.
func TestTheAutomaticInstallChecksTheSettingAgain(t *testing.T) {
	f := newFake(t)
	rec := &recorder{}
	u := readyOn(t, f, t.TempDir(), rec, nil)
	if _, err := u.Set(true, WhenNow); err != nil {
		t.Fatal(err)
	}
	u.autoStep(context.Background(), func() string { return "0.1.3" }, func() (int, int) {
		_, _ = u.Set(false, "")
		return 0, 0
	})
	time.Sleep(50 * time.Millisecond)
	if n := len(plans(rec)); n != 0 {
		t.Fatalf("installed after the switch went off: %d", n)
	}
}

// A helper that could not be stopped keeps its file (#106): no automatic
// download removes it.
func TestNoAutomaticDownloadBesideAHelperStillAlive(t *testing.T) {
	f := newFake(t)
	u := f.updater(t.TempDir(), &recorder{}, nil)
	u.mu.Lock()
	u.status.Phase, u.status.Reason = Failed, errHelperAlive.Error()
	u.mu.Unlock()
	u.autoStep(context.Background(), func() string { return "0.1.3" }, nothingOpen)
	if s := u.Status(); s.Phase != Failed || s.Reason != errHelperAlive.Error() {
		t.Fatalf("%+v", s)
	}
}

// By hand, a folder that cannot be written is found by Install, as before
// (#124's delta review: macOS then opens the disk image for the person who
// clicked); turned on afterwards, on quit settles it as the manual phase.
func TestByHandTheFolderIsFoundByInstallAndOnQuitSettlesIt(t *testing.T) {
	f := newFake(t)
	rec := &recorder{}
	u := f.updater(t.TempDir(), rec, func(o *Options) { o.Writable = func() bool { return false } })
	if _, err := u.Set(false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Download(); err != nil {
		t.Fatal(err)
	}
	if s := settle(t, u); s.Phase != Ready {
		t.Fatalf("by hand: %+v", s)
	}
	if _, err := u.Set(true, WhenQuit); err != nil {
		t.Fatal(err)
	}
	if u.InstallOnQuit(context.Background()) || len(plans(rec)) != 0 {
		t.Fatal("tried on quit")
	}
	if s := u.Status(); s.Phase != Manual || s.Folder == "" {
		t.Fatalf("%+v", s)
	}
	u.mu.Lock()
	failed := u.held.FailedOnQuit
	u.mu.Unlock()
	if failed != "" {
		t.Fatalf("kept as failed on quit: %q", failed)
	}
}

// Windows' helper installs after the exit, out of sight: the next start
// running an older build is that install having failed, kept as such; the
// release it names running is the install done, and nothing is kept.
func TestTheNextStartChecksTheInstallHandedOverAtQuit(t *testing.T) {
	f := newFake(t)
	for _, c := range []struct {
		next   string
		failed bool
	}{{"0.1.2", true}, {"0.1.3", false}} {
		dir := t.TempDir()
		u := readyOn(t, f, dir, &recorder{}, nil)
		if !u.InstallOnQuit(context.Background()) {
			t.Fatal("not handed over")
		}
		next := f.updater(dir, &recorder{}, func(o *Options) { o.Version = c.next })
		s := next.Status()
		if got := s.Phase == Failed && strings.Contains(s.Reason, "update-install.log"); got != c.failed {
			t.Errorf("next start on %s: %+v", c.next, s)
		}
		// Settled once: a third start says the same, and holds nothing pending.
		again := f.updater(dir, &recorder{}, func(o *Options) { o.Version = c.next })
		if again.Status().Phase != s.Phase || again.held.InstalledOnQuit != "" {
			t.Errorf("third start on %s: %+v %+v", c.next, again.Status(), again.held)
		}
	}
}

// A release kept as failed on quit and then installed by hand is not said
// as failed by the build that runs it.
func TestAFailureOnQuitIsForgottenOnceThatReleaseRuns(t *testing.T) {
	f := newFake(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, settingsFile), []byte(`{"failedOnQuit":"0.1.3","failedReason":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if s := f.updater(dir, &recorder{}, nil).Status(); s.Phase != Failed {
		t.Fatalf("0.1.2: %+v", s)
	}
	if s := f.updater(dir, &recorder{}, func(o *Options) { o.Version = "0.1.3" }).Status(); s.Phase == Failed {
		t.Fatalf("0.1.3 runs it: %+v", s)
	}
}

// The writes of updater.json land in order: what is on disk is the last
// setting, however the writes interleave.
func TestTheSettingOnDiskIsTheLastOne(t *testing.T) {
	f := newFake(t)
	dir := t.TempDir()
	u := f.updater(dir, &recorder{}, nil)
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			when := WhenQuit
			if i%2 == 0 {
				when = WhenNow
			}
			_, _ = u.Set(i%3 == 0, when)
		}()
	}
	wg.Wait()
	want := u.Status()
	if got := f.updater(dir, &recorder{}, nil).Status(); got.Enabled != want.Enabled || got.When != want.When {
		t.Fatalf("on disk %v/%s, in memory %v/%s", got.Enabled, got.When, want.Enabled, want.When)
	}
}

// At once, turned on after a download by hand: the folder is settled before
// the install starts, and one that cannot be written is the manual phase, with
// no installer run (on macOS, no disk image opened unasked).
func TestAtOnceSettlesTheFolderBeforeInstalling(t *testing.T) {
	f := newFake(t)
	rec := &recorder{}
	writable := true
	u := f.updater(t.TempDir(), rec, func(o *Options) { o.Writable = func() bool { return writable } })
	if _, err := u.Set(false, WhenNow); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Download(); err != nil {
		t.Fatal(err)
	}
	if s := settle(t, u); s.Phase != Ready {
		t.Fatalf("%+v", s)
	}
	writable = false
	if _, err := u.Set(true, WhenNow); err != nil {
		t.Fatal(err)
	}
	// Synchronous: the folder is settled before any install would start.
	u.autoStep(context.Background(), func() string { return "0.1.3" }, nothingOpen)
	if s := u.Status(); s.Phase != Manual || s.Folder == "" || len(plans(rec)) != 0 {
		t.Fatalf("%+v, %d installs", s, len(plans(rec)))
	}
}

// A folder found not writable is kept for the next start (#178, C37.5): it
// says the manual phase again, with the folder, and the automatic update does
// not download that release once more at every start. A person's download
// clears it, and a release this build is not older than, or a folder gone,
// is not said.
func TestTheManualPhaseIsKeptForTheNextStart(t *testing.T) {
	f := newFake(t)
	dir := t.TempDir()
	notWritable := func(o *Options) { o.Writable = func() bool { return false } }
	u := f.updater(dir, &recorder{}, notWritable)
	if _, err := u.Download(); err != nil {
		t.Fatal(err)
	}
	first := settle(t, u)
	if first.Phase != Manual || first.Folder == "" {
		t.Fatalf("first start: %+v", first)
	}
	waitSaved(t, dir, `"manualVersion":"0.1.3"`)

	next := f.updater(dir, &recorder{}, notWritable)
	s := next.Status()
	if s.Phase != Manual || s.Version != "0.1.3" || s.Folder != first.Folder {
		t.Fatalf("next start: %+v, want the manual phase of 0.1.3 in %s", s, first.Folder)
	}
	fetched := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return strings.Count(strings.Join(f.hits, "\n"), "/dl/"+f.asset)
	}
	downloads := fetched()
	next.autoStep(context.Background(), func() string { return "0.1.3" }, func() (int, int) { return 0, 0 })
	if fetched() != downloads {
		t.Error("the automatic update downloaded the release found not writable again")
	}

	// The release now runs: nothing to say.
	if s := f.updater(dir, &recorder{}, func(o *Options) { notWritable(o); o.Version = "0.1.3" }).Status(); s.Phase == Manual {
		t.Errorf("0.1.3 runs it: %+v", s)
	}
	// Its folder gone: nothing to open, so nothing said.
	if err := os.RemoveAll(first.Folder); err != nil {
		t.Fatal(err)
	}
	if s := f.updater(dir, &recorder{}, notWritable).Status(); s.Phase == Manual {
		t.Errorf("folder gone: %+v", s)
	}
	// A person's download, into a folder now writable, clears what was kept.
	last := f.updater(dir, &recorder{}, func(o *Options) { o.Writable = func() bool { return true } })
	if _, err := last.Download(); err != nil {
		t.Fatal(err)
	}
	if s := settle(t, last); s.Phase != Ready {
		t.Fatalf("after a download by hand: %+v", s)
	}
	last.mu.Lock()
	kept := last.held.ManualVersion
	last.mu.Unlock()
	if kept != "" {
		t.Errorf("after a download by hand, %q is still kept as not writable", kept)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, _ := os.ReadFile(filepath.Join(dir, settingsFile))
		if !strings.Contains(string(raw), "manualVersion") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("updater.json still keeps it: %s", raw)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitSaved waits until updater.json in dir holds want: the settings are
// written on a goroutine of their own.
func waitSaved(t *testing.T, dir, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, _ := os.ReadFile(filepath.Join(dir, settingsFile))
		if strings.Contains(string(raw), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("updater.json is %s, want it to hold %s", raw, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
