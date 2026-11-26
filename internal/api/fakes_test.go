package api

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// The backend the conformance test drives.
//
// It is deliberately **not** a stub that answers `ok` to everything. The
// interesting half of this contract is the four states that are not `ok`, and a
// fake that never produces one would put every failed arm of every envelope
// through no schema at all. So: the cluster list carries one unreachable cluster
// beside a healthy one, the overview's top panel is `absent`, a Helm release is
// `unreadable`, the extension list is `empty`, and the log stream opens and then
// says why it stopped.

var testMoment = time.Date(2026, 9, 11, 9, 31, 2, 0, time.UTC)

type fakeProbe struct{}

func (fakeProbe) Ready(context.Context) (bool, string) { return true, "3 of 3 contexts answered" }

type fakeSessions struct{}

func (fakeSessions) LoginJSON(w http.ResponseWriter, r *http.Request)  { w.WriteHeader(204) }
func (fakeSessions) LogoutJSON(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }
func (fakeSessions) Current(*http.Request) (Session, bool) {
	return Session{
		Identity:          "operator",
		Method:            SessionMethodSSO,
		Groups:            []string{"oidc:platform"},
		IdleExpiresAt:     testMoment.Add(30 * 24 * time.Hour),
		AbsoluteExpiresAt: testMoment.Add(90 * 24 * time.Hour),
	}, true
}

// fakeSignIn offers both ways in, so that every session operation in the
// contract is registered and can be exercised. The two redirects answer the way
// internal/auth's do - a Location and no body - because the contract declares
// no content for either.
type fakeSignIn struct {
	password, sso    bool
	launch, handoffs bool
}

