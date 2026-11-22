package kubecron

import (
	"errors"
	"net/http/httptest"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/thonicdev/kubetower/extension"
)

func suggestOn(h *fakeHost) (string, error) {
	return New().Suggest(httptest.NewRequest("GET", "/", nil), h, "kind")
}

func TestSuggestFoundCollector(t *testing.T) {
	h := &fakeHost{clients: fake.NewClientset(collectorService("none"))}
	got, err := suggestOn(h)
	if err != nil || got == "" {
		t.Errorf("Suggest = %q, %v; want a suggestion", got, err)
	}
	if len(h.revealed) != 0 || h.forwards != 0 {
		t.Errorf("the probe read a token (%v) or dialled (%d)", h.revealed, h.forwards)
	}
}

func TestSuggestAbsentCollector(t *testing.T) {
	h := &fakeHost{clients: fake.NewClientset()}
	if got, err := suggestOn(h); err != nil || got != "" {
		t.Errorf("Suggest = %q, %v; want nothing", got, err)
	}
}

func TestSuggestUnreadableIsAnErrorNotAbsent(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, "", errors.New("no"))
	h := &fakeHost{clients: fake.NewClientset(collectorService("none")), listErr: forbidden}
	if got, err := suggestOn(h); err == nil || got != "" {
		t.Errorf("Suggest = %q, %v; want an error", got, err)
	}
}

func TestSuggestUnreachableIsAnError(t *testing.T) {
	h := &fakeHost{clients: fake.NewClientset(), listErr: errors.New("connection refused")}
	if got, err := suggestOn(h); err == nil || got != "" {
		t.Errorf("Suggest = %q, %v; want an error", got, err)
	}
}

// On a console several people share, a labeled collector is not suggested:
// nothing is listed, and only the Service the environment names counts.
func TestSuggestOnASharedConsoleLooksOnlyWhereTheEnvironmentNames(t *testing.T) {
	r := extension.MarkShared(httptest.NewRequest("GET", "/", nil))
	h := &fakeHost{clients: fake.NewClientset(collectorService("none")), settings: map[string]string{SettingCollector: "kubecron/kubecron"}}
	if got, err := (KubeCron{}).Suggest(r, h, "prod"); got != "" || err != nil {
		t.Errorf("a labeled or set collector was suggested on a shared console: %q, %v", got, err)
	}
	if len(h.listed) != 0 || len(h.consoleReads) != 0 {
		t.Errorf("listed %v, console reads %v", h.listed, h.consoleReads)
	}
	named := KubeCron{collector: collectorConfig{Namespace: "kubecron", Service: "kubecron"}}
	if got, err := named.Suggest(r, h, "prod"); got == "" || err != nil {
		t.Errorf("the collector the environment names was not suggested: %q, %v", got, err)
	}
	if len(h.listed) != 0 {
		t.Errorf("a Service was listed: %v", h.listed)
	}
}
