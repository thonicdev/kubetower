package kubecron

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// One CronJob's history, and one run of it, from the collector only: Kubernetes
// garbage-collects the Jobs.
//
// # Four absences that are not the same absence
//
//   - A day before ObservedSince is a day nobody was recording, not a day the
//     job did not run.
//   - A log with no lines and a non-zero size aged out of the log retention; it
//     is not a run that printed nothing.
//   - A sample series with no points and a summary lost its raw points and kept
//     its peaks.
//   - A sample series with neither is a cluster that records no samples, or a
//     run that finished inside one sampling interval; MetricsEnabled tells them
//     apart, and the second is the common case.

// DefaultHeatmapDays is the calendar's window, KubeCron's own.
const DefaultHeatmapDays = 90

// DefaultRunPage is one page of history: KubeCron's own page size rather than
// the API's 100, because this is one panel of a page.
const DefaultRunPage = 50

// MaxLogLines bounds the log panel. The API's default is uncapped, and the
// collector answers a limit with the last N lines: a job that failed says why
// at the bottom.
const MaxLogLines = 1000

// Day is one square of the calendar.
type Day struct {
	Total, Succeeded, Running int
}

// Failed subtracts Running as well as Succeeded, or every in-flight run would
// paint today red.
func (d Day) Failed() int {
	if n := d.Total - d.Succeeded - d.Running; n > 0 {
		return n
	}
	return 0
}

// DailyWindow is a CronJob's calendar.
type DailyWindow struct {
	// Days is keyed by YYYY-MM-DD, and only days with a run appear.
	Days map[string]Day
	// WindowDays is the window the collector applied, which it clamps.
	WindowDays    int
	ObservedSince time.Time
}

// Daily reads a CronJob's calendar.
func (c *Collector) Daily(ctx context.Context, namespace, name string, days int) (DailyWindow, Availability) {
	if days <= 0 {
		days = DefaultHeatmapDays
	}
	var doc struct {
		Days  int `json:"days"`
		Daily []struct {
			Day       string `json:"day"`
			Total     int    `json:"total"`
			Succeeded int    `json:"succeeded"`
			Running   int    `json:"running"`
		} `json:"daily"`
		ObservedSince *time.Time `json:"observed_since"`
	}
	path := fmt.Sprintf("/clusters/%s/cronjobs/%s/%s/daily",
		url.PathEscape(c.ClusterID), url.PathEscape(namespace), url.PathEscape(name))
	decodeFailed, err := c.read(ctx, path, url.Values{"days": {strconv.Itoa(days)}}, &doc)
	switch {
	case decodeFailed:
		return DailyWindow{}, Availability{Detail: fmt.Sprintf("the collector at %s answered the calendar of %s/%s with something unreadable", c.Where(), namespace, name)}
	case err != nil:
		return DailyWindow{}, Availability{Detail: fmt.Sprintf("the collector at %s could not be asked for the calendar of %s/%s: %s", c.Where(), namespace, name, shortError(err))}
	}
	window := DailyWindow{Days: make(map[string]Day, len(doc.Daily)), WindowDays: doc.Days, ObservedSince: c.ObservedSince}
	if doc.ObservedSince != nil {
		window.ObservedSince = *doc.ObservedSince
	}
	if window.WindowDays <= 0 {
		window.WindowDays = days
	}
	for _, d := range doc.Daily {
		if d.Day != "" {
			window.Days[d.Day] = Day{Total: d.Total, Succeeded: d.Succeeded, Running: d.Running}
		}
	}
	return window, Availability{OK: true}
}

// RunRecord is one execution as the collector recorded it.
type RunRecord struct {
	// ID addresses the run everywhere else in the contract, on its own.
	ID  string
	Pod string
	// Node and Image are often absent.
	Node, Image string
	// Trigger is "scheduled" or "manual"; a manual run proves nothing about
	// whether the schedule fired.
	Trigger    string
	StartedAt  time.Time
	FinishedAt time.Time
	// Status is "running", "succeeded" or "failed".
	Status string
	// ExitCode is nil while running and when never seen. Zero is a real code.
	ExitCode     *int
	RetryCount   int
	LogSizeBytes int64
	// Duration is the collector's own figure, nil while running.
	Duration                           *time.Duration
	AvgCPUMillicores, MaxCPUMillicores *int64
	AvgMemoryBytes, MaxMemoryBytes     *int64
}

// RunPage is one page of run history.
type RunPage struct {
	Runs []RunRecord
	// NextCursor is what to hand back as `before`; empty on the last page.
	NextCursor    string
	ObservedSince time.Time
}

