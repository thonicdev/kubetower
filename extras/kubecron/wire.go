package kubecron

import (
	"sort"
	"time"
)

// The JSON the page reads. A time that may be unknown is a pointer, omitted
// when zero, and a duration is nullable milliseconds, so "not recorded" and
// "took no time" stay apart. web/app/extras/kubecron/data.ts declares the
// same shapes.

type cronJobWire struct {
	Namespace          string       `json:"namespace"`
	Name               string       `json:"name"`
	Schedule           string       `json:"schedule"`
	TimeZone           string       `json:"timeZone,omitempty"`
	Suspended          bool         `json:"suspended"`
	Verdict            string       `json:"verdict"`
	Unresolved         string       `json:"unresolved,omitempty"`
	DueAt              *time.Time   `json:"dueAt,omitempty"`
	LateMs             *int64       `json:"lateMs,omitempty"`
	NextAt             *time.Time   `json:"nextAt,omitempty"`
	LastScheduleTime   *time.Time   `json:"lastScheduleTime,omitempty"`
	LastSuccessfulTime *time.Time   `json:"lastSuccessfulTime,omitempty"`
	ActiveCount        int          `json:"activeCount"`
	Runs               []retainWire `json:"runs"`
	History            *outcomeWire `json:"history,omitempty"`
}

