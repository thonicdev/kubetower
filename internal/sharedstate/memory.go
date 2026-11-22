package sharedstate

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Memory is the store of a single replica: a map, in this process.
//
// A restart empties it, signing every single sign-on session out and dropping
// every confirmation plan. With two replicas it is not a store at all, since
// each pod holds a different map; the chart deploys Valkey for more than one.
type Memory struct {
	now func() time.Time

	mu      sync.Mutex
	entries map[string]memoryEntry
}

type memoryEntry struct {
	value   []byte
	expires time.Time
}

// NewMemory builds an empty store reading the clock through now, so a test can
// move time rather than wait for it.
func NewMemory(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{now: now, entries: map[string]memoryEntry{}}
}

// Put writes a key and sweeps what has expired, so the map is bounded by what
// is alive. Nothing else sweeps: a read refuses an expired key anyway.
func (m *Memory) Put(_ context.Context, key string, value []byte, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	for k, e := range m.entries {
		if !now.Before(e.expires) {
			delete(m.entries, k)
		}
	}
	if !now.Before(expires) {
		delete(m.entries, key)
		return nil
	}
	m.entries[key] = memoryEntry{value: append([]byte(nil), value...), expires: expires}
	return nil
}

// PutNew writes a key only where no live one is, under the one lock.
func (m *Memory) PutNew(_ context.Context, key string, value []byte, expires time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if !now.Before(expires) {
		return false, nil
	}
	if e, ok := m.entries[key]; ok && now.Before(e.expires) {
		return false, nil
	}
	m.entries[key] = memoryEntry{value: append([]byte(nil), value...), expires: expires}
	return true, nil
}

// Get reads a key. Memory always answers, so the error is always nil.
func (m *Memory) Get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok || !m.now().Before(e.expires) {
		return nil, false, nil
	}
	return append([]byte(nil), e.value...), true, nil
}

// Take reads a key and deletes it under one lock, so two callers get it once.
func (m *Memory) Take(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	delete(m.entries, key)
	if !ok || !m.now().Before(e.expires) {
		return nil, false, nil
	}
	return e.value, true, nil
}

// Extend moves a live key's expiry, and never creates one.
func (m *Memory) Extend(_ context.Context, key string, expires time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	now := m.now()
	if !ok || !now.Before(e.expires) {
		delete(m.entries, key)
		return false, nil
	}
	if !now.Before(expires) {
		delete(m.entries, key)
		return true, nil
	}
	e.expires = expires
	m.entries[key] = e
	return true, nil
}

// Delete forgets a key.
func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, key)
	return nil
}

// Describe names the store for the start-up log.
func (m *Memory) Describe() string {
	return "in this process's memory (one replica: a restart signs single sign-on sessions out)"
}

// Len is how many keys are alive under prefix, for tests checking a bound.
func (m *Memory) Len(prefix string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, now := 0, m.now()
	for k, e := range m.entries {
		if strings.HasPrefix(k, prefix) && now.Before(e.expires) {
			n++
		}
	}
	return n
}