func (f fakeSignIn) OffersPassword() bool { return f.password }
func (f fakeSignIn) OffersSSO() bool      { return f.sso }
func (f fakeSignIn) Methods() SignInMethods {
	return SignInMethods{Password: f.password, SSO: SSO{State: SSOUnavailable,
		Detail: "The identity provider did not answer: dial tcp: connection refused."}}
}
func (fakeSignIn) StartSSO(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Location", "https://idp.example/auth?state=s")
	w.WriteHeader(http.StatusFound)
}
func (fakeSignIn) FinishSSO(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Location", "/")
	w.WriteHeader(http.StatusSeeOther)
}
func (f fakeSignIn) OffersLaunch() bool    { return f.launch }
func (f fakeSignIn) AcceptsHandoffs() bool { return f.handoffs }
func (fakeSignIn) LaunchSignIn(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Location", "/")
	w.WriteHeader(http.StatusFound)
}
func (fakeSignIn) Handoff(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"sealed":"c2VjcmV0LWNvZGUtZm9yLWEtd2luZG93LXRoaXMtcHJvY2Vzcy1vcGVucw"}`))
}

type fakeProfile struct{}

func (fakeProfile) Capabilities(*http.Request) Capabilities {
	return Capabilities{
		Mode: "container", PodExec: true, PortForward: true, PortForwardLimit: 8,
		// Every member filled, so TestTheReadOnlyStateCarriesEveryMemberTheContractDeclares
		// has something to measure and the conformance test sees `unmatched`.
		ReadOnly: ReadOnlyState{
			Fence: FenceUnknown, Measured: false,
			Detail:    "the guardrail could not be probed: no shell. It may or may not be in front of kubectl and helm.",
			Unmatched: []string{"prdo"},
		},
		Version: "0.0.0-dev", KindPreferences: true,
	}
}

type fakeClusters struct{}

func (fakeClusters) Known(name string) bool {
	return name == testCluster || name == "staging" || name == guardedCluster
}

// List is three clusters, and between them they carry **every member the
// contract declares** — which is what TestTheClusterListCarriesEveryMemberTheContractDeclares
// asserts against `api/openapi.yaml`. A fake that filled only the three required
// members would leave that test with nothing to measure, and it is precisely the
// unfilled optional member that the conformance test alone cannot see.
func (fakeClusters) List(context.Context) ([]Cluster, *Failure) {
	unreachable := Because(CodeClusterUnreachable, "the API server did not answer in time").Retry(true)
	notProbed := Because(CodeNotProbedYet, "this cluster has not been probed yet").Retry(true)
	first, second := 0, 1
	return []Cluster{
		{Name: "prod", Label: "Production", Initials: "PR", Colour: "amber",
			Bar: 1, Order: &first, ReadOnly: true, State: StateOK},
		// One unreachable cluster leaves the other nine ok, which is why
		// reachability is per item and not only on the envelope. It carries the
		// remedy, because a cluster that answered has nobody to name.
		// An EKS context, signing in through the AWS CLI.
		{Name: "staging", Label: "Staging", Initials: "ST", Colour: "teal-pale", Bar: 1, Order: &second,
			AWS: true, State: StateUnreachable, Reason: &unreachable,
			// When the probe found it so: the overview reads this to know
			// whether the verdict is fresh enough to stand in for a read.
			CheckedAt: timeRef(time.Date(2026, 9, 11, 9, 41, 0, 0, time.UTC)),
			Remedy: &Remedy{
				Who:  "Whoever runs the network",
				What: "Nothing answered in time: a VPN, a firewall, or the cluster is genuinely down.",
			}},
		// Hidden, on no hotbar, on the local machine, and nobody has asked it
		// yet — which is `pending` and not `unreadable`. It is the ordinary
		// state of every cluster at start-up.
		{Name: "docker-desktop", Label: "docker-desktop", Initials: "DD", Colour: "blue",
			Hidden: true, Loopback: true, State: StatePending, Reason: &notProbed},
	}, nil
}

// Probe answers the entry List draws, as if this cluster had just been asked
// again: staging is still unreachable.
func (f fakeClusters) Probe(ctx context.Context, name string) (Cluster, *Failure) {
	items, _ := f.List(ctx)
	for _, item := range items {
		if item.Name == name {
			return item, nil
		}
	}
	return Cluster{}, Fail(StateAbsent, CodeNoSuchKind, "no cluster called "+name)
}

// Nav carries both shapes a menu has, because the members test takes the union
// across the groups: a complete group with its items, and the deferred heading
// whose items are somewhere else entirely.
func (fakeClusters) Nav(context.Context, string) (NavMenu, *Failure) {
	return NavMenu{Groups: []NavGroup{{
		Key: "workloads", Label: "Workloads",
		// `namespaced`, as `internal/web` sets it on a namespaced kind's row.
		Items: []Ref{{Text: "Pods", Href: "/c/prod/pods", Namespaced: true, Aliases: []string{"po", "pod", "pods"}}},
	}, {
		// No items, and that is not an answer about this cluster while
		// `deferred` is true. A client that reads it as one draws a heading onto
		// nothing — which is the defect `getNavGroup` exists to close.
		Key: "custom-resources", Label: "Custom Resources",
		Items: []Ref{}, Deferred: true,
	}}}, nil
}

// NavGroup is the deferred heading, filled: one nested disclosure per API group,
// with a note saying why it is short. All three of its members that `Nav` cannot
// carry — `groups`, `note`, and a `deferred` that is false — are here, because
// the members test measures the union of both operations' answers.
func (fakeClusters) NavGroup(_ context.Context, _, group string) (NavGroup, *Failure) {
	if group != "custom-resources" {
		return NavGroup{}, Absent("no menu group named " + group + " is fetched separately")
	}
	return NavGroup{
		Key: "custom-resources", Label: "Custom Resources", Items: []Ref{},
		Note: "one API group could not be listed",
		AccessNote: "One kind this console may not list is not shown: " +
			"the identity it reads with has to be granted the read on it.",
		Groups: []NavGroup{{
			Key: "cert-manager.io", Label: "cert-manager.io",
			Items: []Ref{{Text: "Certificates", Href: "/c/prod/certificates.cert-manager.io"}},
		}},
	}, nil
}

// NavOccupancy carries all three states and a reason, so each is validated
// against the contract.
func (fakeClusters) NavOccupancy(context.Context, string) (NavOccupancy, *Failure) {
	return NavOccupancy{Kinds: map[string]KindOccupancy{
		"pods":                         {State: OccupancySome},
		"certificates.cert-manager.io": {State: OccupancyNone},
		"applications.argoproj.io":     {State: OccupancyUnknown, Reason: "this console may not list it"},
	}, Note: "the custom resource kinds could not be listed"}, nil
}

// Extensions is empty rather than absent: this build carries extensions and none
// is enabled on this cluster, which is the `empty` arm and a different sentence
// from a build that carries none.
func (fakeClusters) Extensions(context.Context, string) ([]Extension, *Failure) {
	// One row with every member, so the response is validated against the
	// contract with its settings in it rather than as an empty list.
	return []Extension{{
		Key: "sample-extra", Label: "Sample", Summary: "A sample extension.", Needs: "nothing",
		Enabled: true, Settings: []ExtensionSetting{{Key: "list-column", Label: "Show its column",
			Summary: "Off, the column is not drawn.", Kind: "bool", Value: true, Default: true}, {
			Key: "collector", Label: "Collector", Summary: "Where it reads from.", Kind: "text",
			Text: "ops/cron", Placeholder: "found by its label", Env: "KT_SAMPLE_COLLECTOR", Pinned: true}},
	}}, nil
}

// One suggestion, so the response is validated against the contract with a
// row in it.
func (fakeClusters) ExtensionSuggestions(*http.Request, string) ([]ExtensionSuggestion, *Failure) {
	return []ExtensionSuggestion{{Key: "sample-extra", Label: "Sample", Evidence: "1 sample Service"}}, nil
}

func (fakeClusters) SetExtension(context.Context, string, string, bool) error { return nil }
func (fakeClusters) SetExtensionSetting(context.Context, string, string, string, bool) error {
	return nil
}

func (fakeClusters) SetExtensionText(string, string, string, string) error {
	return nil
}

type fakeSettings struct{}

// Every member of the settings payload carried at least once, for the reason the
// other fakes are generous: `TestTheSettingsPayloadCarriesEveryMemberTheContractDeclares`
// asserts both directions against `api/openapi.yaml`.
//
// **`Archive` is an envelope and a zero one is not valid.** Left unset it
// marshals as `{"state":""}`, which the response-conformance test refuses -
// the envelope doing its job.
func (fakeSettings) Settings() Settings {
	oldest := testMoment
	checked := testMoment
	return Settings{
		General: SettingsWrite{
			ArchiveEnabled: boolPtr(true), CaptureLogs: boolPtr(false), RetentionDays: intPtr(21),
		},
		// 21 is not one of the five offered, so it is **inserted in order**. A
		// control that cannot represent its own current state changes it: with
		// retention at 21 no option would carry `selected`, the browser would submit the
		// first, and a Save opened to tick an unrelated checkbox would destroy twenty
		// days of archived events while saying "Saved."
		RetentionChoices: []RetentionChoice{
			{Days: 1, Label: "1 day"},
			{Days: 3, Label: "3 days"},
			{Days: 7, Label: "7 days"},
			{Days: 14, Label: "14 days"},
			{Days: 21, Label: "21 days"},
			{Days: 30, Label: "30 days"},
		},
		Archive: Data(ArchiveFacts{
			Path: "/state/archive", Bytes: 4 << 20, Days: 6, OldestAt: &oldest,
			WritesRefused: true,
			Note:          "the archive volume is read-only, so nothing has been written since 09:00",
		}),
		NodeShell: NodeShellSettings{
			Enabled: true, Available: true, Image: "busybox:1.36",
			// Shown so a user can see what emptying the field goes back to
			// before they do it.
			DefaultImage: "alpine:3.24",
			Persisted:    boolPointer(true),
			Note:         "written to /state/nodeshell.json",
		},
		Info: SettingsInfo{
			StatePath:       "/state",
			KubeconfigFiles: []string{"/kube/config", "/kube/work (not there)"},
			KubeconfigShadowed: []KubeconfigShadow{{
				Context: "prod", KeptFrom: "/kube/config", ShadowedIn: "/kube/work",
				Sentence: "The context prod is taken from /kube/config; the one in /kube/work " +
					"is not merged and does not appear anywhere in this console.",
			}},
			ListenAddress:    "127.0.0.1:5823",
			Version:          "kubetower 0.7.2 (2026-09-11, windows/amd64)",
			Development:      false,
			StateWrittenBy:   "0.8.0",
			StateIsNewer:     true,
			RepositoryURL:    "https://github.com/thonicdev/kubetower",
			DocumentationURL: "https://thonicdev.github.io/kubetower-site/docs/",
			// The `newer` arm deliberately, and not `current`: it is the only one
			// that carries every member, so a fixture on the comfortable arm would
			// leave `latest`, `releaseUrl` and `checkedAt` declared and unmeasured
			// — which is how members of `info` could reach the contract
			// with no Go field behind them.
			Update: &SettingsUpdate{
				State:      "newer",
				Latest:     "0.8.0",
				ReleaseURL: "https://github.com/thonicdev/kubetower/releases/tag/v0.8.0",
				CheckedAt:  &checked,
				Note:       "KubeTower 0.8.0 has been released. This build is 0.7.2.",
				// Mid-download, so every member of the installer is measured.
				Install: &UpdateInstall{
					Phase: "downloading", Enabled: true, When: "now", Password: true, Supported: true, Installation: "linux-appimage",
					Version: "0.8.0", Asset: "kubetower-0.8.0-x86_64.AppImage", Done: 400, Total: 1000,
					Reason: "", Ends: UpdateEnds{Terminals: 2, Forwards: 1},
				},
			},
		},
	}
}

func (f fakeSettings) SaveGeneral(SettingsWrite) (Settings, error) {
	out := f.Settings()
	// **`persisted: false` is not a failure**, and the fixture carries that arm
	// rather than the comfortable one: a value that validated is in effect
	// whether or not the file could be written, and what the user needs to be
	// told is the narrower true thing.
	out.Applied = &SettingsApplied{
		Persisted: false,
		Note:      "This is in effect now and will not survive a restart: read-only file system",
	}
	return out, nil
}

func (fakeSettings) SaveNodeShell(want NodeShellSettings) (NodeShellSettings, error) {
	want.Available = true
	want.DefaultImage = "alpine:3.24"
	want.Persisted = boolPointer(true)
	return want, nil
}

func (fakeSettings) PreviewClear(string) (PlanFacts, error) {
	return PlanFacts{
		Verb:         "discard",
		Targets:      []PlanTarget{{Kind: "preference", Name: "rail"}},
		Count:        1,
		Destructive:  true,
		Confirmation: &Confirmation{Kind: "acknowledge"},
	}, nil
}

func (fakeSettings) Clear(string) (ActOutcome, error) {
	return ActOutcome{Succeeded: 1, Results: []ActResult{
		{Target: PlanTarget{Kind: "preference", Name: "rail"}, OK: true},
	}}, nil
}

// Health answers with one check per state that is interesting, because the page
// exists precisely to say which of the five is true.
// The health page, with **every member of every health schema carried at least
// once**, for the reason `fakeReads.Overview` is generous:
// `TestTheHealthReportCarriesEveryMemberTheContractDeclares` drives this and
// asserts both directions against `api/openapi.yaml`, so a member left out here
// fails rather than passing quietly.
//
// `force` is ignored: what it changes is which rows the *adapter* reads, and the
// adapter is `internal/web`. What it must not change is the shape.
func (fakeSettings) Health(_ context.Context, _ bool) (HealthReport, *Failure) {
	absent := Because(CodeNotInstalled, "no metrics source is installed on this cluster")
	expired := Because(CodeCredentialsExpired, "this SSO session lapsed 5h ago")
	checked := testMoment
	took := 184
	total, probed, answered, pending := 14, 2, 1, 12
	refreshing := true
	return HealthReport{
		Checks: []HealthCheck{
			{
				Key: "cluster/prod", Label: "prod", Cluster: "prod", State: StateOK,
				ReadOnly: true, Version: "v1.29.4", MetricsSource: "prometheus",
				CheckedAt: &checked, TookMs: &took,
			},
			{
				Key: "cluster/staging", Label: "staging", Cluster: "staging",
				State: StateUnreadable, Reason: &expired,
				// Who fixes it. The judgment the page is read for, and three
				// different people hide behind one red row.
				Remedy: &Remedy{
					Who:        "You",
					What:       "Sign in again on the host: `aws sso login`.",
					Actionable: true,
					Command:    "aws sso login --profile staging",
				},
			},
			{Key: "metrics/prod", Label: "Metrics", Cluster: "prod", State: StateAbsent, Reason: &absent},
		},
		// A report that is only a list of rows cannot say what is missing from
		// it: twelve of these fourteen contexts have no row at all.
		Total: &total, Probed: &probed, Answered: &answered, Pending: &pending,
		Refreshing: &refreshing,
		Headline:   "1 context of 14 answering",
		Tone:       "warn",
		Counts: []HealthCount{
			{Label: "SSO session", Count: 1, Code: CodeCredentialsExpired, Tone: "bad"},
		},
		Notices: []HealthNotice{{
			Key:      "cache/container",
			Title:    "This container's credential cache",
			Detail:   "2 entries of 14 in /root/.aws/cli/cache will not read",
			Command:  "rm /root/.aws/cli/cache/a.json /root/.aws/cli/cache/b.json",
			Paths:    []string{"/root/.aws/cli/cache/a.json", "/root/.aws/cli/cache/b.json"},
			Severity: "bad",
			// True here; the third arm - could not be established at all - is what
			// `measured: false` is for and it must not draw as either of the other
			// two.
			Measured: boolPointer(true),
			Remedy:   &Remedy{Who: "You", What: "Delete the two entries named above.", Actionable: true},
		}},
		// The home's session card: one expired session, so
		// the command is carried. `problem` is the unread arm's, carried here
		// beside a measured list only so every member is exercised.
		SSO: &HealthSSO{
			Measured: true,
			Problem:  "carried for the member check only",
			Sessions: []HealthSSOSession{{
				Name: "corp", State: "expired", ExpiresAt: &checked,
				Command: "aws sso login --sso-session corp",
			}},
		},
	}, nil
}

type fakePrefs struct{}

// Preferences answers with a rail whose second and third hotbars are empty and
// whose fourth cluster is hidden, because "a bar with nothing on it" and "a bar
// that is not there" are the shape the schema's required `bars` exists to keep
// apart — an empty bar is still a bar, and a client draws its digit.
func (fakePrefs) Preferences() Preferences {
	return Preferences{
		Rail: RailPreferences{
			BarCount:   3,
			ActiveBar:  1,
			Bars:       [][]string{{"prod", "staging"}, {}, {}},
			Hidden:     []string{"sandbox"},
			Nameplates: map[string]RailNameplate{"prod": {Name: "Production", Colour: "amber"}},
		},
		Columns: ColumnPreferences{
			Density:             "compact",
			WidthsAreDeliberate: true,
			Kinds: map[string]KindColumns{"pods": {
				Cells: []ColumnCell{{Column: "Name", Width: intPtr(240), Order: 0, Visible: boolPtr(true)}},
			}},
		},
	}
}

func (fakePrefs) SetRailBar(int) error                              { return nil }
func (fakePrefs) SetRailAssign(string, int) error                   { return nil }
func (fakePrefs) SetRailMove(string, int) error                     { return nil }
func (fakePrefs) SetRailStep(string, int) error                     { return nil }
func (fakePrefs) SetRailPlace(string, string, bool) error           { return nil }
func (fakePrefs) SetRailHidden(string, bool) error                  { return nil }
func (fakePrefs) SetRailNameplate(RailNameplateWrite) error         { return nil }
func (fakePrefs) SetColumnWidth(string, string, *int) error         { return nil }
func (fakePrefs) SetColumnOrder(string, string, int) error          { return nil }
func (fakePrefs) SetColumnPlace(string, string, string, bool) error { return nil }
func (fakePrefs) SetColumnVisible(string, string, bool) error       { return nil }
func (fakePrefs) SetColumnDensity(string) error                     { return nil }
func (fakePrefs) SetSeenVersion(string) error                       { return nil }
func (fakePrefs) ResetColumns(string, string) error                 { return nil }

// fakeKindPrefs keeps one person's choices, a favorite on one cluster.
type fakeKindPrefs struct{}

func (fakeKindPrefs) KindPreferences(context.Context) (KindPreferences, error) {
	return KindPreferences{ShowUnused: false, Favourites: map[string][]string{"prod": {"pods"}}}, nil
}

func (fakeKindPrefs) SetKindPreferences(_ context.Context, prefs KindPreferences) (KindPreferences, error) {
	return prefs, nil
}

type fakeReads struct{}

// Namespaces answers with the arm nothing else in this file exercises: `ok`
// **carrying a reason**. A reading kept from before the cluster stopped
// answering is the one success that qualifies itself, and a fixture that
// returned a plain `ok` would put `stale_reading` — and `readAt`, which is what
// makes it actionable — through no schema at all.
func (fakeReads) Namespaces(context.Context, string) (NamespaceReading, *Failure) {
	return NamespaceReading{
		Names:  []string{"default", "kube-system"},
		ReadAt: testMoment,
		Stale:  true,
		Why:    Unreachable("the API server did not answer in time"),
	}, nil
}

// ListResources carries, across its columns and its cells, **every member the
// contract declares** for ListPage, Column, Cell and BulkAffordance.
//
// That is a requirement rather than thoroughness:
// TestTheListPageCarriesEveryMemberTheContractDeclares reads the declared member
// set out of `api/openapi.yaml` at run time and asserts both directions over
// this response. A member dropped from here fails that test, which is the
// point: a member declared in the contract and missing from the Go struct would
// otherwise go unnoticed.
func (fakeReads) ListResources(_ context.Context, q ListQuery) (ListPage, *Failure) {
	unmeasured := Because(CodeNotInstalled, "no metrics source is installed")
	// The "+": permitted in the one namespace in view, and
	// on the guarded cluster refused with its reason - two answers, because a
	// namespace and a refusal's reason are never both on one.
	create := &CreateAffordance{Permitted: true, Namespace: "default", Kind: "Pod"}
	if q.Cluster == guardedCluster {
		refused := Because(CodeVerbDenied, guardedCluster+" is marked read-only in KT_READONLY_CLUSTERS; changes are refused there").Retry(false)
		create = &CreateAffordance{Permitted: false, Reason: &refused}
	}
	measured := ReadingOf(1163, true, "1163m")
	return ListPage{
		Kind: "pods", Label: "Pods", Namespaced: true,
		Columns: []Column{
			{Key: "name-0", Label: "Name", Visible: true, Order: 0, Width: nil,
				Sortable: boolPtr(true), Role: RoleName,
				LockedNote: "always shown — it is what says which object each row is"},
			{Key: "namespace-1", Label: "Namespace", Visible: true, Order: 1, Width: nil,
				Role: RoleNamespace, Copyable: true},
			// Filterable, and offering the values the other filters leave.
			{Key: "status-2", Label: "Status", Visible: true, Order: 2, Width: nil,
				Role: RoleOther, Prose: true, Filterable: true,
				FilterValues: &[]string{"CrashLoopBackOff", "Running"}},
			{Key: "controlled-by-3", Label: "Controlled by", Visible: false, Order: 3,
				Width: nil, Role: RoleOther, Secondary: true},
			{Key: "cpu-4", Label: "CPU", Visible: true, Order: 4, Width: intPtr(90),
				Align: "end", Role: RoleOther, Gauged: true},
		},
		Rows: []Row{{
			ID: "default/api-1", Name: "api-1", Namespace: "default",
			Cells: map[string]Cell{
				"name-0":      {Text: "api-1", Sort: "api-1"},
				"namespace-1": {Text: "default", Copy: "default"},
				// Rank is what a worst-first ordering reads; Hint is what hovering
				// says where a cell states a number to go and look up.
				"status-2": {Text: "CrashLoopBackOff", Tone: "bad", Rank: 1,
					Hint: "restarted 12 times in the last hour"},
				"controlled-by-3": {Text: "ReplicaSet/api-7d9",
					Ref: &Ref{Text: "api-7d9", Href: "/c/prod/replicasets/default/api-7d9"},
					Parts: []CellPart{{Text: "ReplicaSet/"},
						{Text: "api-7d9", Href: "/c/prod/replicasets/default/api-7d9"}}},
				// Measured, so it draws a bar.
				"cpu-4": {Text: "1163m", Reading: &measured,
					Gauge: &Gauge{Percent: 72, Tone: "warn"}},
			},
		}, {
			ID: "default/api-2", Name: "api-2", Namespace: "default",
			Cells: map[string]Cell{
				"name-0": {Text: "api-2", Sort: "api-2"},
				// A usage column with no metrics source is absent in every row,
				// and must not draw as 0 — no gauge at all, not a gauge at zero.
				"cpu-4": {Text: "—", State: StateAbsent, Reason: &unmeasured},
			},
		}},
		Bulk: &BulkAffordance{
			Scope: "prod|pods",
			Verbs: []BulkVerb{{Verb: "delete", Label: "Delete", Permitted: true}},
		},
		Create:       create,
		ReadOnlyNote: "prod is marked read-only in KT_READONLY_CLUSTERS; changes are refused there",
		// Why the gauges are empty, on a kind that has them. It names no cause:
		// that is the health page's, which is the one place saying which of
		// three different people fixes it - and the client links there on
		// `unmeasured`, never on `pending`, which is a read still on its way.
		MeasurementNote:  "No measurements for this kind right now, so the usage columns are empty.",
		MeasurementState: MeasurementUnmeasured,
		// The access review narrowed the list: the person may not list pods in
		// every namespace. Built from the question, never from the rows.
		AccessNote:        "You may not list pods in every namespace, so this shows only the namespaces where you may.",
		AllowedNamespaces: []string{"default"},
		Namespaces:        []string{"default"},
		UnknownNamespace:  "No namespace called staging on this cluster.",
		Search:            "api",
		// The filter control's choice, echoed by column key.
		Filters:           []ListFilter{{Column: "status-2", Value: "CrashLoopBackOff"}},
		Selector:          "app=api",
		Count:             2,
		ColumnsCustomised: true,
		RefreshSeconds:    2,
	}, nil
}

// GetResource answers with a page carrying every member the contract declares.
//
// That is the point of it rather than realism: `TestTheDetailPageCarriesEvery
// MemberTheContractDeclares` measures the member set on the wire, and a fake
// that filled the interesting half would agree with whatever the mapper happens
// to do today. The union is spread across several fields and several relations
// on purpose - `prose` belongs to a condition message, `copy` to a name, `pill`
// to a status, `selector` to a relation that is not one object - because a
// single field or relation carrying all of them at once is one that cannot
// exist.
// The span of the fake's one event: a series that has been firing since before
// the user arrived, which is the case `first` and `last` exist for.
var (
	eventFirst = time.Date(2026, 9, 12, 9, 12, 0, 0, time.UTC)
	eventLast  = time.Date(2026, 9, 12, 11, 48, 0, 0, time.UTC)
)

