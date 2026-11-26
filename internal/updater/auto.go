package updater

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/thonicdev/kubetower/internal/update"
	"github.com/thonicdev/kubetower/safego"
	"github.com/thonicdev/kubetower/textutil"
)

// AutoEvery is how often the automatic update looks at the update check's
// answer and at what is open (#122). The check itself keeps its own schedule:
// this asks GitHub nothing.
const AutoEvery = 30 * time.Second

// Auto runs "Install updates automatically" until ctx ends: a newer release
// the check found is downloaded and verified, and with WhenNow installed once
// no terminal and no port-forward is open. latest is the check's newer
// release, or empty; open counts what a restart would close.
func (u *Updater) Auto(ctx context.Context, every time.Duration, latest func() string, open func() (terminals, forwards int)) {
	ticks := time.NewTicker(every)
	defer ticks.Stop()
	for {
		u.autoStep(ctx, latest, open)
		select {
		case <-ctx.Done():
			return
		case <-ticks.C:
		}
	}
}

// autoStep is one look: at most one download or one install started.
func (u *Updater) autoStep(ctx context.Context, latest func() string, open func() (int, int)) {
	s := u.Status()
	if !s.Enabled || !s.Supported {
		return
	}
	switch s.Phase {
	case Idle, Failed:
		// A helper that could not be stopped keeps its file (#106): a new
		// download would remove it from under it.
		if s.Phase == Failed && s.Reason == errHelperAlive.Error() {
			return
		}
		next := latest()
		if next == "" || !update.IsNewer(next, u.o.Version) || next == u.failedAtQuit() || !u.once(&u.autoDownloaded, next) {
			return
		}
		if _, err := u.Download(); err != nil {
			u.logf("update: the automatic download did not start: %s", textutil.FirstLine(err.Error()))
		}
	case Ready:
		// A password is never asked unprompted, and on quit is InstallOnQuit's.
		if s.When != WhenNow || s.Password {
			return
		}
		if terminals, forwards := open(); terminals > 0 || forwards > 0 {
			return
		}
		if !u.once(&u.autoInstalled, s.Version) {
			return
		}
		if u.notWritable() {
			return
		}
		now := func(s Status) bool { return s.Enabled && s.When == WhenNow && !s.Password }
		if _, err := u.install(ctx, now); err != nil {
			u.logf("update: the automatic install did not start: %s", textutil.FirstLine(err.Error()))
		}
	}
}

// notWritable settles the installation's folder for a verified update: one
// that cannot be written puts the update in the manual phase (Open folder).
func (u *Updater) notWritable() bool {
	if u.writable() {
		return false
	}
	u.mu.Lock()
	file := u.planned.File
	u.mu.Unlock()
	u.manual(file, fmt.Errorf("%w: %s", ErrNotWritable, filepath.Dir(u.o.Installation.Path)))
	return true
}

// failedAtQuit is the release kept as not installed at the last quit.
func (u *Updater) failedAtQuit() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.held.FailedOnQuit
}

// once records version in *held and says whether it was not already there.
func (u *Updater) once(held *string, version string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if *held == version {
		return false
	}
	*held = version
	return true
}

func (u *Updater) logf(format string, args ...any) {
	if u.o.Logf != nil {
		u.o.Logf(format, args...)
	}
}

// QuitWait bounds InstallOnQuit, the copy and the installer together:
// quitting is never held longer than this.
var QuitWait = time.Minute

// InstallOnQuit installs a verified update as KubeTower quits, where the
// automatic setting says WhenQuit and no password is needed: the new build is
// put in place, nothing is started, and the next start runs it. It says
// whether it installed. internal/launch calls it once the server has stopped.
//
// A failure is kept in updater.json (failedOnQuit): the next start says it,
// and neither downloads that release again nor tries it at the next quit
// until a person asks. A folder that cannot be written was settled at the
// download already (the manual phase), so it does not get here.
func (u *Updater) InstallOnQuit(ctx context.Context) bool {
	u.mu.Lock()
	s := u.status
	if !s.Enabled || !s.Supported || s.When != WhenQuit || s.Password || s.Phase != Ready {
		u.mu.Unlock()
		return false
	}
	plan := u.planned
	u.mu.Unlock()
	// Turned on after a download by hand: the folder is settled now, and a
	// folder that cannot be written is the manual phase, never a failure.
	if u.notWritable() {
		return false
	}
	u.mu.Lock()
	if u.status.Phase != Ready {
		u.mu.Unlock()
		return false
	}
	u.status.Phase = Installing
	u.status.Reason = ""
	u.mu.Unlock()

	plan.Log = u.startLog()
	plan.Quit = true
	u.step("install %s on quit from %s into %s (%s)", plan.Version, plan.File, plan.Installation.Path, plan.Installation.Kind)
	fail := func(reason string) bool {
		u.step("not installed on quit: %s", reason)
		u.logf("update: not installed on quit: %s", reason)
		u.mu.Lock()
		u.status.Phase, u.status.Reason = Failed, reason
		u.mu.Unlock()
		u.failedOnQuit(plan.Version, reason)
		return false
	}
	if !update.IsNewer(plan.Version, u.o.Version) {
		return fail(ErrNotNewer.Error())
	}
	install := u.installer()
	if install == nil {
		return fail(ErrUnsupported.Error())
	}
	bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), QuitWait)
	defer cancel()
	done := make(chan error, 1)
	safego.Go("update install on quit", func() error {
		staged, err := u.stage(plan)
		if err != nil {
			done <- err
			return nil
		}
		plan.File = staged
		useLog(plan.Log)
		done <- install(bounded, plan)
		return nil
	}, func(err error) {
		if err != nil {
			done <- err
		}
	})
	select {
	case err := <-done:
		if err != nil {
			return fail(textutil.FirstLine(err.Error()))
		}
	case <-bounded.Done():
		return fail(fmt.Sprintf("not done within %s", QuitWait))
	}
	// Handed over: the next start checks it runs this release (Windows'
	// helper installs after the exit, where a failure is not seen here).
	u.mu.Lock()
	u.held.InstalledOnQuit = plan.Version
	u.mu.Unlock()
	if err := u.saveSettings(); err != nil {
		u.logf("update: the install on quit could not be noted: %s", textutil.FirstLine(err.Error()))
	}
	u.step("installed on quit; the next start runs %s", plan.Version)
	u.logf("update: %s installed; the next start runs it", plan.Version)
	return true
}
