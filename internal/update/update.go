// Package update asks GitHub, once a day at most, whether a newer KubeTower
// release exists, and says so. It never downloads or installs anything:
// internal/updater does, on the desktop, when the operator asks, and only what
// a signed checksums.txt vouches for.
//
// A desktop binary has no package manager telling the operator it is four
// releases old. This offers a version, a link and a sentence, and the operator
// decides. Installing needs a signing story, and half of one is worse than
// none: the release's checksums.txt is signed (internal/releasesig), and
// internal/updater installs nothing that signature does not cover.
//
//   - Desktop only: a pod calling github.com on boot meets air gaps and egress
//     policy and reaches out for operators who did not ask; the in-cluster
//     build is upgraded by its image tag (InContainer).
//   - It never blocks or delays startup: Check runs on its own goroutine after
//     the server listens, and every failure is a sentence, never an exit.
//   - Offline and up to date are different States with different sentences.
//   - A development build is never told it is out of date: `dev` cannot be
//     ordered against a release.
//   - No telemetry: one unauthenticated GET with no query string, no instance
//     identifier and no version in the User-Agent.
//
// It is not asked on every start, or a restart loop would be rate-limited: the
// last attempt goes in update.json in the state directory, beside every other
// file the console writes.
package update

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thonicdev/kubetower/internal/statefile"
)

// Repository is where KubeTower lives: the link the Infos tab draws beside the
// version, and the repository the endpoint below names.
const Repository = "https://github.com/thonicdev/kubetower"

// latestRelease is GitHub's own answer to "what is the newest release": the
// most recent full release, never a draft or pre-release, which `/releases`
// would include.
const latestRelease = "https://api.github.com/repos/thonicdev/kubetower/releases/latest"

// cacheFile sits beside credentials.json, version.json and the rest.
const cacheFile = "update.json"

// How long an answer is kept before asking again. A success is good for a day;
// a failure only for an hour, so an operator who comes back online is not told
// "could not check" until tomorrow. Caching the failure at all stops a restart
// loop with no network from producing a DNS timeout per start.
const (
	goodFor    = 24 * time.Hour
	retryAfter = time.Hour
)

// RecheckEvery is how often a running console asks itself whether its answer
// has aged; the windows, not this tick, decide whether GitHub is asked.
//
// A quarter of the shorter window: an attempt is dated a moment after its tick,
// so a tick equal to the window would find a failure just under an hour old and
// retry it after two. This bounds the lateness at one tick.
const RecheckEvery = retryAfter / 4

// maxBody is what we are willing to read from api.github.com. A release is a
// few kilobytes, but an unbounded read from a host we do not control risks the
// process's memory.
const maxBody = 1 << 20

// State is which of the five answers this is. Three of them answer a question
// that could not be asked, and none of those may be collapsed into Current.
type State string

const (
	// Off: the operator turned the check off. No request was made.
	Off State = "off"
	// NotApplicable: nothing to compare. A development build, or a console in a
	// cluster, upgraded by its image tag.
	NotApplicable State = "notApplicable"
	// Current: asked, answered, and this build is the latest release.
	Current State = "current"
	// Newer: asked, answered, and a newer release exists.
	Newer State = "newer"
	// Failed: asked and could not find out (offline, refused, rate-limited or
	// unreadable). Not Current, and must not be drawn like it.
	Failed State = "failed"
)

// Report is what the Settings page is told. The sentence is composed here so
// the start-up log and the Infos tab say the same thing.
type Report struct {
	State State `json:"state"`
	// Latest is the release GitHub named, without its leading `v`. Empty unless
	// State is Current or Newer.
	Latest string `json:"latest,omitempty"`
	// ReleaseURL is that release's own page. Empty unless there is a release to
	// point at.
	ReleaseURL string `json:"releaseUrl,omitempty"`
	// CheckedAt is when the request that produced this was made. Zero when no
	// request was made (every Off and NotApplicable report), so the page does
	// not date them.
	CheckedAt time.Time `json:"checkedAt,omitempty"`
	// Note is the one sentence about this outcome.
	Note string `json:"note,omitempty"`
}

