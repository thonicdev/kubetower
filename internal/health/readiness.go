package health

// What `/kt-healthz` is allowed to mean.
//
// Answering 200 unconditionally would keep a process whose kubeconfig mount had
// vanished, or whose `aws` binary could not be run, `healthy` and in service
// forever: the container up, the HTTP server listening, and the one thing it
// exists to do — reach clusters — impossible.
//
// The condition below is deliberately narrow, and the narrowness is the design:
//
//   - **The kubeconfig no longer reads** — the mount went away, or the file was
//     replaced with something that is not a kubeconfig. Nothing this process
//     does can work, and no amount of waiting fixes it.
//
//     With several files (`KUBECONFIG` is a list) this is a choice, and the
//     answer is **any one of them, not all of them** —
//     see kubeconfigProblem, which also says why a *missing* path is not one of
//     them and names the file either way.
//   - **Nothing at all answers** — every context in the kubeconfig failed its
//     last probe. That is this process's problem far more often than fourteen
//     clusters' problem: a missing credential plugin, a lost network namespace,
//     a clock so far out that every TLS handshake fails.
//
// And what is deliberately *not* degraded: some clusters unreachable. That is
// the normal state of this installation — an SSO session lapses, a VPN drops,
// somebody's IAM policy changes — and it is what the health page is for.
// Restarting a container over it would take away the one screen that explains
// what happened, at the exact moment the operator opened it.
//
// It also never probes inline. The container's probe has a five-second budget
// (compose.dev.yaml) and a single cluster is allowed ten; a readiness answer
// that waits for fourteen of them times out and reports a failure it invented
// itself. So this reads the last round out of the cache and asks for the next
// one in the background.

import (
	"context"
	"fmt"
	"time"

	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/safego"
	"github.com/thonicdev/kubetower/textutil"
)

// warmSlack is how much longer than one cluster's timeout a background round is
// given. All probes concurrently, so a round costs the slowest single cluster
// and not the sum of fourteen.
const warmSlack = 5 * time.Second

// surveyWait is how long a readiness answer waits for the kubeconfig to be
// read. A file on a mount that has stopped answering blocks its reader with no
// way to call it off; past this the answer is that the kubeconfig does not
// read, and the read already running is waited on by the next answer rather
// than joined by a second one.
var surveyWait = 2 * time.Second

// Readiness is the answer, in a shape a handler can turn into a status code and
// one line of text.
type Readiness struct {
	// OK is whether this process can do its job at all.
	OK bool
	// Reason is the one line to log or to answer with. Always set, including
	// when OK — a probe that says only "ok" is a probe nobody can debug.
	Reason string
	// Total is how many contexts the kubeconfig declares.
	Total int
	// Probed is how many of them have an answer at all yet, and Answered how
	// many of those answered. Probed below Total is not a fault: the first round
	// may still be running.
	Probed   int
	Answered int
}

