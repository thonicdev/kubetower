package kubecron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"
)

// hostWithTwoCronJobs is hostWithCollector with a second visible CronJob whose
// calendar the collector cannot answer, and the hidden one it knows about.
func hostWithTwoCronJobs(t *testing.T) (*fakeHost, *fakeCollector) {
	srv, fc := newFakeCollector(t, map[string]string{
		"/api/v1/collector": discoveryDoc,
		"/api/v1/clusters/docker-desktop/cronjobs/ops/nightly/daily": `{"days":90,"daily":[{"day":"2026-10-19","total":4,"succeeded":3,"running":0}]}`,
		"/api/v1/clusters/docker-desktop/cronjobs/ops/nightly/runs": `{"runs":[
		  {"id":"r2","pod_name":"nightly-2","trigger":"scheduled","started_at":"2026-10-20T01:00:00Z","status":"succeeded","exit_code":0,"duration_ms":1000},
		  {"id":"r1","pod_name":"nightly-1","trigger":"scheduled","started_at":"2026-10-19T23:00:00Z","status":"failed","exit_code":3,"duration_ms":5000,"max_cpu_millicores":120},
		  {"id":"r0","pod_name":"nightly-0","trigger":"scheduled","started_at":"2026-10-18T23:00:00Z","status":"succeeded","exit_code":0}],"next_cursor":"x"}`,
		"/api/v1/clusters/docker-desktop/cronjobs/web/backup/runs": `{"runs":[
		  {"id":"b1","pod_name":"backup-1","trigger":"scheduled","started_at":"2026-10-19T12:00:00Z","status":"succeeded","exit_code":0}],"next_cursor":""}`,
		"/api/v1/clusters/docker-desktop/cronjobs/secret-team/hidden/daily": `{"days":90,"daily":[{"day":"2026-10-19","total":5,"succeeded":0,"running":0}]}`,
		"/api/v1/clusters/docker-desktop/cronjobs/secret-team/hidden/runs":  `{"runs":[{"id":"h1","pod_name":"hidden-1","started_at":"2026-10-19T10:00:00Z","status":"failed"}]}`,
	})
	h := &fakeHost{
		objects: map[string][]unstructured.Unstructured{
			"cronjobs": {
				cronJob(t, "web", "backup", "0 12 * * *", testNow.Add(-time.Hour), nil),
				cronJob(t, "ops", "nightly", "0 * * * *", testNow.Add(-time.Hour), nil),
				cronJob(t, "secret-team", "hidden", "0 * * * *", testNow.Add(-time.Hour), nil),
			},
		},
		clients:     fake.NewClientset(collectorService("bearer"), tokenSecret()),
		forwardTo:   strings.TrimPrefix(srv.URL, "http://"),
		hiddenNames: map[string]bool{"secret-team/hidden": true},
	}
	return h, fc
}

func TestTheHeatmapHoldsEveryVisibleCalendarAndSaysWhichCouldNotBeRead(t *testing.T) {
	h, fc := hostWithTwoCronJobs(t)
	rec := serve(t, testExtra().heatmap(h), "/x/docker-desktop/kubecron/daily", nil)
	var out heatmapAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if out.Collector.State != "ok" || out.WindowDays != 90 || out.RunDays != 90 || out.ObservedSince == nil {
		t.Errorf("head %+v", out)
	}
	if len(out.CronJobs) != 1 || out.CronJobs[0].Name != "nightly" || len(out.CronJobs[0].Days) != 1 || out.CronJobs[0].Days[0].Failed != 1 {
		t.Errorf("calendars %+v", out.CronJobs)
	}
	// backup's calendar is not served: it is unread, with why, and never an
	// empty calendar.
	if len(out.Unread) != 1 || out.Unread[0].Name != "backup" || out.Unread[0].Detail == "" {
		t.Errorf("unread %+v", out.Unread)
	}
	if strings.Contains(rec.Body.String(), "hidden") {
		t.Errorf("a CronJob the person may not list leaked: %s", rec.Body)
	}
	if _, asked := fc.queries["/api/v1/clusters/docker-desktop/cronjobs/secret-team/hidden/daily"]; asked {
		t.Error("the collector was asked about a CronJob the person may not list")
	}
	if got := fc.queries["/api/v1/clusters/docker-desktop/cronjobs/ops/nightly/daily"]; got != "days=90" {
		t.Errorf("window asked: %q", got)
	}
	if h.forwards != 1 || h.stopped != 1 {
		t.Errorf("forwards %d, stopped %d", h.forwards, h.stopped)
	}
}

