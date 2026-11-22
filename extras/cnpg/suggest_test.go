package cnpg

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/thonicdev/kubetower/extension"
)

func TestSuggest(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)

	got, err := New().Suggest(r, demoHost(), "prod")
	if err != nil || got != "3 CloudNativePG clusters in 2 namespaces" {
		t.Fatalf("%q %v", got, err)
	}

	absent := newHost()
	absent.reg.served = map[string]bool{}
	if got, err := New().Suggest(r, absent, "prod"); err != nil || got != "" {
		t.Fatalf("not installed: %q %v", got, err)
	}
	if got, err := New().Suggest(r, newHost(), "prod"); err != nil || got != "" {
		t.Fatalf("installed, no cluster: %q %v", got, err)
	}

	refused := demoHost()
	refused.listErr["clusters"] = fmt.Errorf("x: %w", extension.ErrDenied)
	if got, err := New().Suggest(r, refused, "prod"); !errors.Is(err, extension.ErrDenied) || got != "" {
		t.Fatalf("refused is an error, not nothing found: %q %v", got, err)
	}
}
