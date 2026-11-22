package kubecron

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/safego"
)

// The page's heatmap: every CronJob's calendar at once, and one day's runs
// across them.
//
// The collector's API is per CronJob only, so both routes ask it once per
// CronJob, through one port-forward, a few at a time. Host.List, for the
// person, decides which CronJobs: one it does not return is never asked
// about, even when the page names it.

// HeatmapMaxDays bounds the page's window: fifty-three weeks, GitHub's year.
const HeatmapMaxDays = 371

// heatmapDays is the window every heatmap asks the collector for, the
// page's and a CronJob's alike, so both draw at the same size.
func heatmapDays(c *Collector) int {
	if c.RunDays <= 0 {
		return DefaultHeatmapDays
	}
	return min(c.RunDays, HeatmapMaxDays)
}

// DayRunPage is how many of one CronJob's runs a day is read to: the API's
// maximum. A CronJob firing every minute runs 1 440 times a day.
const DayRunPage = 100

// heatmapWorkers is how many CronJobs are asked about at once, so as not to
// queue hundreds of requests on one tunnel.
const heatmapWorkers = 8

// HeatmapMaxCronJobs and HeatmapBudget bound one page load: past either, a
// CronJob is not asked about and is answered as unread, with why. A hung
// collector holds the request and its port-forward for 10 s a call.
const (
	HeatmapMaxCronJobs = 500
	HeatmapBudget      = 20 * time.Second
)

type heatmapAnswer struct {
	Collector collectorState `json:"collector"`
	// Complete and Note are the CronJob list's: an incomplete list is a
	// heatmap of the CronJobs that were read, and says so.
	Complete bool   `json:"complete"`
	Note     string `json:"note,omitempty"`
	// WindowDays is the window asked for, and RunDays how far back the
	// collector keeps run records: a day older than that is not recorded,
	// which is not a day without runs.
	WindowDays    int        `json:"windowDays"`
	RunDays       int        `json:"runDays,omitempty"`
	ObservedSince *time.Time `json:"observedSince,omitempty"`
	// CronJobs are every visible CronJob whose calendar was read, sorted, with
	// the days it ran (only those). A CronJob with no run in the window is
	// present with no days: it was asked and answered.
	CronJobs []cronDailyWire `json:"cronJobs"`
	// Unread are the CronJobs whose calendar could not be read, each with why.
	Unread []unreadWire `json:"unread"`
}

type cronDailyWire struct {
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Days      []dayWire `json:"days"`
}

type unreadWire struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Detail    string `json:"detail"`
}

// heatmap is GET /daily: the calendar of every CronJob the person may list.
func (k KubeCron) heatmap(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		page, err := h.List(r, cluster, extension.ListQuery{GVR: cronJobsGVR})
		if err != nil {
			writeListFailure(w, cluster, err)
			return
		}
		out := heatmapAnswer{Complete: page.Complete, Note: page.Note, CronJobs: []cronDailyWire{}, Unread: []unreadWire{}}
		if len(page.Items) == 0 {
			// No calendar to draw, so no tunnel is opened.
			out.Collector = k.locate(r, h, cluster).collectorState
			writeJSON(w, http.StatusOK, out)
			return
		}

		c, state, stop := k.openSession(r, h, cluster)
		defer stop()
		out.Collector = state
		if c == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}
		out.WindowDays, out.RunDays, out.ObservedSince = heatmapDays(c), c.RunDays, timePtr(c.ObservedSince)

		refs := make([]CronJobRef, 0, len(page.Items))
		for _, item := range page.Items {
			refs = append(refs, CronJobRef{Namespace: item.GetNamespace(), Name: item.GetName()})
		}
		sortRefs(refs)
		type result struct {
			window DailyWindow
			avail  Availability
		}
		results := eachCronJob(r.Context(), refs, k.maxCronJobs, k.budget, func(ctx context.Context, ref CronJobRef) result {
			window, avail := c.Daily(ctx, ref.Namespace, ref.Name, out.WindowDays)
			return result{window, avail}
		})
		for i, ref := range refs {
			res := results[i]
			if res.skipped != "" || !res.value.avail.OK {
				out.Unread = append(out.Unread, unreadWire{Namespace: ref.Namespace, Name: ref.Name, Detail: res.why(res.value.avail.Detail)})
				continue
			}
			out.CronJobs = append(out.CronJobs, cronDailyWire{Namespace: ref.Namespace, Name: ref.Name, Days: wireDaily(res.value.window).Days})
		}
		sort.Slice(out.CronJobs, func(a, b int) bool {
			return out.CronJobs[a].Namespace+"/"+out.CronJobs[a].Name < out.CronJobs[b].Namespace+"/"+out.CronJobs[b].Name
		})
		writeJSON(w, http.StatusOK, out)
	})
}

type dayAnswer struct {
	Day       string         `json:"day"`
	Collector collectorState `json:"collector"`
	// Runs are that day's runs of the CronJobs asked about, newest first.
	Runs []dayRunWire `json:"runs"`
	// Partial names the CronJobs that ran more that day than one page of
	// history holds: only their newest runs are here.
	Partial []CronJobRef `json:"partial"`
	Unread  []unreadWire `json:"unread"`
}

type dayRunWire struct {
	runWire
	Namespace string `json:"namespace"`
	CronJob   string `json:"cronJob"`
}

