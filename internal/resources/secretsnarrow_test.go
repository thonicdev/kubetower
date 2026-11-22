package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/identity"
)

// secretsServer is an API server holding Secrets in three namespaces. It
// answers a metadata-only list with metadata, a list with the Secrets and
// their data, and records every request, so a test can say which Secrets'
// data the console was sent.
type secretsServer struct {
	mu   sync.Mutex
	seen []string // "<path> metadata" or "<path> data"
	// selectors is each request's labelSelector, in order.
	selectors []string
	// ignoreAccept answers a metadata-only list with the Secrets themselves, as
	// an API server that does not honor the Accept header would.
	ignoreAccept bool
}

var heldSecrets = map[string][]string{
	"shop":        {"db", "api-key"},
	"billing":     {"ledger"},
	"kube-system": {"bootstrap"},
}

func (s *secretsServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	metadata := strings.Contains(r.Header.Get("Accept"), "as=PartialObjectMetadataList")
	namespace := ""
	if rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/namespaces/"); ok {
		namespace, _, _ = strings.Cut(rest, "/")
	} else if r.URL.Path != "/api/v1/secrets" {
		http.NotFound(w, r)
		return
	}
	kind := "data"
	if metadata && !s.ignoreAccept {
		kind = "metadata"
	}
	s.mu.Lock()
	s.seen = append(s.seen, r.URL.Path+" "+kind)
	s.selectors = append(s.selectors, r.URL.Query().Get("labelSelector"))
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if kind == "metadata" {
		list := metav1.PartialObjectMetadataList{TypeMeta: metav1.TypeMeta{Kind: "PartialObjectMetadataList", APIVersion: "meta.k8s.io/v1"}}
		for ns, names := range heldSecrets {
			for _, name := range names {
				list.Items = append(list.Items, metav1.PartialObjectMetadata{
					TypeMeta:   metav1.TypeMeta{Kind: "PartialObjectMetadata", APIVersion: "meta.k8s.io/v1"},
					ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}})
			}
		}
		_ = json.NewEncoder(w).Encode(list)
		return
	}
	list := corev1.SecretList{TypeMeta: metav1.TypeMeta{Kind: "SecretList", APIVersion: "v1"}}
	for ns, names := range heldSecrets {
		if namespace != "" && ns != namespace {
			continue
		}
		for _, name := range names {
			list.Items = append(list.Items, corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
				Data:       map[string][]byte{"password": []byte("value-of-" + ns + "-" + name)}})
		}
	}
	_ = json.NewEncoder(w).Encode(list)
}

func (s *secretsServer) selectorsAsked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.selectors...)
}

func (s *secretsServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

// shopOnly may list Secrets in `shop` and nowhere else.
func shopOnly(q accesstest.Question) (bool, error) {
	a := q.Attributes
	return a.Resource == "secrets" && a.Verb == "list" && a.Namespace == "shop", nil
}

func secretsReader(t *testing.T, s *secretsServer, answer func(accesstest.Question) (bool, error)) Reader {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	clients, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	gate := access.New(access.Options{Reviews: (&accesstest.Reviews{Answer: answer}).For, InCluster: true})
	return Reader{Gate: gate, Cluster: "prod", Clients: clients}
}

func alice() context.Context {
	return identity.With(context.Background(), identity.Who{Name: "alice@example.com", Method: identity.SSO})
}

// TestANarrowedSecretsListReadsNoSecretItDrops: a person who may list Secrets
// in `shop` only is shown shop's - and the console is sent no other
// namespace's Secret, rather than every Secret of the cluster, values
// included, read with the console's credentials and then dropped.
func TestANarrowedSecretsListReadsNoSecretItDrops(t *testing.T) {
	s := &secretsServer{}
	r := secretsReader(t, s, shopOnly)

	secrets, seen, err := fetchSecrets(alice(), r, "")
	if err != nil {
		t.Fatalf("fetchSecrets: %v", err)
	}
	if !seen.Narrowed {
		t.Error("the list was not marked narrowed")
	}
	got := map[string]bool{}
	for _, secret := range secrets {
		got[secret.Namespace+"/"+secret.Name] = true
	}
	if len(got) != 2 || !got["shop/db"] || !got["shop/api-key"] {
		t.Errorf("alice was shown %v, want shop's two Secrets", got)
	}
	requests := s.requests()
	want := []string{"/api/v1/secrets metadata", "/api/v1/namespaces/shop/secrets data"}
	if strings.Join(requests, "; ") != strings.Join(want, "; ") {
		t.Errorf("the console asked the API server %q, want only a metadata list and shop's Secrets %q", requests, want)
	}
}

// A person who may list Secrets everywhere costs one list; one
// namespace asked is that namespace's list.
func TestASecretsListAllowedWholeIsOneList(t *testing.T) {
	s := &secretsServer{}
	r := secretsReader(t, s, func(accesstest.Question) (bool, error) { return true, nil })

	secrets, seen, err := fetchSecrets(alice(), r, "")
	if err != nil || seen.Narrowed || len(secrets) != 4 {
		t.Fatalf("a whole list answered %d Secrets (narrowed %t, %v), want all 4", len(secrets), seen.Narrowed, err)
	}
	if _, _, err := fetchSecrets(alice(), r, "billing"); err != nil {
		t.Fatalf("one namespace: %v", err)
	}
	requests := s.requests()
	want := []string{"/api/v1/secrets data", "/api/v1/namespaces/billing/secrets data"}
	if strings.Join(requests, "; ") != strings.Join(want, "; ") {
		t.Errorf("the console asked %q, want %q", requests, want)
	}
}

// An API server that answers the metadata-only list with the Secrets
// themselves is refused, not decoded: the namespaces read out of it would
// make this the cluster-wide read under another name.
func TestASecretsMetadataListAnsweredWithDataIsRefused(t *testing.T) {
	s := &secretsServer{ignoreAccept: true}
	r := secretsReader(t, s, shopOnly)

	_, _, err := fetchSecrets(alice(), r, "")
	if err == nil || !strings.Contains(err.Error(), "not their metadata") {
		t.Fatalf("a metadata list answered with Secrets was not refused: %v", err)
	}
	for _, req := range s.requests() {
		if strings.HasPrefix(req, "/api/v1/namespaces/") {
			t.Errorf("a namespace was read after the metadata list failed: %s", req)
		}
	}
}