// Readiness reports whether this process can reach anything.
//
// The context bounds only the kubeconfig read; the cluster round it may start
// runs on its own deadline, because it outlives the request that asked for it.
func (h *Health) Readiness(ctx context.Context) Readiness {
	if reason := h.kubeconfigProblem(); reason != "" {
		return Readiness{Reason: reason}
	}
	if h.clusters == nil {
		// No manager at all. Not something a running binary can produce — main
		// refuses to start without one — but a Readiness that panicked would be
		// a worse answer than one that says so.
		return Readiness{Reason: "there is no cluster manager"}
	}

	names := h.clusters.Contexts()
	r := Readiness{Total: len(names)}
	if r.Total == 0 {
		// A kubeconfig that parses and declares nothing is the same accident as
		// one that does not parse — an empty file where a mount used to be —
		// wearing a shape the parser happens to accept.
		r.Reason = "the kubeconfig parses but declares no contexts"
		return r
	}

	h.mu.RLock()
	stuck := h.stuckRound()
	for _, name := range names {
		row, ok := h.cache[name]
		if !ok {
			continue
		}
		r.Probed++
		if row.State == StateOK {
			r.Answered++
		}
	}
	h.mu.RUnlock()

	// Whatever the answer, keep the next one coming. Nothing else in the binary
	// probes on a timer, so without this the cache is only ever filled by an
	// operator opening the health page — and the probe would report "starting"
	// for the lifetime of the container.
	h.warm()

	switch {
	case stuck != "":
		// **What the cache says is only as recent as the last round that
		// finished.** A round that has overrun its own bound is held up by
		// something that does not answer - a kubeconfig on a hung mount, a
		// credential plugin that never returns - and while it is held up the
		// rows above stop moving. Reporting them would keep answering the last
		// thing the process knew, as if it still knew it.
		r.Reason = stuck
	case r.Probed == 0:
		// The first round has not come back. Reporting a failure here would make
		// every start look broken for as long as the slowest cluster takes.
		r.OK = true
		r.Reason = fmt.Sprintf("%d contexts, none probed yet", r.Total)
	case r.Answered == 0:
		r.Reason = fmt.Sprintf("none of the %d contexts probed answered", r.Probed)
	default:
		r.OK = true
		r.Reason = fmt.Sprintf("%d of %d contexts answered", r.Answered, r.Total)
	}
	return r
}

// kubeconfigProblem reports why the kubeconfig cannot be read, or "".
//
// # What "degraded" means when KUBECONFIG is a list
//
// **Any one of the files, and not all of them.** With one file "the kubeconfig
// no longer reads" has one reading; with five it has two, and the weak one is
// wrong. Four files broken and one still reading is four fifths of the
// operator's clusters gone from the rail with a page that draws the fifth and
// says nothing — the answer that could not be obtained rendered as an answer,
// and worse here than usual because the rail cannot even name what is missing:
// it never saw those contexts. So one broken file degrades the process.
//
// **A path that is simply absent does not**, and that asymmetry is the whole
// design. `Precedence` skips a missing file exactly as kubectl does, and a
// `KUBECONFIG` naming a file that lives on the operator's other laptop is
// ordinary rather than broken — degrading over it would restart the container
// for a line somebody wrote on purpose. The exception is a path that **was**
// readable when this process started: that is a vanished mount, and it is
// caught by the snapshot `New` takes.
//
// **And the reason names the file**, which is the entire value of it:
// "the kubeconfig no longer reads" over five files is a sentence an operator
// cannot act on, and the one thing this endpoint is for is being actionable
// from `docker inspect`.
//
// Read through cluster.KubeconfigSurvey rather than through a load of our own,
// so "does not parse" here means what would stop cluster.Manager.Reload
// rebuilding a client rather than a second opinion that can disagree with it.
func (h *Health) kubeconfigProblem() string {
	if len(h.kubeconfig) == 0 {
		return ""
	}

	survey, ok := h.surveyed(surveyWait)
	if !ok {
		return "the kubeconfig no longer reads: reading " + h.kubeconfig.String() + " has not finished within " +
			surveyWait.String() + ", so the file system it is on is not answering"
	}
	for _, file := range survey.Files {
		if file.Problem != "" {
			return "the kubeconfig no longer reads: " + file.Path + ": " +
				textutil.FirstLineN(file.Problem, detailLine)
		}
		if file.Absent && h.kubeconfigRead[file.Path] {
			return "the kubeconfig no longer reads: " + file.Path +
				" was read at startup and has gone"
		}
	}
	if len(survey.Read()) == 0 {
		// Every path missing. Not one file's problem, so it is named as the
		// list's — and it is degraded rather than "a kubeconfig with no
		// clusters", which is what an empty bind mount looks like.
		return "the kubeconfig no longer reads: not one of " + h.kubeconfig.String() + " is there"
	}
	return ""
}