func (fakeReads) GetResource(context.Context, ObjectRef) (DetailPage, *Failure) {
	unreadable := Because(CodeUndecodable, "the ConfigMap could not be decoded")
	return DetailPage{
		Kind: "pods", Namespace: "default", Name: "api-1", Title: "Pod",
		Tabs: []Tab{{Key: "manifest", Label: "Manifest",
			Endpoint: "/api/c/prod/manifest?kind=pods&namespace=default&name=api-1", Lazy: boolPtr(true)}},
		// The charts, as an address and not a tab: they are drawn at the top of
		// the details panel. A pod has them, which is what this fake is.
		Metrics: "/api/c/prod/metrics?kind=pods&namespace=default&name=api-1",
		// What an Event says, on the one kind where the object *is* a sentence.
		// The fake is a pod, which would not carry one in production - but the
		// conformance test measures the member set the contract declares, and a
		// member no fake fills is a member nothing proves can travel.
		Event: &EventSubject{
			Message: "Back-off pulling image \"registry.internal/api:v2\"",
			Reason:  "BackOff", Type: "Warning", Action: "Pulling",
			Source: "kubelet", Node: "node-3", Count: 47,
			First: &eventFirst, Last: &eventLast,
			Object: "Pod/api-1", ObjectField: "spec.containers{api}",
		},
		// What this pod exposes, with the judgment the client must not make:
		// a UDP port is not forwardable and carries the reason rather than a
		// button that fails after the click.
		//
		// Three rows, because a Service's port and a pod's fill different
		// members of one shape: `targetPort`, `nodePort` and `appProtocol`
		// belong to the first, `hostPort` and `container` to the second. A fake
		// carrying only one of the two would let the other half reach the wire
		// untested.
		Ports: []ObjectPort{{
			Name: "http", Port: 8080, Protocol: "TCP", Container: "api",
			HostPort: 18080, Forwardable: true,
		}, {
			Name: "https", Port: 443, Protocol: "TCP", TargetPort: "http",
			NodePort: 31443, AppProtocol: "https", Forwardable: true,
		}, {
			Name: "metrics", Port: 9125, Protocol: "UDP", Container: "api",
			Reason: &Reason{Code: CodeNotSupportedHere, Message: "port-forwarding is TCP, and this port is UDP"},
		}},
		PortsNote: "Only the ports the object declares are listed; a process may listen on others.",
		// What the cluster still remembers, riding with the object. A series
		// event with a span and a count, because an event that fired 340 times
		// and one that fired once are the same row without them.
		Events: []ObjectEvent{{
			Type: "Warning", Reason: "BackOff", From: "kubelet on node-3",
			Message: "Back-off restarting failed container api in pod api-1_default(8f2c)",
			Count:   340, First: &eventFirst, Last: &eventLast,
		}},
		// And the note beside them rather than instead of them: a list can be
		// short *and* capped, and a panel that could only say one of the two
		// would have to choose which half to lie about.
		EventsNote: "Kubernetes keeps about an hour of these; older ones are gone from the cluster.",
		Sections: []DetailSection{
			{Key: "summary", Label: "Summary", State: StateOK, Fields: []DetailField{
				{Label: "Status", Value: "Running", Tone: "ok", Pill: true},
				{Label: "PodScheduled", Value: "0/6 nodes are available: 6 Insufficient cpu.", Prose: true},
				{Label: "Node", Value: "node-3", Ref: &Ref{Text: "node-3", Href: "/c/prod/nodes/node-3"},
					Hint: "the machine this pod is running on", Copy: "node-3"},
			}},
			// A section with its own state and no fields: the case the section
			// arm exists for, and the one a page-level failure cannot express.
			{Key: "data", Label: "Data", State: StateUnreadable, Reason: &unreadable},
		},
		Labels: map[string]string{"app": "api", "app.kubernetes.io/managed-by": "Helm"},
		// Already treated, which is the only shape this member is ever in on the
		// wire: `last-applied-configuration` is gone and the note says so. A
		// fixture carrying the raw map would be a payload the producer cannot
		// produce, and would make this test agree with the defect.
		Annotations: map[string]string{
			"deployment.kubernetes.io/revision": "7",
			"meta.helm.sh/release-name":         "shop",
		},
		// A Role's rules on a pod's page, which is the same license every other
		// kind-specific member here takes: this fixture is what the *contract
		// types* can carry, not what a pod is. Two rules, so that a graded one and
		// an ungraded one are both on the wire — and one of them
		// `nonResourceURLs`-only, because a rule with no resources at all is the
		// shape a naive table renders as an empty cell.
		Rules: &RuleSet{
			State: StateOK,
			Rules: []PolicyRule{
				{
					APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"},
					ResourceNames: []string{"shop-prod"},
					Wildcards:     "api groups, resources, verbs", WildcardTone: "bad",
				},
				{NonResourceURLs: []string{"/healthz"}, Verbs: []string{"get"}},
			},
			Note: "A * is drawn as it is stored.",
		},
		Containers: []PodContainer{
			// Init first and marked, which is the order they run in. Nothing has
			// been reported about it: the three probe entries exist and say the
			// container declares none, and the status says the kubelet has not
			// spoken rather than that the container is not ready. Those are the
			// two absences this surface exists to keep apart, so the fixture
			// carries them on the container where they are ordinary.
			{
				Name: "wait-for-db", Image: "busybox:1.36", Init: true,
				// A script after `sh -c`, one element with its newlines: the
				// command and the arguments as the spec gives them.
				Command: []string{"sh", "-c"},
				Args:    []string{"until nc -z db 5432; do\n  sleep 2\ndone"},
				Probes: []ContainerProbe{
					{Kind: "liveness", State: ProbeAbsent},
					{Kind: "readiness", State: ProbeAbsent},
					{Kind: "startup", State: ProbeAbsent},
				},
				Status: &ContainerStatus{
					State:   ContainerUnreported,
					Problem: "the kubelet has reported nothing about this container yet",
				},
			},
			// A container that ran and was killed, because `terminated`, a reason,
			// a message and an exit code are members no running container carries
			// and a fixture with only healthy containers would leave unmeasured.
			// Its startup probe is declared in a shape this build cannot name,
			// which is the third probe state and the one that must never render
			// like the first container's `absent`.
			{
				Name: "cache", Image: "redis:7.2", Init: false,
				Probes: []ContainerProbe{
					{
						Kind: "liveness", State: ProbeDeclared, Handler: "exec",
						Target: "redis-cli ping", PeriodSeconds: 5, FailureThreshold: 3,
					},
					{Kind: "readiness", State: ProbeAbsent},
					{
						Kind: "startup", State: ProbeUnreadable,
						Problem: "this startup probe uses a handler this console does not recognize",
					},
				},
				Status: &ContainerStatus{
					State: ContainerTerminated, Ready: false, Reason: "OOMKilled",
					Message:  "Container cache was terminated: memory limit exceeded",
					Restarts: 7, ExitCode: 137,
					// The previous run, which a restarted container keeps and the
					// panel draws as its last termination.
					LastTermination: &ContainerTermination{
						ExitCode: 137, Signal: 9, Reason: "OOMKilled",
						Message:    "memory limit exceeded",
						StartedAt:  timeRef(time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC)),
						FinishedAt: timeRef(time.Date(2026, 9, 11, 9, 41, 0, 0, time.UTC)),
					},
				},
			},
			{
				Name: "api", Image: "shop/api:1.4.2", Init: false, PullPolicy: "Always",
				Requests: []ResourceAmount{{Name: "cpu", Value: "100m"}},
				Limits:   []ResourceAmount{{Name: "memory", Value: "512Mi"}},
				Env: []EnvVar{
					{Name: "LOG_LEVEL", Value: "info"},
					{Name: "NODE_NAME", From: "field spec.nodeName"},
					// A Secret: named, never resolved, and the panel can say what
					// asking will cost before it is asked.
					{Name: "DB_PASSWORD", Secret: true,
						Source: &EnvSource{Kind: "Secret", Name: "db-credentials", Key: "password"}},
					// A ConfigMap: resolved on the panel open and drawn, clipped.
					{Name: "NGINX_CONF", Held: true, Clipped: true, Resolved: "worker_processes auto;",
						Source: &EnvSource{Kind: "ConfigMap", Name: "nginx", Key: "nginx.conf"}},
					// An absence with a control behind it, and one without.
					{Name: "MISSING", Problem: "the ConfigMap was not read within this panel's budget",
						Absence: "unread",
						Source:  &EnvSource{Kind: "ConfigMap", Name: "extras", Key: "missing"}},
					{Name: "OPTIONAL", Problem: "the ConfigMap does not carry that key, and the reference is optional",
						Absence: "expected",
						Source:  &EnvSource{Kind: "ConfigMap", Name: "params", Key: "tuning", Optional: true}},
				},
				EnvFrom: []EnvImport{
					{Kind: "ConfigMap", Name: "app-settings", Prefix: "APP_", Keys: 4},
					{Kind: "Secret", Name: "unreadable", Optional: true,
						Problem: "this account may not read that Secret", Absence: "unread"},
					// An optional import of an object that does not exist: the pod
					// working as written.
					{Kind: "ConfigMap", Name: "absent", Optional: true,
						Problem: "there is no ConfigMap absent here, and the import is marked optional: true",
						Absence: "expected"},
				},
				Imported: []EnvVar{{Name: "APP_TIMEOUT", Held: true, Resolved: "30s",
					Source: &EnvSource{Kind: "ConfigMap", Name: "app-settings", Key: "TIMEOUT"}}},
				// Both probes declared, with the timings that decide behavior and
				// the two handler shapes that render differently - an httpGet has a
				// path and a port, a tcpSocket has only a port. The startup probe is
				// absent, which on a container with the other two is the ordinary
				// case and is still written out.
				Probes: []ContainerProbe{
					{
						Kind: "liveness", State: ProbeDeclared, Handler: "httpGet",
						Target: "/healthz:8080", InitialDelaySeconds: 15, PeriodSeconds: 10,
						TimeoutSeconds: 1, FailureThreshold: 3,
					},
					{
						Kind: "readiness", State: ProbeDeclared, Handler: "tcpSocket",
						Target: ":8080", PeriodSeconds: 5, TimeoutSeconds: 2, FailureThreshold: 3,
					},
					{Kind: "startup", State: ProbeAbsent},
				},
				Status: &ContainerStatus{State: ContainerRunning, Ready: true, Restarts: 2},
			},
			// A debug container added since, after the app containers: marked, with
			// the container whose processes it joined.
			{
				Name: "kt-debug-x7k2p", Image: "busybox:1.36", Ephemeral: true, Target: "api",
				Command: []string{"sh"},
				Probes: []ContainerProbe{
					{Kind: "liveness", State: ProbeAbsent},
					{Kind: "readiness", State: ProbeAbsent},
					{Kind: "startup", State: ProbeAbsent},
				},
				Status: &ContainerStatus{State: ContainerRunning, Ready: false},
			},
		},
		EnvNote: "2 of 5 envFrom imports were not read: this panel stops after 3.",
		Certificates: &CertificateSet{
			// One bundle that parsed and one that could not, because "a partial
			// chain is never drawn as a whole one" is a property of the pair.
			Bundles: []CertificateBundle{
				{Key: "tls.crt", Certificates: []Certificate{
					{
						Position: 1, Role: "leaf", Subject: "CN=shop.example", Issuer: "CN=R3",
						SANs: []string{"shop.example", "www.shop.example"}, SANsNote: "3 more",
						NotBefore: &testMoment, NotAfter: &testMoment,
						Expiry: "expires in 61d4h", ExpiryTone: "ok",
						Serial: "03:9a:1f", SignatureAlgorithm: "SHA256-RSA", PublicKey: "RSA 2048",
					},
					// A self-signed root beside it: `selfSigned` and `isCA` are
					// omitempty booleans, so a fixture carrying only a leaf would
					// leave both unmeasured by the member set above.
					{
						Position: 2, Role: "root", Subject: "CN=ISRG Root X1", Issuer: "CN=ISRG Root X1",
						SelfSigned: true, IsCA: true,
						NotBefore: &testMoment, NotAfter: &testMoment,
						Expiry: "expires in 1462d", ExpiryTone: "ok",
						Serial: "82:10:cf", SignatureAlgorithm: "SHA256-RSA", PublicKey: "RSA 4096",
					},
				}},
				{Key: "ca.crt", Note: "the second PEM block is a certificate request, not a certificate"},
			},
			// Named, never read.
			PrivateKeys: []string{"tls.key"},
			Note:        "",
		},
		URLs: []ObjectURL{{
			Role: "Ingress", Address: "https://shop.example/api", Host: "shop.example",
			Path: "/api(/|$)(.*)", TLS: true, Service: "api",
			Note: "the path is a pattern; only its literal beginning is opened",
		}},
		URLsNote: "1 rule points at a resource backend and has no address.",
		Relations: []Relation{
			{Role: "Controlled by", Kind: "replicasets", Namespace: "default", Name: "api-7c9",
				Label: "api-7c9", Detail: "Running on node-3", Linkable: true},
			{Role: "Secret", Kind: "secrets", Namespace: "default", Name: "db-credentials",
				Label: "db-credentials", Mounts: []string{"/etc/creds", "+2 more"}, Linkable: true},
			// Nothing behind it: drawn inert, same icon, same role, no href.
			{Role: "Volume", Label: "cache", Detail: "emptyDir", Linkable: false},
			// A selector rather than one object.
			{Role: "Selects", Kind: "pods", Selector: "app=api", Label: "app=api", Linkable: false},
			// The one role that points backwards. An ordinary row under
			// one more role, which is why the referrers cost no new member at
			// all; what is new is the *answer* beside them, below.
			{Role: "Used by", Kind: "deployments", Namespace: "default", Name: "api",
				Label: "Deployment/api", Detail: "volume conf; key log.level read by api",
				Mounts: []string{"/etc/api"}, Linkable: true},
		},
		RelationsNote: "12 of 40 volumes shown.",
		// `empty` is the fixture because it is the only state that carries both
		// members at once — and because it is the majority answer on the two
		// kinds this runs for, so it is the one worth pinning.
		UsedBy: &UsedBy{
			State: StateEmpty,
			Reason: ptrTo(Because(CodeEmptyScope,
				"Nothing that was looked at names this ConfigMap. That is not the same as unused.").
				WithDetail("Listed in namespace default: Deployments, StatefulSets, DaemonSets, CronJobs, Jobs that have not finished.")),
		},
		Actions: []DetailAction{{Verb: "delete", Label: "Delete", Permitted: true,
			Destructive: boolPtr(true)}},
		// The server profile's per-pod Shell, on the arm that carries both
		// members: the person may and the console's ServiceAccount may not.
		Shell: &ShellOffer{Permitted: false, Reason: ptrTo(Because(CodeVerbDenied,
			"you may, but this console may not: its ServiceAccount is not granted create on pods/exec in default"))},
		// Debug on a read-only cluster: refused with the marking's sentence.
		Debug: &ShellOffer{Permitted: false, Reason: ptrTo(Because(CodeVerbDenied,
			"prod is marked read-only in KT_READONLY_CLUSTERS; changes are refused there"))},
	}, nil
}

