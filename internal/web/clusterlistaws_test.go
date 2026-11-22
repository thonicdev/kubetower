package web

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// TestTheClusterListSaysWhichContextsSignInThroughTheAWSCLI pins `aws` on
// `GET /api/clusters`: the home puts the AWS SSO session's
// notice on the cards this flag marks, and nothing else checked that the list
// fills it. Through the real `apiClusters.List` over a kubeconfig with one
// context of each kind.
func TestTheClusterListSaysWhichContextsSignInThroughTheAWSCLI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: Config
current-context: eks
clusters:
- name: remote
  cluster:
    server: https://prod.example:6443
contexts:
- name: eks
  context: {cluster: remote, user: aws-cli}
- name: eks-by-path
  context: {cluster: remote, user: aws-cli-by-path}
- name: vault
  context: {cluster: remote, user: aws-vault}
- name: oidc
  context: {cluster: remote, user: kubelogin}
- name: static
  context: {cluster: remote, user: token}
users:
- name: aws-cli
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: aws
      args: [eks, get-token, --cluster-name, prod, --region, eu-west-3]
- name: aws-cli-by-path
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: /usr/local/bin/aws
      args: [--profile, prod, eks, get-token, --cluster-name, prod]
- name: aws-vault
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: aws-vault
      args: [exec, prod, --, aws, eks, get-token, --cluster-name, prod]
- name: kubelogin
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: kubectl
      args: [oidc-login, get-token]
- name: token
  user: {token: none}
`), 0o600); err != nil {
		t.Fatalf("writing the kubeconfig: %v", err)
	}
	manager, err := cluster.NewManager(path)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	server, err := New(Options{Clusters: manager})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	clusters, fail := apiClusters{server}.List(context.Background())
	if fail != nil {
		t.Fatalf("the cluster list failed: %+v", fail)
	}
	got := map[string]bool{}
	for _, entry := range clusters {
		got[entry.Name] = entry.AWS
	}

	// `aws-vault` is false by design: the flag reads the exec command's
	// basename, as api.Cluster.AWS says. Pinned so that widening it is a choice.
	want := map[string]bool{"eks": true, "eks-by-path": true, "vault": false, "oidc": false, "static": false}
	if len(got) != len(want) {
		t.Fatalf("the list has %d clusters, %v; want %d", len(got), got, len(want))
	}
	for name, aws := range want {
		if got[name] != aws {
			t.Errorf("%s is sent aws=%t; want %t", name, got[name], aws)
		}
	}
}
