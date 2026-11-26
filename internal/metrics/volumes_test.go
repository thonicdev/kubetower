package metrics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// volumeProm answers the two kubelet_volume_stats queries from a table of
// series, and records every query it was sent.
type volumeProm struct {
	mu      sync.Mutex
	queries []string
	used    map[ClaimKey]float64
	cap     map[ClaimKey]float64
	err     error
	// failCapacity fails only the capacity query.
	failCapacity bool
}

func (p *volumeProm) Query(_ context.Context, _ string, promQL string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queries = append(p.queries, promQL)
	if p.err != nil {
		return nil, p.err
	}
	table := p.used
	if strings.Contains(promQL, "capacity_bytes") {
		if p.failCapacity {
			return nil, errors.New("capacity query timed out")
		}
		table = p.cap
	}
	var rows []string
	for k, v := range table {
		rows = append(rows, fmt.Sprintf(`{"metric":{"namespace":%q,"persistentvolumeclaim":%q},"value":[1,"%v"]}`, k.Namespace, k.Name, v))
	}
	return []byte(`{"status":"success","data":{"resultType":"vector","result":[` + strings.Join(rows, ",") + `]}}`), nil
}

func (p *volumeProm) QueryRange(context.Context, string, string, time.Time, time.Time, time.Duration) ([]byte, error) {
	return nil, errors.New("not used")
}

var (
	dbClaim  = ClaimKey{Namespace: "shop", Name: "data-db-0"}
	logClaim = ClaimKey{Namespace: "shop", Name: "logs"}
	// otherNS shares a name with a claim asked in shop: the selector matches
	// it, and the reader must drop it.
	otherNS = ClaimKey{Namespace: "billing", Name: "logs"}
)

func TestVolumeFillKeepsOnlyTheClaimsAskedWithBothFigures(t *testing.T) {
	p := &volumeProm{
		used: map[ClaimKey]float64{dbClaim: 8e9, logClaim: 1e9, otherNS: 5e9},
		cap:  map[ClaimKey]float64{dbClaim: 1e10, otherNS: 1e10},
	}
	got, err := ReadVolumeFill(context.Background(), p, "prod", []ClaimKey{logClaim, dbClaim, {Namespace: "billing", Name: "data-db-0"}})
	if err != nil {
		t.Fatal(err)
	}
	// logs has no capacity series, billing/data-db-0 none at all, and
	// billing/logs was never asked: only shop/data-db-0 is read.
	if got.State != WindowRead || len(got.Claims) != 1 || got.Claims[0].ClaimKey != dbClaim ||
		got.Claims[0].Used != 8e9 || got.Claims[0].Capacity != 1e10 {
		t.Fatalf("%+v", got)
	}
	if got.Detail != "1 of 3 claims" {
		t.Errorf("detail %q", got.Detail)
	}
	if len(p.queries) != 2 {
		t.Fatalf("%d queries: %q", len(p.queries), p.queries)
	}
	for _, q := range p.queries {
		if !strings.HasPrefix(q, "max by (namespace, persistentvolumeclaim) (kubelet_volume_stats_") ||
			!strings.Contains(q, `namespace=~"billing|shop"`) || !strings.Contains(q, `persistentvolumeclaim=~"data-db-0|logs"`) {
			t.Errorf("query %q", q)
		}
	}
}

func TestVolumeFillSaysWhichAnswerItIs(t *testing.T) {
	ctx := context.Background()
	asked := []ClaimKey{dbClaim}

	if got, err := ReadVolumeFill(ctx, nil, "prod", asked); err != nil || got.State != WindowNoPrometheus {
		t.Errorf("no backend: %+v %v", got, err)
	}
	// The pool's own answer for a cluster with no Prometheus found.
	none := &volumeProm{err: NoHistory{Sentence: "no Prometheus was found in this cluster"}}
	if got, err := ReadVolumeFill(ctx, none, "prod", asked); err != nil || got.State != WindowNoPrometheus ||
		got.Detail != "no Prometheus was found in this cluster" {
		t.Errorf("none found: %+v %v", got, err)
	}
	down := &volumeProm{err: errors.New("connection refused")}
	if got, err := ReadVolumeFill(ctx, down, "prod", asked); err != nil || got.State != WindowUnreadable ||
		!strings.Contains(got.Detail, "connection refused") {
		t.Errorf("unreadable: %+v %v", got, err)
	}
	empty := &volumeProm{used: map[ClaimKey]float64{}, cap: map[ClaimKey]float64{}}
	if got, err := ReadVolumeFill(ctx, empty, "prod", asked); err != nil || got.State != WindowEmpty {
		t.Errorf("empty: %+v %v", got, err)
	}
	// One of the two queries failing leaves no claim with both figures: empty,
	// and never a zero capacity drawn as full.
	half := &volumeProm{used: map[ClaimKey]float64{dbClaim: 8e9}, failCapacity: true}
	if got, err := ReadVolumeFill(ctx, half, "prod", asked); err != nil || got.State != WindowEmpty || len(got.Claims) != 0 {
		t.Errorf("half: %+v %v", got, err)
	}
}

func TestVolumeFillRefusesWhatCannotBeAClaimAndAsksNothing(t *testing.T) {
	p := &volumeProm{}
	for _, bad := range []ClaimKey{
		{Namespace: "shop", Name: `logs"}or vector(1)`},
		{Namespace: "Shop", Name: "logs"},
		{Namespace: "", Name: "logs"},
	} {
		if _, err := ReadVolumeFill(context.Background(), p, "prod", []ClaimKey{bad}); !errors.Is(err, ErrBadClaim) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if len(p.queries) != 0 {
		t.Fatalf("asked %q", p.queries)
	}
	// A claim's name is a subdomain, up to 253 characters, with dots.
	long := ClaimKey{Namespace: "shop", Name: strings.Repeat("a", 200) + ".data"}
	if _, err := ReadVolumeFill(context.Background(), p, "prod", []ClaimKey{long}); err != nil {
		t.Errorf("a long claim name: %v", err)
	}
	if !strings.Contains(p.queries[0], `a\.data`) {
		t.Errorf("the dot is not escaped: %q", p.queries[0])
	}
}

func TestVolumeFillAsksInBatches(t *testing.T) {
	p := &volumeProm{used: map[ClaimKey]float64{}, cap: map[ClaimKey]float64{}}
	var asked []ClaimKey
	for i := range 2*volumeBatch + 1 {
		k := ClaimKey{Namespace: "shop", Name: fmt.Sprintf("data-%03d", i)}
		asked = append(asked, k)
		p.used[k], p.cap[k] = 1, 2
	}
	got, err := ReadVolumeFill(context.Background(), p, "prod", asked)
	if err != nil || got.State != WindowRead || len(got.Claims) != len(asked) {
		t.Fatalf("%d claims, %v", len(got.Claims), err)
	}
	// Three batches, two queries each.
	if len(p.queries) != 6 {
		t.Fatalf("%d queries", len(p.queries))
	}
	for _, q := range p.queries {
		if n := strings.Count(q, "|") + 1; n > volumeBatch {
			t.Errorf("a query names %d claims", n)
		}
	}
}
