// Package health says which clusters answer, and when they do not, who fixes it.
//
// It is the page an operator opens first, and the reason is not that it lists
// clusters — the rail does that. It is that a
// Kubernetes client failure is four unrelated problems wearing the same coat.
// Raw, they all read as "the cluster is broken":
//
//	Get "https://…/api/v1/…": dial tcp: lookup x on 127.0.0.11:53: no such host
//	error: You must be logged in to the server (Unauthorized)
//	An error occurred (AccessDenied) when calling the AssumeRole operation
//
// The first is a name that does not resolve from inside the container — a
// networking question. The second is almost always an SSO session that lapsed —
// the operator renews it in ten seconds. The third is an IAM grant this tool
// cannot influence at all, and which fails identically from the host, so time
// spent debugging KubeTower is time wasted. Three different people, three
// different fixes, one indistinguishable red row.
//
// Classifying them is the entire value of this package.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/discovery"

	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/safego"
	"github.com/thonicdev/kubetower/textutil"
)

// State is what happened when we asked, in the order a reader cares about.
const (
	StateOK      = "ok"      // the API server answered
	StateSSO     = "sso"     // the SSO session lapsed — the operator renews it
	StateIAM     = "iam"     // assume-role refused — an IAM grant, not our fault
	StateDNS     = "dns"     // the name does not resolve from here
	StateRefused = "refused" // something answered the address and said no
	StateTimeout = "timeout" // nothing answered in time — VPN, firewall, or down
	StatePlugin  = "plugin"  // the credential plugin ran and refused; its own words are not here
	StateMissing = "missing" // the credential plugin's binary is not on this process's PATH
	StateKO      = "ko"      // anything else; the first line of the error is kept
)

// StateMissing is separate from StatePlugin and the distinction is the whole
// value of both.
//
// StatePlugin means the binary was found, ran, and refused — an expired session,
// a refused assume-role, a corrupt credential cache — and its own words are not
// in the error at all (client-go passes only the exit code). StateMissing means
// there was no binary to run. Those are different people's problems and
// different sentences: one is "check your session, then your IAM", the other is
// "install it, or this process was handed a PATH that cannot see it". Folded
// together, the second reads as the first.

// Cluster is one row.
//
// It carries no DNS-friendly form of Name: links percent-encode the name
// (internal/web.clusterHome) and only *accept* the DNS-friendly form on the way
// in, through cluster.Manager.Resolve. If a link ever needs the transformed
// name, it is one call away at the point of use.
type Cluster struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	Detail   string `json:"detail"`
	ReadOnly bool   `json:"readOnly"`

	// Version of the API server, when it answered. Not decoration: a console
	// pointed at a cluster three minors ahead fails on APIs that moved, and the
	// failure looks like a bug in the console.
	Version string `json:"version,omitempty"`

	// Metrics is the backend behind the charts, so an empty chart has an
	// explanation instead of looking broken.
	Metrics string `json:"metrics,omitempty"`

	CheckedAt time.Time `json:"checkedAt"`
	Took      string    `json:"took,omitempty"`

	// Command renews this context's credentials where that is one command the
	// operator runs: `aws sso login --profile p` on an AWS context whose SSO
	// session lapsed. Empty everywhere else.
	Command string `json:"command,omitempty"`
}

// OK is a convenience for templates, which should not compare strings.
func (c Cluster) OK() bool { return c.State == StateOK }

// Actionable reports whether the operator can do something about this row
// themselves. An IAM refusal cannot be fixed from this machine; a lapsed SSO
// session can, in one command.
//
// StateMissing is one: a credential plugin this process cannot find is fixed
// by installing it or by starting the console where it can be seen, both of
// which are the operator's and neither of which is a ticket.
func (c Cluster) Actionable() bool {
	return c.State == StateSSO || c.State == StateMissing
}

// SSO session states.
//
// The middle one is why this is not a boolean, and getting it wrong sends the
// operator to fix something that is not broken: an access token past its expiry
// with a refresh token behind it is renewed silently by the AWS CLI on the next
// call. The clusters keep answering. Telling anyone to log in at that point is
// noise that teaches them to ignore the banner.
const (
	SSOValid     = "valid"
	SSORenewable = "renewable"
	SSOExpired   = "expired"
	SSOAbsent    = "absent"
)

// Session is what the AWS CLI left in its cache. EKS contexts authenticate
// through it, so its state is the commonest cause of a cluster going dark — and
// the one thing no Kubernetes API can report, because the failure happens before
// any Kubernetes call is made.
type Session struct {
	// Checked says this process actually got to look. False means it could not,
	// and that is a third answer rather than a flavor of "no session".
	//
	// Answering `Session{State: SSOAbsent}` for *every* os.ReadDir failure would
	// make a `~/.aws/sso/cache` that exists and will not list arrive at
	// internal/web as "there is no session", which bannerOf reads as nothing to
	// say. The banner naming `aws sso login` would then be removed by the very
	// failure that makes it necessary — and removed in the *reassuring*
	// direction, on the one page whose whole value is being believed. The
	// trigger is ordinary rather than exotic: the operator's `~/.aws` is mounted
	// into the container read-write so that SSO renewal is shared, and a uid
	// mismatch on that mount produces exactly this while every EKS context fails
	// inside `aws eks get-token`.
	//
	// An empty answer and a failed one are different answers, and
	// credcache.go next door carries the same field for the same reason.
	//
	// A directory that is simply *not there* is Checked: that is the ordinary
	// state of an installation with no SSO profiles, and it is an answer.
	Checked   bool      `json:"checked"`
	Found     bool      `json:"found"`
	State     string    `json:"state"`
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
	Session   string    `json:"session,omitempty"`

	// Dir is where this looked and Problem is why it could not, both empty when
	// nothing went wrong. Reported rather than summarized for the reason
	// CredentialCache reports its own Dir: "the SSO cache could not be read"
	// without the path is a sentence the operator cannot act on.
	Dir     string `json:"dir,omitempty"`
	Problem string `json:"problem,omitempty"`
}

// Expired reports whether the operator must act. Renewable is deliberately not
// expired: see the constants above.
func (s Session) Expired() bool { return s.Found && s.State == SSOExpired }

// Renewable is a session whose own expiry has passed while its refresh token and
// client registration are still good. The AWS CLI is supposed to renew it on the
// next call without anyone being told; whether it actually has is a different
// question, and one only the clusters can answer.
func (s Session) Renewable() bool { return s.Found && s.State == SSORenewable }

// Unknown is the third answer: nobody could tell. A method rather than a bare
// `!Checked` at the call site, because a template must not have to know that the
// absence of a fact is spelled as the negation of another one.
func (s Session) Unknown() bool { return !s.Checked }

// Metrics is the slice of the metrics layer this package needs. An interface
// rather than the package, so a cluster overview does not drag metric discovery
// into every test that wants a row.
type Metrics interface {
	Discover(ctx context.Context, cluster string, force bool) string
	// Cached answers from what is already known and never probes. The background
	// round uses it: discovering Prometheus means standing up a port-forward, and
	// this page prints the backend's name in a cell rather than querying it.
	// Empty means nothing is known yet, which the page must render as unknown and
	// not as none.
	Cached(cluster string) string
}

// Health probes clusters and remembers the answers for a while.
// defaultFailedTTL is how long a failure is believed.
//
// Five seconds: long enough that fourteen contexts are not re-probed on every
// fragment of a page being drawn, short enough that renewing an SSO session and
// switching back to the tab shows the cluster answering rather than a minute of
// a stale refusal.
const defaultFailedTTL = 5 * time.Second