// Every member of `HelmRelease` carried at least once, for the reason
// `fakeReads.Overview` is generous:
// `TestTheHelmPayloadsCarryEveryMemberTheContractDeclares` asserts both
// directions against `api/openapi.yaml`.
//
// The first release is the case a bare payload cannot express: revision
// 41 is current and **failed**, and revision 40 is the one Helm marks deployed.
// A row reporting only "41, failed" invites the conclusion that nothing is
// running.
func (fakeReads) HelmReleases(context.Context, string, []string) (HelmReleaseReading, *Failure) {
	undecodable := Because(CodeUndecodable, "the release's stored payload could not be decoded")
	updated := testMoment
	width := 240
	return HelmReleaseReading{AccessNote: "You may not list Secrets in every namespace, so this shows " +
		"the releases of the namespaces where you may.",
		Columns: []Column{
			{Key: "name-0", Label: "Name", Visible: true, Order: 0, Sortable: boolPtr(true), Role: "name",
				LockedNote: "always shown — it is what says which object each row is"},
			{Key: "status-3", Label: "Status", Visible: true, Order: 1, Width: &width, Role: "other", Filterable: true},
			{Key: "description-8", Label: "Description", Visible: false, Order: 2, Role: "other"},
		},
		ColumnsCustomised: true,
		Releases: []HelmRelease{
			{
				Name: "ingress", Namespace: "default",
				Revision: 41, DeployedRevision: 40,
				Chart: "ingress-nginx", ChartVersion: "4.11.2", AppVersion: "1.11.2",
				Deprecated:  true,
				Description: "Upgrade \"ingress\" failed: timed out waiting for the condition",
				Note:        "Running: revision 40 is the one Helm marks deployed",
				Status:      "failed", UpdatedAt: &updated, Age: "4m", State: StateOK,
			},
			// Listed and marked unreadable, never dropped: a release that exists and
			// cannot be read is not a release that does not exist.
			{Name: "sh.helm.release.v1.broken.v1", Namespace: "default",
				State: StateUnreadable, Reason: &undecodable},
		}}, nil
}

