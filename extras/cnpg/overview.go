package cnpg

import (
	"net/http"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/textutil"
)

// Counts are what the overview's filter tiles show.
type Counts struct {
	Clusters   int `json:"clusters"`
	Healthy    int `json:"healthy"`
	Degraded   int `json:"degraded"`
	Switchover int `json:"switchover"`
	// ArchiveFailing counts the clusters whose WAL archiving is configured
	// and failing.
	ArchiveFailing int `json:"archiveFailing"`
	// BackupLate counts the clusters that are backed up and have no
	// successful backup within StaleAfter.
	BackupLate int `json:"backupLate"`
	// NotBackedUp counts the clusters nothing backs up.
	NotBackedUp int `json:"notBackedUp"`
	// CertsSoon counts the clusters with a certificate expired or within
	// CertWarnWithin.
	CertsSoon int `json:"certsSoon"`
}

// Overview is GET /.
type Overview struct {
	Now time.Time `json:"now"`
	// Complete is false when the Clusters were cut short or narrowed for the
	// person; Note says which, and the counts are then floors.
	Complete bool      `json:"complete"`
	Note     string    `json:"note,omitempty"`
	Counts   Counts    `json:"counts"`
	Clusters []Summary `json:"clusters"`
	// Sources are the secondary reads: Backups, ScheduledBackups, Poolers.
	Sources []Source `json:"sources"`
	// StaleAfterHours and CertWarnDays are the thresholds the tiles use, so
	// the page says them rather than repeating them.
	StaleAfterHours int `json:"staleAfterHours"`
	CertWarnDays    int `json:"certWarnDays"`
}

// grouped is what the secondary reads give each Cluster.
type grouped struct {
	backups   map[string][]BackupMark
	scheduled map[string]int
	poolers   map[string]int
}

func clusterKey(namespace, name string) string { return namespace + "/" + name }

// readOwned lists kind in namespace (or in every one), capped at limit, and
// hands each object naming a Cluster to each - only those naming only, when
// it is set. A kind not served here is an absence, said in the Source.
func readOwned(r *http.Request, h extension.Host, cluster, namespace string, k kinds, only, kind string, limit int64,
	each func(owner string, item *unstructured.Unstructured)) Source {
	crd, served, err := k.lookup(kind)
	if err != nil {
		return Source{Kind: kind, State: "failed", Note: textutil.FirstLineN(err.Error(), 200)}
	}
	if !served {
		return Source{Kind: kind, State: "absent"}
	}
	page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR(), Namespace: namespace, Limit: limit})
	source := sourceOf(kind, page, err)
	for i := range page.Items {
		item := &page.Items[i]
		owner := str(item.Object, "spec", "cluster", "name")
		if owner == "" || (only != "" && owner != only) {
			continue
		}
		each(owner, item)
	}
	return source
}

// readBackups reads the Backups, reduced to marks as they are read and
// grouped by Cluster, newest first.
func readBackups(r *http.Request, h extension.Host, cluster, namespace string, k kinds, only string) (map[string][]BackupMark, Source) {
	out := map[string][]BackupMark{}
	source := readOwned(r, h, cluster, namespace, k, only, BackupKind, MaxBackups, func(owner string, item *unstructured.Unstructured) {
		key := clusterKey(item.GetNamespace(), owner)
		out[key] = append(out[key], markOf(item))
	})
	for key := range out {
		newestFirst(out[key])
	}
	return out, source
}

// readSecondary is what the overview reads beside the Clusters: Backups,
// and how many active ScheduledBackups and Poolers name each Cluster.
func readSecondary(r *http.Request, h extension.Host, cluster string, k kinds) (grouped, []Source) {
	g := grouped{scheduled: map[string]int{}, poolers: map[string]int{}}
	var backups Source
	g.backups, backups = readBackups(r, h, cluster, "", k, "")
	scheduled := readOwned(r, h, cluster, "", k, "", ScheduledKind, MaxSmall, func(owner string, item *unstructured.Unstructured) {
		if suspended, _ := nested(item.Object, "spec", "suspend").(bool); !suspended {
			g.scheduled[clusterKey(item.GetNamespace(), owner)]++
		}
	})
	poolers := readOwned(r, h, cluster, "", k, "", PoolerKind, MaxSmall, func(owner string, item *unstructured.Unstructured) {
		g.poolers[clusterKey(item.GetNamespace(), owner)]++
	})
	return g, []Source{backups, scheduled, poolers}
}

// overview answers every Cluster the person may read, summarized.
func (x CNPG) overview(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		k, crd, ok := resolveClusters(w, r, h, cluster)
		if !ok {
			return
		}
		page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR(), Limit: MaxClusters})
		if err != nil {
			if writeRefused(w, err) {
				return
			}
			writeProblem(w, http.StatusBadGateway, CodeListFailed, "CloudNativePG's Clusters could not be listed.", err)
			return
		}
		now := x.clock().UTC()
		g, sources := readSecondary(r, h, cluster, k)
		out := Overview{
			Now: now, Complete: page.Complete, Note: page.Note, Sources: sources,
			Clusters:        make([]Summary, 0, len(page.Items)),
			StaleAfterHours: int(StaleAfter / time.Hour), CertWarnDays: int(CertWarnWithin / (24 * time.Hour)),
		}
		for i := range page.Items {
			c := &page.Items[i]
			key := clusterKey(c.GetNamespace(), c.GetName())
			out.Clusters = append(out.Clusters, summarise(c, g.backups[key], g.scheduled[key], g.poolers[key], now))
		}
		page.Items = nil
		sortSummaries(out.Clusters)
		out.Counts = count(out.Clusters)
		writeJSON(w, http.StatusOK, out)
	})
}

// severity orders the cards: what needs a look first.
var severity = map[Health]int{HealthFailing: 0, HealthSwitchover: 1, HealthDegraded: 2, HealthProgressing: 3, HealthHealthy: 4}

func sortSummaries(list []Summary) {
	sort.SliceStable(list, func(a, b int) bool {
		if severity[list[a].Health] != severity[list[b].Health] {
			return severity[list[a].Health] < severity[list[b].Health]
		}
		if list[a].Namespace != list[b].Namespace {
			return list[a].Namespace < list[b].Namespace
		}
		return list[a].Name < list[b].Name
	})
}

func count(list []Summary) Counts {
	c := Counts{Clusters: len(list)}
	for _, s := range list {
		switch s.Health {
		case HealthHealthy:
			c.Healthy++
		case HealthDegraded, HealthFailing:
			c.Degraded++
		case HealthSwitchover:
			c.Switchover++
		}
		if s.Archive.State == "failing" {
			c.ArchiveFailing++
		}
		if s.Backup.Late {
			c.BackupLate++
		}
		if !s.Backup.Expected {
			c.NotBackedUp++
		}
		if s.Certs.State == "warn" || s.Certs.State == "bad" {
			c.CertsSoon++
		}
	}
	return c
}
