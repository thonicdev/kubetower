// Package storage is the Storage Inspector extra: what storage the cluster
// holds, what of it is orphaned, what is filling up and what a snapshot backs,
// behind its own per-cluster switch.
//
// It is read-only. No route writes, and no answer offers a delete or a resize:
// an orphan links to its object's own page, where the console's verbs live.
//
// Every read goes through the Host, reviewed for the person behind the
// request: `List` for the PersistentVolumes, PersistentVolumeClaims,
// StorageClasses, Pods, StatefulSets and the snapshot kinds, `Definitions` to
// tell a snapshot kind that is not installed from one that cannot be read, and
// `Usage` for the kubelets' volume figures (`stats/summary` through the node
// proxy). No Secret is read. The page holds no cache and starts no goroutine.
//
// Absent and unreadable stay two answers wherever both can happen: the
// snapshot kinds not installed against installed and refused, and a claim no
// kubelet measured against a claim nothing mounts.
package storage

import (
	"net/http"

	"github.com/thonicdev/kubetower/extension"
)

// Key is `storage`: the page's URL segment, the KT_EXTRAS name and the key
// the per-cluster switch is stored under.
const Key = "storage"

// Storage is the extension `cmd/kubetower` registers. It holds nothing between
// requests.
type Storage struct{}

// New is the constructor `cmd/kubetower` calls.
func New() Storage { return Storage{} }

func (Storage) Key() string   { return Key }
func (Storage) Label() string { return "Storage Inspector" }

// Summary is the catalog entry.
func (Storage) Summary() string {
	return "See the cluster's volumes, claims and storage classes, which of them are orphaned, " +
		"how full each claim is and when it was last snapshotted. Read-only."
}

// Needs is what the cluster must grant.
func (Storage) Needs() string {
	return "the right to list PersistentVolumes, PersistentVolumeClaims, StorageClasses, Pods and StatefulSets; " +
		"nodes/proxy for how full each claim is; and the snapshot kinds when they are installed"
}

// Legacy is empty: the key has never been anything else.
func (Storage) Legacy() []string { return nil }

// Routes are reads only, so every one is a GET.
func (Storage) Routes(h extension.Host) []extension.Route {
	return []extension.Route{
		{Method: http.MethodGet, Pattern: "/{$}", Handler: overviewHandler(h)},
		// The Storage tab in a PersistentVolumeClaim's panel.
		{Method: http.MethodGet, Pattern: "/claim/{namespace}/{name}", Handler: claimHandler(h)},
		// The Storage tab in a Pod's panel: the claims it mounts.
		{Method: http.MethodGet, Pattern: "/pod/{namespace}/{name}", Handler: podHandler(h)},
	}
}