// The release page, in its two shapes.
//
// `payloadNote` cannot appear beside `notes` and `values` on one answer: it is
// what says those two could not be read at all. Measuring a single response would
// mean excepting one of them from the member test, which is that test switched
// off on the member it was written for — so this fake answers about the release
// it was asked for, and `undecodable` is the second shape.
//
// **Neither shape proves the server sends any of it.** A member test over a
// fake that fills `notes` and `values` stays green even if
// `internal/web` fills neither. What proves it
// is `TestTheHelmReleasePageIsFilledFromTheProducersThatHoldIt`, over there.
func (fakeReads) HelmRelease(_ context.Context, _, _, release string) (HelmReleaseDetail, *Failure) {
	updated := testMoment
	kept := 2
	if release == undecodableRelease {
		return HelmReleaseDetail{
			Release: HelmRelease{
				Name: undecodableRelease, Namespace: "default", Revision: 7,
				Status: "deployed", UpdatedAt: &updated, Age: "9d", State: StateOK,
			},
			Revisions: []HelmRevision{{
				Revision: 7, Status: "deployed", Tone: "ok", UpdatedAt: &updated,
				Mark: "current-and-deployed",
				Note: "this revision's stored payload would not decode",
			}},
			HistoryMax: &kept,
			PayloadNote: "Revision 7 is the current one and its stored document would not " +
				"decode, so its notes and its values could not be read.",
		}, nil
	}
	return HelmReleaseDetail{
		Release: HelmRelease{
			Name: "ingress", Namespace: "default",
			Revision: 41, DeployedRevision: 40,
			Chart: "ingress-nginx", ChartVersion: "4.11.2", AppVersion: "1.11.2",
			Deprecated:  true,
			Description: "Upgrade \"ingress\" failed: timed out waiting for the condition",
			Note:        "Running: revision 40 is the one Helm marks deployed",
			Status:      "failed", UpdatedAt: &updated, Age: "4m", State: StateOK,
		},
		// Two facts that usually coincide and diverge exactly when it matters, so
		// the two marks are on two different rows here.
		Revisions: []HelmRevision{
			{
				Revision: 41, Status: "failed", Tone: "bad", UpdatedAt: &updated,
				ChartVersion: "4.11.2", AppVersion: "1.11.2",
				Description: "Upgrade \"ingress\" failed", Mark: "current",
				Note: "this revision's stored payload would not decode",
			},
			{
				Revision: 40, Status: "deployed", Tone: "ok", UpdatedAt: &updated,
				ChartVersion: "4.11.1", AppVersion: "1.11.1",
				Description: "Upgrade complete", Mark: "deployed",
			},
		},
		HistoryMax:     &kept,
		ValuesSupplied: true,
		NotesRendered:  true,
	}, nil
}

