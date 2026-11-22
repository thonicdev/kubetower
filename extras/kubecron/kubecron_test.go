package kubecron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/schedule"
)

// --- the judgment -------------------------------------------------------------

func TestTheVerdictsComeFromTheCronJobsOwnStatus(t *testing.T) {
	items := []unstructured.Unstructured{
		// Every 5 minutes, last fired 2 minutes ago: on time.
		cronJob(t, "ops", "fresh", "*/5 * * * *", testNow.Add(-2*time.Minute), nil),
		// Hourly, last fired 3 hours ago: missed.
		cronJob(t, "ops", "stale", "0 * * * *", testNow.Add(-3*time.Hour), nil),
		// A zone that does not exist: unknown, with the zone named.
		cronJob(t, "ops", "badzone", "0 * * * *", testNow.Add(-time.Hour), map[string]any{"timeZone": "Mars/Olympus"}),
		cronJob(t, "ops", "paused", "0 * * * *", testNow.Add(-24*time.Hour), map[string]any{"suspend": true}),
	}
	report := judgeAll(listed{Items: items, Complete: true}, nil, nil, testNow)

	got := map[string]CronJob{}
	for _, cj := range report.CronJobs {
		got[cj.Name] = cj
	}
	for name, want := range map[string]schedule.Verdict{
		"stale": schedule.VerdictMissed, "badzone": schedule.VerdictUnknown, "paused": schedule.VerdictSuspended,
	} {
		if got[name].Verdict != want {
			t.Errorf("%s: verdict %q, want %q", name, got[name].Verdict, want)
		}
	}
	if v := got["fresh"].Verdict; v == schedule.VerdictMissed || v == schedule.VerdictUnknown {
		t.Errorf("fresh: verdict %q", v)
	}
	if !strings.Contains(got["badzone"].Unresolved, "Mars/Olympus") {
		t.Errorf("an unknown verdict does not name the zone: %q", got["badzone"].Unresolved)
	}
	if report.Missed != 1 || report.Unknown != 1 || report.Suspended != 1 || report.Total != 4 {
		t.Errorf("counts: %+v", report)
	}
	// Worst first: missed, then unknown, then the rest, suspended last.
	order := []string{}
	for _, cj := range report.CronJobs {
		order = append(order, cj.Name)
	}
	if strings.Join(order, ",") != "stale,badzone,fresh,paused" {
		t.Errorf("order %v", order)
	}
}

// A failed Jobs read costs the recent runs and never the verdicts.
func TestAJobsReadThatFailsKeepsTheVerdicts(t *testing.T) {
	items := []unstructured.Unstructured{cronJob(t, "ops", "stale", "0 * * * *", testNow.Add(-3*time.Hour), nil)}
	report := judgeAll(listed{Items: items, Complete: true}, nil, errors.New("jobs.batch is forbidden"), testNow)
	if report.CronJobs[0].Verdict != schedule.VerdictMissed {
		t.Errorf("verdict %q", report.CronJobs[0].Verdict)
	}
	if report.Retained.OK || !strings.Contains(report.Retained.Detail, "verdicts are unaffected") {
		t.Errorf("retained: %+v", report.Retained)
	}
}

func TestRetainedRunsAreIndexedUnderTheirCronJobAndManualOnesAreNot(t *testing.T) {
	jobs := []unstructured.Unstructured{
		job(t, "ops", "nightly-1", "nightly", testNow.Add(-2*time.Hour), testNow.Add(-2*time.Hour+40*time.Second), "Complete"),
		job(t, "ops", "nightly-2", "nightly", testNow.Add(-time.Hour), testNow.Add(-time.Hour+5*time.Second), "Failed"),
		job(t, "ops", "by-hand", "", testNow.Add(-time.Hour), testNow, "Complete"),
	}
	runs := indexRuns(jobs)
	got := runs["ops/nightly"]
	if len(got) != 2 || got[0].Name != "nightly-2" {
		t.Fatalf("runs %+v", got)
	}
	// A failure is dated by its condition: completionTime is set on success only.
	if got[0].Status != "Failed" || got[0].Class != "bad" || got[0].Duration(testNow) != 5*time.Second {
		t.Errorf("failed run: %+v, duration %v", got[0], got[0].Duration(testNow))
	}
	if got[1].Status != "Complete" || got[1].Class != "ok" {
		t.Errorf("complete run: %+v", got[1])
	}
	if len(runs) != 1 {
		t.Errorf("a manual Job was indexed under a schedule: %v", runs)
	}
}

// --- finding the collector -----------------------------------------------------

// nothing is the defaults: no variable, no setting.
var nothing = resolve(collectorConfig{}, "", "")

func TestNoCollectorIsTheOrdinaryCaseAndSaysSo(t *testing.T) {
	f := discover(context.Background(), viaClientset(fake.NewClientset()), nothing)
	if f.Ref != nil || f.State != "absent" || !strings.Contains(f.Detail, "no KubeCron collector") {
		t.Errorf("%+v", f)
	}
	if c := lastCheck(f.Checks); c.Step != stepCollector || c.State != checkMissing || c.Source != fromDefault {
		t.Errorf("check %+v", c)
	}
}

// secretsRead is every Secret the fake clientset was asked for, and whether
// any Service was listed.
func secretsRead(clients *fake.Clientset) (secrets []string, listedServices bool) {
	for _, a := range clients.Actions() {
		switch {
		case a.GetVerb() == "get" && a.GetResource().Resource == "secrets":
			secrets = append(secrets, a.GetNamespace()+"/"+a.(k8stesting.GetAction).GetName())
		case a.GetVerb() == "list" && a.GetResource().Resource == "services":
			listedServices = true
		}
	}
	return secrets, listedServices
}