// backgroundFailedMax bounds how often the *background* round re-probes a cluster
// that keeps failing. Foreground requests are untouched by it.
//
// The readiness probe warms every 60 s; a failure is stale after
// defaultFailedTTL, so without this all of it would be re-probed on every warm.
// A context whose `aws eks get-token` exits 255 costs an `/usr/bin/aws` Python
// interpreter start-up of 0.53–0.59 s of CPU before it does anything. Three of
// those a minute is **2.7–3.4 % of a core, permanently, with nobody looking at
// the UI**, for three failures re-tried forever.
//
// internal/recorder does the same, and its comment says why: "an expired SSO session fails identically every two
// minutes for as long as the operator is away from their desk". Ten minutes rather
// than that package's hour, because this feeds a page whose entire job is to say
// which clusters are down.
const backgroundFailedMax = 10 * time.Minute

type Health struct {
	clusters *cluster.Manager
	metrics  Metrics
	readOnly func(string) bool
	awsDir   string
	// credDir overrides where the assume-role cache is looked for. Empty means
	// derive it from awsDir — see CredentialCacheDir. An option only so a test can
	// point it somewhere writable.
	credDir string
	// hostCredDir overrides the operator's own cache directory. Empty means read
	// KT_HOST_AWS_CACHE. An option only so a test can point it somewhere.
	hostCredDir string
	ttl         time.Duration
	// failedTTL is how long a *failure* is kept. See ttlFor: much shorter than
	// ttl, because an unreachable cluster is the one somebody is fixing.
	failedTTL time.Duration
	timeout   time.Duration
	// kubeconfig is the list of files the manager was built from, kept so the
	// readiness probe can ask whether they still read. See readiness.go.
	kubeconfig cluster.Kubeconfig
	// kubeconfigRead is which of those paths were readable when this was built.
	//
	// The snapshot is what lets readiness tell two absences apart, and they need
	// opposite answers: a path that was never there is one kubectl skips and one
	// an operator listed on purpose for another machine, while a path that read
	// at startup and is gone now is a vanished mount. Taken at
	// construction because that is the only moment "before anything went wrong"
	// exists.
	kubeconfigRead map[string]bool

	// now is injectable so the cache can be tested without sleeping.
	now func() time.Time

	// readDir and readFile are the two reads under `~/.aws` — the SSO cache and
	// the assume-role cache in credcache.go — injectable for the same kind of
	// reason as now: the failure they exist to report cannot be produced on
	// every host.
	//
	// One seam for both caches rather than a pair each, and that is faithful
	// rather than lazy: the failure both branches exist for is a uid mismatch on
	// the one bind mount of `~/.aws`, which takes out every directory under it
	// at once.
	//
	// A directory that lists is the ordinary case and a directory that is absent
	// is easy to arrange; a directory that **exists and refuses to list**
	// needs mode bits that deny the owner, which Windows does not have, and
	// internal/archive/unreadable_test.go therefore skips itself here. Worse, the
	// obvious substitute is a lie on this platform: `os.ReadDir` over a path that
	// is a *file* answers ERROR_PATH_NOT_FOUND, which `errors.Is(err,
	// fs.ErrNotExist)` reports as "not there" — so the one
	// filesystem trick that would work everywhere else silently exercises the
	// wrong branch. A test that only ever runs in CI is a branch nobody can check
	// where it is written, so the seam is here and both tests exist.
	readDir  func(string) ([]os.DirEntry, error)
	readFile func(string) ([]byte, error)

	mu    sync.RWMutex
	cache map[string]Cluster
	// fails counts consecutive failed probes per cluster. Only the background
	// round reads it — see backgroundWait — so a page and the Retry button still
	// re-ask immediately however long a cluster has been down. Guarded by mu.
	fails map[string]int
	// warming and warmed bound the background round the readiness probe asks
	// for: one at a time, and no more often than the TTL. Guarded by mu, and
	// set outside the goroutine that does the work. See readiness.go.
	warming bool
	warmed  time.Time
	// warmStarted is when the round in progress started, so readiness can
	// tell a round that is running from one that is held up. Guarded by mu.
	warmStarted time.Time
	// surveying is the read of the kubeconfig in progress, and survey how it
	// is read; nil is the kubeconfig's own Survey, a field so a test can stand
	// in a mount that hangs. Guarded by mu. See readiness.go.
	surveying *surveying
	survey    func() cluster.KubeconfigSurvey
	// flights is the foreground probe of each cluster in progress, for a
	// second caller to wait on rather than start its own. Guarded by mu, and
	// made on first use so a struct literal in a test needs nothing. See
	// joined.
	flights map[string]*flight
	// asks is the API-server ask of each cluster still running, whoever started
	// it, and stays until the ask itself returns - past the deadline of the
	// probe that started it. Guarded by mu, made on first use. See ask.
	asks map[string]*asking
	// ask is how a cluster is asked its version; nil is serverGitVersion. A
	// field so a test can stand in a credential plugin that never returns.
	ask func(ctx context.Context, client *cluster.Client) (string, error)
}

// asking is one ask of a cluster's API server, still running or just done.
// answer is set before done closes.
type asking struct {
	done   chan struct{}
	answer probeAnswer
}

// flight is one foreground probe in progress. Its row is the answer, to be
// read once done is closed.
type flight struct {
	done chan struct{}
	row  Cluster
}

type Options struct {
	Clusters *cluster.Manager
	Metrics  Metrics
	ReadOnly func(string) bool
	// Kubeconfig is the list of files the manager was built from — the same
	// list, which is why it is a cluster.Kubeconfig and not a path: handing this
	// one element of a five-file `KUBECONFIG` would make `/kt-healthz` report
	// on a fifth of what the console actually reads.
	//
	// Empty means the readiness probe cannot check whether they still read, and
	// says so by not checking rather than by failing — see readiness.go.
	Kubeconfig cluster.Kubeconfig
	// AWSDir is the AWS CLI's SSO cache. Empty means $HOME/.aws/sso/cache.
	AWSDir string
	// CredDir is the AWS CLI's assume-role cache. Empty means the sibling of
	// AWSDir — $HOME/.aws/cli/cache. See credcache.go for what is read there and
	// why a page about clusters reads it at all.
	CredDir string
	// HostCredDir is the operator's own assume-role cache, mounted read-only.
	// Empty means read KT_HOST_AWS_CACHE, which is what compose.dev.yaml sets.
	HostCredDir string
	// TTL is how long an answer is reused. Zero means one minute.
	TTL time.Duration
	// FailedTTL overrides how long a failure is cached. Zero means
	// defaultFailedTTL; it exists as an option so a test can reach it.
	FailedTTL time.Duration
	// Timeout bounds a single probe. Zero means ten seconds.
	//
	// Deliberately shorter than the general request timeout: this page's job is
	// to tell you quickly which clusters are awake, and one unreachable cluster
	// must not hold the other thirteen. A slow answer here is itself the answer.
	Timeout time.Duration
}

func New(opts Options) *Health {
	h := &Health{
		clusters:    opts.Clusters,
		metrics:     opts.Metrics,
		readOnly:    opts.ReadOnly,
		awsDir:      opts.AWSDir,
		credDir:     opts.CredDir,
		hostCredDir: opts.HostCredDir,
		kubeconfig:  opts.Kubeconfig,
		ttl:         opts.TTL,
		// See the field: this is the "before anything went wrong" snapshot, and
		// there is no later moment that can stand in for it.
		kubeconfigRead: readableNow(opts.Kubeconfig),
		failedTTL:      opts.FailedTTL,
		timeout:        opts.Timeout,
		now:            time.Now,
		readDir:        os.ReadDir,
		readFile:       os.ReadFile,
		cache:          map[string]Cluster{},
		fails:          map[string]int{},
	}
	if h.readOnly == nil {
		h.readOnly = func(string) bool { return false }
	}
	if h.failedTTL == 0 {
		h.failedTTL = defaultFailedTTL
	}
	if h.ttl == 0 {
		h.ttl = time.Minute
	}
	if h.timeout == 0 {
		h.timeout = 10 * time.Second
	}
	if h.awsDir == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			h.awsDir = filepath.Join(home, ".aws", "sso", "cache")
		}
	}
	return h
}

