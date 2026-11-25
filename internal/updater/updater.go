package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/thonicdev/kubetower/internal/releasesig"
	"github.com/thonicdev/kubetower/internal/statefile"
	"github.com/thonicdev/kubetower/internal/update"
	"github.com/thonicdev/kubetower/safego"
	"github.com/thonicdev/kubetower/textutil"
)

// Phase is where the update stands.
type Phase string

const (
	Idle        Phase = "idle"
	Downloading Phase = "downloading"
	Verifying   Phase = "verifying"
	Ready       Phase = "ready"
	Installing  Phase = "installing"
	Failed      Phase = "failed"
	// Manual: the installation's folder is not writable. The verified file
	// waits in Folder, for the operator to install by hand.
	Manual Phase = "manual"
)

// Status is what the Settings page is told.
type Status struct {
	Phase Phase `json:"phase"`
	// Enabled is the operator's "Install updates" setting.
	Enabled bool `json:"enabled"`
	// Supported says this installation has an asset in a release and an
	// installer in this build. False draws no Download.
	Supported bool `json:"supported"`
	// Installation is how this KubeTower was installed.
	Installation Kind `json:"installation"`
	// Version and Asset are the release being downloaded or ready.
	Version string `json:"version,omitempty"`
	Asset   string `json:"asset,omitempty"`
	// Done and Total are the asset's bytes, while downloading.
	Done  int64 `json:"done,omitempty"`
	Total int64 `json:"total,omitempty"`
	// Reason is the one line of a failure.
	Reason string `json:"reason,omitempty"`
	// Folder holds the verified file, in the manual phase.
	Folder string `json:"folder,omitempty"`
}

// Plan is what an installer is handed: a verified file and what it replaces.
type Plan struct {
	File         string
	SHA256       string
	Version      string
	Installation Installation
	// Log is the install's step log (installLog), which a helper that outlives
	// the console appends to. Paths and exit codes only: never a token or a
	// URL.
	Log string
}

// Installer replaces the running KubeTower with plan.File and restarts it.
// On success it does not return: the process exits.
type Installer func(ctx context.Context, plan Plan) error

var (
	// ErrDisabled: the operator turned "Install updates" off.
	ErrDisabled = errors.New("installing updates is turned off")
	// ErrUnsupported: no asset or no installer for this installation.
	ErrUnsupported = errors.New("this installation cannot be updated from the console")
	// ErrBusy: a download or an install is already running.
	ErrBusy = errors.New("an update is already in progress")
	// ErrNotReady: Install was asked before a verified download.
	ErrNotReady = errors.New("no verified update is ready")
	// ErrNotNewer: the release is not newer than this build.
	ErrNotNewer = errors.New("the latest release is not newer than this build")
	// ErrNotRestarted: the new build is in place, but starting it failed. The
	// update is installed: the next start of KubeTower runs it.
	ErrNotRestarted = errors.New("the update is installed but KubeTower did not start again")
	// ErrNotWritable: the installer cannot write where KubeTower is installed.
	// The update ends in the manual phase rather than failed.
	ErrNotWritable = errors.New("the installation folder is not writable")
	// ErrNoFolder: Open folder was asked outside the manual phase.
	ErrNoFolder = errors.New("no verified file waits to be installed by hand")
)

// installers is filled by the per-platform files (#49, #50, #51).
var installers = map[Kind]Installer{}

// sweepers clean what an install cut short left beside an installation, at
// start. Filled by the platform files.
var sweepers = map[Kind]func(path string){}

// sweepBeside removes the entries beside path that match the patterns: the
// temporary names an install writes before its rename.
func sweepBeside(path string, patterns ...string) {
	for _, pattern := range patterns {
		found, _ := filepath.Glob(filepath.Join(filepath.Dir(path), pattern))
		for _, one := range found {
			_ = os.RemoveAll(one)
		}
	}
}

// installable says whether a kind's installer can run on this machine, for
// the kinds that need a tool (pkexec and a package manager): without it the
// installation is not supported, rather than failing after the download.
var installable = map[Kind]func() bool{}