// readableNow is the startup snapshot behind Health.kubeconfigRead.
//
// Its own function so that "which files read" is one gesture rather than a loop
// inside a constructor, and so that a test can hand New a list and know what it
// recorded without reaching into the struct.
func readableNow(files cluster.Kubeconfig) map[string]bool {
	read := map[string]bool{}
	for _, path := range files.Survey().Read() {
		read[path] = true
	}
	return read
}

// warm asks for a round in the background, at most one at a time and no more
// often than the TTL.
//
// Detached from the caller's context on purpose: it is started by a request that
// is about to be answered, and a probe canceled the moment /kt-healthz replies
// would never finish and the cache would never fill. Check detaches each probe
// itself, for the same reason on the page's own path, so the deadline below
// is a second bound rather than the only one — the round is already bounded by
// one cluster's timeout apiece.
func (h *Health) warm() { h.round(true) }

// roundBound is how long a background round may take before it counts as held
// up: its own deadline, and as long again for what runs past a deadline.
func (h *Health) roundBound() time.Duration { return 2 * (h.timeout + warmSlack) }

// stuckRound says why the cache is not to be believed, or "": a background
// round started longer ago than roundBound and has not finished. Called with
// mu held.
func (h *Health) stuckRound() string {
	if !h.warming || h.warmStarted.IsZero() {
		return ""
	}
	age := h.now().Sub(h.warmStarted)
	if age <= h.roundBound() {
		return ""
	}
	return fmt.Sprintf("the probes' last round started %s ago and has not finished (it is given %s): something "+
		"it reads is not answering, so what the clusters said is that old", age.Round(time.Second), h.roundBound())
}

// surveyed reads the kubeconfig, waiting at most wait. A read still running
// from an earlier answer is waited on rather than doubled, so a mount that
// hangs holds one reader and not one per readiness answer.
func (h *Health) surveyed(wait time.Duration) (cluster.KubeconfigSurvey, bool) {
	h.mu.Lock()
	running := h.surveying
	if running == nil {
		running = &surveying{done: make(chan struct{})}
		h.surveying = running
		read := h.survey
		if read == nil {
			read = h.kubeconfig.Survey
		}
		safego.Go("readiness: reading the kubeconfig", func() error {
			running.survey = read()
			return nil
		}, func(error) {
			h.mu.Lock()
			h.surveying = nil
			h.mu.Unlock()
			close(running.done)
		})
	}
	h.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-running.done:
		return running.survey, true
	case <-timer.C:
		return cluster.KubeconfigSurvey{}, false
	}
}

// surveying is one read of the kubeconfig in progress; survey is set before
// done closes.
type surveying struct {
	done   chan struct{}
	survey cluster.KubeconfigSurvey
}

// round is warm's body, with the one thing the two callers disagree about.
//
// The readiness probe wants the backoff — it asks every sixty seconds for ever,
// with nobody looking, and backgroundFailedMax exists for exactly that. The
// health page does not: an operator who has just run `aws sso login` and opened
// the page must not be shown a ten-minute-old refusal because a timer says so,
// which is the argument already written above backgroundDue. Same gate, same one
// round at a time, different answer to "may a failing cluster be re-asked".
func (h *Health) round(backoff bool) {
	h.mu.Lock()
	if h.warming || (!h.warmed.IsZero() && h.now().Sub(h.warmed) < h.ttl) {
		h.mu.Unlock()
		return
	}
	h.warming = true
	h.warmStarted = h.now()
	h.mu.Unlock()

	// Through safego, so a panic does not end the process: `warming` is cleared
	// in `done`, after a panic
	// as after a round, so readiness is never held by a round that died.
	safego.Go("readiness: a round of probes", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), h.timeout+warmSlack)
		defer cancel()
		h.all(ctx, false, backoff)
		return nil
	}, func(error) {
		h.mu.Lock()
		h.warming = false
		h.warmed = h.now()
		h.mu.Unlock()
	})
}
