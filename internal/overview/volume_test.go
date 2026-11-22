package overview

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// volumeProm answers a range query by the counter it names.
type volumeProm struct {
	mu      sync.Mutex
	queries []string
	byName  map[string]string
	err     error
}

func (p *volumeProm) Query(context.Context, string, string) ([]byte, error) { return nil, nil }

func (p *volumeProm) QueryRange(_ context.Context, _, q string, _, _ time.Time, _ time.Duration) ([]byte, error) {
	p.mu.Lock()
	p.queries = append(p.queries, q)
	p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	for name, body := range p.byName {
		if strings.Contains(q, name) {
			return []byte(body), nil
		}
	}
	return []byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`), nil
}

const volumeMatrix = `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[1700000000,"100"],[1700000030,"200"]]}]}}`

func TestVolumeQueryNamesTheClaim(t *testing.T) {
	q := volumeQuery(metricVolumeUsed, "shop", `data"x`)
	for _, want := range []string{metricVolumeUsed, `namespace="shop"`, `persistentvolumeclaim="data\"x"`, "max(", "avg without"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q lacks %q", q, want)
		}
	}
}

func TestReadVolumeHistory(t *testing.T) {
	window := Windows[1]
	t.Run("draws both lines", func(t *testing.T) {
		p := &volumeProm{byName: map[string]string{metricVolumeUsed: volumeMatrix, metricVolumeCapacity: volumeMatrix}}
		got := ReadVolumeHistory(context.Background(), p, "c", "shop", "data", window)
		if !got.Drawable() || len(got.Used.Points) != 2 || len(got.Capacity.Points) != 2 || got.Err != nil {
			t.Fatalf("got %+v", got)
		}
		if len(p.queries) != 2 {
			t.Errorf("want one query per counter, got %d", len(p.queries))
		}
	})
	t.Run("no prometheus asks nothing", func(t *testing.T) {
		got := ReadVolumeHistory(context.Background(), nil, "c", "shop", "data", window)
		if got.Drawable() || got.Err != nil {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("no series is not drawable and not an error", func(t *testing.T) {
		got := ReadVolumeHistory(context.Background(), &volumeProm{}, "c", "shop", "data", window)
		if got.Drawable() || got.Err != nil {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("a failed read is not drawable and carries the error", func(t *testing.T) {
		got := ReadVolumeHistory(context.Background(), &volumeProm{err: errors.New("boom")}, "c", "shop", "data", window)
		if got.Drawable() || got.Err == nil {
			t.Fatalf("got %+v", got)
		}
	})
}