// All probes every context and returns the rows, worst first.
//
// Concurrently, and this matters more than it looks: probing fourteen contexts
// one after another, each able to wait out its own timeout, takes half a minute
// to draw. In parallel the page costs the slowest single
// cluster, not the sum of them.
// All probes every context. Foreground: no backoff, so a page and the Retry
// button always re-ask.
func (h *Health) All(ctx context.Context, force bool) []Cluster {
	return h.all(ctx, force, false)
}

func (h *Health) all(ctx context.Context, force, background bool) []Cluster {
	if h.clusters == nil {
		return nil
	}
	names := h.clusters.Contexts()
	rows := make([]Cluster, len(names))

	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		// Through safego, so a panic does not end the process: a panic checking
		// one context is that
		// context's row, `ko` with the panic as its detail, and the others are
		// drawn.
		safego.Go("health: checking a context", func() error {
			rows[i] = h.check(ctx, name, force, background)
			return nil
		}, func(err error) {
			if err != nil {
				rows[i] = Cluster{Name: name, State: StateKO, Detail: err.Error(), CheckedAt: time.Now()}
			}
			wg.Done()
		})
	}
	wg.Wait()

	// Drop the contexts that were never actually asked. Check leaves a row with
	// no state when a probe was abandoned and there is no earlier answer to fall
	// back on, and a stateless row would be drawn by the page as "unclassified"
	// — a red row about a cluster nobody spoke to. Fewer rows is a worse page;
	// an invented failure is a wrong one, and this page is only worth having if
	// it is believed.
	kept := rows[:0]
	for _, row := range rows {
		if row.State == "" {
			continue
		}
		kept = append(kept, row)
	}
	rows = kept

	Sort(rows)
	return rows
}

// Check probes one context, using the cached answer when it is fresh enough.
//
// The probe runs **detached from the caller**, with this package's own bound.
// That is not a nicety, it is the difference between a page that can be trusted
// and one that cannot.
//
// The health page refreshes itself every thirty seconds and htmx aborts the
// request in flight the moment the operator clicks anything. Canceled with it,
// every probe would come back at once as "no answer within 10s" and be cached
// under that for the full TTL: the next person to open the page — very often
// the same person, one second later — would be told the entire estate was down,
// on the one screen whose whole purpose is to be believed about which clusters
// answer. Nothing would have happened except a reader navigating away.
//
// Detaching prevents it from both ends. A canceled round cannot be cut short, so
// it cannot produce a false answer; and it runs to completion, so the click that
// abandoned the page is what fills the cache for the next one. The same trade
// internal/web/usagecache.go and internal/customresources make, for the same
// reason, and the same one internal/health/readiness.go makes for its
// background round.
//
// The metrics discovery below is detached too. It is the expensive half — it may
// open a tunnel to Prometheus — and a discovery canceled halfway answers "no
// backend", which would be cached as a statement about the cluster made from a
// failure to ask it: a failed answer drawn as an empty one.
// ttlFor is how long an answer about this cluster is worth keeping, and a
// failure is worth keeping for much less time than a success.
//
// Sharing one minute would make a renewed SSO session take a minute to be
// believed: the operator runs `aws sso login` on the host, comes back, and the
// console repeats a cached "no answer" at them for as long as it takes to lose
// confidence in the console.
//
// The asymmetry is the point. A cluster that answers is not going to stop in the
// next minute, and re-asking is a wasted round trip against fourteen contexts. A
// cluster that did *not* answer is the one being actively fixed — a session
// renewed, a VPN reconnected, a laptop woken — so the cheapest thing this
// console can do is ask again shortly. The cost of being wrong is one extra
// probe; the cost of a shared TTL is a minute of disbelief.
// fresh reports whether a cached answer is still worth serving.
//
// Its own function rather than an expression inside Check, so that the rule and
// the use of the rule can both be tested — Check itself needs a cluster to probe
// and cannot be reached from a unit test.
func (h *Health) fresh(cached Cluster) bool {
	return h.now().Sub(cached.CheckedAt) < h.ttlFor(cached)
}

// backgroundDue reports whether the background round may re-probe a cluster that
// is currently failing.
//
// The window doubles with each consecutive failure from failedTTL up to
// backgroundFailedMax: 5s, 10s, 20s, 40s … 10 min. So a cluster that has just
// started failing is still re-checked almost at once — the SSO case, where the
// operator is actively fixing it — and one that has been failing for ten minutes
// is asked once every ten minutes instead of once a minute.
//
// Deliberately not applied to foreground calls. The health page exists to say
// which clusters are down, and an operator who has just renewed a session must
// not read a ten-minute-old refusal because a timer says so.
func (h *Health) backgroundDue(name string, cached Cluster) bool {
	h.mu.RLock()
	n := h.fails[name]
	h.mu.RUnlock()

	wait := h.ttlFor(cached)
	for i := 1; i < n && wait < backgroundFailedMax; i++ {
		wait *= 2
	}
	if wait > backgroundFailedMax {
		wait = backgroundFailedMax
	}
	return h.now().Sub(cached.CheckedAt) >= wait
}

// recordOutcome keeps the consecutive-failure count the backoff reads. An answer
// resets it, so one good probe restores the short window immediately rather than
// unwinding the doubling one round at a time.
func (h *Health) recordOutcome(name string, state string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if state == StateOK {
		delete(h.fails, name)
		return
	}
	h.fails[name]++
}

func (h *Health) ttlFor(cached Cluster) time.Duration {
	if cached.State == StateOK {
		return h.ttl
	}
	if h.failedTTL > 0 {
		return h.failedTTL
	}
	return defaultFailedTTL
}

// Check probes one context. Foreground, so the backoff below never applies: an
// operator who has just run `aws sso login` presses Retry and is answered.
func (h *Health) Check(ctx context.Context, name string, force bool) Cluster {
	return h.check(ctx, name, force, false)
}

func (h *Health) check(ctx context.Context, name string, force, background bool) Cluster {
	if !force {
		h.mu.RLock()
		cached, ok := h.cache[name]
		h.mu.RUnlock()
		if ok && h.fresh(cached) {
			return cached
		}
		// A cluster that keeps failing is left alone by the *background* round,
		// and only by it. Each probe of a failing EKS context spawns a Python
		// interpreter that fails; doing that every minute forever is what cost
		// 2.7-3.4 % of a core with the UI closed. See backgroundFailedMax.
		if background && ok && cached.State != StateOK && !h.backgroundDue(name, cached) {
			return cached
		}
	}
	if background {
		return h.probeAndStore(ctx, name, true)
	}
	return h.joined(ctx, name, func() Cluster { return h.probeAndStore(ctx, name, false) })
}

// joined runs one foreground probe of a cluster at a time, and hands a caller
// that arrives while it runs the same answer instead of a second probe.
//
// **A forced probe skips the cache, so nothing else stops them stacking.**
// Arriving on a cluster the rail shows stale and red several times within ten
// seconds - the overview's Refresh, a reload, the cluster opened from the rail
// again - would start one probe each, every one spawning the `aws` interpreter for an EKS context and
// each waiting out the same timeout. The person sets the pace and not a loop,
// so it is a handful and not a storm; it is still a handful of answers to one
// question asked at one moment.
//
// Joining is right because the probe in flight is as fresh as the one that
// would be started: it is answering now. The background round is left out on
// purpose - it reads metrics discovery from the cache where a foreground
// probe asks, so its row is not the same answer.
//
// **A caller that waits still answers to its own context**, as
// metrics.Discover's join does: a request that went away stops waiting and is
// answered from the last real round (lastKnown), which claims nothing new
// about the cluster. The probe it was waiting on runs on and fills the cache
// for whoever asks next. Without it a Refresh whose client had gone would hold
// its handler for up to a probe and a discovery.
func (h *Health) joined(ctx context.Context, name string, probe func() Cluster) Cluster {
	h.mu.Lock()
	if running, ok := h.flights[name]; ok {
		h.mu.Unlock()
		select {
		case <-running.done:
			return running.row
		case <-ctx.Done():
			return h.lastKnown(name)
		}
	}
	if h.flights == nil {
		h.flights = map[string]*flight{}
	}
	mine := &flight{done: make(chan struct{})}
	h.flights[name] = mine
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.flights, name)
		h.mu.Unlock()
		close(mine.done)
	}()
	mine.row = probe()
	return mine.row
}

