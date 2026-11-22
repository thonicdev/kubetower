package web

import (
	"strconv"
	"strings"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/health"
)

// The health page's counts, its notices, and its states. Without the counts the
// page cannot tell a cluster that failed from one nobody asked yet.
//
// `clusterState` keeps refused, IAM, plugin, SSO and DNS failures apart because
// each has a different person behind it: the user renews a session in seconds and
// cannot touch an IAM grant. `Reason.message` is prose a client must not branch
// on; the code carries the fact and `HealthCheck.remedy` the sentences.

// healthReport is the whole page, from the probe. A plain read serves the last
// round, so opening the page is free; `forced` is the *Check again* gesture.
func healthReport(s *Server, probe Health, rows []health.Cluster, unprobed, total int, forced bool) api.HealthReport {
	report := api.HealthReport{Checks: make([]api.HealthCheck, 0, len(rows)+1)}

	for _, row := range rows {
		report.Checks = append(report.Checks, healthCheckOf(row))
	}
	operator := hasAnOperator(s)
	if sso := probe.SSO(); operator && sso.Checked {
		report.Checks = append(report.Checks, ssoCheck(sso, rows))
	}

	fillHealthCounts(&report, rows, unprobed, total, forced)
	// Every notice is about the AWS CLI on this machine, so under the server profile
	// there is none: the pod has no `~/.aws` and signs with its ServiceAccount.
	if operator {
		report.Notices = healthNotices(probe, rows)
		report.SSO = ssoSessionsOf(probe.SSOSessions())
	}
	return report
}

// ssoSessionsOf is the home's session card, as data: names, states and expiries
// only, no host path. An unread cache carries no sessions and `measured: false`,
// drawn as "not read", never as no session.
func ssoSessionsOf(list health.Sessions) *api.HealthSSO {
	out := &api.HealthSSO{Measured: list.Checked, Sessions: []api.HealthSSOSession{}}
	if !list.Checked {
		out.Problem = list.Problem
		return out
	}
	for _, session := range list.Sessions {
		entry := api.HealthSSOSession{Name: session.Session, State: session.State}
		if !session.ExpiresAt.IsZero() {
			at := session.ExpiresAt.UTC()
			entry.ExpiresAt = &at
		}
		if session.State == health.SSOExpired {
			entry.Command = ssoLoginCommand(session.Session)
		}
		out.Sessions = append(out.Sessions, entry)
	}
	return out
}

// hasAnOperator is whether the SSO session this page reports on can exist: a
// person's AWS CLI on the machine the console runs on. Not under the server
// profile, where the row and the notices are left out of the response entirely
// rather than marked not applicable. A nil server (tests) counts as the desktop.
func hasAnOperator(s *Server) bool { return s == nil || !s.inCluster }

// healthCheckOf is one cluster's row, mapped from `health.Cluster`.
func healthCheckOf(row health.Cluster) api.HealthCheck {
	state, reason := clusterState(row.Name, row, true)
	check := api.HealthCheck{
		Key:     "cluster/" + row.Name,
		Label:   row.Name,
		Cluster: row.Name,
		State:   state,
		Reason:  reason,
		// The same remedy the rail's tooltip uses, so two surfaces cannot describe one
		// cluster two ways.
		Remedy:        clusterRemedy(row, true),
		ReadOnly:      row.ReadOnly,
		Version:       row.Version,
		MetricsSource: row.Metrics,
	}
	if !row.CheckedAt.IsZero() {
		at := row.CheckedAt
		check.CheckedAt = &at
	}
	if ms, ok := tookMillis(row.Took); ok {
		check.TookMs = &ms
	}
	return check
}

// tookMillis parses the probe's rendered duration ("184ms", "1.2s") into the
// number the contract wants. ok=false rather than 0 for a probe that did not
// complete: `tookMs: 0` would claim it was instant.
func tookMillis(took string) (int, bool) {
	took = strings.TrimSpace(took)
	switch {
	case took == "":
		return 0, false
	case strings.HasSuffix(took, "ms"):
		value, err := strconv.ParseFloat(strings.TrimSuffix(took, "ms"), 64)
		if err != nil {
			return 0, false
		}
		return int(value + 0.5), true
	case strings.HasSuffix(took, "s"):
		value, err := strconv.ParseFloat(strings.TrimSuffix(took, "s"), 64)
		if err != nil {
			return 0, false
		}
		return int(value*1000 + 0.5), true
	}
	return 0, false
}

