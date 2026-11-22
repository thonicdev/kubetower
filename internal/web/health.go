package web

// The health report: which contexts answer, and who fixes the ones that do not.
// An expired SSO session, an IAM grant nobody here holds, a name that does not
// resolve and a cluster that is down all fail alike and need four different
// people. `internal/health` classifies; this file only words its answer:
//
//   - The SSO banner appears for an expired session, never for a renewable
//     one the AWS CLI renews silently, unless the clusters contradict it.
//   - "Yours to fix" comes only from health.Cluster.Actionable(): an IAM
//     refusal fails identically from the host, and sending the operator after
//     it wastes their afternoon.

import (
	"context"
	"strconv"
	"strings"

	"github.com/thonicdev/kubetower/internal/health"
	"github.com/thonicdev/kubetower/internal/recorder"
)

// Health is the slice of internal/health the health report needs. Nil is valid:
// the console starts and says this build has no health probe.
type Health interface {
	All(ctx context.Context, force bool) []health.Cluster
	// Check probes one context; `force` skips the cache. The overview's Refresh asks
	// it for the cluster on screen only.
	Check(ctx context.Context, name string, force bool) health.Cluster
	// Known is what is already in hand, with no cluster contacted: what a plain read
	// serves, so it costs nothing (internal/health/known.go). All is what "check
	// again" calls.
	Known() health.Snapshot
	SSO() health.Session
	// SSOSessions is every session in the same cache, for the home's session card;
	// SSO() is its first entry.
	SSOSessions() health.Sessions
	// CredentialCache is the AWS CLI's assume-role cache, read from disk: like SSO,
	// a fact about this machine no cluster error can tell (internal/health/credcache.go).
	CredentialCache() health.CredentialCache
	// HostCredentialCache is the operator's own cache, which this process can read
	// and not write. Separate from CredentialCache: it breaks a different thing and
	// needs a different sentence.
	HostCredentialCache() health.CredentialCache
}

// Recorders is the event poller, reduced to its status. It polls every cluster on
// a timer and reports each failure once, so an archive that stopped collecting
// from some clusters looks like a healthy one anywhere else. Nil means no
// recorder.
type Recorders interface {
	Status() recorder.Status
}

// ssoBanner is the notice about the operator's SSO session. Three arms, not two:
// the cache can hold a lapsed session, hold nothing, or refuse to be read, and
// the third is never drawn as the second.
type ssoBanner struct {
	Session string
	Expired string
	// Lapsed marks a session renewable on paper that the clusters contradict; the
	// wording differs because the claim does.
	Lapsed bool
	// Unknown marks the third state: the SSO cache could not be read, so neither
	// "expired" nor "fine".
	Unknown bool
	// Dir and Problem are the directory and the reason, set only on Unknown, named
	// so the reader can act on them.
	Dir     string
	Problem string
	// Command is what to type, spelled out. Empty on Unknown: there the fix is a
	// mount or a mode, and offering `aws sso login` would be wrong.
	Command string
}

// cacheBanner is the notice about the AWS CLI's assume-role cache. An entry
// written by two processes at once holds two JSON documents, every read fails,
// and the error does not say so (see internal/health/credcache.go); only looking
// at the directory finds it.
type cacheBanner struct {
	// Dir and Files are what to delete: names only, since these files hold live
	// temporary credentials.
	Dir   string
	Files []string
	// Unreadable is the permissions case: a different fix, a different sentence.
	Unreadable []string
	// Total is how many entries were examined, so "1 of 14" can be said.
	Total int

	// Unknown is the third arm: a directory that cannot be listed has no entries, so
	// `Broken()` is false, and without this there would be no notice for the one
	// failure only looking can find. Separate from the SSO banner's because the fix
	// differs (deleting a file, not `aws sso login`).
	Unknown bool
	// Problem is why it could not be listed, empty unless Unknown.
	Problem string
}

type healthCount struct {
	Label string
	Count int
	Class string
}