// probeAndStore asks the cluster, now, and caches the answer.
func (h *Health) probeAndStore(ctx context.Context, name string, background bool) Cluster {
	probeCtx := context.WithoutCancel(ctx)

	row := Cluster{
		Name:     name,
		ReadOnly: h.readOnly(name),
	}

	started := h.now()
	got := h.probe(probeCtx, name)
	if got.abandoned {
		// Not an answer about this cluster, so it must not become one. Nothing is
		// cached and nothing is invented: whatever the last real round said, or
		// nothing at all.
		//
		// Unreachable while the detachment above holds — the only Done a detached
		// context has is our own deadline — and kept as the second opinion for the
		// day somebody removes it. The cache is what outlives the request; a wrong
		// entry in it is read by every page for the next minute.
		return h.lastKnown(name)
	}
	// **Stamped from after the probe, not before it.** The row is a
	// statement about a moment, and the moment it is about is the one the cluster
	// answered in (or failed to). Taking the reading at the *start* makes the row
	// as old as the probe was long the instant it is written, which is 10 s for a
	// context that timed out against a failedTTL of 5: `fresh` is then false for
	// it immediately, so every render re-probes every timing-out context, each
	// spawning the ~0.55 s `aws` interpreter that backgroundFailedMax exists to
	// bound. `backgroundDue` reads the same stamp, so the backoff would be short
	// by one probe duration too — and the "checked at" the page prints would be a
	// lie by the length of the timeout, on a page read for exactly that number.
	//
	// One reading for both fields rather than two calls to h.now(): the age and
	// the duration must not be able to disagree about when this finished.
	finished := h.now()
	row.CheckedAt = finished
	row.State, row.Detail, row.Version = got.state, got.detail, got.version
	row.Command = got.command
	row.Took = finished.Sub(started).Round(time.Millisecond).String()

	// Only worth asking a cluster that answered, and it is the expensive half:
	// discovery may open a tunnel to Prometheus.
	//
	// Bounded here rather than inherited. Detaching drops the caller's deadline
	// along with its cancellation, so this is the one call in the round that
	// would otherwise have nothing at all stopping it — and it is the one that
	// opens a tunnel. One cluster's timeout is the same budget the probe itself
	// gets, and for the same reason: a slow answer here is itself the answer.
	if row.State == StateOK && h.metrics != nil {
		if background {
			// Read, never probe. Discovery's job when its window has expired is to
			// *find* Prometheus, which means a port-forward stood up, health-checked
			// and torn down — measured at 98 of them in 81 idle minutes across the
			// operator's estate, 1.9-2.6 s each, with the UI closed. This cell holds
			// a backend's name; it is not worth a tunnel. Empty until somebody opens
			// a page that genuinely needs the answer, and empty is drawn as unknown.
			row.Metrics = h.metrics.Cached(name)
		} else {
			metricsCtx, cancel := context.WithTimeout(probeCtx, h.timeout)
			row.Metrics = h.metrics.Discover(metricsCtx, name, false)
			cancel()
		}
	}

	h.recordOutcome(name, row.State)

	h.mu.Lock()
	h.cache[name] = row
	h.mu.Unlock()
	return row
}

// lastKnown is what Check answers with when the ask was abandoned: the previous
// round's row, however old — it is stamped with when it was taken, so its age is
// on the page — or a row with no state at all, which All drops.
func (h *Health) lastKnown(name string) Cluster {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if row, ok := h.cache[name]; ok {
		return row
	}
	return Cluster{Name: name}
}

// probeResult is what one ask came back with.
//
// abandoned is the field that matters and it is not a state, deliberately: it
// says nothing was learned about this cluster, which is a different thing from
// having learned that it is unwell. Every other field is empty when it is set.
// See the note on Check.
type probeResult struct {
	state     string
	detail    string
	version   string
	abandoned bool
	// command renews an SSO session, on StateSSO for an AWS context (ssoCommand).
	command string
}

func classified(err error) probeResult {
	state, detail, version := classify(err)
	return probeResult{state: state, detail: detail, version: version}
}

// probe asks the API server its version, which is the cheapest call that proves
// the whole chain works: DNS, routing, TLS, the exec credential plugin, and
// authorization. A list of pods would prove the same thing and also fail on a
// cluster where the operator simply cannot read that namespace, which is not a
// health problem and must not be reported as one.
func (h *Health) probe(ctx context.Context, name string) probeResult {
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()

	client, err := h.clusters.Client(name)
	if err != nil {
		return classified(err)
	}

	// **Two bounds, and each covers what the other cannot.**
	//
	// The goroutine bounds *this caller*: whatever the ask is doing, the select
	// below returns at our own deadline and the page is drawn. It is not
	// removable, and the reason is not the network — it is the exec credential
	// plugin. client-go runs it with a plain `exec.Command` and no context at all
	// (client-go 0.37.0, plugin/pkg/client/auth/exec/exec.go:458, inside
	// roundTripper.RoundTrip), so an `aws eks get-token` that hangs is a
	// RoundTrip that hangs, and *no* context stops it. Delete the goroutine and
	// one wedged credential plugin holds the whole health round for as long as it
	// hangs, with nothing bounding it — and a misbehaving `aws` is an ordinary
	// event, not an exotic one.
	//
	// The context bounds *the work* (`ServerVersionWithContext`, client-go
	// 0.37.0). Without it the abandoned goroutine runs on to client-go's own
	// request timeout — 30 s, internal/cluster/limits.go — so it outlives a 10 s
	// deadline by 20 s, holding a socket and a TLS session all the while: against
	// fourteen contexts on a server that takes the request and never answers,
	// with a 2 s deadline, **56 goroutines and 14 connections still up 28 s after
	// the round returned**, draining at exactly 30 s. With the context passed
	// they go with the deadline. Neither shape *leaks* — every one of them
	// terminates — so this is about socket lifetime, not a leak.
	running, joinedAsk := h.startAsk(ctx, name, client)

	select {
	case <-ctx.Done():
		// Which Done this is decides whether there is anything to say at all.
		//
		// Our own deadline means the cluster was asked and did not answer — a
		// fact about the cluster, worth a row and worth caching. A cancellation
		// means the ask was called off from outside; the cluster may be perfectly
		// well and was simply never given the chance to say so. Reading the two
		// as one would let an aborted page mark healthy clusters as timed out.
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return probeResult{abandoned: true}
		}
		return h.timedOut()
	case <-running.done:
		a := running.answer
		// An ask started by an earlier probe ran on that probe's context, so
		// an error that is its deadline or its cancellation says nothing about
		// the cluster now: for this probe, it did not answer in time.
		if joinedAsk && a.err != nil && (errors.Is(a.err, context.DeadlineExceeded) || errors.Is(a.err, context.Canceled)) {
			return h.timedOut()
		}
		return h.answered(ctx, a, client)
	}
}

