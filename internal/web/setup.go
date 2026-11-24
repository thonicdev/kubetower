package web

import (
	"context"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/updater"
)

// SetupInfo is what `internal/launch` tells this package about its own
// installation, decided once at startup. It feeds the Settings page's Infos tab
// and its version line.
type SetupInfo struct {
	// StateDir is where credentials.json and the rest of the state live.
	StateDir string

	// StateEphemeral is the deployment saying StateDir does not survive a restart
	// (`KT_STATE_PERSISTENT=false`, from the chart's `persistence.enabled`). The
	// console cannot tell an emptyDir from a claim, so it does not guess. It takes
	// the archive and the node-shell image off the Settings page and their writes
	// off the router; false, the zero value, is persistent.
	StateEphemeral bool

	// Version is the whole line `kubetower --version` prints, composed by
	// `versionLine` in internal/launch and handed over as one opaque string, so the
	// page and `--version` cannot disagree. Empty means the caller did not say, and
	// the page draws no build section.
	Version string

	// Development says no version was stamped into this binary (a `go build`, `go
	// run`, or an image built without `KT_VERSION`). A separate flag, not parsed
	// from Version: `developmentBuild` in internal/launch is the one place that
	// knows.
	Development bool

	// Kubeconfig is the kubeconfig files this process read, in precedence order,
	// with what each declared and which names one file took from another. A context
	// declared twice exists once, the second file's entry silently discarded, so the
	// page names it as well as the scrolling startup log. It is the survey taken at
	// startup, which every client and URL was built from.
	Kubeconfig cluster.KubeconfigSurvey

	// StateWrittenBy is the version that last wrote the state directory, and
	// StateIsNewer says it is newer than the running build. Shown on the page
	// because the failure is silent: an older build meeting a newer format loads
	// defaults.
	StateWrittenBy string
	StateIsNewer   bool

	// RepositoryURL is where KubeTower lives, drawn beside the version. Handed over,
	// since the update check asks the same repository.
	RepositoryURL string

	// DocumentationURL is the public documentation, the Infos tab's third link.
	// Handed over because the desktop's Help menu opens the same address.
	DocumentationURL string

	// ListenAddress is the address the console is bound to, not KT_LISTEN as typed
	// (which may name two on a dual-stack machine). Empty when the caller did not
	// say.
	ListenAddress string

	// WithoutPassword says this console asks nobody for a password: the loopback
	// desktop, whose windows open signed in.
	WithoutPassword bool

	// Updates is the update check, asked afresh every time the page is read: the
	// check runs after the server is listening, so it never delays the health page,
	// and a snapshot would stay the pre-check answer. Nil means nothing checks, and
	// the page draws no update line.
	Updates func() UpdateReport

	// CheckUpdates is Check now: the same check past its cache, answering once it
	// has settled. Nil where nothing checks, and the route is not registered.
	CheckUpdates func(context.Context) UpdateReport

	// Installer downloads and installs an update (#47), on the desktop only.
	// Nil where nothing installs: the routes are then not registered.
	Installer UpdateInstaller

	// OpenWork counts the terminals and port-forwards a restart would end.
	OpenWork func() (terminals, forwards int)

	// ToolWindows draws a forward's flow UI in a desktop window (#65), on a
	// desktop that can draw a native window. Nil elsewhere: the route is then
	// not registered and `capabilities.toolWindow` is false.
	ToolWindows api.ToolWindows
}

// UpdateInstaller is internal/updater.Updater, as the Settings page drives it.
type UpdateInstaller interface {
	Status() updater.Status
	SetEnabled(on bool) (updater.Status, error)
	Download() (updater.Status, error)
	Install(ctx context.Context) (updater.Status, error)
	OpenFolder() (updater.Status, error)
}

// UpdateReport is what SetupInfo.Updates answers with, declared here so a caller
// with no checker can return one without importing internal/update.
type UpdateReport struct {
	// State is `off`, `notApplicable`, `current`, `newer` or `failed`; `failed` is
	// not `current`. See api.SettingsUpdate.
	State      string
	Latest     string
	ReleaseURL string
	// CheckedAt is zero when no request was made: every `off` and `notApplicable`.
	CheckedAt time.Time
	Note      string
	// Checking is whether a request is in flight now.
	Checking bool
}
