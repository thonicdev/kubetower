package overview

import "testing"

// "Nothing needs acting on" is the one badge on this console that talks an
// operator out of reading the rest of the page. It may only appear when it is
// the whole truth.
//
// Reading `len(o.Alerts) == 0` alone — node conditions only — a cluster whose
// workloads could not be listed at all (a lapsed SSO session, an RBAC gap, an
// API server that timed out) would be drawn with a green "nothing needs acting
// on" across the top, while the "Not shown" panel further down the same page
// lists the things that failed to load: an answer that could not be obtained,
// rendered as an answer, and rendered as the reassuring one.

func TestAPageThatCouldNotBeReadIsNotAllWell(t *testing.T) {
	o := ClusterOverview{
		// No node alert at all. Node conditions alone would call this all well.
		Alerts: nil,
		Degraded: []PanelError{
			{Panel: "Workloads", Reason: "the API server did not answer in time"},
		},
	}
	if o.AllWell() {
		t.Error("a cluster with a panel that could not be filled reports AllWell. " +
			"The badge says nothing needs acting on, above a dashboard that failed to " +
			"load — an empty answer and a failed answer are different answers")
	}
}

func TestANodeAlertIsStillNotAllWell(t *testing.T) {
	o := ClusterOverview{Alerts: []Alert{{Subject: "node-a", Summary: "NotReady"}}}
	if o.AllWell() {
		t.Error("a NotReady node reports AllWell")
	}
}

// The badge has to be reachable, or the rule above has quietly replaced a badge
// that lied with a badge that never appears — which is the mirror defect and
// just as hard to notice.
func TestAHealthyClusterIsStillAllWell(t *testing.T) {
	o := ClusterOverview{Alerts: nil, Degraded: nil}
	if !o.AllWell() {
		t.Error("a cluster with no alerts and nothing degraded does not report AllWell, " +
			"so the badge can never be drawn at all")
	}
}
