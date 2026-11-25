package updater

import (
	"context"
	"time"

	"github.com/thonicdev/kubetower/internal/update"
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
		next := latest()
		if next == "" || !update.IsNewer(next, u.o.Version) || !u.once(&u.autoDownloaded, next) {
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
		if _, err := u.Install(ctx); err != nil {
			u.logf("update: the automatic install did not start: %s", textutil.FirstLine(err.Error()))
		}
	}
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

// QuitWait bounds InstallOnQuit: quitting is never held longer than this.
var QuitWait = time.Minute

// InstallOnQuit installs a verified update as KubeTower quits, where the
// automatic setting says WhenQuit and no password is needed: the new build is
// put in place, nothing is started, and the next start runs it. It says
// whether it installed. internal/launch calls it once the server has stopped.
func (u *Updater) InstallOnQuit(ctx context.Context) bool {
	u.mu.Lock()
	s := u.status
	if !s.Enabled || !s.Supported || s.When != WhenQuit || s.Password || s.Phase != Ready {
		u.mu.Unlock()
		return false
	}
	plan := u.planned
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
		return false
	}
	if !update.IsNewer(plan.Version, u.o.Version) {
		return fail(ErrNotNewer.Error())
	}
	staged, err := u.stage(plan)
	if err != nil {
		return fail(textutil.FirstLine(err.Error()))
	}
	plan.File = staged
	useLog(plan.Log)
	install := u.installer()
	if install == nil {
		return fail(ErrUnsupported.Error())
	}
	bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), QuitWait)
	defer cancel()
	if err := install(bounded, plan); err != nil {
		return fail(textutil.FirstLine(err.Error()))
	}
	u.step("installed on quit; the next start runs %s", plan.Version)
	u.logf("update: %s installed; the next start runs it", plan.Version)
	return true
}