// Runs reads one page of a CronJob's history, newest first. `before` goes
// through url.Values, never into the path: see getter.
func (c *Collector) Runs(ctx context.Context, namespace, name string, limit int, before string) (RunPage, Availability) {
	if limit <= 0 {
		limit = DefaultRunPage
	}
	var doc struct {
		Runs          []wireRun  `json:"runs"`
		NextCursor    string     `json:"next_cursor"`
		ObservedSince *time.Time `json:"observed_since"`
	}
	path := fmt.Sprintf("/clusters/%s/cronjobs/%s/%s/runs",
		url.PathEscape(c.ClusterID), url.PathEscape(namespace), url.PathEscape(name))
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if before != "" {
		query.Set("before", before)
	}
	decodeFailed, err := c.read(ctx, path, query, &doc)
	switch {
	case decodeFailed:
		return RunPage{}, Availability{Detail: fmt.Sprintf("the collector at %s answered the run history of %s/%s with something unreadable", c.Where(), namespace, name)}
	case err != nil:
		return RunPage{}, Availability{Detail: fmt.Sprintf("the collector at %s could not be asked for the run history of %s/%s: %s", c.Where(), namespace, name, shortError(err))}
	}
	page := RunPage{NextCursor: doc.NextCursor, ObservedSince: c.ObservedSince}
	if doc.ObservedSince != nil {
		page.ObservedSince = *doc.ObservedSince
	}
	for _, r := range doc.Runs {
		page.Runs = append(page.Runs, r.record())
	}
	return page, Availability{OK: true}
}

// wireRun is one run as it arrives, shared by the two endpoints that return
// it.
type wireRun struct {
	ID               string     `json:"id"`
	PodName          string     `json:"pod_name"`
	NodeName         *string    `json:"node_name"`
	ContainerImage   *string    `json:"container_image"`
	Trigger          string     `json:"trigger"`
	StartedAt        time.Time  `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at"`
	Status           string     `json:"status"`
	ExitCode         *int       `json:"exit_code"`
	RetryCount       int        `json:"retry_count"`
	LogSizeBytes     int64      `json:"log_size_bytes"`
	DurationMs       *int64     `json:"duration_ms"`
	AvgCPUMillicores *int64     `json:"avg_cpu_millicores"`
	MaxCPUMillicores *int64     `json:"max_cpu_millicores"`
	AvgMemoryBytes   *int64     `json:"avg_memory_bytes"`
	MaxMemoryBytes   *int64     `json:"max_memory_bytes"`
}

func (r wireRun) record() RunRecord {
	rec := RunRecord{
		ID: r.ID, Pod: r.PodName, Trigger: r.Trigger, StartedAt: r.StartedAt, Status: r.Status,
		ExitCode: r.ExitCode, RetryCount: r.RetryCount, LogSizeBytes: r.LogSizeBytes,
		Duration:         millis(r.DurationMs),
		AvgCPUMillicores: r.AvgCPUMillicores, MaxCPUMillicores: r.MaxCPUMillicores,
		AvgMemoryBytes: r.AvgMemoryBytes, MaxMemoryBytes: r.MaxMemoryBytes,
	}
	if r.NodeName != nil {
		rec.Node = *r.NodeName
	}
	if r.ContainerImage != nil {
		rec.Image = *r.ContainerImage
	}
	if r.FinishedAt != nil {
		rec.FinishedAt = *r.FinishedAt
	}
	return rec
}

// Run reads one execution, with the CronJob it belongs to, so a panel opened
// from a link can name its subject and the page can check the person may see
// that CronJob before showing anything of it.
//
// found is false when the collector does not hold this run (a link older than
// the run retention), which is a different sentence from a collector that
// could not be asked.
func (c *Collector) Run(ctx context.Context, runID string) (rec RunRecord, namespace, name string, found bool, avail Availability) {
	var doc struct {
		Run     *wireRun `json:"run"`
		CronJob struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
		} `json:"cronjob"`
	}
	decodeFailed, err := c.read(ctx, "/runs/"+url.PathEscape(runID), nil, &doc)
	switch {
	case decodeFailed:
		return RunRecord{}, "", "", false, Availability{Detail: fmt.Sprintf("the collector at %s answered with something unreadable", c.Where())}
	case notFound(err):
		return RunRecord{}, "", "", false, Availability{OK: true, Detail: fmt.Sprintf("the collector at %s no longer holds this run", c.Where())}
	case err != nil:
		return RunRecord{}, "", "", false, Availability{Detail: fmt.Sprintf("the collector at %s could not be asked about this run: %s", c.Where(), shortError(err))}
	case doc.Run == nil:
		return RunRecord{}, "", "", false, Availability{OK: true, Detail: fmt.Sprintf("the collector at %s no longer holds this run", c.Where())}
	}
	return doc.Run.record(), doc.CronJob.Namespace, doc.CronJob.Name, true, Availability{OK: true}
}

// LogLine is one line, stamped with when the collector received it.
type LogLine struct {
	At   time.Time
	Text string
}

// RunLog is what a run printed, as much as is still held.
type RunLog struct {
	// Lines, oldest first.
	Lines []LogLine
	// SizeBytes survives the body: the only thing separating a silent run from
	// an expired one.
	SizeBytes int64
	// Expired is the collector's own reading: no lines, a non-zero size.
	Expired bool
	// Truncated: the limit was reached, older lines exist above.
	Truncated bool
	// Running: the run is in flight, so this is not all of it yet.
	Running bool
}

