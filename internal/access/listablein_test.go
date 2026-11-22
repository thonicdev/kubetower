package access_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/identity"
)

func secretsPerson(group string) context.Context {
	return identity.With(context.Background(), identity.Who{Name: "someone@example.com", Method: identity.SSO, Groups: []string{group}})
}

func listableGate() *access.Gate {
	return access.New(access.Options{Reviews: (&accesstest.Reviews{Answer: secretReaders}).For, InCluster: true})
}

var clusterNamespaces = []string{"shop", "billing", "kube-system"}

// The namespaces a person may list a kind in are their own: alice's is shop.
func TestListableInNamesThePersonsNamespaces(t *testing.T) {
	allowed, complete, err := listableGate().ListableIn(secretsPerson("oidc:shop-admins"), "prod", listSecrets, clusterNamespaces)
	if err != nil || !complete || strings.Join(allowed, ",") != "shop" {
		t.Fatalf("allowed %v complete %v err %v, want [shop], complete, no error", allowed, complete, err)
	}
}

// A person allowed in none of the cluster's namespaces is refused, in one
// sentence about every namespace - never an empty set read as an empty list.
func TestListableInRefusesAPersonAllowedNowhere(t *testing.T) {
	allowed, complete, err := listableGate().ListableIn(secretsPerson("oidc:viewers"), "prod", listSecrets, clusterNamespaces)
	if !errors.Is(err, access.ErrDenied) {
		t.Fatalf("err = %v (allowed %v, complete %v), want a denial", err, allowed, complete)
	}
	if want := "the cluster says you may not list secrets in any namespace"; err.Error() != want {
		t.Errorf("sentence = %q, want %q", err.Error(), want)
	}
}

// A review that could not be made leaves the set unknown: nothing is
// concluded from what it lacks, and nobody is refused on an answer nobody gave.
func TestListableInWithAReviewNotMadeIsIncomplete(t *testing.T) {
	gate := access.New(access.Options{InCluster: true, Reviews: (&accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		if q.Attributes.Namespace == "billing" {
			return false, errors.New("the review did not answer")
		}
		return false, nil
	}}).For})
	_, complete, err := gate.ListableIn(secretsPerson("oidc:viewers"), "prod", listSecrets, clusterNamespaces)
	if err != nil || complete {
		t.Errorf("complete %v err %v, want an incomplete answer and no refusal", complete, err)
	}
}

// No namespace to ask about is not "allowed nowhere".
func TestListableInWithNoNamespacesIsIncomplete(t *testing.T) {
	_, complete, err := listableGate().ListableIn(secretsPerson("oidc:viewers"), "prod", listSecrets, nil)
	if err != nil || complete {
		t.Errorf("complete %v err %v, want an incomplete answer and no refusal", complete, err)
	}
}
