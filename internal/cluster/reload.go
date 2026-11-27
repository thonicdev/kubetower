package cluster

import (
	"bytes"
	gocontext "context"
	"errors"
	"io/fs"
	"os"
	"reflect"
	"time"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Picking up a kubeconfig edited while the console runs.
//
// Reload existed; nothing called it after start-up, so an exec block corrected
// in the file kept failing until the console was quit and opened again. The
// files are polled rather than watched: an editor that saves by renaming, a
// file that appears later in a `KUBECONFIG` list and a directory that is not
// there yet all defeat a file watcher, and reading a few small files every
// couple of seconds costs nothing. Their contents are compared, not their
// modification times, which two saves inside one clock tick can share. A Retry
// reads them at once (ReloadIfChanged), so it never waits for the tick.

// WatchEvery is how often Watch reads the kubeconfig files.
const WatchEvery = 2 * time.Second

// settleFor is how long a kubeconfig that looks caught halfway through a save
// must stay so before it is believed. `kubectl config use-context` and kubectx
// truncate the file and then write it: read in between, it is empty, and every
// cluster would leave the console for one tick. A file really emptied stays
// empty, and is applied once this has passed.
const settleFor = 3 * time.Second

// fileRead is one kubeconfig file as read: its bytes, or why there were none.
type fileRead struct {
	data    []byte
	absent  bool
	problem string
}

func (f fileRead) equal(g fileRead) bool {
	return f.absent == g.absent && f.problem == g.problem && bytes.Equal(f.data, g.data)
}

// kubeconfigRead is the files and the merge made from them.
type kubeconfigRead struct {
	files []fileRead
	raw   *clientcmdapi.Config
}

// pendingRead is a read ReloadIfChanged holds back, and since when.
type pendingRead struct {
	files []fileRead
	since time.Time
}

// readFiles reads every file in the list, in order.
func readFiles(paths Kubeconfig) []fileRead {
	out := make([]fileRead, len(paths))
	for i, path := range paths {
		data, err := os.ReadFile(path)
		switch {
		case err == nil:
			out[i] = fileRead{data: data}
		case errors.Is(err, fs.ErrNotExist):
			out[i] = fileRead{absent: true}
		default:
			out[i] = fileRead{problem: err.Error()}
		}
	}
	return out
}

func sameFiles(a, b []fileRead) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].equal(b[i]) {
			return false
		}
	}
	return true
}

// errTorn is a read whose files changed while they were merged.
var errTorn = errors.New("a kubeconfig file changed while it was read")

// readAttempts bounds how often read tries for files that hold still.
const readAttempts = 3

// read reads the files, merges them, and reads them again: the merge is kept
// only if the files did not change in between, so the merge applied is always
// the merge of the bytes recorded (#153). client-go's loader reads the paths
// itself, so the bytes cannot be handed to it; checking them on both sides is
// what makes the merge theirs. A file truncated between two reads would
// otherwise apply a merge without it, recorded under bytes that still held
// it, and the next tick, seeing those bytes unchanged, would never put it
// back.
func (m *Manager) read() (kubeconfigRead, error) {
	for attempt := 0; ; attempt++ {
		files := readFiles(m.kubeconfig)
		if m.betweenReads != nil {
			m.betweenReads()
		}
		raw, err := m.kubeconfig.Load()
		if err != nil {
			return kubeconfigRead{}, err
		}
		if sameFiles(files, readFiles(m.kubeconfig)) {
			return kubeconfigRead{files: files, raw: raw}, nil
		}
		if attempt+1 >= readAttempts {
			return kubeconfigRead{}, errTorn
		}
	}
}

// halfway reports whether a read looks like a save caught halfway: a file that
// held something and is now empty or gone (an editor that renames the
// original away before writing the new one, #153), or a kubeconfig that had
// contexts and now has none.
func halfway(before []fileRead, previous *clientcmdapi.Config, after kubeconfigRead) bool {
	for i, file := range after.files {
		if i < len(before) && len(before[i].data) > 0 && file.problem == "" && (file.absent || len(file.data) == 0) {
			return true
		}
	}
	return previous != nil && len(previous.Contexts) > 0 && len(after.raw.Contexts) == 0
}

// sameContext reports whether a context resolves to the same entries in two
// kubeconfigs: the context, its cluster and its user. A client built from one
// is then right for the other.
func sameContext(a, b *clientcmdapi.Config, name string) bool {
	if a == nil || b == nil {
		return a == b
	}
	ca, cb := a.Contexts[name], b.Contexts[name]
	if ca == nil || cb == nil {
		return false
	}
	return reflect.DeepEqual(ca, cb) &&
		reflect.DeepEqual(a.Clusters[ca.Cluster], b.Clusters[cb.Cluster]) &&
		reflect.DeepEqual(a.AuthInfos[ca.AuthInfo], b.AuthInfos[cb.AuthInfo])
}

// ReloadIfChanged re-reads the kubeconfig when its files changed since the last
// read, and reports whether it applied it.
//
// A file saved but not changed is not a reload. A file that does not parse or
// cannot be read is an error, and the previous kubeconfig stays. A read that
// looks caught halfway through a save (halfway) is held back, silently, until
// it has lasted settleFor: the write that follows replaces it first.
func (m *Manager) ReloadIfChanged() (bool, error) {
	if m.inCluster {
		return false, nil
	}
	// One at a time: the watcher's tick and a cluster's Retry would otherwise
	// apply an older read after a newer one, until the next tick (#153).
	m.reloading.Lock()
	defer m.reloading.Unlock()
	files := readFiles(m.kubeconfig)
	m.mu.RLock()
	last, previous, pending := m.files, m.raw, m.pending
	m.mu.RUnlock()
	if sameFiles(files, last) {
		if pending.files != nil {
			m.mu.Lock()
			m.pending = pendingRead{}
			m.mu.Unlock()
		}
		return false, nil
	}

	read, err := m.read()
	if errors.Is(err, errTorn) {
		// Still being written: the next tick reads it whole.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if halfway(last, previous, read) {
		now := m.clock()
		if pending.files == nil || !sameFiles(read.files, pending.files) {
			m.mu.Lock()
			m.pending = pendingRead{files: read.files, since: now}
			m.mu.Unlock()
			return false, nil
		}
		if now.Sub(pending.since) < settleFor {
			return false, nil
		}
	}
	if m.beforeApply != nil {
		m.beforeApply()
	}
	m.apply(read)
	return true, nil
}

func (m *Manager) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// Watch calls ReloadIfChanged every interval until ctx ends, logging each
// reload and each distinct failure once.
func (m *Manager) Watch(ctx gocontext.Context, every time.Duration, logf func(string, ...any)) {
	if m.inCluster {
		return
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	said := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		changed, err := m.ReloadIfChanged()
		switch {
		case err != nil:
			if err.Error() != said && logf != nil {
				logf("kubeconfig changed and could not be read, the previous one stays: %v", err)
			}
			said = err.Error()
		case changed:
			said = ""
			if logf != nil {
				logf("kubeconfig changed: re-read %s, %d context(s)", m.kubeconfig, len(m.Contexts()))
			}
		}
	}
}
