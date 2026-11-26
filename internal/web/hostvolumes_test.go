package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
)

// volumeSeries answers the kubelet_volume_stats queries with one series per
// claim it holds, whatever the selector, as a backend would for every claim
// it scrapes; the reader keeps the ones asked.
type volumeSeries struct {
	mu    sync.Mutex
	asked []string
	held  []extension.ClaimRef
}

func (v *volumeSeries) Query(_ context.Context, _, promQL string) ([]byte, error) {
	v.mu.Lock()
	v.asked = append(v.asked, promQL)
	v.mu.Unlock()
	value := "1000"
	if strings.Contains(promQL, "capacity_bytes") {
		value = "4000"
	}
	var rows []string
	for _, c := range v.held {
		rows = append(rows, fmt.Sprintf(`{"metric":{"namespace":%q,"persistentvolumeclaim":%q},"value":[1,%q]}`, c.Namespace, c.Name, value))
	}
	return []byte(`{"status":"success","data":{"resultType":"vector","result":[` + strings.Join(rows, ",") + `]}}`), nil
}

func (v *volumeSeries) QueryRange(context.Context, string, string, time.Time, time.Time, time.Duration) ([]byte, error) {
	return nil, errors.New("not used")
}

func (v *volumeSeries) calls() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.asked...)
}

var (
	shopData    = extension.ClaimRef{Namespace: "shop", Name: "data"}
	billingData = extension.ClaimRef{Namespace: "billing", Name: "ledger"}
	readers     = func() context.Context { return person("dave@example.com", "oidc:readers") }
	shopClaims  = func() context.Context { return person("erin@example.com", "oidc:shop-claims") }
)

// Each person gets the fill of the claims in the namespaces they may list
// claims in, and is told when that is not all of them (#91).
func TestVolumeFillShowsEachPersonTheClaimsTheyMayList(t *testing.T) {
	series := &volumeSeries{held: []extension.ClaimRef{shopData, billingData}}
	h := usageHost(t, nil, series)
	asked := []extension.ClaimRef{shopData, billingData}

	whole, err := h.VolumeFill(asPerson(readers()), "prod", asked)
	if err != nil {
		t.Fatal(err)
	}
	if whole.State != extension.WindowRead || len(whole.Claims) != 2 || !whole.Complete || whole.Note != "" {
		t.Fatalf("dave: %+v", whole)
	}
	if c := whole.Claims[1]; c.ClaimRef != shopData || c.UsedBytes != 1000 || c.CapacityBytes != 4000 {
		t.Errorf("dave's shop/data: %+v", c)
	}

	before := len(series.calls())
	narrowed, err := h.VolumeFill(asPerson(shopClaims()), "prod", asked)
	if err != nil {
		t.Fatal(err)
	}
	if narrowed.State != extension.WindowRead || len(narrowed.Claims) != 1 || narrowed.Claims[0].ClaimRef != shopData ||
		narrowed.Complete || narrowed.Note == "" {
		t.Fatalf("erin: %+v", narrowed)
	}
	// The claim of a namespace she may not list is never named to the backend.
	for _, q := range series.calls()[before:] {
		if strings.Contains(q, "billing") || strings.Contains(q, "ledger") {
			t.Errorf("a query for erin names what she may not list: %s", q)
		}
	}
}

func TestVolumeFillForAPersonWhoMayListNoClaimIsRefusedAndUnasked(t *testing.T) {
	series := &volumeSeries{held: []extension.ClaimRef{shopData}}
	_, err := usageHost(t, nil, series).VolumeFill(asPerson(shopViewer()), "prod", []extension.ClaimRef{shopData})
	var refused *access.Refused
	if !errors.As(err, &refused) || !errors.Is(err, extension.ErrDenied) {
		t.Fatalf("alice: %v; want a refusal", err)
	}
	if calls := series.calls(); len(calls) != 0 {
		t.Errorf("the backend was asked for a person who may list no claim: %v", calls)
	}
}

func TestVolumeFillWithNoPrometheusSaysSo(t *testing.T) {
	got, err := usageHost(t, nil, nil).VolumeFill(asPerson(readers()), "prod", []extension.ClaimRef{shopData})
	if err != nil {
		t.Fatal(err)
	}
	if got.State != extension.WindowNoPrometheus || got.Detail == "" || len(got.Claims) != 0 {
		t.Errorf("answered %+v", got)
	}
}

func TestVolumeFillRefusesWhatCannotBeAClaimAndWrapsAnUnknownCluster(t *testing.T) {
	h := usageHost(t, nil, &volumeSeries{})
	if _, err := h.VolumeFill(asPerson(readers()), "prod", []extension.ClaimRef{{Namespace: "shop", Name: `x"}`}}); err == nil {
		t.Error("a claim name with a quote was asked")
	}
	if _, err := h.VolumeFill(asPerson(readers()), "nowhere", []extension.ClaimRef{shopData}); !errors.Is(err, extension.ErrUnknownCluster) {
		t.Errorf("an unknown cluster: %v", err)
	}
}