// startAsk asks the cluster its version on a goroutine of its own, or hands
// back the ask of that cluster still running from an earlier probe; joined
// says it is the latter.
//
// **One ask per cluster, however many probes time out on it.** The probe
// returns at its deadline, but the goroutine behind it cannot be stopped when
// the credential plugin hangs, since client-go runs the plugin with no
// context. Without this, each probe of that cluster - the background round
// every minute, every Retry - would start another goroutine and another
// plugin process, all hung on the same thing, and they would stack for as
// long as the plugin hangs. With it, the next probe waits on the ask already
// running, for its own deadline, and starts nothing.
func (h *Health) startAsk(ctx context.Context, name string, client *cluster.Client) (*asking, bool) {
	h.mu.Lock()
	if running, ok := h.asks[name]; ok {
		h.mu.Unlock()
		return running, true
	}
	if h.asks == nil {
		h.asks = map[string]*asking{}
	}
	mine := &asking{done: make(chan struct{})}
	h.asks[name] = mine
	h.mu.Unlock()

	ask := h.ask
	if ask == nil {
		ask = func(ctx context.Context, client *cluster.Client) (string, error) {
			return serverGitVersion(ctx, client.Clients.Discovery())
		}
	}
	// Through safego, so a panic does not end the process: a panic in the ask is
	// its answer's error, still posted, so the probe classifies it rather than
	// waiting it out.
	safego.Go("health: asking the API server its version", func() error {
		got, err := ask(ctx, client)
		mine.answer = probeAnswer{version: got, err: err}
		return nil
	}, func(err error) {
		if err != nil {
			mine.answer = probeAnswer{err: err}
		}
		h.mu.Lock()
		delete(h.asks, name)
		h.mu.Unlock()
		close(mine.done)
	})
	return mine, false
}

// probeAnswer is what probe's goroutine came back with.
type probeAnswer struct {
	version string
	err     error
}

// timedOut is the row for an ask our own deadline ended.
func (h *Health) timedOut() probeResult {
	return probeResult{state: StateTimeout, detail: "no answer within " + h.timeout.String()}
}

// answered reads what the ask came back with, when it came back through the
// channel rather than being overtaken by ctx.Done(). ctx is the probe's own,
// carrying its deadline.
func (h *Health) answered(ctx context.Context, a probeAnswer, client *cluster.Client) probeResult {
	// The same question, asked of the answer rather than of the channel.
	//
	// When the ask was called off before it began, both arms of this select
	// are ready at once: `ctx.Done()` is closed, and the goroutine has
	// already come back with client-go's `context canceled`. Go picks
	// between ready arms at random, so one run in some number would take
	// this arm and classify the cancellation as `ko (context canceled)` - a
	// state, cached, about a cluster nobody asked. Rare on a developer
	// machine, it shows under `-race` on a loaded runner, where the goroutine
	// is slower to start and quicker to be scheduled. An error that *is* the
	// cancellation is the abandoned case whichever arm delivered it.
	if a.err != nil {
		if errors.Is(a.err, context.Canceled) ||
			(ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded)) {
			return probeResult{abandoned: true}
		}
		// **Our own deadline, whatever words the error found for it.** An
		// error that comes back once the deadline has passed is that deadline
		// - the ctx.Done() arm's answer, delivered by this arm - and not a
		// fact about the cluster. Asked of the clock and not of ctx.Err(),
		// because the timer that closes Done can run after the deadline has
		// passed, and of the clock and not of the text, because the text is
		// whoever noticed first. On a loaded runner the goroutine in probe can
		// start after the deadline: client-go's rate limiter then refuses
		// before a byte is sent with "rate: Wait(n=1) would exceed context
		// deadline", which classify reads as `ko`, an unclassified red row,
		// while Done is still open.
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			return h.timedOut()
		}
		result := withIdentity(heard(ctx, classified(a.err), execOf(client)), client)
		if result.state == StateSSO {
			result.command = ssoCommand(client, execOf(client))
		}
		return result
	}
	return probeResult{state: StateOK, detail: "the API server answered", version: a.version}
}

// withIdentity adds *as whom* to a failure that is about a credential.
//
// A bare 401 from EKS means the presigned URL was signed with credentials that
// worked and the identity was refused — so the identity is the entire
// diagnosis. Without it, `kubectl` in a terminal works and the console answers
// `Unauthorized` from the same kubeconfig, with nothing anywhere saying the two
// resolved different AWS profiles.
//
// They can. `LoadDefaultConfig` reads `AWS_PROFILE` from *this process's*
// environment when the kubeconfig names none, and a console started from a
// Start-menu shortcut, a `.desktop` entry or a `.app` bundle does not inherit a
// shell's environment. `internal/binpath` is the same problem one variable
// over — there a thin PATH meant the plugin could not be found, which fails
// loudly; here a thin environment means it authenticates as somebody else, which
// returns a bare 401.
//
// Only on the two credential states. On a DNS failure or a refused connection
// the identity is noise, and a row that says everything says nothing.
func withIdentity(result probeResult, client *cluster.Client) probeResult {
	if client == nil || client.EKS == nil {
		return result
	}
	if result.state != StateSSO && result.state != StateIAM {
		return result
	}
	// **The ARN first, because it is the answer and the profile is the
	// question.** A profile name tells the operator which line of
	// `~/.aws/config` was consulted, and two machines whose `prod-devops`
	// resolve to two different roles produce the identical row. This is what the
	// authenticator actually saw, so it is what an access entry or an aws-auth
	// mapping is compared against.
	//
	// Empty until the first refusal has been diagnosed, which is one probe's
	// worth of delay and never a wait: `Identity` here is internal/ekstoken's
	// non-asking half, so this cannot put an STS call inside a page render.
	if client.EKS.Identity != nil {
		if who := client.EKS.Identity(); who != "" {
			result.detail += " · KubeTower " + who
			if client.EKS.Profile != "" {
				result.detail += " (AWS profile " + client.EKS.Profile + ")"
			}
			result.detail += ". Compare that with the cluster's EKS access entries or its " +
				"aws-auth ConfigMap"
			return result
		}
	}

	if client.EKS.Profile != "" {
		result.detail += " · KubeTower signs this context as AWS profile " + client.EKS.Profile
		return result
	}
	// AWS_ACCESS_KEY_ID leads. With no profile named it beats every profile in
	// the file (config@v1.33.3/resolve_credentials.go:117), so it is the first
	// thing to look at. `internal/cluster`'s profileNote
	// says the same thing for the log; they are two sentences for two readers
	// and they have to agree about the order.
	result.detail += " · KubeTower signs this context with no profile named in the kubeconfig, " +
		"so the default resolution decided — this process's own AWS_ACCESS_KEY_ID first, then " +
		"AWS_PROFILE, then `default`. A console launched from a shortcut does not inherit a " +
		"shell's environment, so a terminal where kubectl works can be using another one"
	return result
}

// serverGitVersion asks the API server its version, cancelably where client-go
// allows it.
//
// The assertion is how a context reaches the request at all: `Discovery()` is
// typed as `discovery.DiscoveryInterface`, whose `ServerVersion()` takes none,
// and the contextual method lives on a second interface that client-go's own
// `*DiscoveryClient` also satisfies (discovery/discovery_client.go). So the
// assertion holds for every client this binary builds.
//
// The fallback is for an implementation that is not client-go's — a fake in
// somebody's test — and it probes uncancelably rather than refusing. That is
// the right trade because the goroutine in probe() is the bound that matters to
// the caller either way; losing the context costs socket lifetime, and refusing
// would cost the answer.
func serverGitVersion(ctx context.Context, d discovery.DiscoveryInterface) (string, error) {
	if withCtx, ok := d.(discovery.ServerVersionInterfaceWithContext); ok {
		info, err := withCtx.ServerVersionWithContext(ctx)
		if err != nil {
			return "", err
		}
		return info.GitVersion, nil
	}
	info, err := d.ServerVersion()
	if err != nil {
		return "", err
	}
	return info.GitVersion, nil
}