func TestTheHeatmapWithNoCollectorSaysAbsentAndListFailureIsAFailure(t *testing.T) {
	h, _ := hostWithTwoCronJobs(t)
	h.clients = fake.NewClientset()
	rec := serve(t, testExtra().heatmap(h), "/x/docker-desktop/kubecron/daily", nil)
	var out heatmapAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if out.Collector.State != "absent" || len(out.CronJobs) != 0 {
		t.Errorf("%+v", out)
	}
	h.listErr = errors.New("cronjobs.batch is forbidden")
	rec = serve(t, testExtra().heatmap(h), "/x/docker-desktop/kubecron/daily", nil)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), CodeListFailed) {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestADayHoldsThatDaysRunsOfTheNamedVisibleCronJobs(t *testing.T) {
	h, fc := hostWithTwoCronJobs(t)
	req := httptest.NewRequest(http.MethodGet,
		"/x/docker-desktop/kubecron/days/2026-10-19?cronjob=ops/nightly&cronjob=web/backup&cronjob=secret-team/hidden&cronjob=ops/nightly", nil)
	req.SetPathValue("cluster", "docker-desktop")
	req.SetPathValue("day", "2026-10-19")
	rec := httptest.NewRecorder()
	testExtra().day(h).ServeHTTP(rec, req)
	var out dayAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// nightly's run of the 20th and of the 18th are not the 19th's; newest first.
	var ids []string
	for _, r := range out.Runs {
		ids = append(ids, r.ID+"@"+r.Namespace+"/"+r.CronJob)
	}
	if strings.Join(ids, ",") != "r1@ops/nightly,b1@web/backup" {
		t.Errorf("runs %v", ids)
	}
	if out.Runs[0].ExitCode == nil || *out.Runs[0].ExitCode != 3 || out.Runs[0].MaxCPUMillicores == nil {
		t.Errorf("run figures %+v", out.Runs[0])
	}
	// nightly's page reached the 18th, so the 19th is whole: nothing partial.
	if len(out.Partial) != 0 || len(out.Unread) != 0 {
		t.Errorf("partial %+v unread %+v", out.Partial, out.Unread)
	}
	if strings.Contains(rec.Body.String(), "hidden") {
		t.Errorf("a CronJob the person may not list leaked: %s", rec.Body)
	}
	if _, asked := fc.queries["/api/v1/clusters/docker-desktop/cronjobs/secret-team/hidden/runs"]; asked {
		t.Error("the collector was asked about a CronJob the person may not list")
	}
	if got := fc.queries["/api/v1/clusters/docker-desktop/cronjobs/ops/nightly/runs"]; !strings.Contains(got, "before=2026-10-20T00%3A00%3A00Z") {
		t.Errorf("cursor asked: %q", got)
	}
}

func TestADayThatIsNotADayIsRefused(t *testing.T) {
	h, _ := hostWithTwoCronJobs(t)
	rec := serve(t, testExtra().day(h), "/x/docker-desktop/kubecron/days/yesterday", map[string]string{"day": "yesterday"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), CodeBadDay) {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestADayWithNoNameAsksEveryVisibleCronJobAndMarksAPartialOne(t *testing.T) {
	h, _ := hostWithTwoCronJobs(t)
	// The 20th: nightly's page holds r2 and then older runs, so it is whole;
	// backup has nothing that day.
	rec := serve(t, testExtra().day(h), "/x/docker-desktop/kubecron/days/2026-10-20", map[string]string{"day": "2026-10-20"})
	var out dayAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(out.Runs) != 1 || out.Runs[0].ID != "r2" || len(out.Partial) != 0 {
		t.Errorf("%+v", out)
	}
	// The 18th: the page ends on that day with a cursor left, so there may be
	// more of it than one page.
	rec = serve(t, testExtra().day(h), "/x/docker-desktop/kubecron/days/2026-10-18", map[string]string{"day": "2026-10-18"})
	out = dayAnswer{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Partial) != 1 || out.Partial[0].Name != "nightly" {
		t.Errorf("partial %+v", out.Partial)
	}
}

// Past the cap, a CronJob is not asked about and is answered as unread, with
// why; the first ones by namespace and name are read.
func TestPastTheCapACronJobIsUnreadAndNeverAsked(t *testing.T) {
	h, fc := hostWithTwoCronJobs(t)
	k := testExtra()
	k.maxCronJobs = 1
	rec := serve(t, k.heatmap(h), "/x/docker-desktop/kubecron/daily", nil)
	var out heatmapAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(out.CronJobs) != 1 || out.CronJobs[0].Name != "nightly" {
		t.Errorf("read %+v", out.CronJobs)
	}
	if len(out.Unread) != 1 || out.Unread[0].Name != "backup" || !strings.Contains(out.Unread[0].Detail, "first 1 CronJobs") {
		t.Errorf("unread %+v", out.Unread)
	}
	if _, asked := fc.queries["/api/v1/clusters/docker-desktop/cronjobs/web/backup/daily"]; asked {
		t.Error("a CronJob past the cap was asked about")
	}
}

// A collector that hangs is cut off at the page's budget, and the CronJob is
// unread with the reason, rather than holding the request.
func TestACollectorThatHangsIsCutOffAtTheBudget(t *testing.T) {
	h, fc := hostWithTwoCronJobs(t)
	fc.mu.Lock()
	fc.slow = map[string]time.Duration{"/api/v1/clusters/docker-desktop/cronjobs/ops/nightly/daily": 5 * time.Second}
	fc.mu.Unlock()
	k := testExtra()
	k.budget = 200 * time.Millisecond
	started := time.Now()
	rec := serve(t, k.heatmap(h), "/x/docker-desktop/kubecron/daily", nil)
	if took := time.Since(started); took > 3*time.Second {
		t.Errorf("the page waited %s on a hanging collector", took)
	}
	var out heatmapAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(out.CronJobs) != 0 || len(out.Unread) != 2 {
		t.Fatalf("%+v", out)
	}
	if out.Unread[0].Name != "nightly" || !strings.Contains(out.Unread[0].Detail, "deadline exceeded") {
		t.Errorf("unread %+v", out.Unread[0])
	}
}

// Once the budget has run out, a CronJob not yet asked is not asked at all.
func TestOnceTheBudgetIsSpentTheRestIsNotAsked(t *testing.T) {
	refs := make([]CronJobRef, 20)
	for i := range refs {
		refs[i] = CronJobRef{Namespace: "ns", Name: fmt.Sprint(i)}
	}
	var mu sync.Mutex
	asks := 0
	out := eachCronJob(context.Background(), refs, 100, 150*time.Millisecond, func(context.Context, CronJobRef) bool {
		mu.Lock()
		asks++
		mu.Unlock()
		time.Sleep(120 * time.Millisecond)
		return true
	})
	skipped := 0
	for i, a := range out {
		if a.skipped != "" {
			skipped++
			if !strings.Contains(a.skipped, "ran out") {
				t.Errorf("%d: %q", i, a.skipped)
			}
		} else if !a.value {
			t.Errorf("%d: neither asked nor skipped", i)
		}
	}
	// Eight at a time, 120 ms each: the first two rounds start inside 150 ms,
	// the third does not.
	if skipped == 0 || asks+skipped != len(refs) || out[0].skipped != "" {
		t.Errorf("asked %d, skipped %d", asks, skipped)
	}
}

// With no name, `namespace=` narrows the day to one namespace's CronJobs.
func TestADayWithANamespaceAsksOnlyThatNamespace(t *testing.T) {
	h, fc := hostWithTwoCronJobs(t)
	req := httptest.NewRequest(http.MethodGet, "/x/docker-desktop/kubecron/days/2026-10-19?namespace=web", nil)
	req.SetPathValue("cluster", "docker-desktop")
	req.SetPathValue("day", "2026-10-19")
	rec := httptest.NewRecorder()
	testExtra().day(h).ServeHTTP(rec, req)
	var out dayAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(out.Runs) != 1 || out.Runs[0].ID != "b1" {
		t.Errorf("%+v", out.Runs)
	}
	if _, asked := fc.queries["/api/v1/clusters/docker-desktop/cronjobs/ops/nightly/runs"]; asked {
		t.Error("a CronJob of another namespace was asked about")
	}
}

// A CronJob's heatmap asks the collector for the page's window, so the two are
// drawn at one size, never the page's year beside 90 days on a CronJob. Kept
// runs past a year are clamped to the year on both.
func TestACronJobsCalendarAsksForThePagesWindow(t *testing.T) {
	daily := `{"days":371,"daily":[{"day":"2026-10-19","total":4,"succeeded":3,"running":0}]}`
	srv, fc := newFakeCollector(t, map[string]string{
		"/api/v1/collector": strings.Replace(discoveryDoc, `"run_days":90`, `"run_days":400`, 1),
		"/api/v1/clusters/docker-desktop/cronjobs/ops/nightly/daily": daily,
		"/api/v1/clusters/docker-desktop/cronjobs/ops/nightly/runs":  `{"runs":[],"next_cursor":""}`,
	})
	h := &fakeHost{
		objects:   map[string][]unstructured.Unstructured{"cronjobs": {cronJob(t, "ops", "nightly", "0 * * * *", testNow.Add(-time.Hour), nil)}},
		clients:   fake.NewClientset(collectorService("bearer"), tokenSecret()),
		forwardTo: strings.TrimPrefix(srv.URL, "http://"),
	}
	const asked = "/api/v1/clusters/docker-desktop/cronjobs/ops/nightly/daily"

	rec := serve(t, testExtra().heatmap(h), "/x/docker-desktop/kubecron/daily", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	page := fc.queries[asked]
	// Forgotten, so a detail route that stopped asking is caught.
	delete(fc.queries, asked)

	rec = serve(t, testExtra().detail(h), "/x/docker-desktop/kubecron/cronjobs/ops/nightly",
		map[string]string{"namespace": "ops", "name": "nightly"})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := fc.queries[asked]; got != "days=371" || page != got {
		t.Errorf("the page asked %q and the CronJob %q, want days=371 on both", page, got)
	}
}
