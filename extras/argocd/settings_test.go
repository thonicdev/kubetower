package argocd_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	argocd "github.com/thonicdev/kubetower/extras/argocd"
)

func checksByKey() map[string]func(string) error {
	out := map[string]func(string) error{}
	for _, s := range (argocd.Delivery{}).Settings() {
		out[s.Key] = s.Check
	}
	return out
}

// The Argo CD column in every list is off until somebody asks for it, once the
// extra itself is turned on (every extra is off until it is).
func TestTheListColumnIsOffByDefault(t *testing.T) {
	for _, s := range (argocd.Delivery{}).Settings() {
		if s.Key == argocd.SettingListColumn {
			if s.Default || s.Text {
				t.Errorf("%q = %+v, want an unchecked checkbox", s.Key, s)
			}
			return
		}
	}
	t.Fatalf("no %q setting", argocd.SettingListColumn)
}

func TestAnAddressIsHttpOrHttpsWithAHostAndNoUser(t *testing.T) {
	for _, key := range []string{argocd.SettingArgoURL, argocd.SettingFluxURL} {
		check := checksByKey()[key]
		if check == nil {
			t.Fatalf("%q has no check", key)
		}
		for _, ok := range []string{"", "https://argocd.example.com", "http://10.0.0.1:8080/ui/", "https://x.test/path?q=1"} {
			if err := check(ok); err != nil {
				t.Errorf("%s %q refused: %v", key, ok, err)
			}
		}
		for _, bad := range []string{"javascript:alert(1)", "file:///etc/passwd", "ftp://x.test", "https://", "http:///path", "argocd.example.com", "https://user:pw@x.test", "https://user@x.test"} {
			if check(bad) == nil {
				t.Errorf("%s %q accepted", key, bad)
			}
		}
	}
}

func TestTheToolIsAChoiceOfFour(t *testing.T) {
	for _, s := range (argocd.Delivery{}).Settings() {
		if s.Key != argocd.SettingTool {
			continue
		}
		var got []string
		for _, c := range s.Choices {
			got = append(got, c.Value+"="+c.Label)
		}
		want := "auto=Auto-detect argocd=Argo CD flux=Flux both=Both"
		if !s.Text || strings.Join(got, " ") != want {
			t.Errorf("tool = %+v, want a text choice of %s", s, want)
		}
		return
	}
	t.Fatal("no tool setting")
}

func TestTheSettingsRouteAnswersWhatIsKept(t *testing.T) {
	host := &stubHost{settings: map[string]string{
		argocd.SettingTool:    "Flux",
		argocd.SettingArgoURL: "https://argo.test/",
		argocd.SettingFluxURL: "javascript:alert(1)",
	}}
	body, status := serveAt(t, host, "/settings")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	var got argocd.SettingsAnswer
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Tool != "flux" || got.ArgoURL != "https://argo.test" || got.FluxURL != "" {
		t.Errorf("got %+v", got)
	}
	if strings.Contains(string(body), "javascript") {
		t.Error("a refused address was sent")
	}
}

func TestNothingKeptIsAutoWithNoLinks(t *testing.T) {
	body, _ := serveAt(t, &stubHost{}, "/settings")
	if strings.TrimSpace(string(body)) != `{"tool":"auto"}` {
		t.Errorf("got %s", body)
	}
}