func (fakeReads) RevealHelmValues(_ context.Context, q HelmRevealQuery) (HelmValues, *Failure) {
	return HelmValues{Revision: q.Revision, Values: "controller:\n  replicaCount: 2\n"}, nil
}

func (fakeReads) RevealHelmNotes(_ context.Context, q HelmRevealQuery) (HelmNotes, *Failure) {
	return HelmNotes{Revision: q.Revision, Notes: "The ingress controller is now running."}, nil
}

// undecodableRelease is the release whose current revision cannot be read, so
// that "there are none" and "nobody could read them" are two responses here
// rather than one member doing both jobs.
const undecodableRelease = "undecodable"

// The dashboard, with **every member of every panel schema carried at least
// once**.
//
// That is not decoration: `TestTheDashboardCarriesEveryMemberTheContractDeclares`
// drives this and asserts both directions against `api/openapi.yaml`, so a panel
// entry here that leaves a declared member out fails the test rather than
// passing quietly. An absent optional member conforms to the response-conformance
// test, which is exactly why that test cannot see this class of defect and this
// fake has to be generous.
func (fakeReads) Overview(context.Context, string) (Overview, *Failure) {
	collected := testMoment
	// The trouble row's own record of leaving its state - three days before the
	// page was read, so `age` below is what the server would have said.
	since := testMoment.Add(-3 * 24 * time.Hour)
	healthy := 2
	allWell := false
	return Overview{
		Cluster:     "prod",
		Version:     "v1.29.4",
		CollectedAt: &collected,
		// False, because this fake's own trouble panel has a row in it. A fake
		// asserting `true` beside a crash-looping container would be the very
		// claim the badge exists to refuse.
		AllWell:          &allWell,
		NodesSummary:     &Ref{Text: "2 of 3 nodes healthy", Href: "/c/prod/nodes"},
		NodesTone:        "warn",
		WorkloadsSummary: &Ref{Text: "429 pods", Href: "/c/prod/pods"},
		Counts: Items([]CountEntry{{
			Kind: "deployments", Label: "Deployments", Count: 3, Healthy: &healthy,
			Note: "1 not ready", Tone: "warn",
			Ref: &Ref{Text: "Deployments", Href: "/c/prod/deployments"},
		}}),
		Capacity: Items([]CapacityCell{{
			Label:     "CPU",
			Requested: ReadingOf(12, true, "12 cores"),
			Capacity:  ReadingOf(32, true, "32 cores"),
			// No metrics source: null and untrustworthy, never 0.
			Used:        &Reading{Trustworthy: false, Text: "no metrics source"},
			RequestedOf: "12 / 32 cores",
			// No usedOf: nothing measured `used`, so there is no hover to write.
			Ref:  &Ref{Text: "CPU requested", Href: "/c/prod/nodes"},
			Tone: "ok",
		}, {
			// A measured `used`, so the shape of `usedOf` is exercised too
			// (each line's hover, in the capacity's unit).
			Label:       "Memory",
			Requested:   ReadingOf(48<<30, true, "48.0 GiB"),
			Capacity:    ReadingOf(128<<30, true, "128.0 GiB"),
			Used:        func() *Reading { r := ReadingOf(40<<30, true, "40.0 GiB"); return &r }(),
			RequestedOf: "48 / 128 GiB",
			UsedOf:      "40 / 128 GiB",
			Ref:         &Ref{Text: "Memory requested", Href: "/c/prod/nodes"},
			Tone:        "ok",
		}}),
		// One missing metrics source makes one panel absent, not the dashboard
		// unreadable — and never usage drawn as zero beside a correct capacity.
		Top: ListFailed[TopBoard](StateAbsent,
			Because(CodeNotInstalled, "no metrics source is installed").Retry(true)),
		Events: Items([]EventEntry{{
			Text: "Back-off restarting failed container", Href: "/c/prod/pods?search=api-0&open=api-0",
			Hint: "BackOff", At: &collected, Age: "4m",
			Scope:   &Ref{Text: "default", Href: "/c/prod/events?namespace=default"},
			Object:  &Ref{Text: "Pod/api-0", Href: "/c/prod/pods?search=api-0&open=api-0"},
			Reason:  "BackOff",
			Message: "Back-off restarting failed container",
			// 340 and not 1. A count is the difference between an incident and a
			// line of log.
			Count: 340,
			Tone:  "warn",
		}}),
		// More than the one row carried: the feed was cut, and the page says so.
		EventsTotal: intPointer(250),
		Alerts: Items([]AlertEntry{{
			Subject: Ref{Text: "node-2", Href: "/c/prod/nodes?search=node-2&open=node-2"},
			Summary: "under memory pressure", Detail: "kubelet has observed memory pressure",
			Tone: "bad",
		}}),
		Trouble: Items([]TroubleEntry{{
			Kind:   &Ref{Text: "Deployment", Href: "/c/prod/deployments"},
			Scope:  &Ref{Text: "default", Href: "/c/prod/deployments?namespace=default"},
			Object: Ref{Text: "api", Href: "/c/prod/deployments?search=api&open=api"},
			Detail: "1 of 3 replicas ready", Since: &since, Age: "3d", Tone: "bad",
		}}),
		Restarting: Items([]RestartEntry{{
			Scope:     &Ref{Text: "default", Href: "/c/prod/pods?namespace=default"},
			Object:    Ref{Text: "api-0", Href: "/c/prod/pods?search=api-0&open=api-0"},
			Container: "api", Restarts: 12, Reason: "OOMKilled",
			LastExitAt: &collected, Age: "4m", Tone: "bad",
		}}),
		RestartingTotal: intPointer(1),
		Phases: Items([]PhaseEntry{{
			Phase: "Running", Count: 420, Tone: "ok",
			Ref: &Ref{Text: "Running", Href: "/c/prod/pods"},
		}}),
		// `zoomable` is explicitly false rather than absent: the cluster's own
		// series cannot be re-asked at a wider window today, and a client told
		// nothing would have to guess.
		History: Data(MetricsPanel{Caption: "prometheus, last hour",
			Zoomable: boolPointer(false),
			Series: []MetricsSeries{{
				Key: "cpu-used", Label: "CPU used", Unit: "cores",
				Data: Series{StartedAt: testMoment, StepSeconds: 60,
					Values: []*float64{Sample(0.4), nil, Sample(0.6)}},
			}}}),
		NotShown: []SkippedPanel{{
			Panel:  "Warning events",
			Reason: "events is forbidden: User cannot list resource \"events\"",
			// Which drawn member this failure spoilt (never nil). The
			// contract makes it required, and a nil slice marshals to null,
			// which is what the conformance test catches here.
			Affects: []string{"events"},
		}},
		AccessNote: "You may not list everything this page counts in every namespace, so its pods, events and " +
			"workloads are those of the namespaces where you may.",
	}, nil
}

func (fakeReads) Metrics(context.Context, MetricsQuery) (MetricsPanel, *Failure) {
	return MetricsPanel{
		Caption: "the last 1h",
		// A measurement this cluster cannot answer for is drawn as a disabled
		// control carrying the sentence, never omitted: a selector that silently
		// drops the network icon leaves the reader concluding the feature is
		// missing from the console.
		Metrics: []MetricsChoice{
			{Key: "cpu", Label: "CPU", Available: true, Selected: true},
			{Key: "network", Label: "Network", Available: false,
				Reason: &Reason{Code: CodeNotInstalled,
					Message: "this cluster's Prometheus does not export container network counters"}},
		},
		Windows:  []MetricsWindow{{Key: "1h", Label: "1h", Selected: true}, {Key: "6h", Label: "6h"}},
		Zoomable: boolPointer(true),
		Series: []MetricsSeries{{
			Key: "cpu", Label: "CPU", Unit: "cores",
			Data: Series{
				StartedAt:   testMoment,
				StepSeconds: 60,
				// A gap is null and is never omitted: omitting one shifts every later
				// sample onto the wrong moment and the chart still draws.
				Values: []*float64{Sample(0.4), nil, Sample(0.6)},
			},
		}, {
			// A line with no series and a reading of now: the memory query was
			// refused and metrics-server, which keeps no history, answered
			// Usage and history are two questions.
			Key: "memory", Chart: "memory", Label: "Memory", Unit: "bytes",
			Data: Series{StartedAt: testMoment, StepSeconds: 60, Values: []*float64{},
				Trustworthy: boolPointer(false)},
			Reason: &Reason{Code: CodeNotInstalled,
				Message: "prod has metrics-server and no Prometheus, so it holds no history to query"},
			Current: &Reading{Value: Sample(48 << 20), Trustworthy: true, Text: "48 MiB"},
		}, {
			// A finished pod's network line, empty at 1h: its run is in the 24h
			// period, and it moved 3 MiB in.
			Key: "network_rx", Chart: "network", Label: "Network in", Unit: "bytes/s",
			Data: Series{StartedAt: testMoment, StepSeconds: 30, Values: []*float64{},
				Trustworthy: boolPointer(false)},
			Reason: &Reason{Code: CodeEmptyScope,
				Message: "this pod ran 8s, 21h56m ago — before this 1h window; the 24h period holds it"},
			Period:   "24h",
			RunTotal: &Reading{Value: Sample(3 << 20), Trustworthy: true, Text: "3 MiB"},
		}}}, nil
}

