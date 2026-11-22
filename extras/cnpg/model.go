package cnpg

import (
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The model: each object the operator writes, reduced as soon as it is read
// to what the page draws, and the judgments made on it. Nothing here reads
// the cluster; the handlers hand it what Host.List returned.

// Health is one Cluster's state, as the page's tiles count it.
type Health string

const (
	// HealthHealthy: the operator says so and every instance is ready.
	HealthHealthy Health = "healthy"
	// HealthSwitchover: a switchover or a failover is under way.
	HealthSwitchover Health = "switchover"
	// HealthDegraded: fewer instances ready than expected, or one failed.
	HealthDegraded Health = "degraded"
	// HealthFailing: the operator cannot proceed, or no instance is ready.
	HealthFailing Health = "failing"
	// HealthProgressing: anything else the operator is doing - creating a
	// replica, upgrading, applying a configuration.
	HealthProgressing Health = "progressing"
)

// The operator's phases this model branches on (api/v1/cluster_types.go).
const (
	phaseHealthy    = "Cluster in healthy state"
	phaseSwitchover = "Switchover in progress"
	phaseFailOver   = "Failing over"
	phaseFirst      = "Setting up primary"
)

// failingPhases are the phases where the operator says it cannot go on by
// itself. Matched on their opening words, which have been stable across
// releases, rather than on the whole sentence.
var failingPhases = []string{
	"Cluster cannot proceed",
	"Cluster has incomplete or invalid image catalog",
	"Cluster is unrecoverable",
	"Cluster cannot execute instance online upgrade",
	"Unable to create required cluster objects",
	"Invalid cluster definition",
}

// Instance is one instance of a Cluster.
type Instance struct {
	Name string `json:"name"`
	// Role is primary or replica: the status's currentPrimary, never a label.
	Role string `json:"role"`
	// State is healthy, replicating, failed or unknown, as instancesStatus
	// groups the instances. Unknown is an instance the status does not place.
	State string `json:"state"`
	// Timeline is the timeline the instance last reported; 0 when not
	// reported.
	Timeline int64 `json:"timeline,omitempty"`
	// Target marks the instance a switchover or failover is promoting.
	Target bool `json:"target,omitempty"`
}

// Archive is the continuous WAL archiving of one Cluster.
type Archive struct {
	// State is none (nothing configured), ok, failing or pending (configured,
	// and the operator has said nothing yet).
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

// BackupMark is one Backup, as a timeline draws it.
type BackupMark struct {
	Name      string     `json:"name"`
	Phase     string     `json:"phase"`
	Method    string     `json:"method,omitempty"`
	CreatedAt *time.Time `json:"createdAt,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	StoppedAt *time.Time `json:"stoppedAt,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// BackupState is what the page says about a Cluster's backups.
type BackupState struct {
	// State is none (nothing backs it up), never (backed up, never
	// successfully), failing (the last one failed), late (the last success
	// is older than StaleAfter) or ok.
	State string `json:"state"`
	// Expected says something asks for backups: a backup configuration, a
	// ScheduledBackup or a Backup.
	Expected bool `json:"expected"`
	// Late is what the "older than 24 h" tile counts: expected, and no success
	// within StaleAfter.
	Late        bool         `json:"late"`
	LastSuccess *time.Time   `json:"lastSuccess,omitempty"`
	LastFailure *time.Time   `json:"lastFailure,omitempty"`
	Message     string       `json:"message,omitempty"`
	Scheduled   int          `json:"scheduled"`
	Recent      []BackupMark `json:"recent"`
	// Total is how many Backups were read for this cluster.
	Total int `json:"total"`
}

// Certificate is one certificate the operator reports an expiry for.
type Certificate struct {
	Secret    string    `json:"secret"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Certs is a Cluster's certificates, judged on the earliest.
type Certs struct {
	// State is ok, warn (within CertWarnWithin), bad (expired) or unknown
	// (none reported).
	State    string        `json:"state"`
	Earliest *time.Time    `json:"earliest,omitempty"`
	List     []Certificate `json:"list"`
}

// Summary is one Cluster, as its card and its tile counts read it.
type Summary struct {
	Namespace   string      `json:"namespace"`
	Name        string      `json:"name"`
	Phase       string      `json:"phase"`
	PhaseReason string      `json:"phaseReason,omitempty"`
	Health      Health      `json:"health"`
	Expected    int64       `json:"expected"`
	Ready       int64       `json:"ready"`
	Primary     string      `json:"primary,omitempty"`
	Target      string      `json:"target,omitempty"`
	Timeline    int64       `json:"timeline,omitempty"`
	PromotedAt  *time.Time  `json:"promotedAt,omitempty"`
	Instances   []Instance  `json:"instances"`
	Archive     Archive     `json:"archive"`
	Backup      BackupState `json:"backup"`
	Certs       Certs       `json:"certs"`
	Image       string      `json:"image,omitempty"`
	// NodesUsed is how many nodes hold the instances; 1 with several
	// instances is no high availability.
	NodesUsed int64 `json:"nodesUsed,omitempty"`
	Poolers   int   `json:"poolers"`
	// StorageBytes is spec.storage.size, the declared size of each
	// instance's data volume; 0 when it could not be read.
	StorageBytes    int64      `json:"storageBytes,omitempty"`
	WALStorageBytes int64      `json:"walStorageBytes,omitempty"`
	CreatedAt       *time.Time `json:"createdAt,omitempty"`
}

// summarize reduces one Cluster to its Summary. backups and scheduled are
// the cluster's own, already grouped; poolers is how many Poolers name it.
func summarise(c *unstructured.Unstructured, backups []BackupMark, scheduled int, poolers int, now time.Time) Summary {
	s := Summary{
		Namespace:   c.GetNamespace(),
		Name:        c.GetName(),
		Phase:       str(c.Object, "status", "phase"),
		PhaseReason: str(c.Object, "status", "phaseReason"),
		Expected:    num(c.Object, "spec", "instances"),
		Ready:       num(c.Object, "status", "readyInstances"),
		Primary:     str(c.Object, "status", "currentPrimary"),
		Target:      str(c.Object, "status", "targetPrimary"),
		Timeline:    num(c.Object, "status", "timelineID"),
		PromotedAt:  timeAt(c.Object, "status", "currentPrimaryTimestamp"),
		Image:       str(c.Object, "status", "image"),
		NodesUsed:   num(c.Object, "status", "topology", "nodesUsed"),
		Poolers:     poolers,
	}
	if s.Image == "" {
		s.Image = str(c.Object, "spec", "imageName")
	}
	if ts := c.GetCreationTimestamp(); !ts.IsZero() {
		t := ts.UTC()
		s.CreatedAt = &t
	}
	s.StorageBytes = quantity(str(c.Object, "spec", "storage", "size"))
	s.WALStorageBytes = quantity(str(c.Object, "spec", "walStorage", "size"))
	s.Instances = instances(c)
	s.Health = health(s)
	s.Archive = archive(c)
	s.Backup = backupState(c, backups, scheduled, now)
	s.Certs = certs(c, now)
	return s
}

// instances lists every instance the status names, primary first, then by
// name.
func instances(c *unstructured.Unstructured) []Instance {
	primary := str(c.Object, "status", "currentPrimary")
	target := str(c.Object, "status", "targetPrimary")
	state := map[string]string{}
	if groups, ok := nested(c.Object, "status", "instancesStatus").(map[string]any); ok {
		for st, names := range groups {
			for _, n := range list(names) {
				if name, ok := n.(string); ok {
					state[name] = st
				}
			}
		}
	}
	reported, _, _ := unstructured.NestedMap(c.Object, "status", "instancesReportedState")
	names := map[string]bool{}
	for _, n := range list(nested(c.Object, "status", "instanceNames")) {
		if name, ok := n.(string); ok {
			names[name] = true
		}
	}
	for name := range state {
		names[name] = true
	}
	for name := range reported {
		names[name] = true
	}
	if primary != "" {
		names[primary] = true
	}
	out := make([]Instance, 0, len(names))
	for name := range names {
		in := Instance{Name: name, Role: "replica", State: "unknown"}
		if st, ok := state[name]; ok {
			in.State = st
		}
		if rep, ok := reported[name].(map[string]any); ok {
			in.Timeline = num(rep, "timeLineID")
		}
		if name == primary {
			in.Role = "primary"
		}
		if target != "" && target != primary && name == target {
			in.Target = true
		}
		out = append(out, in)
	}
	sort.Slice(out, func(a, b int) bool {
		if (out[a].Role == "primary") != (out[b].Role == "primary") {
			return out[a].Role == "primary"
		}
		return out[a].Name < out[b].Name
	})
	return out
}

// health judges a summary, worst first.
func health(s Summary) Health {
	for _, p := range failingPhases {
		if strings.HasPrefix(s.Phase, p) {
			return HealthFailing
		}
	}
	if s.Phase == phaseSwitchover || s.Phase == phaseFailOver || (s.Primary != "" && s.Target != "" && s.Primary != s.Target) {
		return HealthSwitchover
	}
	if s.Expected > 0 && s.Ready == 0 && s.Phase != phaseFirst && s.Phase != "" {
		return HealthFailing
	}
	failed := false
	for _, in := range s.Instances {
		if in.State == "failed" {
			failed = true
		}
	}
	if failed || (s.Expected > 0 && s.Ready < s.Expected && s.Phase == phaseHealthy) {
		return HealthDegraded
	}
	if s.Phase == phaseHealthy {
		return HealthHealthy
	}
	// Any other phase is the operator at work - creating a replica, upgrading
	// - where fewer instances ready is what the work looks like.
	return HealthProgressing
}

// archiving says whether WAL archiving is configured: the in-tree Barman
// object store, or a plugin that declares itself the WAL archiver.
func archiving(c *unstructured.Unstructured) bool {
	if _, ok := nested(c.Object, "spec", "backup", "barmanObjectStore").(map[string]any); ok {
		return true
	}
	for _, p := range list(nested(c.Object, "spec", "plugins")) {
		if m, ok := p.(map[string]any); ok {
			if v, ok := m["isWALArchiver"].(bool); ok && v {
				if enabled, ok := m["enabled"].(bool); !ok || enabled {
					return true
				}
			}
		}
	}
	return false
}

// archive judges WAL archiving on the configuration first: the operator's
// ContinuousArchiving condition is True on a cluster that archives nothing.
func archive(c *unstructured.Unstructured) Archive {
	if !archiving(c) {
		return Archive{State: "none"}
	}
	cond, ok := condition(c, "ContinuousArchiving")
	if !ok {
		return Archive{State: "pending"}
	}
	if cond.status == "False" {
		return Archive{State: "failing", Message: cond.message}
	}
	return Archive{State: "ok"}
}

// backupConfigured is a backup method in the spec: the Barman object store,
// volume snapshots, or a plugin.
func backupConfigured(c *unstructured.Unstructured) bool {
	if archiving(c) {
		return true
	}
	if _, ok := nested(c.Object, "spec", "backup", "volumeSnapshot").(map[string]any); ok {
		return true
	}
	return false
}

// backupState judges a cluster's backups from its Backup objects, newest
// first: status.lastSuccessfulBackup is never set by a backup plugin, so it
// is only a fallback.
func backupState(c *unstructured.Unstructured, backups []BackupMark, scheduled int, now time.Time) BackupState {
	b := BackupState{Scheduled: scheduled, Total: len(backups)}
	b.Expected = backupConfigured(c) || scheduled > 0 || len(backups) > 0
	for i := range backups {
		m := backups[i]
		switch m.Phase {
		case "completed":
			at := firstTime(m.StoppedAt, m.StartedAt, m.CreatedAt)
			if at != nil && (b.LastSuccess == nil || at.After(*b.LastSuccess)) {
				b.LastSuccess = at
			}
		case "failed", "walArchivingFailing", "invalid backup definition":
			at := firstTime(m.StoppedAt, m.StartedAt, m.CreatedAt)
			if at != nil && (b.LastFailure == nil || at.After(*b.LastFailure)) {
				b.LastFailure = at
				b.Message = m.Error
			}
		}
	}
	if b.LastSuccess == nil {
		b.LastSuccess = timeAt(c.Object, "status", "lastSuccessfulBackup")
	}
	if n := len(backups); n > RecentBackups {
		b.Recent = append([]BackupMark{}, backups[:RecentBackups]...)
	} else {
		b.Recent = append([]BackupMark{}, backups...)
	}
	cond, condOK := condition(c, "LastBackupSucceeded")
	lastFailed := len(backups) > 0 && isFailed(backups[0].Phase)
	switch {
	case !b.Expected:
		b.State = "none"
	case lastFailed || (condOK && cond.status == "False"):
		b.State = "failing"
		if b.Message == "" && condOK {
			b.Message = cond.message
		}
	case b.LastSuccess == nil:
		b.State = "never"
	case now.Sub(*b.LastSuccess) > StaleAfter:
		b.State = "late"
	default:
		b.State = "ok"
	}
	b.Late = b.Expected && (b.LastSuccess == nil || now.Sub(*b.LastSuccess) > StaleAfter)
	return b
}

func isFailed(phase string) bool {
	return phase == "failed" || phase == "walArchivingFailing" || phase == "invalid backup definition"
}

// certLayouts are the ways an expiry is written: Go's time.String(), which
// is what the operator writes, then RFC 3339.
var certLayouts = []string{"2006-01-02 15:04:05 -0700 MST", "2006-01-02 15:04:05.999999999 -0700 MST", time.RFC3339Nano}

func parseCertTime(s string) (time.Time, bool) {
	for _, layout := range certLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// certs reads status.certificates.expirations, soonest first.
func certs(c *unstructured.Unstructured, now time.Time) Certs {
	out := Certs{State: "unknown", List: []Certificate{}}
	exp, _, _ := unstructured.NestedMap(c.Object, "status", "certificates", "expirations")
	for secret, v := range exp {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if t, ok := parseCertTime(s); ok {
			out.List = append(out.List, Certificate{Secret: secret, ExpiresAt: t})
		}
	}
	sort.Slice(out.List, func(a, b int) bool {
		if !out.List[a].ExpiresAt.Equal(out.List[b].ExpiresAt) {
			return out.List[a].ExpiresAt.Before(out.List[b].ExpiresAt)
		}
		return out.List[a].Secret < out.List[b].Secret
	})
	if len(out.List) == 0 {
		return out
	}
	first := out.List[0].ExpiresAt
	out.Earliest = &first
	switch left := first.Sub(now); {
	case left <= 0:
		out.State = "bad"
	case left < CertWarnWithin:
		out.State = "warn"
	default:
		out.State = "ok"
	}
	return out
}

// markOf reduces one Backup to its mark. A Backup with no status at all is
// one the operator never started - measured on a Backup asked of a cluster
// with nothing to back up to - and reads "never started", not "pending".
func markOf(b *unstructured.Unstructured) BackupMark {
	m := BackupMark{
		Name:      b.GetName(),
		Phase:     str(b.Object, "status", "phase"),
		Method:    str(b.Object, "status", "method"),
		StartedAt: timeAt(b.Object, "status", "startedAt"),
		StoppedAt: timeAt(b.Object, "status", "stoppedAt"),
		Error:     clip(str(b.Object, "status", "error"), 300),
	}
	if m.Method == "" {
		m.Method = str(b.Object, "spec", "method")
	}
	if m.Phase == "" {
		m.Phase = "not started"
	}
	if ts := b.GetCreationTimestamp(); !ts.IsZero() {
		t := ts.UTC()
		m.CreatedAt = &t
	}
	return m
}

// newestFirst orders marks by the latest time each carries.
func newestFirst(marks []BackupMark) {
	sort.SliceStable(marks, func(a, b int) bool {
		ta, tb := firstTime(marks[a].CreatedAt, marks[a].StartedAt), firstTime(marks[b].CreatedAt, marks[b].StartedAt)
		switch {
		case ta == nil:
			return false
		case tb == nil:
			return true
		}
		if !ta.Equal(*tb) {
			return ta.After(*tb)
		}
		return marks[a].Name > marks[b].Name
	})
}

type cond struct{ status, reason, message string }

func condition(c *unstructured.Unstructured, kind string) (cond, bool) {
	for _, raw := range list(nested(c.Object, "status", "conditions")) {
		m, ok := raw.(map[string]any)
		if !ok || m["type"] != kind {
			continue
		}
		s, _ := m["status"].(string)
		r, _ := m["reason"].(string)
		msg, _ := m["message"].(string)
		return cond{status: s, reason: r, message: clip(msg, 300)}, true
	}
	return cond{}, false
}

// Condition is one of a Cluster's conditions, for its page.
type Condition struct {
	Type    string     `json:"type"`
	Status  string     `json:"status"`
	Reason  string     `json:"reason,omitempty"`
	Message string     `json:"message,omitempty"`
	At      *time.Time `json:"at,omitempty"`
}

func conditions(c *unstructured.Unstructured) []Condition {
	out := []Condition{}
	for _, raw := range list(nested(c.Object, "status", "conditions")) {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		cd := Condition{Type: s(m["type"]), Status: s(m["status"]), Reason: s(m["reason"]), Message: clip(s(m["message"]), 300)}
		if t, err := time.Parse(time.RFC3339, s(m["lastTransitionTime"])); err == nil {
			cd.At = &t
		}
		out = append(out, cd)
	}
	return out
}

// --- reading unstructured maps -------------------------------------------

func nested(obj map[string]any, path ...string) any {
	var cur any = obj
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

func str(obj map[string]any, path ...string) string { return s(nested(obj, path...)) }

func s(v any) string {
	out, _ := v.(string)
	return out
}

func num(obj map[string]any, path ...string) int64 {
	switch v := nested(obj, path...).(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case float64:
		return int64(v)
	}
	return 0
}

func list(v any) []any {
	out, _ := v.([]any)
	return out
}

func timeAt(obj map[string]any, path ...string) *time.Time {
	raw := str(obj, path...)
	if raw == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
}

func firstTime(ts ...*time.Time) *time.Time {
	for _, t := range ts {
		if t != nil {
			return t
		}
	}
	return nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Cut on a rune boundary.
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}

// quantity is a Kubernetes quantity in bytes, or 0 when it is absent or
// unreadable.
func quantity(raw string) int64 {
	if raw == "" {
		return 0
	}
	q, err := resource.ParseQuantity(raw)
	if err != nil {
		return 0
	}
	return q.Value()
}
