package jvm

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/thonicdev/kubetower/textutil"
)

// Reading `jcmd <pid> PerfCounter.print`: the JVM's performance counters,
// for the garbage collectors' counts and times alone.
//
// **Only the counters named here are read; the rest of the output is
// dropped where it is parsed.** PerfCounter.print also prints
// sun.rt.javaCommand (the main class and its arguments), java.property.* and
// the JVM's arguments (sun.rt.jvmArgs, java.rt.vmArgs…), any of which can
// hold a password. So nothing of it is returned but the numbers below and the
// collectors' names, nothing of it is put in a failure sentence, and the test
// checks that no other value leaves the route.
//
// The counters are the same from JDK 8 to 21: sun.gc.collector.<n>.name,
// .invocations and .time (in ticks of sun.os.hrt.frequency), and
// sun.os.hrt.ticks, the ticks since the JVM started. Collector 0 is the young
// generation's for every generational collector; the others collect the old
// generation, or the whole heap.

// The most collectors read; HotSpot has two or three.
const maxCollectors = 8

// gcCollector is one garbage collector's work since the JVM started.
type gcCollector struct {
	Name string `json:"name"`
	// Young is set for collector 0, the young generation's.
	Young       bool  `json:"young"`
	Invocations int64 `json:"invocations"`
	// Seconds is the time it held the JVM, in seconds.
	Seconds float64 `json:"seconds"`
	// Share is Seconds as a percentage of the uptime, one decimal.
	Share float64 `json:"share"`
}

// gcWork is the GC gauge: every collector's count and time, and their sum as
// a share of the uptime.
type gcWork struct {
	Collectors []gcCollector `json:"collectors"`
	// Policy is sun.gc.policy.name: `Copy:MSC`, `GarbageFirst`…
	Policy string `json:"policy,omitempty"`
	// Uptime is the seconds since the JVM started, by its own clock.
	Uptime float64 `json:"uptime"`
	// Share is the time in every collector as a percentage of the uptime,
	// one decimal.
	Share float64 `json:"share"`
}

// The counters read, by their exact names: a name not in this set, or not of
// a collector's three, is never looked at.
const (
	perfFrequency = "sun.os.hrt.frequency"
	perfTicks     = "sun.os.hrt.ticks"
	perfPolicy    = "sun.gc.policy.name"
	perfCollector = "sun.gc.collector."
)

// parseGC reads the GC counters out of PerfCounter.print's body; ok is false
// when the clock or any collector is missing.
func parseGC(body []string) (gcWork, bool) {
	var freq, ticks int64
	policy := ""
	type raw struct {
		name        string
		invocations int64
		time        int64
		seen        int
	}
	byIndex := map[int]*raw{}
	for _, line := range body {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case perfFrequency:
			freq, _ = strconv.ParseInt(value, 10, 64)
			continue
		case perfTicks:
			ticks, _ = strconv.ParseInt(value, 10, 64)
			continue
		case perfPolicy:
			policy = textutil.Clip(strings.Trim(value, `"`), 80)
			continue
		}
		rest, ok := strings.CutPrefix(key, perfCollector)
		if !ok {
			continue
		}
		idx, field, ok := strings.Cut(rest, ".")
		n, err := strconv.Atoi(idx)
		if !ok || err != nil || n < 0 || n >= maxCollectors {
			continue
		}
		c := byIndex[n]
		if c == nil {
			c = &raw{}
			byIndex[n] = c
		}
		switch field {
		case "name":
			c.name = textutil.Clip(strings.Trim(value, `"`), 80)
			c.seen++
		case "invocations":
			c.invocations, _ = strconv.ParseInt(value, 10, 64)
			c.seen++
		case "time":
			c.time, _ = strconv.ParseInt(value, 10, 64)
			c.seen++
		}
	}
	if freq <= 0 || ticks <= 0 || len(byIndex) == 0 {
		return gcWork{}, false
	}
	uptime := float64(ticks) / float64(freq)
	out := gcWork{Policy: policy, Uptime: round(uptime, 1), Collectors: []gcCollector{}}
	indexes := make([]int, 0, len(byIndex))
	for i := range byIndex {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	var total float64
	for _, i := range indexes {
		c := byIndex[i]
		if c.seen < 3 {
			return gcWork{}, false
		}
		secs := float64(c.time) / float64(freq)
		total += secs
		out.Collectors = append(out.Collectors, gcCollector{Name: c.name, Young: i == 0, Invocations: c.invocations,
			Seconds: round(secs, 3), Share: round(100*secs/uptime, 1)})
	}
	out.Share = round(100*total/uptime, 1)
	return out, true
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}