// Log reads what one run printed.
func (c *Collector) Log(ctx context.Context, runID string, limit int) (RunLog, Availability) {
	if limit <= 0 {
		limit = MaxLogLines
	}
	var doc struct {
		Lines []struct {
			Ts   time.Time `json:"ts"`
			Line string    `json:"line"`
		} `json:"lines"`
		LogSizeBytes int64 `json:"log_size_bytes"`
		Expired      bool  `json:"expired"`
		Truncated    bool  `json:"truncated"`
		Running      bool  `json:"running"`
	}
	decodeFailed, err := c.read(ctx, "/runs/"+url.PathEscape(runID)+"/logs", url.Values{"limit": {strconv.Itoa(limit)}}, &doc)
	switch {
	case decodeFailed:
		return RunLog{}, Availability{Detail: fmt.Sprintf("the collector at %s answered this run's log with something unreadable", c.Where())}
	case err != nil:
		return RunLog{}, Availability{Detail: fmt.Sprintf("the collector at %s could not be asked for this run's log: %s", c.Where(), shortError(err))}
	}
	log := RunLog{SizeBytes: doc.LogSizeBytes, Expired: doc.Expired, Truncated: doc.Truncated, Running: doc.Running}
	for _, l := range doc.Lines {
		log.Lines = append(log.Lines, LogLine{At: l.Ts, Text: l.Line})
	}
	return log, Availability{OK: true}
}

// Sample is one reading of what a run was using.
type Sample struct {
	At            time.Time
	CPUMillicores int64
	MemoryBytes   int64
}

// SampleSeries is a run's resource usage, with the summary that outlives it.
type SampleSeries struct {
	Samples []Sample
	// IntervalSeconds is the cadence now, which makes "shorter than one
	// interval" a sentence with a number in it.
	IntervalSeconds      int
	AvgCPU, MaxCPU       *int64
	AvgMemory, MaxMemory *int64
	MetricsEnabled       bool
}

// Samples reads one run's resource series.
func (c *Collector) Samples(ctx context.Context, runID string) (SampleSeries, Availability) {
	var doc struct {
		IntervalSeconds int `json:"interval_seconds"`
		Samples         []struct {
			SampledAt     time.Time `json:"sampled_at"`
			CPUMillicores int64     `json:"cpu_millicores"`
			MemoryBytes   int64     `json:"memory_bytes"`
		} `json:"samples"`
		Summary struct {
			AvgCPUMillicores *int64 `json:"avg_cpu_millicores"`
			MaxCPUMillicores *int64 `json:"max_cpu_millicores"`
			AvgMemoryBytes   *int64 `json:"avg_memory_bytes"`
			MaxMemoryBytes   *int64 `json:"max_memory_bytes"`
		} `json:"summary"`
	}
	decodeFailed, err := c.read(ctx, "/runs/"+url.PathEscape(runID)+"/samples", nil, &doc)
	switch {
	case decodeFailed:
		return SampleSeries{}, Availability{Detail: fmt.Sprintf("the collector at %s answered this run's samples with something unreadable", c.Where())}
	case err != nil:
		return SampleSeries{}, Availability{Detail: fmt.Sprintf("the collector at %s could not be asked for this run's samples: %s", c.Where(), shortError(err))}
	}
	series := SampleSeries{
		IntervalSeconds: doc.IntervalSeconds,
		AvgCPU:          doc.Summary.AvgCPUMillicores, MaxCPU: doc.Summary.MaxCPUMillicores,
		AvgMemory: doc.Summary.AvgMemoryBytes, MaxMemory: doc.Summary.MaxMemoryBytes,
		MetricsEnabled: c.MetricsEnabled,
	}
	for _, s := range doc.Samples {
		series.Samples = append(series.Samples, Sample{At: s.SampledAt, CPUMillicores: s.CPUMillicores, MemoryBytes: s.MemoryBytes})
	}
	return series, Availability{OK: true}
}

// parseDay reads a calendar day, YYYY-MM-DD, as the calendar draws it: a UTC
// day. False for anything else, which is then no filter at all.
func parseDay(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	day, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return time.Time{}, false
	}
	return day.UTC(), true
}

// onDay keeps a page's runs that started on day. The page is newest first, so
// a run older than the day means the day is read to its start, and the cursor
// is dropped: "Older" would leave the day.
func onDay(page RunPage, day time.Time) RunPage {
	end := day.AddDate(0, 0, 1)
	out := RunPage{NextCursor: page.NextCursor, ObservedSince: page.ObservedSince}
	for _, r := range page.Runs {
		if r.StartedAt.Before(day) {
			out.NextCursor = ""
			break
		}
		if r.StartedAt.Before(end) {
			out.Runs = append(out.Runs, r)
		}
	}
	return out
}