func lastCheck(checks []check) check {
	if len(checks) == 0 {
		return check{}
	}
	return checks[len(checks)-1]
}

// checkOf is the check for one step, or the zero check.
func checkOf(checks []check, step string) check {
	for _, c := range checks {
		if c.Step == step {
			return c
		}
	}
	return check{}
}

// kubecron-api beside the one labeled Service: turning the extra on is enough.
func TestWithNothingConfiguredTheLabelledServiceAndKubecronAPIAreRead(t *testing.T) {
	clients := fake.NewClientset(collectorService("bearer"), tokenSecret())
	f := discover(context.Background(), viaClientset(clients), nothing)
	if f.Ref == nil || f.State != "ok" || f.Via != viaLabel {
		t.Fatalf("%+v", f)
	}
	if f.Ref.Token != testToken || f.Ref.TokenNamespace != "kubecron" || f.Ref.Where() != "kubecron/kubecron" {
		t.Errorf("ref %+v", f.Ref)
	}
	if secrets, _ := secretsRead(clients); len(secrets) != 1 || secrets[0] != "kubecron/kubecron-api" {
		t.Errorf("secrets read %v, want only kubecron/kubecron-api", secrets)
	}
	collector, secret := checkOf(f.Checks, stepCollector), checkOf(f.Checks, stepSecret)
	if collector.State != checkOK || collector.Value != "kubecron/kubecron" || collector.Source != fromDefault {
		t.Errorf("collector check %+v", collector)
	}
	if secret.State != checkOK || secret.Value != "kubecron/kubecron-api" || secret.Source != fromDefault {
		t.Errorf("secret check %+v", secret)
	}
	if !strings.Contains(f.Source, "found by its label") {
		t.Errorf("source %q", f.Source)
	}
}

// The Service's annotations name another Secret: it is never read.
func TestTheServiceAnnotationNeverChoosesTheSecret(t *testing.T) {
	svc := collectorService("bearer")
	svc.Annotations["kubecron.io/auth-secret"] = "other"
	other := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "kubecron", Name: "other"},
		Data: map[string][]byte{"api-token": []byte("not-this-one")}}
	for name, w := range map[string]wanted{"defaults": nothing, "environment": resolve(testConfig, "", "")} {
		t.Run(name, func(t *testing.T) {
			clients := fake.NewClientset(svc, tokenSecret(), other)
			f := discover(context.Background(), viaClientset(clients), w)
			if f.Ref == nil || f.Ref.Token != testToken {
				t.Fatalf("%+v", f)
			}
			if secrets, _ := secretsRead(clients); len(secrets) != 1 || secrets[0] != "kubecron/kubecron-api" {
				t.Errorf("secrets read %v", secrets)
			}
		})
	}
}

// The Secret is read in the collector's namespace and nowhere else,
// whatever names the collector, and a Secret of the same name elsewhere is
// never read for it.
func TestTheTokenIsReadOnlyInTheCollectorsOwnNamespace(t *testing.T) {
	tenant := collectorService("bearer")
	tenant.Namespace = "team-a"
	for name, w := range map[string]wanted{
		"found by its label":     nothing,
		"set for this cluster":   resolve(collectorConfig{}, "team-a/kubecron", ""),
		"set by the environment": resolve(collectorConfig{Namespace: "team-a", Service: "kubecron"}, "", ""),
	} {
		t.Run(name, func(t *testing.T) {
			clients := fake.NewClientset(tenant, tokenSecret())
			f := discover(context.Background(), viaClientset(clients), w)
			if f.Ref != nil || f.State != "unusable" {
				t.Fatalf("%+v", f)
			}
			if secrets, _ := secretsRead(clients); len(secrets) != 1 || secrets[0] != "team-a/kubecron-api" {
				t.Errorf("secrets read %v, want only team-a/kubecron-api", secrets)
			}
			if c := checkOf(f.Checks, stepSecret); c.State != checkMissing || c.Value != "team-a/kubecron-api" {
				t.Errorf("secret check %+v", c)
			}
		})
	}
}

