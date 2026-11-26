package metrics

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"
)

// Volume fill from the kubelets' own series (#91). The kubelet exports
// kubelet_volume_stats_used_bytes and kubelet_volume_stats_capacity_bytes per
// mounted claim, labelled namespace and persistentvolumeclaim; Prometheus and
// VictoriaMetrics hold them where they scrape the kubelets.

// ClaimKey names one claim, as the caller asked for it.
type ClaimKey struct {
	Namespace string
	Name      string
}

// ClaimBytes is one claim's last reported figures.
type ClaimBytes struct {
	ClaimKey
	Used     int64
	Capacity int64
}

// VolumeRead is what the backend answered about the claims asked.
type VolumeRead struct {
	State  WindowState
	Detail string
	Claims []ClaimBytes // sorted by namespace then name
	ReadAt time.Time
}

// claimNameRE is a DNS-1123 subdomain, what a claim's name is: up to 253
// characters, where a namespace (validName) is a label of 63.
var claimNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)

// ErrBadClaim is a claim whose namespace or name cannot be one: nothing is
// built from it.
var ErrBadClaim = errors.New("not a claim's name")

const (
	// volumeBatch bounds the claims one pair of queries names, so the query
	// string stays well inside what an API server's proxy accepts in a URL.
	volumeBatch = 100
	// volumeBudget bounds the whole read.
	volumeBudget = 15 * time.Second
)

// ReadVolumeFill asks the backend for the claims' used and capacity bytes,
// two fixed instant queries per batch, and keeps only the claims asked: a
// claim with no series is absent, never a zero. No backend is
// WindowNoPrometheus; every query failing is the failure's state; a backend
// that answered with nothing for any claim is WindowEmpty.
func ReadVolumeFill(ctx context.Context, q RangeQuerier, cluster string, claims []ClaimKey) (VolumeRead, error) {
	asked := map[ClaimKey]bool{}
	for _, c := range claims {
		if !validName(c.Namespace) || !claimNameRE.MatchString(c.Name) {
			return VolumeRead{}, fmt.Errorf("%w: %q/%q, so no query was built from it", ErrBadClaim, c.Namespace, c.Name)
		}
		asked[c] = true
	}
	if q == nil {
		return VolumeRead{State: WindowNoPrometheus, Detail: "this installation has no Prometheus configured"}, nil
	}
	if len(asked) == 0 {
		return VolumeRead{State: WindowEmpty, Detail: "no claim was asked", ReadAt: time.Now()}, nil
	}
	keys := make([]ClaimKey, 0, len(asked))
	for k := range asked {
		keys = append(keys, k)
	}
	sortClaims(keys)

	ctx, cancel := context.WithTimeout(ctx, volumeBudget)
	defer cancel()
	used := map[ClaimKey]float64{}
	capacity := map[ClaimKey]float64{}
	var firstErr error
	failed, ran := 0, 0
	for start := 0; start < len(keys); start += volumeBatch {
		batch := keys[start:min(start+volumeBatch, len(keys))]
		sel := claimSelector(batch)
		for _, x := range []struct {
			metric string
			into   map[ClaimKey]float64
		}{
			{"kubelet_volume_stats_used_bytes", used},
			{"kubelet_volume_stats_capacity_bytes", capacity},
		} {
			ran++
			raw, err := q.Query(ctx, cluster, fmt.Sprintf("max by (namespace, persistentvolumeclaim) (%s{%s})", x.metric, sel))
			var rows []row
			if err == nil {
				rows, err = parseRows(raw)
			}
			if err != nil {
				failed++
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			for _, r := range rows {
				k := ClaimKey{Namespace: r.labels["namespace"], Name: r.labels["persistentvolumeclaim"]}
				if r.ok && asked[k] {
					x.into[k] = r.value
				}
			}
		}
	}
	if failed == ran {
		f := failedWindow(firstErr)
		return VolumeRead{State: f.State, Detail: f.Detail, ReadAt: time.Now()}, nil
	}

	out := VolumeRead{State: WindowRead, ReadAt: time.Now()}
	for _, k := range keys {
		u, okU := used[k]
		c, okC := capacity[k]
		if !okU || !okC || c <= 0 || u < 0 {
			continue
		}
		out.Claims = append(out.Claims, ClaimBytes{ClaimKey: k, Used: int64(u), Capacity: int64(c)})
	}
	switch {
	case len(out.Claims) == 0:
		out.State = WindowEmpty
		out.Detail = "the metrics backend holds no kubelet_volume_stats series for these claims"
	case failed > 0:
		out.Detail = fmt.Sprintf("%d of %d claims; %d of %d queries could not be read: %s",
			len(out.Claims), len(keys), failed, ran, shortError(firstErr))
	default:
		out.Detail = fmt.Sprintf("%d of %d claims", len(out.Claims), len(keys))
	}
	return out, nil
}

// claimSelector matches the batch's namespaces and names. It can match a
// claim not asked (a name asked in one namespace that also exists in
// another); the reader keeps only the pairs asked. The names are escaped as
// alternation does: a dot for the regular expression, and that backslash for
// PromQL's double-quoted string - a single one is a parse error ("unknown
// escape sequence") that would fail the whole batch.
func claimSelector(batch []ClaimKey) string {
	namespaces, names := map[string]bool{}, map[string]bool{}
	for _, k := range batch {
		namespaces[k.Namespace] = true
		names[k.Name] = true
	}
	return fmt.Sprintf(`namespace=~"%s",persistentvolumeclaim=~"%s"`, alternation(namespaces), alternation(names))
}

func sortClaims(keys []ClaimKey) {
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Namespace != keys[j].Namespace {
			return keys[i].Namespace < keys[j].Namespace
		}
		return keys[i].Name < keys[j].Name
	})
}
