package updater

import (
	"context"
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