// Where the header is added: a token read in one namespace is never
// sent to a Service in another, even if a ref were built that way.
func TestATokenIsNeverSentToAServiceInAnotherNamespace(t *testing.T) {
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		if r.Header.Get("Authorization") != "" {
			t.Errorf("a token reached %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	ref := CollectorRef{Namespace: "team-a", Service: "kubecron", APIPath: "/api/v1",
		Token: testToken, TokenNamespace: "kubecron", TokenSecret: "kubecron/kubecron-api"}
	get := forwardGetter(http.DefaultClient, strings.TrimPrefix(srv.URL, "http://"), ref)

	c, avail, err := openCollector(context.Background(), get, ref)
	if c != nil || avail.OK || err == nil || !strings.HasPrefix(avail.Detail, `a token read in namespace "kubecron" is never sent to the Service team-a/kubecron`) {
		t.Errorf("openCollector: c=%v avail=%+v err=%v", c, avail, err)
	}
	// And the getter alone, handed the header directly, refuses too.
	if _, err := get(context.Background(), "/api/v1/collector", nil, map[string]string{"Authorization": "Bearer " + testToken}); err == nil {
		t.Error("the getter sent a token to another namespace")
	}
	if asked != 0 {
		t.Errorf("%d requests reached the collector", asked)
	}
}

// Several labeled Services: none is chosen, nothing is read for any of them,
// and the page names them.
func TestSeveralLabelledServicesChooseNone(t *testing.T) {
	h, _ := hostWithCollector(t)
	decoy := collectorService("bearer")
	decoy.Namespace = "aaa-team"
	if err := h.clients.Tracker().Add(decoy); err != nil {
		t.Fatal(err)
	}
	k := testExtra()
	k.collector = collectorConfig{}
	rec := serve(t, k.list(h), "/x/docker-desktop/kubecron/", nil)
	var out listAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Collector.State != "unusable" || !strings.Contains(out.Collector.Detail, "none is chosen") ||
		!strings.Contains(out.Collector.Detail, "Settings") {
		t.Errorf("collector %+v", out.Collector)
	}
	if got := strings.Join(out.Collector.Candidates, ","); got != "aaa-team/kubecron,kubecron/kubecron" {
		t.Errorf("candidates %q", got)
	}
	if c := checkOf(out.Collector.Checks, stepCollector); c.State != checkSeveral {
		t.Errorf("check %+v", c)
	}
	if h.forwards != 0 {
		t.Errorf("%d forwards opened", h.forwards)
	}
	if secrets, _ := secretsRead(h.clients); len(secrets) != 0 {
		t.Errorf("secrets read %v, want none", secrets)
	}
	// Named in Settings, the same cluster is read.
	h.settings = map[string]string{SettingCollector: "kubecron/kubecron"}
	rec = serve(t, k.list(h), "/x/docker-desktop/kubecron/", nil)
	out = listAnswer{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Collector.State != "ok" || len(h.forwardedTo) != 1 || h.forwardedTo[0] != "kubecron/kubecron" {
		t.Errorf("collector %+v, forwards %v", out.Collector, h.forwardedTo)
	}
}

// A cluster's setting is read on every request: changed, the next read uses it
// with no restart.
func TestASettingAppliesOnTheNextRequest(t *testing.T) {
	h, _ := hostWithCollector(t)
	other := collectorService("none")
	other.Namespace, other.Name = "ops", "cron-history"
	other.Labels = nil
	if err := h.clients.Tracker().Add(other); err != nil {
		t.Fatal(err)
	}
	k := testExtra()
	k.collector = collectorConfig{}
	h.settings = map[string]string{SettingCollector: "ops/cron-history"}
	serve(t, k.status(h), "/x/docker-desktop/kubecron/status", nil)
	h.settings = map[string]string{}
	serve(t, k.status(h), "/x/docker-desktop/kubecron/status", nil)
	if got := strings.Join(h.forwardedTo, ","); got != "ops/cron-history,kubecron/kubecron" {
		t.Errorf("forwards opened to %q", got)
	}
}

// The status route opens the console's tunnel only for a person who may list a
// CronJob here, and answers a refused list with the list's own answer.
func TestTheStatusRouteOpensNoTunnelForAPersonWhoMayListNoCronJob(t *testing.T) {
	h, _ := hostWithCollector(t)
	h.hiddenNames = map[string]bool{}
	for _, o := range h.objects["cronjobs"] {
		h.hiddenNames[o.GetNamespace()+"/"+o.GetName()] = true
	}
	k := testExtra()
	if rec := serve(t, k.status(h), "/x/docker-desktop/kubecron/status", nil); rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404: %s", rec.Code, rec.Body)
	}
	h.hiddenNames, h.listErr = nil, errors.New("refused")
	if rec := serve(t, k.status(h), "/x/docker-desktop/kubecron/status", nil); rec.Code == http.StatusOK {
		t.Errorf("a refused list answered 200: %s", rec.Body)
	}
	if h.forwards != 0 {
		t.Errorf("%d tunnels opened, want none", h.forwards)
	}
}

// The environment wins over the cluster's setting, which wins over the
// defaults; each value says where it came from.
func TestTheEnvironmentWinsOverTheSettingWhichWinsOverTheDefaults(t *testing.T) {
	for name, tc := range map[string]struct {
		env                       collectorConfig
		collector, secret         string
		where, from, sec, secFrom string
	}{
		"defaults":    {collectorConfig{}, "", "", "/", fromDefault, DefaultTokenSecret, fromDefault},
		"settings":    {collectorConfig{}, "ops/cron", "cron-api", "ops/cron", fromSetting, "cron-api", fromSetting},
		"environment": {testConfig, "ops/cron", "cron-api", "kubecron/kubecron", fromEnvironment, "kubecron-api", fromEnvironment},
		"mixed":       {collectorConfig{Secret: "env-api"}, "ops/cron", "cron-api", "ops/cron", fromSetting, "env-api", fromEnvironment},
	} {
		w := resolve(tc.env, tc.collector, tc.secret)
		if w.where() != tc.where || w.CollectorFrom != tc.from || w.Secret != tc.sec || w.SecretFrom != tc.secFrom || w.Invalid != "" {
			t.Errorf("%s: %+v", name, w)
		}
	}
	if w := resolve(collectorConfig{}, "not a collector", ""); w.Invalid == "" {
		t.Errorf("a stored value that cannot be used was read: %+v", w)
	}
}

// KT_KUBECRON_TOKEN_KEY names the key of the Secret.
func TestTheTokenKeyCanBeConfigured(t *testing.T) {
	s := tokenSecret()
	s.Data = map[string][]byte{"token": []byte(testToken)}
	cfg := testConfig
	cfg.Key = "token"
	f := discover(context.Background(), viaClientset(fake.NewClientset(collectorService("bearer"), s)), resolve(cfg, "", ""))
	if f.Ref == nil || f.Ref.Token != testToken {
		t.Errorf("%+v", f)
	}
}

// A collector that asks for no token is read without reading a Secret.
func TestACollectorWithoutATokenNeedsNoSecret(t *testing.T) {
	for name, w := range map[string]wanted{"defaults": nothing, "environment": resolve(testConfig, "", "")} {
		t.Run(name, func(t *testing.T) {
			clients := fake.NewClientset(collectorService("none"))
			f := discover(context.Background(), viaClientset(clients), w)
			if f.Ref == nil || f.State != "ok" || f.Ref.Token != "" {
				t.Errorf("%+v", f)
			}
			if c := checkOf(f.Checks, stepSecret); c.State != checkSkipped {
				t.Errorf("secret check %+v", c)
			}
			if secrets, _ := secretsRead(clients); len(secrets) != 0 {
				t.Errorf("secrets read %v, want none", secrets)
			}
		})
	}
}

// A Service that says nothing about a token is read without one when its
// Secret is not there, and the check says none is sent.
func TestACollectorThatSaysNothingIsReadWithoutAMissingToken(t *testing.T) {
	f := discover(context.Background(), viaClientset(fake.NewClientset(collectorService(""))), nothing)
	if f.Ref == nil || f.State != "ok" || f.Ref.Token != "" {
		t.Fatalf("%+v", f)
	}
	if c := checkOf(f.Checks, stepSecret); c.State != checkMissing || !strings.Contains(c.Detail, "none is sent") {
		t.Errorf("secret check %+v", c)
	}
}

// One variable for every cluster: where the Service it names is not, the
// defaults still look, and the page says so.
func TestWhereTheEnvironmentsServiceIsNotTheDefaultsStillLook(t *testing.T) {
	other := collectorService("bearer")
	other.Namespace = "ops"
	secret := tokenSecret()
	secret.Namespace = "ops"
	clients := fake.NewClientset(other, secret)
	f := discover(context.Background(), viaClientset(clients), resolve(collectorConfig{Namespace: "elsewhere", Service: "kubecron"}, "", ""))
	if f.Ref == nil || f.State != "ok" || f.Ref.Where() != "ops/kubecron" || f.Ref.TokenNamespace != "ops" || f.Via != viaLabel {
		t.Fatalf("%+v", f)
	}
	if f.Configured != "elsewhere/kubecron" || !strings.Contains(f.Source, CollectorEnv+" names elsewhere/kubecron, which does not exist in this cluster") {
		t.Errorf("configured %q source %q", f.Configured, f.Source)
	}
	t.Run("and several here: the advice is the variable, which the Settings page would refuse", func(t *testing.T) {
		a, b := collectorService("none"), collectorService("none")
		a.Namespace, b.Namespace = "a", "b"
		f := discover(context.Background(), viaClientset(fake.NewClientset(a, b)), resolve(testConfig, "", ""))
		if f.Ref != nil || !strings.Contains(f.Detail, "none is chosen: set "+CollectorEnv+" to it") || strings.Contains(f.Detail, "Settings") {
			t.Errorf("%+v", f.collectorState)
		}
	})
	t.Run("and none here", func(t *testing.T) {
		f := discover(context.Background(), viaClientset(fake.NewClientset()), resolve(testConfig, "", ""))
		want := CollectorEnv + " names kubecron/kubecron, which does not exist in this cluster, and no Service here is labeled"
		if f.Ref != nil || f.State != "absent" || !strings.Contains(f.Detail, want) {
			t.Errorf("%+v", f)
		}
	})
}

// A Service set for this cluster that is not there is said, and nothing else
// is looked for: the setting is this cluster's own.
func TestASetCollectorThatIsNotThereIsSaid(t *testing.T) {
	clients := fake.NewClientset(collectorService("bearer"), tokenSecret())
	f := discover(context.Background(), viaClientset(clients), resolve(collectorConfig{}, "ops/cron", ""))
	if f.Ref != nil || f.State != "absent" || !strings.Contains(f.Detail, "ops/cron, set for this cluster in Settings, does not exist") {
		t.Errorf("%+v", f)
	}
	if _, listed := secretsRead(clients); listed {
		t.Error("looked for by label as well")
	}
}

// Each failure is its own check, naming what is missing.
func TestEachStepThatFailsSaysWhy(t *testing.T) {
	forbidden := func(resource string) k8stesting.ReactionFunc {
		return func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(corev1.Resource(resource), "", errors.New("RBAC"))
		}
	}
	// Found by its label, the token's namespace is the cluster's: it is read
	// for the person, and their refusal is said as theirs.
	t.Run("secret forbidden, collector found by its label", func(t *testing.T) {
		clients := fake.NewClientset(collectorService("bearer"), tokenSecret())
		clients.PrependReactor("get", "secrets", forbidden("secrets"))
		f := discover(context.Background(), viaClientset(clients), nothing)
		c := checkOf(f.Checks, stepSecret)
		if f.Ref != nil || f.State != "unusable" || c.State != checkDenied ||
			!strings.Contains(c.Detail, `access refused (403): you may not get secrets/kubecron-api in namespace "kubecron"`) {
			t.Errorf("%+v / %+v", f.collectorState, c)
		}
	})
	// Named by the configuration, the token is the console's to read.
	t.Run("secret forbidden, collector configured", func(t *testing.T) {
		clients := fake.NewClientset(collectorService("bearer"), tokenSecret())
		clients.PrependReactor("get", "secrets", forbidden("secrets"))
		f := discover(context.Background(), viaClientset(clients), resolve(testConfig, "", ""))
		c := checkOf(f.Checks, stepSecret)
		if f.Ref != nil || f.State != "unusable" || c.State != checkDenied ||
			!strings.Contains(c.Detail, `access refused (403): the console's credentials may not get secrets/kubecron-api in namespace "kubecron"`) {
			t.Errorf("%+v / %+v", f.collectorState, c)
		}
	})
	t.Run("secret missing", func(t *testing.T) {
		f := discover(context.Background(), viaClientset(fake.NewClientset(collectorService("bearer"))), nothing)
		if c := checkOf(f.Checks, stepSecret); f.State != "unusable" || c.State != checkMissing || !strings.Contains(c.Detail, "was not found") {
			t.Errorf("%+v", f)
		}
	})
	t.Run("no key in the secret", func(t *testing.T) {
		s := tokenSecret()
		s.Data = map[string][]byte{"other": []byte("x")}
		f := discover(context.Background(), viaClientset(fake.NewClientset(collectorService("bearer"), s)), nothing)
		if f.Ref != nil || f.State != "unusable" || !strings.Contains(f.Detail, `no "api-token" key`) {
			t.Errorf("%+v", f)
		}
	})
	t.Run("services cannot be listed", func(t *testing.T) {
		clients := fake.NewClientset()
		clients.PrependReactor("list", "services", forbidden("services"))
		f := discover(context.Background(), viaClientset(clients), nothing)
		c := checkOf(f.Checks, stepCollector)
		if f.State != "unusable" || c.State != checkDenied || !strings.Contains(c.Detail, "may not list services in every namespace") {
			t.Errorf("%+v", f)
		}
	})
	t.Run("the set service forbidden", func(t *testing.T) {
		clients := fake.NewClientset()
		clients.PrependReactor("get", "services", forbidden("services"))
		f := discover(context.Background(), viaClientset(clients), resolve(collectorConfig{}, "ops/cron", ""))
		if c := checkOf(f.Checks, stepCollector); f.State != "unusable" || c.State != checkDenied || !strings.Contains(c.Detail, `services/cron in namespace "ops"`) {
			t.Errorf("%+v", f)
		}
	})
}