// boolPointer is the two-answer shape a bare bool cannot carry: `false` and
// "nobody said" are different, and only the first is a fact.
func boolPointer(v bool) *bool { return &v }

// intPointer is an optional count, set: the overview's totals.
func intPointer(v int) *int { return &v }

// Two answers, because the panel has two halves and each has its own state.
//
// `guardedCluster` is the one where both halves failed to produce a list: the
// object records no history at all, and the ReplicaSets behind it were refused.
// A `reason` rides only on a state that is not ok, so no single answer can carry
// every member the contract declares - which is why
// `changesmembers_test.go` reads two and unions them.
func (fakeReads) Changes(_ context.Context, ref ObjectRef) (ChangesPanel, *Failure) {
	if ref.Cluster == guardedCluster {
		return ChangesPanel{
			Entries:      []HistoryEntry{},
			EntriesState: StateAbsent,
			EntriesReason: &Reason{
				Code:    CodeNotSupportedHere,
				Message: "this object carries no managedFields, so it records no history",
			},
			RolloutsState: StateForbidden,
			RolloutsReason: &Reason{
				Code:    CodeVerbDenied,
				Message: "listing replicasets is refused for these credentials",
			},
		}, nil
	}
	return ChangesPanel{
		Entries:       []HistoryEntry{{At: testMoment, Summary: "kubectl update"}},
		EntriesState:  StateOK,
		Rollouts:      []Rollout{{Revision: "41", At: testMoment, Name: "api-7f9c4b8d6", Replicas: 3, Image: "registry.internal/api:v2.4.1", Current: true, Cause: "kubectl set image"}},
		RolloutsState: StateOK,
	}, nil
}

func (fakeReads) RevealEnv(context.Context, EnvQuery) (EnvValue, *Failure) {
	return EnvValue{Key: "CONNECTION_URL", Value: "postgres://…", Source: "Secret"}, nil
}

// guardedCluster is the cluster name the fakes answer as read-only.
//
// It exists because `editable` and `editableNote` **cannot appear on the same
// response** — the note is what says why editing is refused — so a member test
// measuring one answer would have to except one of them. Two answers and a
// union is the honest shape, and it is the same one listmembers_test.go uses
// across columns and cells.
const guardedCluster = "guarded"

func (fakeReads) DataPanel(_ context.Context, ref ObjectRef) (DataPanel, *Failure) {
	panel := DataPanel{
		Kind: "configmaps", Namespace: "default", Name: "app", ResourceVersion: "42",
		Entries: []DataEntry{
			{Key: "LEVEL", Value: strPtr("info"), Masked: false},
			// A masked entry carries no value, and a binary one says so rather
			// than rendering bytes into a text field. Two entries, because
			// neither state can be expressed by the other.
			{Key: "password", Masked: true, Binary: boolPtr(true)},
			// And the third state, which needs its own spelling: a value
			// that is present and empty. `Value` is a pointer so that this is
			// `"value": ""` on the wire and the masked entry above is no member
			// at all - one of them says *ask for it*, the other says *it is
			// empty*, and a ConfigMap saying the first is a console claiming to
			// withhold what it is not withholding.
			{Key: "EMPTY", Value: strPtr(""), Masked: false},
		},
		Editable: true,
	}
	if ref.Cluster == guardedCluster {
		panel.Editable = false
		panel.EditableNote = guardedCluster + " is marked read-only in KT_READONLY_CLUSTERS; changes are refused there"
	}
	return panel, nil
}

func (fakeReads) Manifest(_ context.Context, ref ObjectRef) (ManifestPanel, *Failure) {
	// A Secret, because the member set includes what the panel says it masked
	// and a ConfigMap masks nothing. The value is the mask, as `detail.Trimmed`
	// leaves it; a fake that carried a real one would be the leak the member
	// exists to prevent, in the one place a leak is never looked for.
	panel := ManifestPanel{
		Lines: []ManifestLine{{Number: 1, Tokens: []ManifestToken{
			{Text: "kind", Class: "key"}, {Text: ": ", Class: "plain"},
			{Text: "Secret", Class: "plain"},
		}}},
		Raw:             "data:\n  password: <masked>\nkind: Secret\n",
		MaskedKeys:      []string{"password"},
		ResourceVersion: "42", Editable: true,
	}
	if ref.Cluster == guardedCluster {
		panel.Editable = false
		panel.EditableNote = guardedCluster + " is marked read-only in KT_READONLY_CLUSTERS; changes are refused there"
	}
	return panel, nil
}

// CreateTemplate answers with the skeleton, so `generic` is carried: a
// written template's answer would leave the member nothing to be measured on.
func (fakeReads) CreateTemplate(_ context.Context, q CreateTemplateQuery) (CreateTemplate, *Failure) {
	return CreateTemplate{
		Document: "apiVersion: example.com/v1\nkind: Widget\nmetadata:\n  name: example-widget\n  namespace: " + q.Namespace + "\n",
		Kind:     "Widget",
		Generic:  true,
	}, nil
}

type fakeActs struct{}

func (fakeActs) PreviewAction(_ context.Context, _ string, req ActionRequest) (PlanFacts, error) {
	return PlanFacts{
		Verb: req.Verb, Targets: []PlanTarget{req.Target}, Count: 1,
		Destructive: true, Irreversible: boolPtr(true),
		// The change as fields, with both flags a flattened sentence loses. A
		// cordon is the case they exist for: the field is not written on the
		// node at all, and `false` is what is in effect.
		Recap: &PlanRecap{
			Field: "spec.unschedulable", Before: "false", After: "true", Absent: true,
		},
		Confirmation: &Confirmation{Kind: "count", Expected: "1"},
	}, nil
}

func (fakeActs) RunAction(_ context.Context, _ string, req ActionRequest, confirmed string) (ActOutcome, error) {
	// The typed value reaches the backend, and the fake says so: for `number`
	// that string **is** the act, and a fake that dropped it would let a
	// handler that dropped it pass too.
	message := "deleted"
	if req.Verb == "scale" {
		message = "scaled to " + confirmed
	}
	return ActOutcome{Succeeded: 1, Results: []ActResult{
		{Target: req.Target, OK: true, Message: message},
	}}, nil
}

func (fakeActs) PreviewBulk(_ context.Context, _ string, req BulkRequest) (PlanFacts, error) {
	return PlanFacts{
		Verb: req.Verb, Targets: req.Targets, Count: len(req.Targets), Destructive: true,
		Confirmation: &Confirmation{Kind: "count", Expected: "2"},
	}, nil
}

// RunBulk half works, because a bulk action that half worked is the normal case
// and an outcome reporting one verdict for twelve objects is the
// empty-versus-failed collapse wearing a different hat.
func (fakeActs) RunBulk(_ context.Context, _ string, req BulkRequest) (ActOutcome, error) {
	denied := Because(CodeVerbDenied, "a disruption budget refused this eviction")
	return ActOutcome{
		Succeeded: 1, Failed: 1,
		Results: []ActResult{
			{Target: req.Targets[0], OK: true, Message: "deleted"},
			{Target: req.Targets[1], OK: false, Reason: &denied},
		},
	}, nil
}

func (fakeActs) PreviewData(_ context.Context, _ string, req DataWrite) (PlanFacts, error) {
	return PlanFacts{
		Verb:    "write",
		Targets: []PlanTarget{{Kind: req.Kind, Namespace: req.Namespace, Name: req.Name}},
		Count:   1, Destructive: true, Diff: "-info\n+debug\n",
		Confirmation: &Confirmation{Kind: "acknowledge"},
	}, nil
}

func (fakeActs) ApplyData(context.Context, string, DataWrite) (ActOutcome, error) {
	return ActOutcome{Succeeded: 1, Results: []ActResult{
		{Target: PlanTarget{Kind: "configmaps", Namespace: "default", Name: "app"}, OK: true},
	}}, nil
}

func (fakeActs) PreviewManifest(context.Context, string, ManifestWrite) (PlanFacts, error) {
	return PlanFacts{
		Verb:    "apply",
		Targets: []PlanTarget{{Kind: "ConfigMap", Namespace: "default", Name: "app"}},
		Count:   1, Destructive: true, Diff: "-a\n+b\n",
		// Every member this struct has. `editmembers_test.go` measures the answer
		// against the contract read at run time, and a fake filling only the
		// required four would leave it with nothing to find - which is how
		// `fields`, `request` and `precondition` could be declared and unsendable.
		Irreversible: boolPtr(false),
		Warnings:     []string{"this object is managed by Helm"},
		Fields:       []string{"spec.replicas"},
		Request:      `{"spec":{"replicas":5}}`,
		Precondition: "310",
		Confirmation: &Confirmation{Kind: "acknowledge"},
	}, nil
}

func (fakeActs) ApplyManifest(context.Context, string, ManifestWrite) (ActOutcome, error) {
	return ActOutcome{Succeeded: 1, Results: []ActResult{
		{Target: PlanTarget{Kind: "ConfigMap", Namespace: "default", Name: "app"}, OK: true},
	}}, nil
}

