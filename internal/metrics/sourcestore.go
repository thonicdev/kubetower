package metrics

// SourceStore is one setting per cluster: the Service a cluster's Prometheus
// API answers on, chosen by hand in Settings › General where discovery cannot
// find it, and the small JSON file in the state directory that remembers it.
//
// Desktop only. Under the server profile the choice is KT_PROMETHEUS_SERVICE,
// the deployment's (internal/launch/prometheus.go): a source a person could set
// would decide where every person's charts come from, and where their queries
// go.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/thonicdev/kubetower/internal/statefile"
)

// SourceFileName is the setting's file inside the state directory.
const SourceFileName = "metricsource.json"

// MaxSourceClusters bounds what the file may hold, so a file edited by hand cannot
// grow the console's memory without end. A kubeconfig with more contexts than
// this is not one anybody sets a source on each of.
const MaxSourceClusters = 500

// sourceFile is what is written: cluster name to `namespace/service:port[/path]`.
type sourceFile struct {
	Sources map[string]string `json:"sources"`
}

// SourceStore is the setting and the file under it. Everything goes through the
// mutex, the file write included, so two concurrent saves cannot interleave.
type SourceStore struct {
	// path is empty when there is nowhere to persist to; the setting then lives
	// as long as the process.
	path string
	say  *statefile.Complainer

	mu      sync.RWMutex
	sources map[string]*PromTarget
}

// NewSourceStore opens the store in dir, which may be empty. It never fails: an
// unreadable state directory loses the setting, not the console, and every
// cluster goes back to discovery.
func NewSourceStore(dir string, logf func(string, ...any)) *SourceStore {
	s := &SourceStore{say: statefile.NewComplainer(logf), sources: map[string]*PromTarget{}}
	if dir == "" {
		return s
	}
	s.path = filepath.Join(dir, SourceFileName)
	s.load()
	return s
}

// Path is the file, or empty when there is none.
func (s *SourceStore) Path() string { return s.path }

func (s *SourceStore) load() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			s.say.Log("cannot read %s, every cluster's metrics source is discovered: %v", s.path, err)
		}
		return
	}
	var stored sourceFile
	if err := json.Unmarshal(raw, &stored); err != nil {
		s.say.Log("%s is unreadable, every cluster's metrics source is discovered: %v", s.path, err)
		return
	}
	names := make([]string, 0, len(stored.Sources))
	for name := range stored.Sources {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if len(s.sources) == MaxSourceClusters {
			s.say.Log("%s names more than %d clusters; the rest are discovered", s.path, MaxSourceClusters)
			break
		}
		// Validated on the way in too: the file is editable by hand.
		target, err := ParseTarget(stored.Sources[name])
		if name == "" || err != nil {
			s.say.Log("%s: the source of %q is ignored and discovered instead: %v", s.path, name, err)
			continue
		}
		s.sources[name] = target
	}
}

// Source is the cluster's source, or nil where discovery decides. The
// signature is Options.Sources'.
func (s *SourceStore) Source(cluster string) *PromTarget {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if target, ok := s.sources[cluster]; ok {
		copied := *target
		return &copied
	}
	return nil
}

// Set records a cluster's source, or forgets it when source is empty.
//
// A value that does not parse is refused and nothing changes. A valid one is
// in effect even when the file cannot be written; the error then wraps
// statefile.ErrNotSaved.
func (s *SourceStore) Set(cluster, source string) (*PromTarget, error) {
	source = strings.TrimSpace(source)
	var target *PromTarget
	if source != "" {
		parsed, err := ParseTarget(source)
		if err != nil {
			return s.Source(cluster), err
		}
		target = parsed
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if target == nil {
		delete(s.sources, cluster)
	} else {
		if _, known := s.sources[cluster]; !known && len(s.sources) >= MaxSourceClusters {
			return nil, fmt.Errorf("a source is already set on %d clusters, the most this keeps", MaxSourceClusters)
		}
		s.sources[cluster] = target
	}
	if s.path == "" {
		return target, nil
	}
	if err := s.write(); err != nil {
		s.say.Once("metrics source: %v", err)
		return target, err
	}
	s.say.Clear()
	return target, nil
}

// write persists the setting. Called with the lock held.
func (s *SourceStore) write() error {
	out := sourceFile{Sources: make(map[string]string, len(s.sources))}
	for name, target := range s.sources {
		out.Sources[name] = target.Address()
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: the setting could not be encoded (%v)", statefile.ErrNotSaved, err)
	}
	return statefile.Write(s.path, append(raw, '\n'))
}