// ssoCheck is the SSO session as a check of its own, a fact about this machine no
// cluster error can tell.
//
// It reads the session's own states (`health.SSOValid`, `SSORenewable`,
// `SSOExpired`, `SSOAbsent`), never `health.StateOK`. A renewable session is fine,
// since the AWS CLI renews it, until failing clusters contradict it: then it is
// the expired row, using `bannerOf`'s own condition so the row and the banner
// cannot disagree. An unreadable cache gets its notice and no row. `ok` is its
// own arm, so an unknown state is never read as fine.
func ssoCheck(sso health.Session, rows []health.Cluster) api.HealthCheck {
	check := api.HealthCheck{Key: "sso", Label: "AWS SSO session"}
	contradicted := false
	if banner := bannerOf(sso, rows); banner != nil && banner.Lapsed {
		contradicted = true
	}
	switch {
	case sso.Unknown():
		// The cache could not be read, which is not "no session". `Problem` is the
		// reason without its path.
		message := "the AWS SSO cache could not be read"
		if sso.Problem != "" {
			message += ": " + sso.Problem
		}
		reason := api.Because(api.CodeReadDenied, message).Retry(true)
		check.State, check.Reason = api.StateUnreadable, &reason
	case !sso.Found:
		// Read, and there is none: `no_session`, not `not_installed`. Retryable, since
		// `aws sso login` makes one.
		reason := api.Because(api.CodeNoSession, "no AWS SSO session on this machine").Retry(true)
		check.State, check.Reason = api.StateEmpty, &reason
	case sso.Expired():
		// `credentials_expired`: the one failure here the user fixes themselves, in one
		// command.
		reason := api.Because(api.CodeCredentialsExpired, "the AWS SSO session has expired").Retry(true)
		check.State, check.Reason = api.StateUnreadable, &reason
		who, what := whoFixes(health.StateSSO)
		check.Remedy = &api.Remedy{Who: who, What: what, Actionable: true}
	case contradicted:
		reason := api.Because(api.CodeCredentialsExpired,
			"the AWS SSO session is renewable, but the clusters disagree: it has not renewed").Retry(true)
		check.State, check.Reason = api.StateUnreadable, &reason
		who, what := whoFixes(health.StateSSO)
		check.Remedy = &api.Remedy{Who: who, What: what, Actionable: true}
	case sso.State == health.SSOValid, sso.Renewable():
		check.State = api.StateOK
	default:
		// A state this build does not know: `undecodable`, not retryable, no remedy.
		reason := api.Because(api.CodeUndecodable,
			"the AWS SSO session is in a state this console does not know: "+strconv.Quote(sso.State))
		check.State, check.Reason = api.StateUnreadable, &reason
	}
	return check
}

// fillHealthCounts is the head of the page: the sentence, its grade, the pills
// and the figures behind them. A context not yet probed has no row, so the rows
// alone would read "0 of 14 answering" during the first round.
func fillHealthCounts(report *api.HealthReport, rows []health.Cluster, unprobed, total int, forced bool) {
	headline, tone, counts := healthSummary(rows, unprobed, total)
	report.Headline, report.Tone = headline, tone
	for _, count := range counts {
		report.Counts = append(report.Counts, api.HealthCount{
			Label: count.Label,
			Count: count.Count,
			Tone:  count.Class,
			// The branchable half beside the words, for ordering and styling the pills.
			Code: healthCountCode(count.Label),
		})
	}

	answered := health.Summary(rows)[health.StateOK]
	probed := len(rows)
	// Of the contexts there are, not the rows in hand: they differ during a first
	// round.
	if total < probed {
		total = probed
	}
	report.Total = &total
	report.Probed = &probed
	report.Answered = &answered
	report.Pending = &unprobed

	// A round is running, so "nothing yet" does not read as "gave up": a forced read
	// is a round, and an ordinary read is refreshing while anything is unasked.
	refreshing := forced || unprobed > 0
	report.Refreshing = &refreshing
}