// stateWords is the state as a label and a color. Amber for the states that are
// somebody else's to fix: red is for the rows this operator acts on.
// page where nothing is.
func stateWords(state string) (label, class string) {
	switch state {
	case health.StateOK:
		return "answering", "ok"
	case health.StateSSO:
		return "SSO session", "bad"
	case health.StateIAM:
		return "IAM refused", "warn"
	case health.StateDNS:
		return "does not resolve", "warn"
	case health.StateRefused:
		return "refused", "bad"
	case health.StateTimeout:
		return "no answer", "warn"
	case health.StatePlugin:
		// Red: the two commonest causes are the operator's, and the third is a ticket
		// they raise.
		return "credentials refused", "bad"
	case health.StateMissing:
		// Nothing refused anything; there was no program to ask. Red: the operator's to
		// fix.
		return "plugin not installed", "bad"
	default:
		return "unclassified", "bad"
	}
}

// whoFixes names the person who fixes a state: the operator renews a session in
// seconds and cannot touch an IAM grant, and only the console can say which.
func whoFixes(state string) (who, what string) {
	switch state {
	case health.StateOK:
		return "", ""
	case health.StateSSO:
		// The row carries its own answer, the notice only adds to it: bannerOf draws
		// nothing when the SSO cache holds no session, which is every installation whose
		// plugin is not `aws sso` (an OIDC or GKE context answering 401 is classified
		// StateSSO too). The sentence claims this line is the whole answer, not that
		// there is no session, since a live session and a failing OIDC context can
		// coexist.
		return "You",
			"Sign in again on the host, not in this container: `aws sso login` for an EKS " +
				"context, or whatever your kubeconfig's credential plugin signs in with. The " +
				"exact command is on this card when the AWS CLI's own SSO cache shows a lapsed " +
				"session; without it, this line is the whole answer."
	case health.StateIAM:
		return "Your cloud administrator",
			"The role cannot be assumed by this identity. It fails identically from the host — " +
				"nothing in this console can change it."
	case health.StateDNS:
		return "Whoever runs the network",
			"The name does not resolve from inside this container: a VPN that is not up, " +
				"or a private hosted zone this container cannot see."
	case health.StateRefused:
		return "This console's configuration",
			"Something answered the address and refused. A cluster on 127.0.0.1 means the host, " +
				"which inside a container is not this process."
	case health.StateTimeout:
		return "Whoever runs the network",
			"Nothing answered in time: a VPN, a firewall, or the cluster is genuinely down."
	case health.StatePlugin:
		// The error cannot tell the causes apart (client-go sends the plugin's stderr to
		// this process's own and keeps none), so the answer is the order to check them,
		// cheapest first, and where the plugin's words are. Both channels are named, not
		// detected: `docker compose logs` in the container, `kubetower.log` in the state
		// directory for a downloaded binary.
		return "You first, then your cloud administrator",
			"The `aws` plugin ran and refused. Its message is not in the error — it is in " +
				"this process's own log: `docker compose logs` in the container, or " +
				"`kubetower.log` in the state directory. In order of cost: an SSO session that has lapsed, " +
				"a corrupt entry in the assume-role cache (named on the home if so), or a " +
				"role this identity may not assume, which is your administrator's."
	case health.StateMissing:
		// A missing binary, not an unknown shape. The causes are told apart by whether a
		// terminal on the same cluster works: if it does, this process was started
		// without the tool on its PATH, as launching from the macOS Finder or Dock does.
		return "You",
			"There is no such program on this process's PATH, which is beside this row. " +
				"If a dock terminal on this cluster works, the tool is installed and this " +
				"process simply cannot see it: a window opened from the Finder or the Dock " +
				"inherits launchd's PATH, not your shell's. Starting the console from a " +
				"terminal is the quickest check."
	default:
		return "Nobody yet", "This failure is not one of the known shapes; the error is beside it."
	}
}