const (
	latestRelease = "https://api.github.com/repos/thonicdev/kubetower/releases/latest"
	assetPrefix   = "https://github.com/thonicdev/kubetower/releases/download/"
	// MaxAsset caps a download whatever the release says.
	MaxAsset = 1 << 30
	maxSig   = 1 << 10
	// settingsFile holds the "Install updates" setting.
	settingsFile = "updater.json"
	workDir      = "update"
	// installLog is the last install's steps, in the state directory beside
	// the work folder rather than in it: forget removes the folder, and the
	// log is read after a failed install (#96).
	installLog = "update-install.log"
)

// ExitWait is how long Install waits, once the installer asked the console to
// stop, before it decides the console did not stop: the helper is stopped and
// the install fails, so "Installing…" never spins for ever (#96).
const ExitWait = 90 * time.Second

// abandonHelper stops a helper still waiting on this process, after ExitWait:
// left waiting, it would run the setup or a relaunch whenever the console
// exits later, unasked, and a second Install would start a second one. It
// also removes what that helper was to move into place. Each platform's
// installers set it; an error is a helper that could not be stopped.
var abandonHelper = func() error { return nil }

// helperLog is the running install's log, for a helper started deep in an
// installer (the Unix relaunch) that is not handed the plan.
var helperLog struct {
	sync.Mutex
	path string
}

func useLog(path string) {
	helperLog.Lock()
	helperLog.path = path
	helperLog.Unlock()
}

func currentLog() string {
	helperLog.Lock()
	defer helperLog.Unlock()
	return helperLog.path
}

// installsAfterExit says the helper, not this process, does the install, once
// the console has exited: Windows' setup and zip swap. Elsewhere the new build
// is already in place when the console is asked to stop, and only the restart
// waits.
var installsAfterExit = false

// errInstalledNotRestarted is the reason an install ends with when the new
// build is in place and the console did not stop to run it (#101).
var errInstalledNotRestarted = errors.New("the update is installed; quit KubeTower and start it again to run it")

// errHelperAlive is the reason an install ends with when its helper could not
// be stopped: it may still act when KubeTower exits.
var errHelperAlive = errors.New("the update's helper could not be stopped; it may still install when KubeTower exits")

// errBackToReady marks an installer's failure that installed nothing and left
// the verified file usable: the update goes back to ready with the reason,
// and Install is offered again without a new download (#101).
var errBackToReady = errors.New("nothing was installed")

// hosts a download may be redirected to: GitHub serves release assets from
// its own storage hosts.
var hosts = map[string]bool{
	"github.com":                           true,
	"api.github.com":                       true,
	"objects.githubusercontent.com":        true,
	"release-assets.githubusercontent.com": true,
}

// Options is everything the updater needs; the zero values are the real ones.
type Options struct {
	Version      string
	GOOS, GOARCH string
	Installation Installation
	// Dir is the state directory. Empty means statefile.Dir().
	Dir string
	// Endpoint, AssetPrefix and Hosts override GitHub, for tests only.
	Endpoint    string
	AssetPrefix string
	Hosts       map[string]bool
	Client      *http.Client
	// Verify checks a checksums.txt and its signature. Nil is the key built in.
	Verify func(sums, sig []byte) (releasesig.Sums, error)
	// Install overrides the platform's installer, for tests.
	Install Installer
	// ExitWait overrides ExitWait, for tests.
	ExitWait time.Duration
	Logf     func(string, ...any)
}

// Updater holds one update at a time.
type Updater struct {
	o Options

	mu      sync.Mutex
	status  Status
	planned Plan
}

// New reads the "Install updates" setting (on unless turned off).
func New(o Options) *Updater {
	u := &Updater{o: o}
	_, supported := AssetName("0.0.0", o.GOOS, o.GOARCH, o.Installation.Kind)
	u.status = Status{
		Phase:        Idle,
		Enabled:      u.readEnabled(),
		Supported:    supported && u.installer() != nil && u.canInstall(),
		Installation: o.Installation.Kind,
	}
	if sweep := sweepers[o.Installation.Kind]; sweep != nil && o.Install == nil && o.Installation.Path != "" {
		path := o.Installation.Path
		safego.Go("update sweep", func() error { sweep(path); return nil }, nil)
	}
	return u
}

