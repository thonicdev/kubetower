package overview

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// RunTotal is what a counter counted over a finished pod's run: its increase
// between the first and last samples Prometheus took of the run, or its one
// value where it took only one.
type RunTotal struct {
	Value float64
	// Samples is the most samples any series under the figure held: 1 means
	// Value is the counter's last value, not an increase.
	Samples int
}

// runGrace is how long after the run's end a sample still counts as the run's.
// The scrape after the last exit reports the counters as they stood at that
// exit, and for a short run it is often the only sample; nothing moves the
// counters of a pod whose containers have all exited.
const runGrace = time.Minute

// CountRun sets the run's total on a finished pod's empty network lines: a run
// too short or too old for the chart still moved a number of bytes. It touches
// only NetIn and NetOut where they are MissingEmpty, as ExplainRun does; a
// query that fails or matches nothing leaves RunTotal nil rather than guessing.
//
// The samples are read raw and the increase taken here: PromQL's increase()
// extrapolates to its range's edges, inventing most of a short run's figure,
// and answers nothing for a single sample.
//
// A total read from fewer than two samples also drops the period ExplainRun
// named: that period cannot draw a rate from one sample either.
func (h ObjectHistory) CountRun(ctx context.Context, metrics Metrics, cluster, namespace, pod string, run *Run) ObjectHistory {
	if run == nil || metrics == nil {
		return h
	}
	selector := fmt.Sprintf(`namespace=%s,pod=%s`, promQuote(namespace), promQuote(pod))
	for _, line := range []struct {
		series *History
		metric string
	}{
		{&h.NetIn, metricNetIn},
		{&h.NetOut, metricNetOut},
	} {
		if line.series.Missing != MissingEmpty {
			continue
		}
		raw, err := metrics.Query(ctx, cluster, runQuery(line.metric, selector, *run))
		if err != nil {
			continue
		}
		if total, ok := parseRunTotal(raw); ok {
			line.series.RunTotal = &total
			if total.Samples < 2 {
				// A rate needs two samples: no period draws this run.
				line.series.Period = ""
			}
		}
	}
	return h
}

// runQuery asks for every raw sample of one counter from the run's start to
// runGrace after its end: a range selector evaluated at that end (`@`), which
// returns the samples themselves rather than a value computed from them.
func runQuery(metric, selector string, run Run) string {
	end := run.End.Add(runGrace)
	span := int64(math.Ceil(end.Sub(run.Start).Seconds()))
	if span < 1 {
		span = 1
	}
	return fmt.Sprintf(`%s{%s}[%ds] @ %d`, metric, selector, span, end.Unix())
}

// promMatrix is a range selector's answer: each series with its labels and its
// raw samples.
type promMatrix struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Values [][]any           `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// parseRunTotal reduces a range selector's answer to the run's total, as the
// panel's own queries reduce theirs: one series per interface, summed, and
// series that differ only by the labels of whoever scraped them averaged
// first (dedup, scrapeLabels). false where no series holds a sample.
func parseRunTotal(raw []byte) (RunTotal, bool) {
	var resp promMatrix
	if err := json.Unmarshal(raw, &resp); err != nil || resp.Status != "success" {
		return RunTotal{}, false
	}
	scrapers := map[string]bool{"__name__": true}
	for _, label := range strings.Split(scrapeLabels, ",") {
		scrapers[strings.TrimSpace(label)] = true
	}
	type group struct {
		sum   float64
		count int
	}
	groups := map[string]*group{}
	samples := 0
	for _, series := range resp.Data.Result {
		values := make([]float64, 0, len(series.Values))
		for _, v := range series.Values {
			if len(v) < 2 {
				continue
			}
			if value, ok := sampleValue(v[1]); ok {
				values = append(values, value)
			}
		}
		if len(values) == 0 {
			continue
		}
		samples = max(samples, len(values))
		key := identity(series.Metric, scrapers)
		if groups[key] == nil {
			groups[key] = &group{}
		}
		groups[key].sum += counted(values)
		groups[key].count++
	}
	if len(groups) == 0 {
		return RunTotal{}, false
	}
	total := 0.0
	for _, g := range groups {
		total += g.sum / float64(g.count)
	}
	return RunTotal{Value: total, Samples: samples}, true
}

// counted is what one series of a counter counted: its one value where there
// is one sample, and otherwise the sum of its rises, a fall being a reset after
// which the counter counted up from zero to its new value.
func counted(values []float64) float64 {
	if len(values) == 1 {
		return values[0]
	}
	total := 0.0
	for i := 1; i < len(values); i++ {
		rise := values[i] - values[i-1]
		if rise < 0 {
			rise = values[i]
		}
		total += rise
	}
	return total
}

// identity is a series' labels without the ones naming who scraped it, as a
// stable key.
func identity(labels map[string]string, without map[string]bool) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		if !without[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		b.WriteString(key + "=" + labels[key] + "\x00")
	}
	return b.String()
}
