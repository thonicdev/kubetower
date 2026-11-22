package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// A read the person's review allowed and the API server then refused the
// console's own account is the installation's missing grant, not the person's
// refusal: under the server profile only. On the desktop the account is the
// person's.
func TestAForbiddenOnTheConsolesOwnReadIsAMissingGrantUnderTheServerProfile(t *testing.T) {
	for _, tc := range []struct {
		name      string
		inCluster bool
		want      bool
	}{
		{"server profile", true, true},
		{"desktop", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/apis/example.io/v1":
					_ = json.NewEncoder(w).Encode(map[string]any{"kind": "APIResourceList", "groupVersion": "example.io/v1",
						"resources": []any{map[string]any{"name": "widgets", "singularName": "widget", "namespaced": true,
							"kind": "Widget", "verbs": []string{"get", "list"}}}})
				default:
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403,` +
						`"message":"widgets.example.io is forbidden: User \"system:serviceaccount:kubetower:kubetower\" cannot list resource",` +
						`"details":{"group":"example.io","kind":"widgets"}}`))
				}
			}))
			t.Cleanup(server.Close)
			kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
			if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\nkind: Config\ncurrent-context: prod\nclusters:\n- name: prod\n  cluster: {server: "+
				server.URL+"}\ncontexts:\n- name: prod\n  context: {cluster: prod, user: sa}\nusers:\n- name: sa\n  user: {token: t}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			manager, err := cluster.NewManager(kubeconfig)
			if err != nil {
				t.Fatal(err)
			}
			reviews := &accesstest.Reviews{}
			s, err := New(Options{Clusters: manager, InCluster: tc.inCluster,
				Access: access.New(access.Options{Reviews: reviews.For, InCluster: tc.inCluster})})
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Host().List(asPerson(shopViewer()), "prod", scopeQuery("widgets"))
			if err == nil {
				t.Fatal("the forbidden read answered no error")
			}
			if !apierrors.IsForbidden(err) {
				t.Errorf("the original 403 is lost: %v", err)
			}
			if got := errors.Is(err, extension.ErrConsoleNotGranted); got != tc.want {
				t.Fatalf("ErrConsoleNotGranted = %v, want %v (%v)", got, tc.want, err)
			}
			if errors.Is(err, extension.ErrDenied) {
				t.Errorf("a console grant read as the person's refusal: %v", err)
			}
			if tc.want {
				refused, ok := extension.ConsoleNotGranted(err)
				if !ok || refused.Status != http.StatusServiceUnavailable || refused.Code != extension.CodeConsoleNotGranted ||
					!strings.Contains(refused.Message, "widgets.example.io") {
					t.Errorf("answered %+v, %v", refused, ok)
				}
			}
		})
	}
}