// Status is the update's state now. Safe from any goroutine.
func (u *Updater) Status() Status {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.status
}

// SetEnabled writes the "Install updates" setting.
func (u *Updater) SetEnabled(on bool) (Status, error) {
	raw, _ := json.Marshal(struct {
		InstallUpdates bool `json:"installUpdates"`
	}{on})
	err := statefile.Write(filepath.Join(u.dir(), settingsFile), raw)
	u.mu.Lock()
	u.status.Enabled = on
	if !on && u.status.Phase != Installing && u.status.Phase != Downloading {
		u.forget()
	}
	out := u.status
	u.mu.Unlock()
	return out, err
}

// Download starts fetching and verifying the latest release's asset. It
// returns at once; Status follows it.
func (u *Updater) Download() (Status, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	switch {
	case !u.status.Enabled:
		return u.status, ErrDisabled
	case !u.status.Supported:
		return u.status, ErrUnsupported
	case u.status.Phase == Downloading || u.status.Phase == Verifying || u.status.Phase == Installing:
		return u.status, ErrBusy
	}
	u.forget()
	u.status.Phase = Downloading
	safego.Go("update download", func() error { u.download(); return nil }, func(err error) {
		// Only a panic gets here: download reports its own failures.
		if err != nil {
			u.fail(textutil.FirstLine(err.Error()))
		}
	})
	return u.status, nil
}

// Install hands the verified file to the platform's installer. It checks the
// file's hash and the version again first: the file sat on disk since.
func (u *Updater) Install(ctx context.Context) (Status, error) {
	u.mu.Lock()
	if !u.status.Enabled {
		defer u.mu.Unlock()
		return u.status, ErrDisabled
	}
	if u.status.Phase != Ready {
		defer u.mu.Unlock()
		return u.status, ErrNotReady
	}
	plan := u.planned
	u.status.Phase = Installing
	u.status.Reason = ""
	out := u.status
	u.mu.Unlock()

	plan.Log = u.startLog()
	u.step("install %s from %s into %s (%s)", plan.Version, filepath.Base(plan.File), plan.Installation.Path, plan.Installation.Kind)
	if !update.IsNewer(plan.Version, u.o.Version) {
		return u.fail(ErrNotNewer.Error()), ErrNotNewer
	}
	staged, err := u.stage(plan)
	if errors.Is(err, errChanged) {
		return u.fail("The downloaded file changed on disk since it was verified."), err
	}
	if err != nil {
		return u.fail(textutil.FirstLine(err.Error())), err
	}
	plan.File = staged
	u.step("staged %s", staged)
	useLog(plan.Log)
	install := u.installer()
	safego.Go("update install", func() error {
		return install(context.WithoutCancel(ctx), plan)
	}, func(err error) {
		switch {
		case err == nil:
			// The installer handed over and asked the console to stop. Either
			// the process exits within ExitWait, or the install failed.
			u.step("installer handed over; waiting for KubeTower to stop")
			u.awaitExit(plan.File)
		case errors.Is(err, errBackToReady):
			u.step("nothing installed: %s", textutil.FirstLine(err.Error()))
			u.backToReady(plan.File, textutil.FirstLine(err.Error()))
		case errors.Is(err, ErrNotRestarted):
			u.installed(err)
		case errors.Is(err, ErrNotWritable):
			u.manual(plan.File, err)
		default:
			u.fail(textutil.FirstLine(err.Error()))
		}
	})
	return out, nil
}

// errNotStopped is the reason an install fails when the console did not stop
// for its helper within ExitWait.
var errNotStopped = errors.New("KubeTower did not stop to install the update; nothing was installed")