type retainWire struct {
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	Class      string     `json:"class,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	DurationMs *int64     `json:"durationMs,omitempty"`
}

type outcomeWire struct {
	Runs          int        `json:"runs"`
	Failed        int        `json:"failed"`
	LastFailure   *time.Time `json:"lastFailure,omitempty"`
	AvgDurationMs *int64     `json:"avgDurationMs,omitempty"`
	MaxDurationMs *int64     `json:"maxDurationMs,omitempty"`
	// Failing: the last three finished runs failed.
	Failing bool `json:"failing"`
}

func wireCronJob(c CronJob, now time.Time) cronJobWire {
	out := cronJobWire{
		Namespace: c.Namespace, Name: c.Name, Schedule: c.Schedule, TimeZone: c.TimeZone,
		Suspended: c.Suspended, Verdict: string(c.Verdict), Unresolved: c.Unresolved,
		DueAt: timePtr(c.DueAt), NextAt: timePtr(c.NextAt),
		LastScheduleTime: timePtr(c.LastScheduleTime), LastSuccessfulTime: timePtr(c.LastSuccessfulTime),
		ActiveCount: c.ActiveCount, Runs: make([]retainWire, 0, len(c.Runs)),
	}
	if late := c.Late(now); late > 0 {
		out.LateMs = msPtr(late)
	}
	for _, r := range c.Runs {
		rw := retainWire{Name: r.Name, Status: r.Status, Class: r.Class, StartedAt: timePtr(r.StartedAt)}
		if d := r.Duration(now); d > 0 {
			rw.DurationMs = msPtr(d)
		}
		out.Runs = append(out.Runs, rw)
	}
	if h := c.History; h != nil {
		out.History = &outcomeWire{Runs: h.Runs, Failed: h.Failed, LastFailure: timePtr(h.LastFailure),
			AvgDurationMs: durMs(h.AvgDuration), MaxDurationMs: durMs(h.MaxDuration), Failing: h.Failing}
	}
	return out
}

type dailyWire struct {
	WindowDays    int        `json:"windowDays"`
	ObservedSince *time.Time `json:"observedSince,omitempty"`
	// Days, oldest first; only days with a run, as the collector sends them.
	Days []dayWire `json:"days"`
}

type dayWire struct {
	Day       string `json:"day"`
	Total     int    `json:"total"`
	Succeeded int    `json:"succeeded"`
	Running   int    `json:"running"`
	Failed    int    `json:"failed"`
}

func wireDaily(d DailyWindow) *dailyWire {
	out := &dailyWire{WindowDays: d.WindowDays, ObservedSince: timePtr(d.ObservedSince), Days: make([]dayWire, 0, len(d.Days))}
	for day, v := range d.Days {
		out.Days = append(out.Days, dayWire{Day: day, Total: v.Total, Succeeded: v.Succeeded, Running: v.Running, Failed: v.Failed()})
	}
	sort.Slice(out.Days, func(a, b int) bool { return out.Days[a].Day < out.Days[b].Day })
	return out
}

type runPageWire struct {
	Runs          []runWire  `json:"runs"`
	NextCursor    string     `json:"nextCursor,omitempty"`
	ObservedSince *time.Time `json:"observedSince,omitempty"`
}

type runWire struct {
	ID               string     `json:"id"`
	Pod              string     `json:"pod"`
	Node             string     `json:"node,omitempty"`
	Image            string     `json:"image,omitempty"`
	Trigger          string     `json:"trigger"`
	StartedAt        time.Time  `json:"startedAt"`
	FinishedAt       *time.Time `json:"finishedAt,omitempty"`
	Status           string     `json:"status"`
	ExitCode         *int       `json:"exitCode,omitempty"`
	RetryCount       int        `json:"retryCount"`
	LogSizeBytes     int64      `json:"logSizeBytes"`
	DurationMs       *int64     `json:"durationMs,omitempty"`
	AvgCPUMillicores *int64     `json:"avgCpuMillicores,omitempty"`
	MaxCPUMillicores *int64     `json:"maxCpuMillicores,omitempty"`
	AvgMemoryBytes   *int64     `json:"avgMemoryBytes,omitempty"`
	MaxMemoryBytes   *int64     `json:"maxMemoryBytes,omitempty"`
}

func wireRunRecord(r RunRecord) runWire {
	return runWire{
		ID: r.ID, Pod: r.Pod, Node: r.Node, Image: r.Image, Trigger: r.Trigger,
		StartedAt: r.StartedAt, FinishedAt: timePtr(r.FinishedAt), Status: r.Status,
		ExitCode: r.ExitCode, RetryCount: r.RetryCount, LogSizeBytes: r.LogSizeBytes,
		DurationMs:       durMs(r.Duration),
		AvgCPUMillicores: r.AvgCPUMillicores, MaxCPUMillicores: r.MaxCPUMillicores,
		AvgMemoryBytes: r.AvgMemoryBytes, MaxMemoryBytes: r.MaxMemoryBytes,
	}
}

func wireRunPage(p RunPage) *runPageWire {
	out := &runPageWire{NextCursor: p.NextCursor, ObservedSince: timePtr(p.ObservedSince), Runs: make([]runWire, 0, len(p.Runs))}
	for _, r := range p.Runs {
		out.Runs = append(out.Runs, wireRunRecord(r))
	}
	return out
}

type logWire struct {
	Lines     []logLineWire `json:"lines"`
	SizeBytes int64         `json:"sizeBytes"`
	Expired   bool          `json:"expired"`
	Truncated bool          `json:"truncated"`
	Running   bool          `json:"running"`
}

type logLineWire struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

func wireLog(l RunLog) *logWire {
	out := &logWire{SizeBytes: l.SizeBytes, Expired: l.Expired, Truncated: l.Truncated, Running: l.Running,
		Lines: make([]logLineWire, 0, len(l.Lines))}
	for _, line := range l.Lines {
		out.Lines = append(out.Lines, logLineWire{At: line.At, Text: line.Text})
	}
	return out
}

type samplesWire struct {
	Samples         []sampleWire `json:"samples"`
	IntervalSeconds int          `json:"intervalSeconds"`
	AvgCPU          *int64       `json:"avgCpuMillicores,omitempty"`
	MaxCPU          *int64       `json:"maxCpuMillicores,omitempty"`
	AvgMemory       *int64       `json:"avgMemoryBytes,omitempty"`
	MaxMemory       *int64       `json:"maxMemoryBytes,omitempty"`
	MetricsEnabled  bool         `json:"metricsEnabled"`
}

type sampleWire struct {
	At            time.Time `json:"at"`
	CPUMillicores int64     `json:"cpuMillicores"`
	MemoryBytes   int64     `json:"memoryBytes"`
}

func wireSamples(s SampleSeries) *samplesWire {
	out := &samplesWire{IntervalSeconds: s.IntervalSeconds, AvgCPU: s.AvgCPU, MaxCPU: s.MaxCPU,
		AvgMemory: s.AvgMemory, MaxMemory: s.MaxMemory, MetricsEnabled: s.MetricsEnabled,
		Samples: make([]sampleWire, 0, len(s.Samples))}
	for _, p := range s.Samples {
		out.Samples = append(out.Samples, sampleWire{At: p.At, CPUMillicores: p.CPUMillicores, MemoryBytes: p.MemoryBytes})
	}
	return out
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func msPtr(d time.Duration) *int64 {
	ms := d.Milliseconds()
	return &ms
}

func durMs(d *time.Duration) *int64 {
	if d == nil {
		return nil
	}
	return msPtr(*d)
}
