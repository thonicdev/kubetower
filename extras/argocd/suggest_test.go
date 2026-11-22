package argocd_test

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/thonicdev/kubetower/extension"
	argocd "github.com/thonicdev/kubetower/extras/argocd"
)

func suggest(t *testing.T, h *stubHost) (string, error) {
	t.Helper()
	return argocd.New().Suggest(httptest.NewRequest("GET", "/", nil), h, "kind")
}

func TestSuggestFoundArgo(t *testing.T) {
	h := &stubHost{crd: extension.CRD{Group: "argoproj.io", Plural: "applications"}}
	got, err := suggest(t, h)
	if err != nil || got != "Argo CD is installed" {
		t.Errorf("Suggest = %q, %v", got, err)
	}
}

func TestSuggestFoundFlux(t *testing.T) {
	h := &stubHost{crd: extension.CRD{Group: "kustomize.toolkit.fluxcd.io", Plural: "kustomizations"}}
	got, err := suggest(t, h)
	if err != nil || got != "Flux CD is installed" {
		t.Errorf("Suggest = %q, %v", got, err)
	}
}

func TestSuggestAbsentIsEmptyWithoutError(t *testing.T) {
	h := &stubHost{crd: extension.CRD{Group: "example.com", Plural: "widgets"}}
	if got, err := suggest(t, h); err != nil || got != "" {
		t.Errorf("Suggest = %q, %v; want nothing", got, err)
	}
	h = &stubHost{lookupErr: extension.ErrNotServed}
	if got, err := suggest(t, h); err != nil || got != "" {
		t.Errorf("not served: Suggest = %q, %v; want nothing", got, err)
	}
}

func TestSuggestUnreadableIsAnErrorNotAbsent(t *testing.T) {
	h := &stubHost{lookupErr: errors.New("customresourcedefinitions is forbidden")}
	if got, err := suggest(t, h); err == nil || got != "" {
		t.Errorf("Suggest = %q, %v; want an error", got, err)
	}
}

func TestSuggestUnreachableIsAnError(t *testing.T) {
	h := &stubHost{definitionsErr: errors.New("connection refused")}
	if got, err := suggest(t, h); err == nil || got != "" {
		t.Errorf("Suggest = %q, %v; want an error", got, err)
	}
}