// awaitExit fails the install, and stops the helper, when this process is
// still running ExitWait after the installer asked it to stop: an install
// that cannot finish leaves "Installing…" in bounded time, with Retry.
func (u *Updater) awaitExit(staged string) {
	wait := u.o.ExitWait
	if wait <= 0 {
		wait = ExitWait
	}
	safego.Go("update exit wait", func() error {
		time.Sleep(wait)
		if u.Status().Phase != Installing {
			return nil
		}
		if err := abandonHelper(); err != nil {
			// A helper still alive may yet install, or restart, when the console
			// exits: nothing it needs is removed, and no second Install is
			// offered beside it (#106).
			u.step("still running after %s; the helper could not be stopped: %s", wait, textutil.FirstLine(err.Error()))
			if u.o.Logf != nil {
				u.o.Logf("update: the helper could not be stopped: %s", textutil.FirstLine(err.Error()))
			}
			u.mu.Lock()
			if u.status.Phase == Installing {
				u.status.Phase = Failed
				u.status.Reason = errHelperAlive.Error()
			}
			u.mu.Unlock()
			return nil
		}
		u.step("still running after %s: helper stopped", wait)
		if installsAfterExit {
			// The helper never ran the install: the verified file is good.
			u.backToReady(staged, errNotStopped.Error())
			return nil
		}
		// The new build is already in place: only the restart did not happen.
		u.mu.Lock()
		if u.status.Phase == Installing {
			u.status.Phase = Failed
			u.status.Reason = errInstalledNotRestarted.Error()
		}
		u.mu.Unlock()
		u.step("installed in place; KubeTower did not stop to run it")
		return nil
	}, nil)
}