// healthSummary is the sentence above the table, and the pills beside it.
func healthSummary(rows []health.Cluster, unprobed, total int) (headline, class string, counts []healthCount) {
	if len(rows) == 0 {
		if unprobed > 0 {
			// Not "0 of 14 answering": a context nobody has asked yet has no state. This is
			// the cold-start render.
			return "asking " + plural(unprobed, "context", "contexts") + "…", "muted", nil
		}
		return "no contexts in the kubeconfig", "bad", nil
	}

	counted := health.Summary(rows)
	answered := counted[health.StateOK]

	switch {
	case answered == len(rows):
		class = "ok"
	case answered == 0:
		class = "bad"
	default:
		class = "warn"
	}
	// Of the contexts there are, not the rows in hand, which differ while a first
	// round is running.
	of := total
	if of < len(rows) {
		of = len(rows)
	}
	headline = plural(answered, "context", "contexts") + " of " + strconv.Itoa(of) + " answering"

	// Worst first, the table's order.
	for _, state := range []string{
		health.StateSSO, health.StateMissing, health.StatePlugin, health.StateKO,
		health.StateRefused, health.StateTimeout, health.StateDNS, health.StateIAM,
	} {
		if n := counted[state]; n > 0 {
			label, pill := stateWords(state)
			counts = append(counts, healthCount{Label: label, Count: n, Class: pill})
		}
	}
	return headline, class, counts
}

// bannerOf decides whether to say anything about the SSO session, and what, or
// returns nil.
//
// An expired session gets a banner. A renewable one does not, since the AWS CLI
// renews it on the next call, unless the clusters contradict it: a session that
// failed to renew makes every EKS context fail, and the failures are the
// evidence. A cache directory that refuses to be listed (a bind-mounted `~/.aws`
// with a uid mismatch) gets the third arm, ungated: it is broken now, not a
// prediction. Silence then means only a live session, or an empty cache with
// every cluster answering.
func bannerOf(session health.Session, rows []health.Cluster) *ssoBanner {
	if session.Unknown() {
		return &ssoBanner{Unknown: true, Dir: session.Dir, Problem: session.Problem}
	}

	lapsed := false
	if !session.Expired() {
		// A renewable session that has not renewed: only worth saying while something is
		// failing.
		if !session.Found || session.ExpiresAt.IsZero() || !session.Renewable() {
			return nil
		}
		if !anyFailing(rows) {
			return nil
		}
		lapsed = true
	}

	banner := &ssoBanner{Session: session.Session, Command: ssoLoginCommand(session.Session), Lapsed: lapsed}
	if !session.ExpiresAt.IsZero() {
		banner.Expired = session.ExpiresAt.Local().Format("2006-01-02 15:04")
	}
	return banner
}

// ssoLoginCommand is what to type to sign a session in again. The cache carries
// an `sso_session` name or a start URL; only a name can go on the command line,
// so a URL gives the bare command.
func ssoLoginCommand(name string) string {
	if name != "" && !strings.Contains(name, "://") {
		return "aws sso login --sso-session " + name
	}
	return "aws sso login"
}

// cacheBannerOf draws the warning when there is a file to delete, or when the
// directory could not be listed (health.CredentialCache.Problem). Unlike the
// lapsed-session banner it waits for no failing cluster: a corrupt entry is
// broken now and does not repair itself. It does not claim which cluster it
// explains, since the entry may belong to a role nobody uses today.
func cacheBannerOf(cache health.CredentialCache) *cacheBanner {
	if cache.Unknown() {
		return &cacheBanner{Dir: cache.Dir, Unknown: true, Problem: cache.Problem}
	}
	if !cache.Broken() {
		return nil
	}
	return &cacheBanner{
		Dir:        cache.Dir,
		Files:      cache.Corrupt,
		Unreadable: cache.Unreadable,
		Total:      cache.Total,
	}
}

// anyFailing reports whether at least one context is not answering, whatever its
// classification: a lapsed session can surface as a timeout.
func anyFailing(rows []health.Cluster) bool {
	for _, row := range rows {
		if row.State != health.StateOK {
			return true
		}
	}
	return false
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
