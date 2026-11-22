// Package cnpg is the Postgres (CNPG) extra: the state of every
// CloudNativePG cluster at a glance, read from the operator's custom
// resources, behind its own per-cluster switch.
//
// **Read-only.** Every read of the cluster goes through Host.Definitions,
// Host.List and Host.Usage, reviewed for the person behind the request;
// nothing goes through Host.Client. The statistics and the tables are read
// through the SQL door (sqlconn.go): a tunnel and the cluster's application
// Secret, both reviewed for the person, and fixed, read-only statements.
// Replication lag in seconds is not in any of the operator's resources: on a
// click it is read by one Host.Exec of one fixed psql query in the primary
// (lag.go), and where it was not measured it is absent, never drawn as zero.
//
// Two things the operator says are not what they look like, and the model
// (model.go) is built around them:
//
//   - the ContinuousArchiving condition is True on a cluster that archives
//     nothing, so whether archiving is configured is read from the spec first;
//   - status.lastSuccessfulBackup is never set by a backup plugin, the way
//     CloudNativePG now backs up, so the last successful backup is computed
//     from the Backup objects.
//
// Nothing is kept between requests: no cache, no goroutine, no watch. Every
// list is capped (ListQuery.Limit) and a list cut short is said.
package cnpg

import (
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
)

// Key is `cnpg`: the page's URL segment, the KT_EXTRAS name and the key the
// per-cluster switch is stored under.
const Key = "cnpg"

// The operator's kinds, as the custom-resource catalog resolves them: the
// plural qualified by the group, so no other vendor's `clusters` is taken
// for CloudNativePG's.
const (
	group            = "postgresql.cnpg.io"
	ClusterKind      = "clusters." + group
	BackupKind       = "backups." + group
	ScheduledKind    = "scheduledbackups." + group
	PoolerKind       = "poolers." + group
	DatabaseKind     = "databases." + group
	PublicationKind  = "publications." + group
	SubscriptionKind = "subscriptions." + group
)

// The ceilings on what one request reads and answers. A list cut short by
// one is said on the page (Page.Complete false), never drawn as whole.
const (
	// MaxClusters is the most Clusters the overview reads.
	MaxClusters = 2000
	// MaxBackups is the most Backups one read holds, across the cluster.
	MaxBackups = 10000
	// MaxSmall is the most ScheduledBackups, Poolers, Databases,
	// Publications or Subscriptions one read holds.
	MaxSmall = 2000
	// MaxPods is the most instance pods the Pods list's column reads.
	MaxPods = 5000
	// MaxInstancePods is the most pods one cluster's page reads.
	MaxInstancePods = 100
	// RecentBackups is how many of a cluster's newest backups the overview
	// keeps for its card; ClusterBackups how many its own page keeps.
	RecentBackups  = 10
	ClusterBackups = 200
	// MaxEvents is how many of a cluster's newest Events its page keeps.
	MaxEvents = 20
)

// StaleAfter is how old the last successful backup may be before a cluster
// that is backed up counts as late. A fixed heuristic, and the page says so.
const StaleAfter = 24 * time.Hour

// CertWarnWithin is how close to its expiry a certificate is drawn as a
// warning: CloudNativePG renews its own seven days ahead, so one still this
// close was not renewed, or is one the operator does not renew.
const CertWarnWithin = 7 * 24 * time.Hour

var (
	podsGVR   = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	eventsGVR = schema.GroupVersionResource{Version: "v1", Resource: "events"}
)

// CNPG is the extension `cmd/kubetower` registers. It holds nothing between
// requests but its clock.
type CNPG struct {
	now func() time.Time
	// dial reaches Postgres through a tunnel: pgconn, or a test's fake.
	dial dial
}

// New is the constructor `cmd/kubetower` calls.
func New() CNPG { return CNPG{now: time.Now} }

func (CNPG) Key() string   { return Key }
func (CNPG) Label() string { return "Postgres (CNPG)" }

// Summary is the catalog entry.
func (CNPG) Summary() string {
	return "See every CloudNativePG cluster at a glance: primary and replicas, instances ready, failovers, " +
		"WAL archiving, the last successful backup and certificate expiry. Read-only."
}

// Needs names the operator and what the person must be able to read.
func (CNPG) Needs() string {
	return "the CloudNativePG operator; the right to list its resources (postgresql.cnpg.io), which Kubernetes' view role does not grant"
}

// Legacy is empty: the key has never been anything else.
func (CNPG) Legacy() []string { return nil }

// Routes are reads only, every one a GET: nothing here changes a cluster.
func (x CNPG) Routes(h extension.Host) []extension.Route {
	return []extension.Route{
		{Method: http.MethodGet, Pattern: "/{$}", Handler: x.overview(h)},
		{Method: http.MethodGet, Pattern: "/clusters/{namespace}/{name}", Handler: x.cluster(h)},
		{Method: http.MethodGet, Pattern: "/pods", Handler: x.podRoles(h)},
		{Method: http.MethodGet, Pattern: "/pods/{namespace}/{name}", Handler: x.pod(h)},
		// The SQL door (sqlconn.go): fixed, read-only statements through a
		// tunnel reviewed for the person, with the cluster's own password.
		{Method: http.MethodGet, Pattern: "/clusters/{namespace}/{name}/stats", Handler: x.stats(h)},
		{Method: http.MethodGet, Pattern: "/clusters/{namespace}/{name}/tables", Handler: x.tables(h)},
		// Replication lag: one fixed psql query in the primary, on a click (lag.go).
		{Method: http.MethodGet, Pattern: "/clusters/{namespace}/{name}/lag", Handler: x.lag(h)},
		{Method: http.MethodGet, Pattern: "/clusters/{namespace}/{name}/tables/{schema}/{table}", Handler: x.rows(h)},
	}
}

func (x CNPG) clock() time.Time {
	if x.now == nil {
		return time.Now()
	}
	return x.now()
}
