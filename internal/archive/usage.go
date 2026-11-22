package archive

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/thonicdev/kubetower/textutil"
)

// Usage is how much disk the archive is costing, and where: the breakdown by
// cluster and day says which switch to move.
type Usage struct {
	// Bytes is everything under the archive root, settings file included.
	Bytes int64 `json:"bytes"`
	// EventBytes and LogBytes split the total by what the data is. Their sum can
	// be slightly less than Bytes: directory entries and the small marker files
	// belong to neither.
	EventBytes int64 `json:"eventBytes"`
	LogBytes   int64 `json:"logBytes"`
	Files      int   `json:"files"`

	// Clusters, largest first.
	Clusters []ClusterUsage `json:"clusters"`
	// Days, oldest first.
	Days []DayUsage `json:"days"`

	// Ceiling is the whole-archive limit, past which log capture pauses.
	Ceiling int64 `json:"ceiling"`
	// ComputedAt is when the walk ran. Approximate says the figure has been
	// adjusted by writes since, rather than measured.
	ComputedAt  time.Time `json:"computedAt"`
	Approximate bool      `json:"approximate"`
	Root        string    `json:"root"`
	// Note reports a walk that could not finish, so a settings page can say the
	// figure is partial instead of quietly under-reporting.
	Note string `json:"note,omitempty"`
}

// ClusterUsage is one cluster's share.
type ClusterUsage struct {
	Cluster    string `json:"cluster"`
	Bytes      int64  `json:"bytes"`
	EventBytes int64  `json:"eventBytes"`
	LogBytes   int64  `json:"logBytes"`
	Days       int    `json:"days"`
}

// DayUsage is one day's share.
type DayUsage struct {
	Day        string `json:"day"`
	Bytes      int64  `json:"bytes"`
	EventBytes int64  `json:"eventBytes"`
	LogBytes   int64  `json:"logBytes"`
}

// Human renders a byte count the way a settings page should show it.
func (u Usage) Human() string { return humanBytes(u.Bytes) }

// Human renders one cluster's share.
func (c ClusterUsage) Human() string { return humanBytes(c.Bytes) }

// Usage is the cached figure, adjusted by what has been written since the last
// walk. A read lock on a cache only the walker writes plus an atomic counter,
// so a refreshing settings page never makes a log line wait.
func (a *Archive) Usage() Usage {
	a.usageMu.RLock()
	u, seen := a.usage, a.usageSeen
	a.usageMu.RUnlock()

	if !seen {
		return a.RefreshUsage()
	}
	if delta := a.pending.Load(); delta != 0 {
		u.Bytes += delta
		if u.Bytes < 0 {
			u.Bytes = 0
		}
		u.Approximate = true
	}
	u.Ceiling = a.maxTotal
	u.Root = a.root
	return u
}

// approxBytes is the cheap total the log sink checks against the ceiling.
func (a *Archive) approxBytes() int64 {
	a.usageMu.RLock()
	base := a.usage.Bytes
	a.usageMu.RUnlock()
	total := base + a.pending.Load()
	if total < 0 {
		return 0
	}
	return total
}

// RefreshUsage walks the tree and replaces the cached figure. It runs when the
// pruner ticks and when no figure has been computed yet, never on a write.
func (a *Archive) RefreshUsage() Usage {
	// One walk at a time, and a caller that queued behind a walk takes its
	// answer, so repeated page renders never stack traversals of the archive.
	arrived := a.usageGen.Load()
	if a.usageArrived != nil {
		a.usageArrived() // test seam; see the field
	}
	a.usageWalk.Lock()
	defer a.usageWalk.Unlock()

	// A walk completed while this call waited for the lock: take its answer.
	// A generation counter, not a timestamp: with a frozen test clock a time
	// window would never refresh.
	if a.usageGen.Load() != arrived {
		a.usageMu.RLock()
		held, seen := a.usage, a.usageSeen
		a.usageMu.RUnlock()
		if seen {
			return held
		}
	}

	u := Usage{
		ComputedAt: a.now(),
		Ceiling:    a.maxTotal,
		Root:       a.root,
	}
	if a.root == "" {
		a.store(u, 0)
		return u
	}

	byCluster := map[string]*ClusterUsage{}
	var problems []string

	// A root that will not list must not read as an empty archive, or the size
	// ceiling could never fire.
	days, rootErr := a.days()

	for _, day := range days {
		dayDir := a.dayDir(day)
		du := DayUsage{Day: day}

		clusters, err := os.ReadDir(dayDir)
		if err != nil {
			problems = append(problems, day)
			continue
		}
		for _, c := range clusters {
			if !c.IsDir() {
				continue
			}
			dir := filepath.Join(dayDir, c.Name())
			name := readCluster(dir)
			cu := byCluster[name]
			if cu == nil {
				cu = &ClusterUsage{Cluster: name}
				byCluster[name] = cu
			}
			cu.Days++

			err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil // an entry removed by a prune mid-walk is not an error
				}
				if d.IsDir() {
					return nil
				}
				info, err := d.Info()
				if err != nil {
					return nil
				}
				size := info.Size()
				u.Files++
				u.Bytes += size
				du.Bytes += size
				cu.Bytes += size

				switch {
				case d.Name() == eventsFileName:
					u.EventBytes += size
					du.EventBytes += size
					cu.EventBytes += size
				case strings.HasSuffix(d.Name(), ".log"):
					u.LogBytes += size
					du.LogBytes += size
					cu.LogBytes += size
				}
				return nil
			})
			if err != nil {
				problems = append(problems, day+"/"+c.Name())
			}
		}
		u.Days = append(u.Days, du)
	}

	// Files directly in the root count too, so the figure matches `du`.
	if entries, err := os.ReadDir(a.root); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if info, err := e.Info(); err == nil {
				u.Bytes += info.Size()
				u.Files++
			}
		}
	}

	u.Clusters = make([]ClusterUsage, 0, len(byCluster))
	for _, cu := range byCluster {
		u.Clusters = append(u.Clusters, *cu)
	}
	sort.SliceStable(u.Clusters, func(i, j int) bool {
		if u.Clusters[i].Bytes == u.Clusters[j].Bytes {
			return u.Clusters[i].Cluster < u.Clusters[j].Cluster
		}
		return u.Clusters[i].Bytes > u.Clusters[j].Bytes
	})
	switch {
	case rootErr != nil:
		// Nothing was measured, so not even a floor.
		u.Note = "the archive directory itself could not be listed, so nothing below was measured and this is not a figure"
	case len(problems) > 0:
		u.Note = fmt.Sprintf("%d directories could not be read, so this is a floor, not a total", len(problems))
	}

	a.store(u, 0)
	return u
}

// store replaces the cache and resets the counter of writes since the walk.
// The reset races with a write happening during the walk, which is why the
// figure is documented as approximate and not as a total.
func (a *Archive) store(u Usage, pending int64) {
	a.usageMu.Lock()
	a.usage = u
	a.usageSeen = true
	a.usageMu.Unlock()
	// After the figure is visible, so a caller that sees the generation move is
	// guaranteed to read the walk that moved it.
	a.usageGen.Add(1)
	a.pending.Store(pending)
}

// dirBytes measures one directory, for reporting what a prune freed. Errors
// are ignored: the figure is only for a log line.
func (a *Archive) dirBytes(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// humanBytes is textutil.Bytes under a local name.
func humanBytes(n int64) string { return textutil.Bytes(n) }
