// Package seen keeps the last KubeTower version whose "What's new" the person
// at this desktop has seen (#80), in the state directory. The server profile
// keeps it in each browser instead: there is no per-person store to add there.
package seen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/thonicdev/kubetower/internal/statefile"
)

// FileName is the file in the state directory.
const FileName = "seen.json"

// Pattern is a release version, MAJOR.MINOR.PATCH[-pre] with no `v`: the
// tag rule of release.yml, which cmd/ktbuild stamps into the binary. It is the
// one shape every keeper of a release checks - this store, the API's write,
// cmd/ktbuild - so a stamped release is always one the store accepts.
// api/openapi.yaml and the console's own memory copy it, and tests pin both.
const Pattern = `^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`

var release = regexp.MustCompile(Pattern)

// Valid says v is a release version this store keeps.
func Valid(v string) bool { return release.MatchString(v) }

// Store is the version last seen. Safe from any goroutine; a nil Store keeps
// nothing and answers "".
type Store struct {
	path string
	say  *statefile.Complainer

	mu      sync.RWMutex
	version string
}

type persisted struct {
	Version string `json:"version,omitempty"`
}

// New reads dir's file. An empty dir keeps the version in memory only.
func New(dir string, logf func(string, ...any)) *Store {
	s := &Store{say: statefile.NewComplainer(logf)}
	if dir = strings.TrimSpace(dir); dir != "" {
		s.path = filepath.Join(dir, FileName)
	}
	s.load()
	return s
}

func (s *Store) load() {
	if s.path == "" {
		return
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			s.say.Log("seen: %s could not be read (%v); What's new shows again", s.path, err)
		}
		return
	}
	var file persisted
	if err := json.Unmarshal(raw, &file); err != nil || (file.Version != "" && !Valid(file.Version)) {
		s.say.Log("seen: %s is not usable; What's new shows again", s.path)
		return
	}
	s.version = file.Version
}

// Version is the last version seen, or "".
func (s *Store) Version() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version
}

// Set keeps v, in memory first: a failed write (statefile.ErrNotSaved) leaves
// it in effect until the next start.
func (s *Store) Set(v string) error {
	if !Valid(v) {
		return fmt.Errorf("%q is not a release version", v)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version = v
	if s.path == "" {
		return nil
	}
	raw, _ := json.Marshal(persisted{Version: v})
	if err := statefile.Write(s.path, raw); err != nil {
		s.say.Once("seen: %v", err)
		return err
	}
	s.say.Clear()
	return nil
}