// CodeBadDay: the day is not a YYYY-MM-DD.
const CodeBadDay = "bad_day"

// day is GET /days/{day}: one day's runs, across the CronJobs named by
// `cronjob=namespace/name` (repeated), or across every visible one when none
// is named, optionally of one namespace with `namespace=`. The page names only
// a few, since an ingress refuses a very long query string (414).
func (k KubeCron) day(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		day, ok := parseDay(r.PathValue("day"))
		if !ok {
			writeProblem(w, http.StatusBadRequest, CodeBadDay, "A day is written YYYY-MM-DD.", nil)
			return
		}
		page, err := h.List(r, cluster, extension.ListQuery{GVR: cronJobsGVR})
		if err != nil {
			writeListFailure(w, cluster, err)
			return
		}
		visible := make(map[CronJobRef]bool, len(page.Items))
		for _, item := range page.Items {
			visible[CronJobRef{Namespace: item.GetNamespace(), Name: item.GetName()}] = true
		}
		var refs []CronJobRef
		if named := r.URL.Query()["cronjob"]; len(named) > 0 {
			seen := map[CronJobRef]bool{}
			for _, n := range named {
				ns, name, found := strings.Cut(n, "/")
				ref := CronJobRef{Namespace: ns, Name: name}
				if found && visible[ref] && !seen[ref] {
					seen[ref] = true
					refs = append(refs, ref)
				}
			}
		} else {
			only := r.URL.Query().Get("namespace")
			for ref := range visible {
				if only == "" || ref.Namespace == only {
					refs = append(refs, ref)
				}
			}
		}
		sortRefs(refs)

		out := dayAnswer{Day: day.Format("2006-01-02"), Runs: []dayRunWire{}, Partial: []CronJobRef{}, Unread: []unreadWire{}}
		if len(refs) == 0 {
			// No CronJob to read the day of, so no tunnel is opened.
			out.Collector = k.locate(r, h, cluster).collectorState
			writeJSON(w, http.StatusOK, out)
			return
		}
		c, state, stop := k.openSession(r, h, cluster)
		defer stop()
		out.Collector = state
		if c == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}
		// The collector's cursor is a start time: the page that starts at the
		// next midnight holds the day's runs, newest first (the detail route's
		// day filter).
		before := day.AddDate(0, 0, 1).Format(time.RFC3339Nano)
		type result struct {
			page  RunPage
			avail Availability
		}
		results := eachCronJob(r.Context(), refs, k.maxCronJobs, k.budget, func(ctx context.Context, ref CronJobRef) result {
			p, avail := c.Runs(ctx, ref.Namespace, ref.Name, DayRunPage, before)
			return result{p, avail}
		})
		for i, ref := range refs {
			res := results[i]
			if res.skipped != "" || !res.value.avail.OK {
				out.Unread = append(out.Unread, unreadWire{Namespace: ref.Namespace, Name: ref.Name, Detail: res.why(res.value.avail.Detail)})
				continue
			}
			kept := onDay(res.value.page, day)
			if kept.NextCursor != "" {
				out.Partial = append(out.Partial, ref)
			}
			for _, rec := range kept.Runs {
				out.Runs = append(out.Runs, dayRunWire{runWire: wireRunRecord(rec), Namespace: ref.Namespace, CronJob: ref.Name})
			}
		}
		sort.SliceStable(out.Runs, func(a, b int) bool { return out.Runs[a].StartedAt.After(out.Runs[b].StartedAt) })
		writeJSON(w, http.StatusOK, out)
	})
}

func sortRefs(refs []CronJobRef) {
	sort.Slice(refs, func(a, b int) bool { return refs[a].Namespace+"/"+refs[a].Name < refs[b].Namespace+"/"+refs[b].Name })
}

// asked is one CronJob's answer, or why it was not asked.
type asked[T any] struct {
	value   T
	skipped string
}

// why is the sentence for an unread CronJob: the skip, or the failed ask's.
func (a asked[T]) why(failed string) string {
	if a.skipped != "" {
		return a.skipped
	}
	return failed
}

// eachCronJob asks about every CronJob, heatmapWorkers at a time, and keeps
// the answers in the order of refs. Past maxRefs, and once budget has run out,
// a CronJob is not asked and says so.
func eachCronJob[T any](ctx context.Context, refs []CronJobRef, maxRefs int, budget time.Duration,
	ask func(context.Context, CronJobRef) T) []asked[T] {
	out := make([]asked[T], len(refs))
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(heatmapWorkers, len(refs)) {
		wg.Add(1)
		// A panic reading one CronJob is that CronJob's sentence; the
		// worker goes on.
		safego.Go("kubecron: a heatmap worker", func() error {
			for i := range next {
				if ctx.Err() != nil {
					out[i].skipped = fmt.Sprintf("not asked: one page load reads for %s at most, and that ran out first", budget)
					continue
				}
				if err := safego.Call("kubecron: reading one CronJob", func() error {
					out[i].value = ask(ctx, refs[i])
					return nil
				}); err != nil {
					out[i].skipped = "not read: " + err.Error()
				}
			}
			return nil
		}, func(error) { wg.Done() })
	}
	for i := range refs {
		if i >= maxRefs {
			out[i].skipped = fmt.Sprintf("not asked: one page load reads the first %d CronJobs, by namespace and name", maxRefs)
			continue
		}
		next <- i
	}
	close(next)
	wg.Wait()
	return out
}
