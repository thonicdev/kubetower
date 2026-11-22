package cluster

import (
	"os"
	"path/filepath"
	"testing"
)

// AWS is what the home reads to put an AWS SSO session's notice only on the
// cards it can concern: a GKE or OIDC context that failed
// on its own sign-in must not be told to run `aws sso login`.
func TestAWSIsTheContextsWhoseUserRunsTheAWSCLI(t *testing.T) {
	kubeconfig := `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster: {server: "https://example.invalid"}
users:
- name: eks
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: aws
      args: [--region, eu-west-3, eks, get-token, --cluster-name, prod]
- name: eks-windows
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: C:\Program Files\Amazon\AWSCLIV2\aws.exe
      args: [eks, get-token, --cluster-name, prod]
- name: gke
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: gke-gcloud-auth-plugin
- name: token
  user: {token: abc}
contexts:
- {name: eks, context: {cluster: c, user: eks}}
- {name: eks-windows, context: {cluster: c, user: eks-windows}}
- {name: gke, context: {cluster: c, user: gke}}
- {name: token, context: {cluster: c, user: token}}
- {name: no-user, context: {cluster: c, user: missing}}
current-context: eks
`
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(kubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(path)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	for context, want := range map[string]bool{
		"eks":         true,
		"eks-windows": true,
		"gke":         false,
		"token":       false,
		"no-user":     false,
		"not-there":   false,
	} {
		if got := manager.AWS(context); got != want {
			t.Errorf("AWS(%q) = %v, want %v", context, got, want)
		}
	}
}