func (fakeActs) PreviewCreate(context.Context, string, ManifestWrite) (PlanFacts, error) {
	return PlanFacts{
		Verb:    "create",
		Targets: []PlanTarget{{Kind: "Deployment", Namespace: "default", Name: "web"}},
		Count:   1, Destructive: false,
		Button:       "Create this Deployment",
		Diff:         "+apiVersion: apps/v1\n+kind: Deployment\n",
		Fields:       []string{"spec.replicas"},
		Request:      `{"apiVersion":"apps/v1","kind":"Deployment"}`,
		Confirmation: &Confirmation{Kind: "acknowledge"},
	}, nil
}

func (fakeActs) ApplyCreate(context.Context, string, ManifestWrite) (ActOutcome, error) {
	return ActOutcome{Succeeded: 1, Results: []ActResult{
		{Target: PlanTarget{Kind: "Deployment", Namespace: "default", Name: "web"}, OK: true, Message: "created"},
	}, Next: []Ref{{Text: "web", Href: "/c/prod/deployments/default/web"}}}, nil
}

func (fakeActs) PreviewNodeShell(_ context.Context, _ string, req NodeShellRequest) (PlanFacts, error) {
	return PlanFacts{
		Verb: "nodeshell", Targets: []PlanTarget{{Kind: "nodes", Name: req.Node}},
		Count: 1, Destructive: true,
		Warnings:     []string{"this opens a root shell on the node"},
		Confirmation: &Confirmation{Kind: "acknowledge"},
	}, nil
}

type fakeDock struct{}

func (fakeDock) OpenDesktopTerminal(DesktopTerminalRequest) (TerminalSession, error) {
	opened := testMoment
	return TerminalSession{ID: "term-1", Channel: "/api/ws/terminal", OpenedAt: &opened}, nil
}

func (fakeDock) LogContainers(context.Context, ObjectRef) ([]Container, *Failure) {
	return []Container{
		{Name: "init-db", Init: boolPtr(true), Ready: boolPtr(true), Restarts: intPtr(0)},
		{Name: "app", Ready: boolPtr(true), Restarts: intPtr(2)},
	}, nil
}

// StreamLogs answers with a condition established before the first line, so the
// test sees the framing that carries it: a `state` frame and an `end` frame,
// rather than a status. A stream that cannot say why it stopped renders a deleted
// pod, a denied read and a quiet container identically.
func (fakeDock) StreamLogs(_ context.Context, _ LogQuery, emit func(LogEvent) error) *Failure {
	at := testMoment
	_ = emit(LogEvent{Type: "line", At: &at, Container: "app", Text: "listening on :8080"})
	return Forbidden("this account may not read this pod's log")
}

// EarlierLogs answers a page that carries every member of LogPage, so the
// members test measures a page that has something in it. `dense` rather than
// `beginning` because `beginning` is the one a lazy implementation reaches for.
func (fakeDock) EarlierLogs(_ context.Context, _ LogQuery, _, _ int) (LogPage, *Failure) {
	return LogPage{
		Lines: []string{"starting up", "listening on :8080"},
		Stop:  LogStopDense,
		Note:  "read 64 MiB without reaching the line before this one",
	}, nil
}

func (fakeDock) Forwards() ([]Forward, error) {
	opened := testMoment
	return []Forward{{
		ID: "fwd-1", Cluster: "prod", Namespace: "default", Pod: "api-1",
		RemotePort: 8080, Address: "127.0.0.1:18080", OpenedAt: &opened,
	}}, nil
}

func (fakeDock) OpenForward(_ context.Context, req ForwardRequest) (Forward, error) {
	opened := testMoment
	return Forward{
		ID: "fwd-2", Cluster: req.Cluster, Namespace: req.Namespace, Pod: req.Pod,
		RemotePort: req.RemotePort, Address: "127.0.0.1:18081", OpenedAt: &opened,
	}, nil
}

func (fakeDock) CloseForward(string) error { return nil }

// ptrTo is the one-line address-of a composite literal cannot have. Beside
// boolPtr in types_logic.go, and here rather than there because nothing outside
// a test builds a Reason by value.
func ptrTo[T any](v T) *T { return &v }

func timeRef(at time.Time) *time.Time { return &at }

// fakeUpdates answers Check now on the arm that carries every member.
type fakeUpdates struct{}

func (fakeUpdates) CheckUpdates(context.Context) SettingsUpdate {
	checked := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	return SettingsUpdate{
		State:      "newer",
		Latest:     "0.8.0",
		ReleaseURL: "https://github.com/thonicdev/kubetower/releases/tag/v0.8.0",
		CheckedAt:  &checked,
		Note:       "KubeTower 0.8.0 has been released. This build is 0.7.2.",
	}
}

// fakePrereqs fills every member of the three answers, so the contract test
// has something to find in each.
type fakePrereqs struct{}

const fakePrereqYAML = "apiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata:\n  name: trivy-operator\n  namespace: argocd\n"

func (fakePrereqs) Prereq(_ context.Context, _, key string) (Prereq, *Failure) {
	return Prereq{
		Key: key, Label: "Trivy Operator",
		Chart: PrereqChart{Repo: "https://aquasecurity.github.io/helm-charts", Name: "trivy-operator",
			Version: "0.38.1", AppVersion: "0.36.1", Namespace: "trivy-system"},
		Argo:        PrereqArgo{State: ArgoAmbiguous, Namespaces: []string{"argocd", "gitops"}, Note: "two controllers"},
		Application: &PrereqApplication{State: ApplicationAbsent, Name: "trivy-operator", Namespace: "argocd", Note: "-"},
		Installable: false,
		Reason:      &Reason{Code: CodeNotSupportedHere, Message: "Argo CD runs in more than one namespace"},
		Helm: &PrereqHelm{Release: "trivy-operator", Repo: "https://aquasecurity.github.io/helm-charts",
			Chart: "trivy-operator", Version: "0.38.1", Namespace: "trivy-system"},
	}, nil
}

func (fakePrereqs) PreviewPrereq(context.Context, string, string) (PlanFacts, error) {
	return PlanFacts{
		Verb:     "install",
		Targets:  []PlanTarget{{Kind: "applications.argoproj.io", Namespace: "argocd", Name: "trivy-operator"}},
		Count:    1,
		Headline: "Creates the Argo CD Application trivy-operator in argocd.",
		Button:   "Install trivy-operator 0.38.1",
		Request:  fakePrereqYAML,
		Confirmation: &Confirmation{Kind: "name", Expected: "trivy-operator",
			Label: "Type trivy-operator to install it"},
	}, nil
}

func (fakePrereqs) InstallPrereq(_ context.Context, _, _, namespace string) (ActOutcome, error) {
	return ActOutcome{Succeeded: 1, Summary: "Created Argo CD Application trivy-operator in " + namespace + ".",
		Results: []ActResult{{Target: PlanTarget{Kind: "applications.argoproj.io", Namespace: namespace,
			Name: "trivy-operator"}, OK: true, Message: "created"}},
		Next: []Ref{{Text: "trivy-operator", Href: "/c/prod/applications.argoproj.io/argocd/trivy-operator"}}}, nil
}

// fakeInstaller is the desktop's updater, idle and ready to download.
type fakeInstaller struct{}

func (fakeInstaller) UpdateInstall() UpdateInstall {
	return UpdateInstall{Phase: "idle", Enabled: true, When: "quit", Supported: true, Installation: "linux-appimage"}
}

func (f fakeInstaller) SetInstallUpdates(on bool, when string) (UpdateInstall, error) {
	out := f.UpdateInstall()
	out.Enabled = on
	if when != "" {
		out.When = when
	}
	return out, nil
}

func (f fakeInstaller) DownloadUpdate() (UpdateInstall, error) {
	out := f.UpdateInstall()
	out.Phase, out.Version, out.Asset, out.Total = "downloading", "0.1.3", "kubetower-0.1.3-x86_64.AppImage", 1000
	return out, nil
}

func (f fakeInstaller) InstallUpdate(context.Context) (UpdateInstall, error) {
	out := f.UpdateInstall()
	out.Phase = "installing"
	return out, nil
}

func (f fakeInstaller) OpenUpdateFolder() (UpdateInstall, error) {
	out := f.UpdateInstall()
	out.Phase, out.Version, out.Folder = "manual", "0.1.3", "/home/alice/.local/state/kubetower/update/install-1"
	return out, nil
}

// fakeToolWindows is the desktop's tool windows, in the native window.
type fakeToolWindows struct{}

func (fakeToolWindows) Available() bool { return true }

func (fakeToolWindows) OpenToolWindow(string, string, string, bool) error { return nil }

// fakeMetricsSources carries every member of MetricsSource, for the member
// test that reads them out of the contract.
type fakeMetricsSources struct{}

func (fakeMetricsSources) MetricsSource(_ context.Context, _ string) MetricsSource {
	return MetricsSource{
		Source: "vm/vmselect-main:8481/select/0/prometheus", Editable: true,
		Kind: "prometheus", Engine: "victoriametrics", Address: "vm/vmselect-main:8481/select/0/prometheus",
		Transport: "proxy", Detail: "service vm/vmselect-main:8481/select/0/prometheus found via the source set in Settings, reached by proxy, answered by VictoriaMetrics",
	}
}

func (f fakeMetricsSources) SaveMetricsSource(ctx context.Context, cluster, source string) (MetricsSource, error) {
	if source == "not a target" {
		return MetricsSource{}, fmt.Errorf("%w: %q is not namespace/service:port[/path]", ErrInvalid, source)
	}
	out := f.MetricsSource(ctx, cluster)
	saved := true
	out.Persisted, out.Note = &saved, "saved"
	return out, nil
}
