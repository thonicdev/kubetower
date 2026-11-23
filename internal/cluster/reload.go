package cluster

import (
	gocontext "context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"time"
)

// Picking up a kubeconfig edited while the console runs.
//
// Reload has always existed; nothing called it after start-up, so an exec block
// corrected in the file kept failing until the console was quit and opened
// again. The files are polled rather than watched: an editor that saves by
// renaming, a file that appears later in a `KUBECONFIG` list and a directory
// that is not there yet all defeat a file watcher, and reading a few small files
// every couple of seconds costs nothing. Their contents are compared, not their
// modification times, which two saves inside one clock tick can share. A Retry
// reads them at once (ReloadIfChanged), so it never waits for the tick.

// WatchEvery is how often Watch reads the kubeconfig files.
const WatchEvery = 2 * time.Second

// digestFiles hashes the contents of every file in the list, in order, with a
// missing file and an unreadable one hashed as what they are.
func digestFiles(paths Kubeconfig) [sha256.Size]byte {
	h := sha256.New()
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		switch {
		case err == nil:
			h.Write([]byte("file\x00"))
			h.Write(raw)
		case errors.Is(err, fs.ErrNotExist):
			h.Write([]byte("absent"))
		default:
			h.Write([]byte("unreadable\x00" + err.Error()))
		}
		h.Write([]byte{0})
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

// ReloadIfChanged re-reads the kubeconfig when its files changed since the last
// read, and reports whether it did.
//
// A file saved but not changed is not a reload: a reload drops every client,
// and with them the credentials they hold. A file that does not parse is
// reported and the previous kubeconfig kept, so a save caught halfway is read
// again on the next call rather than taking every cluster away.
func (m *Manager) ReloadIfChanged() (bool, error) {
	if m.inCluster {
		return false, nil
	}
	m.mu.RLock()
	last := m.digest
	m.mu.RUnlock()
	if digestFiles(m.kubeconfig) == last {
		return false, nil
	}
	if err := m.Reload(); err != nil {
		return false, err
	}
	return true, nil
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