// classify turns a client-go error into one of the states, because each needs a
// different person to fix it.
//
// Order matters and is not alphabetical: an expired SSO session usually also
// mentions a role, so it must be tested before the IAM case or every lapsed
// session is reported as a permissions problem the operator cannot fix. That
// ordering is why the typed predicates are at the *bottom* of this function
// rather than the top, which is the opposite of internal/logs and
// internal/forward — see the comment where they are.
//
// **Most of what this function reads is deliberately text, and that part is
// correct.** Elsewhere a Kubernetes error is classified by its status; this
// classifier has to keep sniffing: an EKS context whose SSO session has
// lapsed fails inside `aws eks get-token` before a byte reaches the cluster, so
// there is no API status to read — no apierrors predicate will ever match an
// expired SSO session, an `sts:AssumeRole` refusal, or a name that does not
// resolve. Three of the five states this function can return exist for exactly
// those, and each names a different person who fixes it.
func classify(err error) (state, detail, version string) {
	message := err.Error()
	lower := strings.ToLower(withoutRequestURL(message))
	// The 401-or-403 question, answered once for this package and for
	// internal/web's stateOf — see Denied. Computed here and *tested* at the
	// position the two text arms held, so the SSO and IAM arms above it keep
	// their first look at a message that carries both a session and a role.
	denial := Denied(err)

	switch {
	case contains(lower, "token has expired", "sso session", "expired or invalid",
		"refresh token", "sso-session", "cached sso token"):
		// No session name here: this is a pure function of the error text, and
		// the name belongs to whoever is running the process. The banner above
		// the table reads it from the AWS config and spells the whole command.
		//
		// **The needles cover the AWS CLI's wording and the SDK's.** A
		// recognized EKS exec block is not run at all — `internal/cluster/manager.go`
		// nils `ExecProvider` and signs the presigned STS URL in this process
		// (`internal/ekstoken`) — so for those contexts the text classified here
		// comes from `aws-sdk-go-v2` and not from `aws`. That is the common case.
		//
		// `cached sso token` is the needle that case needs. Measured against the
		// module in go.mod, `credentials@v1.20.1`: the SDK has two SSO paths and
		// both must be matched. The legacy per-profile form fails through
		// `ssocreds.InvalidTokenError`, whose `Error()` is `const msg = "the SSO
		// session has expired or is invalid"` (ssocreds/sso_credentials_provider.go:160)
		// — caught by `sso session`. The **modern `sso-session` form**, which is
		// what `aws sso login --sso-session <name>` writes, goes through
		// `SSOTokenProvider` instead and fails with
		// `refresh cached SSO token failed, cached SSO token is expired, or not
		// present, and cannot be refreshed` (sso_token_provider.go:104 and :120).
		// A needle for the legacy form alone would match **nothing** there: the
		// message would fall to StateKO, which internal/web draws as
		// *"unclassified"* and *"Nobody yet — this failure is not one of the known
		// shapes"*, for the single most common recoverable failure. `Actionable()`
		// would be false with it, so the row would lose its "yours to fix" mark
		// too, and rank() would put it below three arms that are somebody else's
		// problem.
		//
		// Worse, a machine that has never logged in at all — `failed to read cached
		// SSO token file, open …: no such file or directory`
		// (sso_cached_token.go:149) — would reach the credential-plugin arm on its
		// bare `no such file` needle and be told *"the credential plugin could not
		// be run"*, about a plugin that is not run and does not exist here: a broad
		// needle in a late arm catching a message written by somebody else
		// entirely.
		//
		// One needle rather than four because the SDK wraps all of them through the
		// same words, and a phrase rather than `sso token`: it must not be able to
		// shadow the arms below, which is what this list's first position makes
		// possible. classifysdk_test.go pins the strings and names where each comes
		// from, so the next reader checks the module rather than trusting the test.
		return StateSSO, "SSO session expired — sign in again on the host", ""

	case contains(lower, "assumerole", "accessdenied", "not authorized to perform"):
		return StateIAM, "assume-role refused — an IAM grant; it fails from the host too", ""

	case contains(lower, "no such host", "lookup ", "server misbehaving"):
		return StateDNS, "the name does not resolve from here — VPN, or a private zone", ""

	case contains(lower, "connection refused"):
		// The loopback trap has exactly this shape:
		// a cluster on 127.0.0.1 means the host, which inside a container is
		// not this process. See internal/cluster/loopback.go.
		return StateRefused, "the address answered and refused — is this cluster on the host's loopback?", ""

	// The needles are phrases and not the bare word "timeout". See
	// withoutRequestURL: `?timeout=32s` rides in every client-go request URL, so
	// the bare word would match the address rather than the failure and this arm
	// would swallow the six below it. The list
	// is timeoutPhrases, shared with TimedOut so internal/web reads it too.
	case contains(lower, timeoutPhrases...):
		return StateTimeout, "no answer in time — VPN, a firewall, or the cluster is down", ""

	case contains(lower, "certificate", "x509"):
		return StateKO, "the certificate was rejected — " + textutil.FirstLineN(message, detailLine), ""

	case denial == DenialUnauthorized:
		// A 401 by status, or by its words where there is no status to read —
		// Denied holds both halves and the reason the text half survives. The
		// typed predicate is answered here, at the text arm's rank, and not at
		// the bottom with the others: a status error carries none of the plugin
		// wording the two arms below look for, so nothing below can claim it
		// first, and the arms *above* still get their look — which is the
		// ordering that matters.
		//
		// **Not "usually an expired SSO session".** A recognized EKS block is
		// signed in this process (`internal/ekstoken`), and a lapsed session then
		// surfaces as an SDK error carrying SSO wording — which has an arm of
		// its own **above this one** (see the `sso token`/`sso session` case and
		// classifysdk_test.go, which pins those strings). An AssumeRole refusal
		// is caught by the arm after it. So an EKS context reaching *here* is
		// one whose URL was signed with credentials that worked and whose
		// identity the API server refused: an `aws-auth` entry or an EKS access
		// entry, which is somebody else's ticket and not ten seconds of the
		// operator's time. EKS answers that with a bare 401 and not a 403,
		// because the webhook maps the principal to no Kubernetes user at all —
		// so it is indistinguishable here from an expired credential, and the
		// sentence must not pick one.
		//
		// The other half of this arm's population is a context that is not AWS
		// at all — an OIDC or GKE token, a static one in the kubeconfig — where
		// an expired credential really is the usual cause. Two populations, one
		// string, and nothing in the error separates them, so the row names both
		// rather than asserting one.
		//
		// The state is StateSSO deliberately. It is rank 0, which is "look at
		// this first", and that is correct for both halves; what must not happen
		// is telling the reader which of the two they are in.
		return StateSSO, "Unauthorized — the session behind this context has lapsed, " +
			"or it authenticates as an identity this cluster does not map", ""

	case denial == DenialForbidden:
		// A 403, typed or by its word: the identity was accepted and may not
		// do this. After the 401 arm, never before it — see Denied.
		return StateKO, "the credentials are valid but may not read this — an RBAC binding", ""

	case contains(lower, "failed with exit code"):
		// **The plugin ran and refused, which is not the same thing as the arm
		// below.** client-go's message is exactly
		// `exec: executable aws failed with exit code 255` — it sets the plugin's
		// stderr to the process's own, so whatever `aws` actually said is in
		// `docker compose logs` and *not* in this error. There is therefore
		// nothing here to diagnose from, and the honest answer is to say where
		// the reason is rather than to invent one.
		//
		// Without this arm it would match `"executable"` below and come out as
		// "the credential plugin could not be run", which is wrong in the way that
		// costs time: it sends the reader looking for a missing binary that is
		// demonstrably present. The two commonest causes are a lapsed SSO session
		// and a corrupt entry in the assume-role cache, and the second is why
		// credcache.go exists — the page can look at that directory itself, which
		// is the only way it can be known.
		//
		// **Both channels named, not one.** The plugin's stderr
		// is this process's stderr, which is `docker compose logs` in the
		// container and `kubetower.log` in the state directory on a downloaded
		// binary — where there is no compose file, and where a GUI launch has no
		// stderr anybody reads at all. Named rather than detected:
		// whether anybody is reading stderr has no honest answer, which is the
		// argument internal/launch/logfile.go makes, and pointing an
		// operator at a command their installation does not have is the same
		// defect as pointing them at a banner that is not drawn.
		return StatePlugin, "the credential plugin ran and refused — its own message is in " +
			"this process's log (`docker compose logs`, or `kubetower.log` in the state " +
			"directory); an expired SSO session, a refused assume-role and a " +
			"corrupt AWS credential cache all land here", ""

	case contains(lower, "executable", "no such file", "exec plugin", "not found"):
		// The credential plugin itself is missing. On EKS that is the `aws`
		// binary, which is why the image carries it.
		//
		// **A missing binary is not a permissions problem, and saying so sends
		// the operator to their IAM policy for a fault in their PATH.** Classify
		// on the reason: the *kind* of failure must be named rightly, and the
		// sentence says **where it looked**, which is the one fact that turns this
		// from a mystery into an install command.
		if exe, ok := missingExecutable(message); ok {
			return StateMissing, fmt.Sprintf("the kubeconfig authenticates through %s, and this "+
				"process cannot find it. Searched: %s", exe, textutil.Clip(searchPath(), pathLine)), ""
		}
		return StateKO, "the credential plugin could not be run — " + textutil.FirstLineN(message, detailLine), ""
	}

	// What is left, after the text above has had its chance at the failures that
	// have no status: the typed predicates, which are what internal/metrics,
	// internal/edit and the two statusFor functions all use.
	//
	// Below the text and not above it on purpose. An expired SSO session's error
	// sometimes arrives as a wrapped Unauthorized, and a typed check running first
	// would answer StateKO — "the credentials are valid but may not read this" —
	// for a session the operator can renew in ten seconds. The text arms are the
	// specific diagnosis; these are the fallback that stops a status the text does
	// not recognize being reported as an unexplained failure.
	//
	// IsUnauthorized and IsForbidden are not here: Denied answers them, tested
	// above at the rank of their text arms. The rule this comment states holds
	// for them too — the SSO and IAM text arms run first.
	switch {
	case refusedConnection(err):
		// Same answer as the "connection refused" arm above, reached by number
		// rather than by words. See refusedConnection.
		return StateRefused, "the address answered and refused — is this cluster on the host's loopback?", ""
	case apierrors.IsTimeout(err), apierrors.IsServerTimeout(err),
		apierrors.IsServiceUnavailable(err),
		// Winsock's connect timeout, by number: its sentence carries none of
		// the timeout phrases and is localized (wsaetimedout).
		timedOutConnecting(err):
		return StateTimeout, "no answer in time — VPN, a firewall, or the cluster is down", ""
	}

	return StateKO, textutil.FirstLineN(message, detailLine), ""
}