// healthCountCode is the pill's branchable half, derived from the label
// `stateWords` composed. Total over `stateWords`' outputs; an unknown label gets
// no code rather than a guessed one.
func healthCountCode(label string) api.ReasonCode {
	switch label {
	case "answering":
		return ""
	case "SSO session":
		return api.CodeCredentialsExpired
	case "IAM refused":
		return api.CodeReadDenied
	case "does not resolve":
		return api.CodeNameNotResolved
	case "refused":
		return api.CodeConnectionRefused
	case "no answer":
		return api.CodeTimeout
	case "credentials refused":
		return api.CodeCredentialsRefused
	case "plugin not installed":
		return api.CodeNotInstalled
	case "unclassified":
		return api.CodeUnclassified
	}
	return ""
}

// healthNotices is the three banners as data: the SSO session, this container's
// credential cache and the operator's own. Each has a third arm, `measured:
// false`, so a cache that could not be listed is not reported as no session.
// `rows` matters: `bannerOf` stays silent about a renewable session unless
// clusters are failing, and they are the evidence.
func healthNotices(probe Health, rows []health.Cluster) []api.HealthNotice {
	var out []api.HealthNotice

	if banner := bannerOf(probe.SSO(), rows); banner != nil {
		out = append(out, ssoNotice(*banner))
	}
	if banner := cacheBannerOf(probe.CredentialCache()); banner != nil {
		out = append(out, cacheNotice("cache/container", "This container's credential cache", *banner))
	}
	if banner := cacheBannerOf(probe.HostCredentialCache()); banner != nil {
		// Read-only from here: it breaks the operator's terminal and not this page, so it
		// is a notice of its own.
		out = append(out, cacheNotice("cache/host", "The operator's own credential cache", *banner))
	}
	return out
}

func ssoNotice(banner ssoBanner) api.HealthNotice {
	notice := api.HealthNotice{
		Key:      "sso",
		Title:    "AWS SSO session",
		Severity: "warn",
		Measured: boolOf(true),
	}
	switch {
	case banner.Unknown:
		notice.Title = "The AWS SSO cache could not be read"
		notice.Detail = banner.Problem
		notice.Measured = boolOf(false)
		// No command: the fix is a mount or a mode, not `aws sso login`.
		if banner.Dir != "" {
			notice.Paths = []string{banner.Dir}
		}
	case banner.Lapsed:
		notice.Title = "This SSO session is renewable and the clusters disagree"
		notice.Detail = banner.Expired
		notice.Severity = "bad"
		notice.Command = banner.Command
	default:
		notice.Title = "This AWS SSO session has expired"
		notice.Detail = banner.Expired
		notice.Severity = "bad"
		notice.Command = banner.Command
	}
	if notice.Detail == "" {
		notice.Detail = banner.Session
	}
	return notice
}

func cacheNotice(key, title string, banner cacheBanner) api.HealthNotice {
	notice := api.HealthNotice{
		Key:      key,
		Title:    title,
		Severity: "warn",
		Measured: boolOf(true),
	}
	if banner.Unknown {
		notice.Detail = banner.Problem
		notice.Measured = boolOf(false)
		if banner.Dir != "" {
			notice.Paths = []string{banner.Dir}
		}
		return notice
	}

	// The names, not a count: they are what the user pastes into `rm`.
	notice.Paths = append(append([]string{}, banner.Files...), banner.Unreadable...)
	notice.Detail = plural(len(notice.Paths), "entry", "entries") +
		" of " + strconv.Itoa(banner.Total) + " in " + banner.Dir + " will not read"
	if len(banner.Files) > 0 {
		notice.Severity = "bad"
		notice.Command = "rm " + strings.Join(prefixed(banner.Dir, banner.Files), " ")
	}
	return notice
}

// prefixed joins the directory onto each name, so it can be pasted.
func prefixed(dir string, names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if dir == "" {
			out = append(out, name)
			continue
		}
		out = append(out, dir+"/"+name)
	}
	return out
}