// Options is everything the checker needs. The environment is read once, in
// internal/launch, so a test can set the answer without setting the machine.
type Options struct {
	// Version is what this binary calls itself (`0.2.1` or `dev`), not the whole
	// `--version` line.
	Version string
	// Development says nothing stamped this binary. Handed over rather than
	// derived from Version: internal/launch owns that decision.
	Development bool
	// InContainer says this is the in-cluster build.
	InContainer bool
	// Enabled is the operator's switch, honored before any request is made.
	Enabled bool

	// Dir is the state directory. Empty means statefile.Dir().
	Dir string
	// Endpoint overrides latestRelease. For tests only.
	Endpoint string
	// Client is the HTTP client. Nil means one with a short timeout.
	Client *http.Client
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
	// Logf is where the outcome is said out loud. Nil means silence.
	Logf func(string, ...any)
}

// Checker holds the last answer and can be asked for it at any time. Check
// writes it, Report reads it.
type Checker struct {
	opts Options

	mu     sync.RWMutex
	report Report

	// run holds one check at a time: the startup check, the scheduled one and
	// "Check now" may overlap, and serialized a second one reads the cache the
	// first just wrote; a forced one asks again only after the first finishes.
	run sync.Mutex
	// checking is whether a request to GitHub is in flight, drawn as "Checking
	// for updates…".
	checking atomic.Bool
}

// New builds a checker and settles the answers that need no network.
//
// The report is correct before Check has run: a Settings page opened in the
// first second must not draw a zero-value state. Off and NotApplicable are
// known here.
func New(o Options) *Checker {
	c := &Checker{opts: o}
	switch {
	case !o.Enabled:
		c.report = Report{State: Off, Note: "The update check is turned off, so nothing was asked."}
	case o.InContainer:
		c.report = Report{State: NotApplicable, Note: "This console runs in a cluster and is " +
			"upgraded by its image tag, so it does not check for releases."}
	case o.Development:
		c.report = Report{State: NotApplicable, Note: "A development build carries no version " +
			"to compare against a release."}
	default:
		c.report = Report{State: Failed, Note: "The check has not run yet."}
	}
	return c
}

// Report is the last answer. Safe from any goroutine.
func (c *Checker) Report() Report {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.report
}

// Check asks, unless something says not to.
//
// The order of the guards is the contract: Off is answered before the cache is
// read and before a socket is opened, so "turned off" means no request, not a
// request whose answer is discarded.
//
// It returns nothing: every outcome is a Report, so a version check can never
// break a startup.
func (c *Checker) Check(ctx context.Context) {
	c.check(ctx, false, true)
}

// CheckNow is Settings › Infos' "Check now": the same check past the cache's
// window, returning the answer it settled on.
//
// The guards still come first, so the button is never a way round
// `KT_UPDATE_CHECK=0`; only the freshness test is skipped.
func (c *Checker) CheckNow(ctx context.Context) Report {
	c.check(ctx, true, true)
	return c.Report()
}

// Checking is whether a request to GitHub is in flight right now.
//
// Read it before Report: the answer is stored before this clears, so false here
// means the report read next is the settled one. In the other order a page
// polling only while a check runs could stop on the old answer.
func (c *Checker) Checking() bool {
	return c.checking.Load()
}