// backToReady puts the install back to ready, with the reason: the verified
// file is still there, so Install is offered again rather than a new download
// (#96: Check now and a failure must never lose a ready update). The copy
// staged for this install is removed; the next Install stages its own.
func (u *Updater) backToReady(staged, reason string) {
	if dir := filepath.Dir(staged); strings.HasPrefix(filepath.Base(dir), "install-") && filepath.Dir(dir) == filepath.Join(u.dir(), workDir) {
		_ = os.RemoveAll(dir)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.status.Phase != Installing {
		return
	}
	u.status.Phase = Ready
	u.status.Reason = reason
	u.step("back to ready, the verified file kept")
	if u.o.Logf != nil {
		u.o.Logf("update: %s", reason)
	}
}

// startLog empties the install log for a new install and returns its path.
func (u *Updater) startLog() string {
	path := filepath.Join(u.dir(), installLog)
	if err := os.WriteFile(path, nil, 0o600); err != nil && u.o.Logf != nil {
		u.o.Logf("update: the install log could not be written: %s", textutil.FirstLine(err.Error()))
	}
	return path
}

// step appends one line to the install log, stamped. Only what is written
// here and by the helper scripts goes in: versions, paths, process ids and
// exit codes - never a URL or a token.
func (u *Updater) step(format string, args ...any) {
	f, err := os.OpenFile(filepath.Join(u.dir(), installLog), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// stage copies the verified file into a folder made for this install, from
// one open handle, hashing what it writes. The installer (and pkexec, as root,
// for a package) reads that copy: nothing swapped at the checked path between
// the check and the install reaches it.
func (u *Updater) stage(plan Plan) (string, error) {
	in, err := os.Open(plan.File)
	if err != nil {
		return "", err
	}
	defer in.Close()
	dir, err := os.MkdirTemp(filepath.Join(u.dir(), workDir), "install-")
	if err != nil {
		return "", err
	}
	staged := filepath.Join(dir, filepath.Base(plan.File))
	out, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, h), io.LimitReader(in, MaxAsset+1))
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != plan.SHA256 {
		err = errChanged
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return staged, nil
}

// errChanged: the file no longer has the hash it was verified with.
var errChanged = errors.New("hash changed")

// canInstall is installable for this kind; an installer given in Options
// (tests) needs nothing.
func (u *Updater) canInstall() bool {
	if u.o.Install != nil {
		return true
	}
	ready := installable[u.o.Installation.Kind]
	return ready == nil || ready()
}

// manual: the installation's folder is not writable. The verified copy stays
// where it is, and Open folder shows it.
func (u *Updater) manual(file string, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.o.Logf != nil {
		u.o.Logf("update: %s", textutil.FirstLine(err.Error()))
	}
	u.step("not writable: the verified file waits in %s", filepath.Dir(file))
	u.status.Phase = Manual
	u.status.Folder = filepath.Dir(file)
	u.status.Reason = ""
}

// OpenFolder shows the folder that holds the verified file, in the manual
// phase, in the platform's file manager.
func (u *Updater) OpenFolder() (Status, error) {
	u.mu.Lock()
	status := u.status
	u.mu.Unlock()
	if status.Phase != Manual || status.Folder == "" {
		return status, ErrNoFolder
	}
	return status, openFolder(status.Folder)
}

// installed: the new build is in place but did not start. Not a failure: the
// update is done and the next start runs it, so the state goes back to idle.
func (u *Updater) installed(err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.o.Logf != nil {
		u.o.Logf("update: %s", textutil.FirstLine(err.Error()))
	}
	u.step("installed; not restarted: %s", textutil.FirstLine(err.Error()))
	u.forget()
}

func (u *Updater) installer() Installer {
	if u.o.Install != nil {
		return u.o.Install
	}
	return installers[u.o.Installation.Kind]
}

// download is the whole fetch, on its own goroutine.
func (u *Updater) download() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	plan, err := u.fetch(ctx)
	if err != nil {
		_ = os.RemoveAll(filepath.Join(u.dir(), workDir))
		u.fail(textutil.FirstLine(err.Error()))
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.planned = plan
	u.status.Phase = Ready
	u.status.Version = plan.Version
	u.status.Done = u.status.Total
}

type release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

func (u *Updater) fetch(ctx context.Context) (Plan, error) {
	var rel release
	if err := u.getJSON(ctx, u.endpoint(), &rel); err != nil {
		return Plan{}, fmt.Errorf("could not read the latest release: %w", err)
	}
	version := strings.TrimPrefix(strings.TrimSpace(rel.Tag), "v")
	if !update.IsNewer(version, u.o.Version) {
		return Plan{}, ErrNotNewer
	}
	name, ok := AssetName(version, u.o.GOOS, u.o.GOARCH, u.o.Installation.Kind)
	if !ok {
		return Plan{}, ErrUnsupported
	}
	find := func(want string) (string, int64, error) {
		for _, a := range rel.Assets {
			if a.Name == want {
				if !strings.HasPrefix(a.URL, u.assetPrefix()) {
					return "", 0, fmt.Errorf("%s is not served from this project's releases", want)
				}
				return a.URL, a.Size, nil
			}
		}
		return "", 0, fmt.Errorf("the release has no %s", want)
	}
	sumsURL, _, err := find("checksums.txt")
	if err != nil {
		return Plan{}, err
	}
	sigURL, _, err := find("checksums.txt.sig")
	if err != nil {
		return Plan{}, fmt.Errorf("the release is not signed: %w", err)
	}
	assetURL, size, err := find(name)
	if err != nil {
		return Plan{}, err
	}
	if size <= 0 || size > MaxAsset {
		return Plan{}, fmt.Errorf("%s declares %d bytes", name, size)
	}
	u.mu.Lock()
	u.status.Version, u.status.Asset, u.status.Total = version, name, size
	u.mu.Unlock()

	sums, err := u.getSmall(ctx, sumsURL, releasesig.MaxSums)
	if err != nil {
		return Plan{}, fmt.Errorf("could not read checksums.txt: %w", err)
	}
	sig, err := u.getSmall(ctx, sigURL, maxSig)
	if err != nil {
		return Plan{}, fmt.Errorf("could not read checksums.txt.sig: %w", err)
	}
	verified, err := u.verify(sums, sig)
	if err != nil {
		return Plan{}, err
	}
	want, err := verified.SHA256(name)
	if err != nil {
		return Plan{}, err
	}

	dir := filepath.Join(u.dir(), workDir)
	if err := os.RemoveAll(dir); err != nil {
		return Plan{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Plan{}, err
	}
	part := filepath.Join(dir, name+".part")
	got, err := u.save(ctx, assetURL, part, size)
	if err != nil {
		return Plan{}, err
	}
	u.mu.Lock()
	u.status.Phase = Verifying
	u.mu.Unlock()
	if got != want {
		return Plan{}, fmt.Errorf("%s does not match its signed sha256", name)
	}
	final := filepath.Join(dir, name)
	if err := os.Rename(part, final); err != nil {
		return Plan{}, err
	}
	return Plan{File: final, SHA256: want, Version: version, Installation: u.o.Installation}, nil
}

func (u *Updater) verify(sums, sig []byte) (releasesig.Sums, error) {
	if u.o.Verify != nil {
		return u.o.Verify(sums, sig)
	}
	return releasesig.VerifyEmbedded(sums, sig)
}

// save streams url into path, hashing as it goes, refusing more than size.
func (u *Updater) save(ctx context.Context, url, path string, size int64) (string, error) {
	resp, err := u.get(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	counted := &progress{u: u}
	n, err := io.Copy(io.MultiWriter(out, hash, counted), io.LimitReader(resp.Body, size+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if n != size {
		return "", fmt.Errorf("received %d bytes, the release declares %d", n, size)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type progress struct{ u *Updater }

func (p *progress) Write(b []byte) (int, error) {
	p.u.mu.Lock()
	p.u.status.Done += int64(len(b))
	p.u.mu.Unlock()
	return len(b), nil
}

func (u *Updater) getJSON(ctx context.Context, url string, into any) error {
	raw, err := u.getSmall(ctx, url, 1<<20)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}

func (u *Updater) getSmall(ctx context.Context, url string, limit int64) ([]byte, error) {
	resp, err := u.get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("more than %d bytes", limit)
	}
	return raw, nil
}

func (u *Updater) get(ctx context.Context, raw string) (*http.Response, error) {
	if err := u.allowed(raw); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "kubetower")
	resp, err := u.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("answered %d", resp.StatusCode)
	}
	return resp, nil
}

// allowed holds every request and redirect to HTTPS on GitHub's hosts.
func (u *Updater) allowed(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	allowed := u.o.Hosts
	if allowed == nil {
		allowed = hosts
		if parsed.Scheme != "https" {
			return fmt.Errorf("refused %s: not https", parsed.Host)
		}
	}
	if !allowed[parsed.Hostname()] && !allowed[parsed.Host] {
		return fmt.Errorf("refused %s: not a GitHub release host", parsed.Host)
	}
	return nil
}

func (u *Updater) client() *http.Client {
	base := u.o.Client
	if base == nil {
		base = &http.Client{Timeout: time.Hour}
	}
	c := *base
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return u.allowed(req.URL.String())
	}
	return &c
}

func (u *Updater) endpoint() string {
	if u.o.Endpoint != "" {
		return u.o.Endpoint
	}
	return latestRelease
}

func (u *Updater) assetPrefix() string {
	if u.o.AssetPrefix != "" {
		return u.o.AssetPrefix
	}
	return assetPrefix
}

// dir is the state directory, absolute: pkexec runs from root's home, and
// apt-get reads a relative "a/b.deb" as a package name.
func (u *Updater) dir() string {
	dir := u.o.Dir
	if dir == "" {
		dir = statefile.Dir()
	}
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

func (u *Updater) readEnabled() bool {
	raw, err := os.ReadFile(filepath.Join(u.dir(), settingsFile))
	if err != nil {
		return true
	}
	var held struct {
		InstallUpdates *bool `json:"installUpdates"`
	}
	if json.Unmarshal(raw, &held) != nil || held.InstallUpdates == nil {
		return true
	}
	return *held.InstallUpdates
}

// forget drops a download, under u.mu.
func (u *Updater) forget() {
	_ = os.RemoveAll(filepath.Join(u.dir(), workDir))
	u.planned = Plan{}
	u.status.Phase, u.status.Version, u.status.Asset = Idle, "", ""
	u.status.Done, u.status.Total, u.status.Reason, u.status.Folder = 0, 0, "", ""
}

func (u *Updater) fail(reason string) Status {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.status.Phase == Installing {
		u.step("failed: %s", reason)
	}
	u.status.Phase = Failed
	u.status.Reason = reason
	if u.o.Logf != nil {
		u.o.Logf("update: %s", reason)
	}
	return u.status
}