// pathLine caps how much of the PATH goes in the Detail cell.
//
// Larger than detailLine because the PATH *is* the diagnosis here rather than a
// tail of somebody else's sentence, and the case this exists for is short: a
// macOS GUI launch has about four directories and thirty characters. A shell's
// own PATH is long, but a process holding one has almost certainly found the
// binary and never reaches this arm.
const pathLine = 320

// missingPlugin matches client-go's own words for a credential plugin whose
// binary could not be found.
//
// Exactly the format string in client-go 0.37.0
// (plugin/pkg/client/auth/exec/exec.go:553, `wrapCmdRunErrorLocked`):
//
//	fmt.Fprintf(&builder, "exec: executable %s not found", a.cmd)
//
// which arrives here inside `Get "<url>": getting credentials: …` followed by an
// install hint. Pinned to that phrasing on purpose: the arm above matches four
// much broader needles — "executable", "no such file", "exec plugin",
// "not found" — and only this shape is known to carry the binary's name in a
// known position. Anything else keeps the vaguer sentence rather than
// having a name guessed out of it.
var missingPlugin = regexp.MustCompile(`executable (\S+) not found`)

// missingExecutable pulls the binary's name out of that message.
func missingExecutable(message string) (string, bool) {
	m := missingPlugin.FindStringSubmatch(message)
	if len(m) != 2 {
		return "", false
	}
	return m[1], true
}

// searchPath is the PATH this process would have searched, as a variable so a
// test can pin it.
//
// Read at classification time rather than captured at startup, because
// internal/binpath appends to it during boot and the honest answer is the list
// that was actually searched — not the one the process was handed.
var searchPath = func() string {
	if p := os.Getenv("PATH"); p != "" {
		return p
	}
	// Distinguished from a short PATH, because "Searched: " with nothing after
	// it reads like a rendering fault rather than a finding.
	return "(this process has no PATH at all)"
}

// requestURL matches the address a failed request was made to, wherever it sits
// in the message and whether or not client-go quoted it.
var requestURL = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"]*`)

// withoutRequestURL removes that address before the vocabularies above read the
// message, so that they classify the *reason* and never the *destination*.
//
// This is not tidiness: without it the page names the wrong person.
// client-go formats a transport failure as `Get "<url>": <reason>`, and it puts
// the configured timeout into every request URL as a query parameter —
// `…/version?timeout=32s`. A timeout arm testing for a bare "timeout" matches
// that, and answers StateTimeout: *"no answer in time — VPN, a firewall, or the
// cluster is down"*, under which internal/web prints **"Whoever runs the
// network"**. Because that arm sits fifth, it would shadow every arm below it —
// certificate, unauthorized, forbidden, the credential-plugin pair, and all
// three typed predicates — for any error carrying a URL, which is every
// transport error there is: an EKS context whose real failure is `exec:
// executable aws failed with exit code 254` would be blamed on the network
// while its endpoint answers 401 in 131ms.
//
// The narrowed needles in the timeout arm are the other half. A cluster's own
// name reaches these strings too, and an EKS context called `prod-timeout-eks`
// would walk straight back into this; stripping the URL cannot help there and
// phrases instead of words can.
func withoutRequestURL(message string) string {
	return requestURL.ReplaceAllString(message, "")
}

// wsaeconnrefused is Winsock's own number for a refused connection.
//
// **Without it every refused connection is "unclassified" on the Windows
// binary**, and the page says "Nobody yet — this failure is not one of the known
// shapes". Against a local port with nothing listening, Go reports
// `dial tcp 127.0.0.1:5897: connectex: No connection could be made because the
// target machine actively refused it.` — which contains none of the words the
// "connection refused" arm looks for, because those are the *Linux* wording. In
// the container that arm is right and this is unreachable; in the downloadable
// binary, which is where a `kind` or docker-desktop cluster on the loopback
// actually lives, it never fires at all.
//
// By number and not by phrase, because the phrase is **localized**: a French
// Windows says "refusé de manière explicite par l'ordinateur cible", so a needle
// added for the English text would be one more arm that works on the machine it
// was written on. And by literal 10061 rather than syscall.WSAECONNREFUSED
// because that constant exists only in the Windows build of syscall, and this
// file must compile everywhere — `errors.Is(err, syscall.ECONNREFUSED)` is *not*
// the answer either: measured false on Windows, since Errno.Is does not fold
// Winsock's numbers onto the POSIX ones.
const wsaeconnrefused = syscall.Errno(10061)

// refusedConnection reports whether the failure was a refused TCP connection,
// on either family of error numbers.
func refusedConnection(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == syscall.ECONNREFUSED || errno == wsaeconnrefused
}

func contains(haystack string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

// detailLine keeps an error to what fits in the Detail column. A Kubernetes
// error can be a paragraph and the first line is the sentence that names the
// problem.
//
// 200, the same as a log panel's refusal: cut at two lengths, the same cluster
// error would be quoted differently on two pages and the shorter one would look
// like a different failure. Two of the three call sites below prefix the cut
// text with a sentence of our own.
const detailLine = 200

// rank orders the states so the page reads worst-first. A row the operator can
// act on outranks one they cannot: a lapsed SSO session is above an IAM refusal
// because one of them is ten seconds of work and the other is a ticket.
func rank(state string) int {
	switch state {
	case StateSSO:
		return 0
	case StateMissing:
		// Beside StateSSO rather than down with the unclassified: it is the
		// operator's, it is one install command, and the row says which binary.
		return 1
	case StatePlugin:
		// Above StateKO: it names two things the operator can check themselves
		// before it is anybody else's problem.
		return 2
	case StateKO:
		return 3
	case StateRefused:
		return 4
	case StateTimeout:
		return 5
	case StateDNS:
		return 6
	case StateIAM:
		return 7
	case StateOK:
		return 8
	}
	return 8
}

// Sort orders rows worst-first, then by name so the page does not reshuffle
// under the reader between two refreshes.
func Sort(rows []Cluster) {
	sort.SliceStable(rows, func(i, j int) bool {
		if a, b := rank(rows[i].State), rank(rows[j].State); a != b {
			return a < b
		}
		return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name)
	})
}