// Watch re-checks every `every` until ctx is done. It is meant to run on its
// own goroutine, after Check.
//
// The cache decides, not the tick: each tick is an ordinary Check, so GitHub is
// asked no more often than the windows allow, and a console open for a week
// still hears of a new release. A tick that only re-read the cache is not
// logged.
//
// Where the check can never ask (off, in a container, a development build),
// Watch returns at once and runs no timer.
func (c *Checker) Watch(ctx context.Context, every time.Duration) {
	if every <= 0 || !c.asks() {
		return
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	c.watch(ctx, ticker.C, nil)
}

// watch is Watch's loop, with the ticks handed in so a test can drive them
// against a fake clock. `ticked`, when not nil, is told after each tick.
func (c *Checker) watch(ctx context.Context, ticks <-chan time.Time, ticked chan<- struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			c.check(ctx, false, false)
			if ticked != nil {
				// Bounded by ctx, so a test that stops reading does not leave
				// the loop parked here.
				select {
				case ticked <- struct{}{}:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// asks is whether this checker may ever make a request: on, outside a cluster,
// and on a stamped build. Check, CheckNow and Watch share it.
func (c *Checker) asks() bool {
	return c.opts.Enabled && !c.opts.InContainer && !c.opts.Development
}

// check is the one body behind Check, CheckNow and Watch. `force` skips the
// freshness test; `sayCached` logs an answer read from the cache as well as one
// asked for.
func (c *Checker) check(ctx context.Context, force, sayCached bool) {
	if !c.asks() {
		if sayCached {
			c.say()
		}
		return
	}

	c.run.Lock()
	defer c.run.Unlock()

	if !force {
		held, ok := c.cached()
		if ok && c.fresh(held) {
			c.set(c.reread(held))
			if sayCached {
				c.say()
			}
			return
		}
	}

	c.checking.Store(true)
	report := c.ask(ctx)
	if report.State == Failed && ctx.Err() != nil {
		// A canceled request (a page reloaded, the process stopping) found
		// nothing out. Recorded, it would replace a good answer with "could
		// not reach GitHub" for an hour, so the last answer stays.
		c.checking.Store(false)
		return
	}
	// Stored before `checking` clears, so a reader that sees it false then
	// reads this answer (see Checking).
	c.set(report)
	c.checking.Store(false)
	c.record(report)
	c.say()
}

// fresh reports whether a cached attempt is still worth believing.
func (c *Checker) fresh(held cache) bool {
	if held.CheckedAt.IsZero() {
		return false
	}
	window := goodFor
	if held.State == string(Failed) {
		window = retryAfter
	}
	return c.now().Sub(held.CheckedAt) < window
}

// ask makes the one request.
func (c *Checker) ask(ctx context.Context) Report {
	at := c.now()
	endpoint := c.opts.Endpoint
	if endpoint == "" {
		endpoint = latestRelease
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Report{State: Failed, CheckedAt: at,
			Note: "Could not check for a newer release: " + err.Error()}
	}
	// GitHub asks for Accept and the version pin and requires a User-Agent,
	// which carries the product name alone: a version there would make a
	// public read a report of which build is running.
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "kubetower")

	response, err := c.client().Do(request)
	if err != nil {
		// Offline is the ordinary case; the sentence says it is not a
		// confirmation that the build is current.
		return Report{State: Failed, CheckedAt: at,
			Note: "Could not reach GitHub, so whether a newer release exists is unknown."}
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return Report{State: Failed, CheckedAt: at, Note: refusal(response.StatusCode)}
	}

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBody))
	if err != nil {
		return Report{State: Failed, CheckedAt: at,
			Note: "GitHub answered and the answer could not be read in full."}
	}
	var answered struct {
		Tag  string `json:"tag_name"`
		Page string `json:"html_url"`
	}
	if err := json.Unmarshal(raw, &answered); err != nil || strings.TrimSpace(answered.Tag) == "" {
		return Report{State: Failed, CheckedAt: at,
			Note: "GitHub answered with something that is not a release."}
	}

	latest := strings.TrimPrefix(strings.TrimSpace(answered.Tag), "v")
	page := strings.TrimSpace(answered.Page)
	if page == "" {
		page = Repository + "/releases/latest"
	}

	return c.verdict(latest, page, at)
}

// verdict compares the release GitHub named with this build, for a fresh
// answer and for a cached one (reread).
func (c *Checker) verdict(latest, page string, at time.Time) Report {
	switch newer(latest, c.opts.Version) {
	case true:
		return Report{State: Newer, Latest: latest, ReleaseURL: page, CheckedAt: at,
			Note: "KubeTower " + latest + " has been released. This build is " +
				c.opts.Version + "."}
	default:
		// Equal, earlier, or a pair that cannot be ordered: none of them is
		// "a newer release exists".
		return Report{State: Current, Latest: latest, ReleaseURL: page, CheckedAt: at,
			Note: "This is the latest release."}
	}
}

// reread is what a fresh cached attempt says to this build.
//
// Which release is newest is GitHub's answer, good for the window whoever
// asked; whether it is newer than the running binary is about the build that
// asked. After an upgrade within the window, the cached verdict would tell the
// new build it is out of date, so the release is compared again here, without
// a request. A failure is about GitHub and is served as it is.
func (c *Checker) reread(held cache) Report {
	switch State(held.State) {
	case Current, Newer:
		if held.Latest != "" {
			page := held.ReleaseURL
			if page == "" {
				page = Repository + "/releases/latest"
			}
			return c.verdict(held.Latest, page, held.CheckedAt)
		}
	}
	return held.Report()
}

// refusal turns a status code into a sentence an operator can act on.
//
// A 404 is what an unauthenticated client gets for a private repository, so it
// often means "not visible" rather than "gone".
func refusal(code int) string {
	switch code {
	case http.StatusNotFound:
		return "GitHub has no published release for this repository, or it is not visible " +
			"to an unauthenticated client."
	case http.StatusForbidden, http.StatusTooManyRequests:
		return "GitHub declined the request, most likely its rate limit for unauthenticated " +
			"clients. The check will be tried again later."
	default:
		return "GitHub answered " + strconv.Itoa(code) + ", so whether a newer release " +
			"exists is unknown."
	}
}

// say puts the outcome in the log, once per start.
//
// In the log as well as on the page: the log is where an operator is sent, and
// silence at startup would be indistinguishable from never running.
func (c *Checker) say() {
	if c.opts.Logf == nil {
		return
	}
	report := c.Report()
	if report.Note == "" {
		return
	}
	c.opts.Logf("update check: %s", report.Note)
}

func (c *Checker) set(r Report) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.report = r
}