// Through the routes: unreachable and refused are their own checks.
func TestTheStatusRouteSaysReachedAndRefused(t *testing.T) {
	read := func(t *testing.T, h *fakeHost) statusAnswer {
		t.Helper()
		k := testExtra()
		k.collector = collectorConfig{}
		rec := serve(t, k.status(h), "/x/docker-desktop/kubecron/status", nil)
		var out statusAnswer
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return out
	}
	t.Run("ok", func(t *testing.T) {
		h, _ := hostWithCollector(t)
		out := read(t, h)
		if out.Collector.State != "ok" || checkOf(out.Collector.Checks, stepToken).State != checkOK || checkOf(out.Collector.Checks, stepReach).State != checkOK {
			t.Errorf("%+v", out.Collector)
		}
		if h.stopped != h.forwards {
			t.Errorf("%d forwards, %d stopped", h.forwards, h.stopped)
		}
		if strings.Contains(fmt.Sprint(out), testToken) {
			t.Error("the token is in the answer")
		}
	})
	t.Run("token refused", func(t *testing.T) {
		h, _ := hostWithCollector(t)
		s := tokenSecret()
		s.Data["api-token"] = []byte("wrong")
		if err := h.clients.Tracker().Update(corev1.SchemeGroupVersion.WithResource("secrets"), s, "kubecron"); err != nil {
			t.Fatal(err)
		}
		out := read(t, h)
		c := checkOf(out.Collector.Checks, stepToken)
		if out.Collector.State != "unusable" || c.State != checkRefused || !strings.Contains(c.Detail, "refused the token from Secret kubecron/kubecron-api (401)") {
			t.Errorf("%+v", out.Collector)
		}
	})
	t.Run("reached, answering a status it cannot use", func(t *testing.T) {
		h, fc := hostWithCollector(t)
		delete(fc.answers, "/api/v1/collector")
		out := read(t, h)
		if c := checkOf(out.Collector.Checks, stepReach); out.Collector.State != "unusable" || c.State != checkOK || !strings.Contains(out.Collector.Detail, "404") {
			t.Errorf("%+v", out.Collector)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		h, _ := hostWithCollector(t)
		h.forwardErr = errors.New("no pod")
		out := read(t, h)
		if c := checkOf(out.Collector.Checks, stepReach); out.Collector.State != "unusable" || c.State != checkFailed || !strings.Contains(c.Detail, "no pod") {
			t.Errorf("%+v", out.Collector)
		}
	})
}

// A configuration that cannot be used is its own state, never "a collector is
// here".
func TestAnInvalidConfigurationIsItsOwnState(t *testing.T) {
	cfg := readConfig(func(k string) string { return map[string]string{CollectorEnv: "kubecron"}[k] })
	clients := fake.NewClientset(collectorService("bearer"), tokenSecret())
	f := discover(context.Background(), viaClientset(clients), resolve(cfg, "ops/cron", ""))
	if f.Ref != nil || f.State != "misconfigured" || !strings.Contains(f.Detail, CollectorEnv) {
		t.Errorf("%+v", f)
	}
	if len(clients.Actions()) != 0 {
		t.Errorf("an invalid configuration still read the cluster: %v", clients.Actions())
	}
}

func TestTheConfigurationIsReadOrRefusedWithASentence(t *testing.T) {
	env := func(collector, secret, key string) func(string) string {
		return func(k string) string {
			return map[string]string{CollectorEnv: collector, TokenSecretEnv: secret, TokenKeyEnv: key}[k]
		}
	}
	if got := readConfig(env("", "", "")); got != (collectorConfig{}) {
		t.Errorf("unset: %+v", got)
	}
	if got := readConfig(env(" kubecron / kubecron ", "kubecron-api", "")); got != testConfig {
		t.Errorf("set, with spaces round each part: %+v", got)
	}
	if got := readConfig(env("kubecron/kubecron", "", "")); got != (collectorConfig{Namespace: "kubecron", Service: "kubecron"}) {
		t.Errorf("no secret: %+v", got)
	}
	if got := readConfig(env("kubecron/kubecron", "kubecron-api", "token")); got.Key != "token" || got.key() != "token" || got.Invalid != "" {
		t.Errorf("a key: %+v", got)
	}
	// Each alone is a value for the defaults to complete.
	if got := readConfig(env("", "kubecron-api", "")); got != (collectorConfig{Secret: "kubecron-api"}) {
		t.Errorf("a secret alone: %+v", got)
	}
	if got := readConfig(env("", "", "token")); got != (collectorConfig{Key: "token"}) {
		t.Errorf("a key alone: %+v", got)
	}
	if testConfig.key() != TokenKey {
		t.Errorf("default key %q", testConfig.key())
	}
	for name, tc := range map[string]struct{ collector, secret, key, want string }{
		"no namespace":            {"kubecron", "", "", CollectorEnv},
		"empty service":           {"kubecron/", "", "", CollectorEnv},
		"too many parts":          {"a/b/c", "", "", CollectorEnv},
		"a namespace in capitals": {"KubeCron/kubecron", "", "", `the namespace "KubeCron", which is not a Kubernetes name`},
		"a service with a dot":    {"kubecron/kube.cron", "", "", `the Service "kube.cron", which is not a Kubernetes name`},
		"a service from a digit":  {"kubecron/1cron", "", "", `the Service "1cron", which is not a Kubernetes name`},
		"a secret with a slash":   {"kubecron/kubecron", "kubecron/kubecron-api", "", "a Secret's name alone"},
		"a secret not a name":     {"kubecron/kubecron", "Kubecron_API", "", "is not a Kubernetes name"},
		"a key not a key":         {"kubecron/kubecron", "kubecron-api", "a key", "is not a Secret's key"},
	} {
		got := readConfig(env(tc.collector, tc.secret, tc.key))
		if got.Invalid == "" || got.Service != "" || got.Secret != "" || !strings.Contains(got.Invalid, tc.want) ||
			!strings.HasSuffix(got.Invalid, "so no collector is read") {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

// The settings the tile edits are checked with the same rules as the
// environment, and declared as lines of text the environment overrides.
func TestTheSettingsAreTextTheEnvironmentOverrides(t *testing.T) {
	settings := KubeCron{}.Settings()
	if len(settings) != 2 || settings[0].Key != SettingCollector || settings[1].Key != SettingTokenSecret {
		t.Fatalf("%+v", settings)
	}
	if settings[0].Env != CollectorEnv || settings[1].Env != TokenSecretEnv || !settings[0].Text || !settings[1].Text {
		t.Errorf("%+v", settings)
	}
	if settings[0].Check("kubecron/kubecron") != nil || settings[0].Check("kubecron") == nil {
		t.Error("the collector's check")
	}
	if settings[1].Check("kubecron-api") != nil || settings[1].Check("ns/kubecron-api") == nil {
		t.Error("the secret's check")
	}
}

func TestTheContractIsAgreedBeforeAnythingIsRead(t *testing.T) {
	ref := CollectorRef{Namespace: "kubecron", Service: "kubecron", APIPath: "/api/v1", Token: testToken, TokenNamespace: "kubecron"}
	for name, tc := range map[string]struct{ doc, want string }{
		"another version":  {`{"api_versions":["v2"],"clusters":[{"id":"a"}]}`, "speaks [v2]"},
		"several clusters": {`{"api_versions":["v1"],"clusters":[{"id":"a"},{"id":"b"}]}`, "observes 2 clusters"},
		"unreadable":       {`not json`, "something unreadable"},
	} {
		t.Run(name, func(t *testing.T) {
			srv, _ := newFakeCollector(t, map[string]string{"/api/v1/collector": tc.doc})
			c, avail, _ := openCollector(context.Background(), forwardGetter(http.DefaultClient, strings.TrimPrefix(srv.URL, "http://"), ref), ref)
			if c != nil || avail.OK || !strings.Contains(avail.Detail, tc.want) {
				t.Errorf("c=%v avail=%+v", c, avail)
			}
		})
	}
}

func TestAWrongTokenIsSaidAsARefusal(t *testing.T) {
	srv, _ := newFakeCollector(t, map[string]string{"/api/v1/collector": discoveryDoc})
	ref := CollectorRef{Namespace: "kubecron", Service: "kubecron", APIPath: "/api/v1", Token: "wrong",
		TokenNamespace: "kubecron", TokenSecret: "kubecron/kubecron-api"}
	_, avail, err := openCollector(context.Background(), forwardGetter(http.DefaultClient, strings.TrimPrefix(srv.URL, "http://"), ref), ref)
	var status *statusError
	if avail.OK || !strings.Contains(avail.Detail, "refused the token from Secret kubecron/kubecron-api") || !errors.As(err, &status) || !status.refused() {
		t.Errorf("avail %+v err %v", avail, err)
	}
}

// A collector that wants a token none could be read for is said as such, not
// as one that refused a token never sent.
func TestARefusalWithNoTokenSentSaysNoneCouldBeRead(t *testing.T) {
	srv, _ := newFakeCollector(t, map[string]string{"/api/v1/collector": discoveryDoc})
	ref := CollectorRef{Namespace: "kubecron", Service: "kubecron", APIPath: "/api/v1"}
	_, avail, _ := openCollector(context.Background(), forwardGetter(http.DefaultClient, strings.TrimPrefix(srv.URL, "http://"), ref), ref)
	if avail.OK || !strings.Contains(avail.Detail, "asks for a token (401), and none could be read to send") {
		t.Errorf("avail %+v", avail)
	}
}

// A run cursor carries a `+` in its offset; in a raw query it is a space and the
// collector silently re-serves page one.
func TestARunCursorKeepsItsPlus(t *testing.T) {
	path := "/api/v1/clusters/docker-desktop/cronjobs/ops/nightly/runs"
	srv, fc := newFakeCollector(t, map[string]string{"/api/v1/collector": discoveryDoc, path: `{"runs":[]}`})
	ref := CollectorRef{Namespace: "kubecron", Service: "kubecron", APIPath: "/api/v1", Token: testToken, TokenNamespace: "kubecron"}
	c, avail, _ := openCollector(context.Background(), forwardGetter(http.DefaultClient, strings.TrimPrefix(srv.URL, "http://"), ref), ref)
	if !avail.OK {
		t.Fatal(avail.Detail)
	}
	cursor := "2026-08-14T02:00:03.418+02:00"
	if _, avail := c.Runs(context.Background(), "ops", "nightly", 0, cursor); !avail.OK {
		t.Fatal(avail.Detail)
	}
	got, _ := url.ParseQuery(fc.queries[path])
	if got.Get("before") != cursor {
		t.Errorf("the collector received before=%q", got.Get("before"))
	}
}

// A CronJob failing every run sorts above the healthy ones once the history is
// joined, even though its schedule is on time.
func TestAFailingCronJobSortsAboveTheHealthyOnes(t *testing.T) {
	items := []unstructured.Unstructured{
		cronJob(t, "ops", "a-healthy", "*/5 * * * *", testNow.Add(-2*time.Minute), nil),
		cronJob(t, "ops", "b-failing", "*/5 * * * *", testNow.Add(-2*time.Minute), nil),
	}
	report := judgeAll(listed{Items: items, Complete: true}, nil, nil, testNow)
	report.WithHistory(map[string]Outcome{"ops/b-failing": {Runs: 6, Failed: 6}}, Availability{OK: true})
	if report.CronJobs[0].Name != "b-failing" || report.Failing != 1 {
		t.Errorf("order %s,%s failing %d", report.CronJobs[0].Name, report.CronJobs[1].Name, report.Failing)
	}
}

func TestNullableDurationsStayAbsent(t *testing.T) {
	if millis(nil) != nil {
		t.Error("nil became a duration")
	}
	neg := int64(-5)
	if millis(&neg) != nil {
		t.Error("a negative duration was kept")
	}
	if (Day{Total: 5, Succeeded: 2, Running: 1}).Failed() != 2 {
		t.Error("a running run counted as a failure")
	}
}

// --- the routes, and who sees what -------------------------------------------

func hostWithCollector(t *testing.T) (*fakeHost, *fakeCollector) {
	srv, fc := newFakeCollector(t, map[string]string{
		"/api/v1/collector": discoveryDoc,
		"/api/v1/clusters/docker-desktop/cronjobs": `{"cronjobs":[
		  {"namespace":"ops","name":"nightly","stats_7d":{"total":10,"failed":3,"avg_duration_ms":40000,"max_duration_ms":null},
		   "last_run":{"status":"failed","started_at":"2026-10-20T11:00:00Z"}},
		  {"namespace":"secret-team","name":"hidden","stats_7d":{"total":5,"failed":5}}]}`,
		"/api/v1/clusters/docker-desktop/cronjobs/ops/nightly/daily": `{"days":90,"daily":[{"day":"2026-10-19","total":4,"succeeded":3,"running":0}]}`,
		"/api/v1/clusters/docker-desktop/cronjobs/ops/nightly/runs":  `{"runs":[{"id":"r1","pod_name":"nightly-1","trigger":"scheduled","started_at":"2026-10-20T11:00:00Z","status":"failed","exit_code":3,"retry_count":0,"log_size_bytes":120,"duration_ms":5000}],"next_cursor":""}`,
		"/api/v1/runs/r1":         `{"run":{"id":"r1","pod_name":"nightly-1","trigger":"scheduled","started_at":"2026-10-20T11:00:00Z","status":"failed","exit_code":3,"log_size_bytes":120,"duration_ms":5000},"cronjob":{"namespace":"ops","name":"nightly"}}`,
		"/api/v1/runs/r1/logs":    `{"lines":[{"ts":"2026-10-20T11:00:01Z","line":"ERROR: connection refused"}],"log_size_bytes":120}`,
		"/api/v1/runs/r1/samples": `{"interval_seconds":15,"samples":[],"summary":{"avg_cpu_millicores":null}}`,
		"/api/v1/runs/r9":         `{"run":{"id":"r9","pod_name":"hidden-1","started_at":"2026-10-20T11:00:00Z","status":"failed"},"cronjob":{"namespace":"secret-team","name":"hidden"}}`,
	})
	h := &fakeHost{
		objects: map[string][]unstructured.Unstructured{
			"cronjobs": {
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

func TestTheListJoinsHistoryOntoVisibleCronJobsOnly(t *testing.T) {
	h, _ := hostWithCollector(t)
	rec := serve(t, testExtra().list(h), "/x/docker-desktop/kubecron/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out listAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Collector.State != "ok" || len(out.CronJobs) != 1 || out.CronJobs[0].Name != "nightly" {
		t.Fatalf("answer %+v", out)
	}
	hist := out.CronJobs[0].History
	if hist == nil || hist.Runs != 10 || hist.Failed != 3 || hist.MaxDurationMs != nil || *hist.AvgDurationMs != 40000 {
		t.Errorf("history %+v", hist)
	}
	if out.Counts.Failing != 1 {
		t.Errorf("a hidden CronJob's failures were counted: %+v", out.Counts)
	}
	if strings.Contains(rec.Body.String(), testToken) {
		t.Error("the collector's token is in the answer")
	}
	if h.forwards != 1 || h.stopped != 1 {
		t.Errorf("forwards opened %d, stopped %d", h.forwards, h.stopped)
	}
}

func TestWithNoCollectorTheListStillJudgesAndSaysAbsent(t *testing.T) {
	h, _ := hostWithCollector(t)
	h.clients = fake.NewClientset()
	rec := serve(t, testExtra().list(h), "/x/docker-desktop/kubecron/", nil)
	var out listAnswer
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || out.Collector.State != "absent" || len(out.CronJobs) != 1 || out.CronJobs[0].Verdict == "" {
		t.Errorf("%d %+v", rec.Code, out)
	}
	if out.History.OK {
		t.Error("history is OK with no collector")
	}
}

func TestAListThatFailsIsAFailureAndNeverAnEmptyPage(t *testing.T) {
	h, _ := hostWithCollector(t)
	h.listErr = errors.New("cronjobs.batch is forbidden")
	rec := serve(t, testExtra().list(h), "/x/docker-desktop/kubecron/", nil)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), CodeListFailed) {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	h.listErr = fmt.Errorf("%w %q", extension.ErrUnknownCluster, "nope")
	rec = serve(t, testExtra().list(h), "/x/nope/kubecron/", nil)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), CodeUnknownCluster) {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestTheDetailCarriesTheCalendarAndTheRuns(t *testing.T) {
	h, _ := hostWithCollector(t)
	rec := serve(t, testExtra().detail(h), "/x/docker-desktop/kubecron/cronjobs/ops/nightly",
		map[string]string{"namespace": "ops", "name": "nightly"})
	var out detailAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if out.Daily == nil || len(out.Daily.Days) != 1 || out.Daily.Days[0].Failed != 1 {
		t.Errorf("daily %+v", out.Daily)
	}
	if out.Runs == nil || len(out.Runs.Runs) != 1 || *out.Runs.Runs[0].ExitCode != 3 {
		t.Errorf("runs %+v", out.Runs)
	}
}

// A CronJob the person may not list is answered as one that does not exist, and
// so is any run of it.
func TestWhatThePersonMayNotListIsNotFound(t *testing.T) {
	h, _ := hostWithCollector(t)
	rec := serve(t, testExtra().detail(h), "/x/docker-desktop/kubecron/cronjobs/secret-team/hidden",
		map[string]string{"namespace": "secret-team", "name": "hidden"})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), CodeCronJobNotFound) {
		t.Errorf("detail: %d %s", rec.Code, rec.Body)
	}
	rec = serve(t, testExtra().run(h), "/x/docker-desktop/kubecron/runs/r9", map[string]string{"id": "r9"})
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "hidden-1") {
		t.Errorf("run: %d %s", rec.Code, rec.Body)
	}
}

func TestOneRunCarriesItsLogAndSaysWhySamplesAreMissing(t *testing.T) {
	h, _ := hostWithCollector(t)
	rec := serve(t, testExtra().run(h), "/x/docker-desktop/kubecron/runs/r1", map[string]string{"id": "r1"})
	var out runAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if out.CronJob != "nightly" || out.Log == nil || len(out.Log.Lines) != 1 || out.Samples == nil || !out.Samples.MetricsEnabled {
		t.Errorf("run %+v", out)
	}
	rec = serve(t, testExtra().run(h), "/x/docker-desktop/kubecron/runs/gone", map[string]string{"id": "gone"})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no longer holds") {
		t.Errorf("gone: %d %s", rec.Code, rec.Body)
	}
}

// A run's stored log is a pod's log: a person who may list the CronJob but not
// `get pods/log` in its namespace gets the run without the log, and the
// collector is never asked for it. The person
// who may gets the log.
func TestARunsStoredLogIsReviewedAsGetPodsLog(t *testing.T) {
	for _, tc := range []struct {
		name      string
		logsErr   error
		wantLog   bool
		wantState string
	}{
		{"allowed", nil, true, ""},
		{"denied", fmt.Errorf("%w: the cluster says you may not get pods/log in namespace ops", extension.ErrLogsDenied), false, "refused"},
		{"unreviewed", fmt.Errorf("%w: the authorizer did not answer", extension.ErrLogsUnreviewed), false, "unreviewed"},
		{"unknown cluster", fmt.Errorf("%w: %q", extension.ErrUnknownCluster, "docker-desktop"), false, "unreviewed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, fc := hostWithCollector(t)
			h.logsErr = tc.logsErr
			rec := serve(t, testExtra().run(h), "/x/docker-desktop/kubecron/runs/r1", map[string]string{"id": "r1"})
			var out runAnswer
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if len(h.logsAsked) != 1 || h.logsAsked[0] != "ops" {
				t.Errorf("get pods/log was asked about %v, want the run's namespace once", h.logsAsked)
			}
			fc.mu.Lock()
			_, askedForLog := fc.queries["/api/v1/runs/r1/logs"]
			fc.mu.Unlock()
			if tc.wantLog {
				if out.Log == nil || len(out.Log.Lines) != 1 || out.LogState != "" {
					t.Errorf("the allowed person did not get the log: %+v", out)
				}
				return
			}
			if out.Log != nil || strings.Contains(rec.Body.String(), "connection refused") {
				t.Errorf("the log reached a person who may not read it: %s", rec.Body)
			}
			if askedForLog {
				t.Error("the collector was asked for a log the person may not read")
			}
			if out.LogState != tc.wantState || out.LogNote == "" {
				t.Errorf("logState %q, logNote %q; want %q and a sentence", out.LogState, out.LogNote, tc.wantState)
			}
			if out.CronJob != "nightly" || out.Run.ID != "r1" {
				t.Errorf("the run itself was withheld with its log: %+v", out)
			}
		})
	}
}

func TestTheKeyAndItsFormerNameAreKept(t *testing.T) {
	k := New()
	if k.Key() != "kubecron" || len(k.Legacy()) != 1 || k.Legacy()[0] != "scheduled" {
		t.Errorf("key %q legacy %v", k.Key(), k.Legacy())
	}
	for _, r := range k.Routes(&fakeHost{}) {
		if r.Method != http.MethodGet {
			t.Errorf("%s %s is not a read", r.Method, r.Pattern)
		}
	}
}