// Summary counts the rows by state, for the one line above the table.
func Summary(rows []Cluster) map[string]int {
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.State]++
	}
	return counts
}

// --- the SSO session ---------------------------------------------------------

// SSO reads the AWS CLI's own cache to find out how long the session has left.
//
// Reading the cache rather than shelling out to `aws sso` is deliberate: the
// question is asked on every page load, and the answer is a file. The cache
// holds one JSON document per session plus one per registered client; the
// session file is the one carrying an expiry, and the one that matters is the
// furthest in the future — or, when they have all lapsed, the most recent.
func (h *Health) SSO() Session {
	list := h.SSOSessions()
	if !list.Checked || len(list.Sessions) == 0 {
		return Session{State: SSOAbsent, Checked: list.Checked, Dir: list.Dir, Problem: list.Problem}
	}
	return list.Sessions[0]
}

// Sessions is every session the AWS CLI's SSO cache holds, for the home's
// session card: SSO() reports the one that matters, this lists them all.
//
// Checked, Dir and Problem mean what they mean on Session, and for the same
// reason: a cache that could not be read is not a cache with nothing in it.
type Sessions struct {
	Checked bool
	Dir     string
	Problem string
	// Sessions holds one entry per session name, the furthest expiry first;
	// two files naming one session keep the later. Every entry is Found.
	Sessions []Session
}

// SSOSessions reads the AWS CLI's SSO cache and lists the sessions in it.
//
// SSO() is this list's first entry, so the two cannot disagree about which
// session the clusters depend on.
func (h *Health) SSOSessions() Sessions {
	if h.awsDir == "" {
		// New() sets this from $HOME and leaves it empty only when os.UserHomeDir
		// failed. There is then no directory this answer could be about, which is
		// "nobody could tell" and not "there is no session".
		return Sessions{Problem: "there is no home directory to find the AWS CLI's SSO cache in"}
	}
	entries, err := h.readDir(h.awsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// The ordinary state of an installation with no SSO profiles at all,
			// and an answer: there is no session because there is no cache.
			return Sessions{Checked: true, Dir: h.awsDir}
		}
		// Anything else — a permissions or uid mismatch on the bind mount of
		// `~/.aws` is the one actually met — is a failure to look. See the
		// Checked field: reporting it as an absent session would remove the
		// banner rather than add one.
		return Sessions{Dir: h.awsDir, Problem: reasonOnly(err)}
	}

	list := Sessions{Checked: true, Dir: h.awsDir}
	// byName is each session's index in list.Sessions, so a second file naming
	// the same session keeps the later expiry rather than drawing it twice.
	byName := map[string]int{}
	// unreadable is the same failure one layer in: the directory's own mode let us
	// read the names and a file would not open, or it opened and would not parse —
	// a torn write, or a session whose expiry is in no shape we know. Counted
	// rather than ignored, because "no session file here" and "the session file
	// could not be read" are the two answers this whole field exists to keep apart.
	unreadable := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := h.readFile(filepath.Join(h.awsDir, entry.Name()))
		if err != nil {
			unreadable++
			continue
		}

		var payload struct {
			ExpiresAt             string `json:"expiresAt"`
			AccessToken           string `json:"accessToken"`
			RefreshToken          string `json:"refreshToken"`
			RegistrationExpiresAt string `json:"registrationExpiresAt"`
			StartURL              string `json:"startUrl"`
			SessionName           string `json:"sessionName"`
		}
		if json.Unmarshal(raw, &payload) != nil {
			unreadable++
			continue
		}

		// The access token is the discriminator, and leaving it out is not a
		// cosmetic slip. This directory holds one file per *client
		// registration* as well as one per session, and a registration carries
		// an `expiresAt` too — roughly ninety days out, against a session's
		// eight to twelve hours. Filter on the expiry alone and the
		// registration always wins the "furthest in the future" contest, so the
		// banner reports a session valid for weeks while the real one lapses
		// overnight and every EKS cluster goes dark with the page still saying
		// everything is fine.
		//
		// Only a session file carries a token. Without this check the banner
		// reports an expiry days away with an empty session name — the empty
		// name being the only visible symptom.
		if payload.AccessToken == "" || payload.ExpiresAt == "" {
			continue
		}

		expires, err := parseAWSTime(payload.ExpiresAt)
		if err != nil {
			unreadable++
			continue
		}

		candidate := Session{
			Checked:   true,
			Dir:       h.awsDir,
			Found:     true,
			ExpiresAt: expires,
			Session:   firstNonEmpty(payload.SessionName, payload.StartURL),
			State:     sessionState(expires, payload.RefreshToken, payload.RegistrationExpiresAt, h.now()),
		}
		if at, seen := byName[candidate.Session]; seen {
			if candidate.ExpiresAt.After(list.Sessions[at].ExpiresAt) {
				list.Sessions[at] = candidate
			}
			continue
		}
		// A file naming neither a session nor a start URL is its own session:
		// keying it on "" would fold every such file into one.
		if candidate.Session != "" {
			byName[candidate.Session] = len(list.Sessions)
		}
		list.Sessions = append(list.Sessions, candidate)
	}
	// Furthest expiry first. Stable, so of two sessions expiring at the same
	// instant the one read first leads.
	sort.SliceStable(list.Sessions, func(i, j int) bool {
		return list.Sessions[i].ExpiresAt.After(list.Sessions[j].ExpiresAt)
	})
	if len(list.Sessions) == 0 && unreadable > 0 {
		// Nothing was found *and* something could not be read, so the absence is
		// not established. One unreadable file beside a session that did read is a
		// different matter: the question was answered, and the answer stands.
		list.Checked = false
		list.Problem = fmt.Sprintf("%d file(s) in the cache could not be read", unreadable)
	}
	return list
}

// reasonOnly is the failure without the path it happened to, because Dir already
// carries the path and the two together do not fit.
//
// *A destination is not a diagnosis*, one directory along. `os.ReadDir`
// returns a *fs.PathError whose Error() is `open <path>: <reason>`, and that
// path can be 180 characters — so the banner would draw the reason cut off
// mid-word by detailLine's 200, with an ellipsis where "Access is denied."
// belongs. It reads perfectly well against a short path, which is why it is
// easy to miss.
func reasonOnly(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) && pathErr.Err != nil {
		return textutil.FirstLineN(pathErr.Err.Error(), detailLine)
	}
	return textutil.FirstLineN(err.Error(), detailLine)
}

// sessionState decides between "nothing to do", "it will sort itself out" and
// "log in, for real this time".
//
// The registration expiry is the subtle half. A refresh token is only good while
// the client registration behind it is itself in date; past that, the AWS CLI
// cannot renew silently and the operator has to log in. Ignoring it reports
// "renewable" on a session that will not renew, which is the one wrong answer
// that leaves someone waiting for a recovery that never comes.
func sessionState(expires time.Time, refreshToken, registrationExpiry string, now time.Time) string {
	if now.Before(expires) {
		return SSOValid
	}
	if refreshToken == "" {
		return SSOExpired
	}
	if registrationExpiry != "" {
		if regExpires, err := parseAWSTime(registrationExpiry); err == nil && !now.Before(regExpires) {
			return SSOExpired
		}
	}
	// Expired, but the AWS CLI renews it silently on the next call. Nothing for
	// the operator to do, and saying otherwise is worse than saying nothing.
	return SSORenewable
}

// parseAWSTime accepts the two shapes the AWS CLI has written over the years.
// A parse failure must not be reported as an expired session — that sends the
// operator to run a command that will not help.
func parseAWSTime(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05UTC", "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, errors.New("unrecognized expiry " + raw)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