func (c *Checker) now() time.Time {
	if c.opts.Now != nil {
		return c.opts.Now()
	}
	return time.Now()
}

// client is the one this checker uses, with a ten-second timeout over the whole
// exchange: http.DefaultClient has none, and a goroutine parked on a half-open
// connection is never found.
func (c *Checker) client() *http.Client {
	if c.opts.Client != nil {
		return c.opts.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// cache is what update.json holds: the last attempt and what it produced.
type cache struct {
	CheckedAt  time.Time `json:"checkedAt"`
	State      string    `json:"state"`
	Latest     string    `json:"latest,omitempty"`
	ReleaseURL string    `json:"releaseUrl,omitempty"`
	Note       string    `json:"note,omitempty"`
}

func (c cache) Report() Report {
	return Report{
		State:      State(c.State),
		Latest:     c.Latest,
		ReleaseURL: c.ReleaseURL,
		CheckedAt:  c.CheckedAt,
		Note:       c.Note,
	}
}

func (c *Checker) path() string {
	dir := c.opts.Dir
	if dir == "" {
		dir = statefile.Dir()
	}
	return filepath.Join(dir, cacheFile)
}

// cached reads the last attempt. An unreadable file is treated as no file:
// getting it wrong costs one extra request.
func (c *Checker) cached() (cache, bool) {
	raw, err := os.ReadFile(c.path())
	if err != nil {
		return cache{}, false
	}
	var held cache
	if err := json.Unmarshal(raw, &held); err != nil {
		return cache{}, false
	}
	if held.State == "" {
		return cache{}, false
	}
	return held, true
}

// record writes the attempt, and says so if it could not.
//
// A failure to write is logged, never returned: asking twice beats a console
// that will not start.
func (c *Checker) record(r Report) {
	raw, err := json.MarshalIndent(cache{
		CheckedAt:  r.CheckedAt,
		State:      string(r.State),
		Latest:     r.Latest,
		ReleaseURL: r.ReleaseURL,
		Note:       r.Note,
	}, "", "  ")
	if err != nil {
		return
	}
	if err := statefile.Write(c.path(), raw); err != nil && c.opts.Logf != nil {
		c.opts.Logf("update check: its timestamp could not be written to %s (%v), "+
			"so the next start will ask again", c.path(), err)
	}
}

// EnvEnabled is the variable that turns the update check off.
const EnvEnabled = "KT_UPDATE_CHECK"

// EnabledFromEnv reads the off switch. On by default: nobody sets a variable
// for a feature they do not know exists. `0`, `false`, `no` and `off`, in any
// case, turn it off; anything else leaves it on, the unsurprising direction.
func EnabledFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvEnabled))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// newer reports whether `latest` is a later release than `running`.
//
// Its own parser rather than statefile's: GitHub may still name an early
// `v`-prefixed tag, while a state stamp never has one. The shared rule is that
// anything that is not three numbers compares as no opinion.
// IsNewer says whether the release latest is newer than the running version,
// as the check decides it: false for any pair that cannot be ordered, such as
// a development build. internal/updater asks it again before installing.
func IsNewer(latest, running string) bool { return newer(latest, running) }

func newer(latest, running string) bool {
	left, ok := triple(latest)
	if !ok {
		return false
	}
	right, ok := triple(running)
	if !ok {
		return false
	}
	for i := range left {
		switch {
		case left[i] > right[i]:
			return true
		case left[i] < right[i]:
			return false
		}
	}
	return false
}

// triple reads `major.minor.patch`, with an optional leading `v` and anything
// after a `-` or `+` discarded: a pre-release or build suffix is not ordered.
func triple(version string) ([3]int, bool) {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if i := strings.IndexAny(version, "-+ \t("); i >= 0 {
		version = version[:i]
	}
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}
