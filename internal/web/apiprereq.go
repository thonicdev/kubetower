package web

// The prerequisite installs behind `/api/c/{cluster}/prereq/{key}`: an
// operator an extra page reads from, installed by creating one Argo CD
// Application through internal/prereq, which writes through the one guarded
// door (edit.Create).
//
// Argo CD is found here, in the console, from what the cluster serves and what
// it runs - never by importing an extra: the core imports no extra, and the
// Argo CD page is one. Two reads, both the console's:
//
//   - the Application kind, from the console's CRD catalog;
//   - the application-controller's pods, by their chart's label, in every
//     namespace, through access.List: reviewed for the person, and narrowed to
//     the namespaces they may list.
//
// One namespace is an answer. Several, or none on a read that could not see
// the whole cluster, is not, and nothing is installed on it: an Application
// placed where no controller watches is reconciled by nobody and looks exactly
// like an install in progress.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/customresources"
	"github.com/thonicdev/kubetower/internal/prereq"
)

// ArgoControllerSelector is the label Argo CD's chart and its plain manifests
// both put on the application-controller's pods.
const ArgoControllerSelector = "app.kubernetes.io/name=argocd-application-controller"

// apiPrereqs is api.Prereqs over the server.
type apiPrereqs struct {
	s *Server
	// probe looks for Argo CD on one cluster. Nil is the server's own
	// (serverProbe); tests hand one over a fake cluster.
	probe func(ctx context.Context, cluster string) (argoProbe, error)
}

// argoProbe is what finding Argo CD reads, for one cluster.
type argoProbe struct {
	gate   *access.Gate
	client *cluster.Client
	// definition is the Application kind as the catalog knows it.
	definition func(ctx context.Context) (customresources.Definition, error)
}

// argoFinding is what the probe saw.
type argoFinding struct {
	api.PrereqArgo
	// resource is the Application kind at the version this cluster serves,
	// set wherever the kind is served.
	resource schema.GroupVersionResource
}

func (p apiPrereqs) probeFor(ctx context.Context, name string) (argoProbe, error) {
	if p.probe != nil {
		return p.probe(ctx, name)
	}
	return p.s.serverProbe(name)
}

// serverProbe is the probe over this server's cluster manager and catalog.
// An unknown cluster is api.ErrNotFound.
func (s *Server) serverProbe(name string) (argoProbe, error) {
	if s.clusters == nil {
		return argoProbe{}, fmt.Errorf("%w: no cluster manager, so no cluster can be reached", api.ErrNotInProfile)
	}
	resolved, ok := s.clusters.Resolve(name)
	if !ok {
		return argoProbe{}, fmt.Errorf("%w: %q is not a context in the kubeconfig", api.ErrNotFound, name)
	}
	client, err := s.clusters.Client(resolved)
	if err != nil {
		return argoProbe{}, fmt.Errorf("%w: %s", api.ErrInvalid, firstLine(err.Error()))
	}
	return argoProbe{
		gate:   s.gate,
		client: client,
		definition: func(ctx context.Context) (customresources.Definition, error) {
			if s.custom == nil {
				return customresources.Definition{}, errors.New("no custom-resource catalog in this build")
			}
			return s.custom.Definition(ctx, client, prereq.ApplicationKind)
		},
	}, nil
}

// find looks for Argo CD. It never fails: a read that could not answer is the
// state `unknown`, with what it said, and never `absent`.
func (p argoProbe) find(ctx context.Context) argoFinding {
	def, err := p.definition(ctx)
	switch {
	case errors.Is(err, customresources.ErrUnknownKind), errors.Is(err, customresources.ErrNotServed):
		return argoFinding{PrereqArgo: api.PrereqArgo{State: api.ArgoAbsent,
			Note: prereq.ApplicationKind + " is not served on this cluster"}}
	case err != nil:
		return argoFinding{PrereqArgo: api.PrereqArgo{State: api.ArgoUnknown,
			Note: "the cluster's kinds could not be read: " + firstLine(err.Error())}}
	}
	found := argoFinding{resource: schema.GroupVersionResource{Group: def.Group, Version: def.Version, Resource: def.Plural}}
	if !def.Namespaced {
		found.State, found.Note = api.ArgoUnknown, prereq.ApplicationKind+" is cluster-scoped on this cluster"
		return found
	}

	question := access.Attributes{Verb: "list", Version: "v1", Resource: "pods"}
	pods, seen, err := access.List(ctx, p.gate, p.client.Context, question,
		func(pod *corev1.Pod) string { return pod.Namespace },
		func(ctx context.Context) ([]corev1.Pod, error) {
			list, err := p.client.Clients.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: ArgoControllerSelector})
			if err != nil {
				return nil, err
			}
			return list.Items, nil
		})
	if err != nil {
		found.State, found.Note = api.ArgoUnknown, "Argo CD's controller could not be looked for: "+firstLine(err.Error())
		return found
	}

	namespaces := map[string]bool{}
	for i := range pods {
		namespaces[pods[i].Namespace] = true
	}
	found.Namespaces = make([]string, 0, len(namespaces))
	for ns := range namespaces {
		found.Namespaces = append(found.Namespaces, ns)
	}
	sort.Strings(found.Namespaces)

	switch {
	case len(found.Namespaces) == 1:
		found.State, found.Namespace, found.Namespaces = api.ArgoFound, found.Namespaces[0], nil
	case len(found.Namespaces) > 1:
		found.State = api.ArgoAmbiguous
		found.Note = "an application-controller runs in " + strings.Join(found.Namespaces, ", ")
	case seen.Filtered():
		found.State, found.Namespaces = api.ArgoUnknown, nil
		found.Note = "no application-controller in the namespaces you may list pods in"
	default:
		found.State, found.Namespaces = api.ArgoAbsent, nil
		found.Note = prereq.ApplicationKind + " is served, and no pod is labeled " + ArgoControllerSelector
	}
	return found
}

// application reads whether the Application an install would create exists,
// by name, reviewed for the person.
func (p argoProbe) application(ctx context.Context, found argoFinding, name string) *api.PrereqApplication {
	out := &api.PrereqApplication{Name: name, Namespace: found.Namespace}
	question := access.Attributes{Verb: "get", Group: found.resource.Group, Version: found.resource.Version,
		Resource: found.resource.Resource, Namespace: found.Namespace, Name: name}
	_, err := p.gate.Read(ctx, p.client.Context, question, func(ctx context.Context) error {
		_, err := p.client.Dynamic.Resource(found.resource).Namespace(found.Namespace).Get(ctx, name, metav1.GetOptions{})
		return err
	})
	switch {
	case err == nil:
		out.State = api.ApplicationPresent
	case apierrors.IsNotFound(err):
		out.State = api.ApplicationAbsent
	default:
		out.State, out.Note = api.ApplicationUnknown, firstLine(err.Error())
	}
	return out
}

// Prereq is what the page reads before it offers the install.
func (p apiPrereqs) Prereq(ctx context.Context, name, key string) (api.Prereq, *api.Failure) {
	scope := api.Scope{Cluster: name}
	prerequisite, ok := prereq.Lookup(key)
	if !ok {
		reason := api.Because(api.CodeNoSuchObject, fmt.Sprintf("%q is not something this console installs", key))
		return api.Prereq{}, &api.Failure{State: api.StateAbsent, Reason: reason.Retry(false)}
	}
	probe, err := p.probeFor(ctx, name)
	if err != nil {
		return api.Prereq{}, stateOf(err, scope)
	}
	found := probe.find(ctx)
	var app *api.PrereqApplication
	if found.State == api.ArgoFound {
		app = probe.application(ctx, found, prerequisite.Key)
	}
	writable, note := true, ""
	if p.s != nil && p.s.clusters != nil {
		writable, note = p.s.writable(name)
	}
	var create *api.CreateAffordance
	if writable && found.State == api.ArgoFound && app.State == api.ApplicationAbsent && p.s != nil && p.s.objects != nil {
		if where, err := (editResolver{objects: p.s.objects}).Locate(ctx, name, prereq.ApplicationKind); err == nil {
			create = createVerdict(ctx, probe.gate, where, found.Namespace)
		}
	}
	return prereqFacts(prerequisite, found, app, writable, note, create), nil
}

// prereqFacts composes the answer from what was read. Pure, so every state the
// page can draw is tested without a cluster.
//
// Installable needs all of: a writable cluster, Argo CD found once, no
// Application of this name there yet, and `create` not refused for the person.
// The reason names the first that fails, in that order.
func prereqFacts(p prereq.Prerequisite, found argoFinding, app *api.PrereqApplication,
	writable bool, readOnly string, create *api.CreateAffordance) api.Prereq {
	out := api.Prereq{
		Key: p.Key, Label: p.Label,
		Chart: api.PrereqChart{Repo: p.RepoURL, Name: p.Chart, Version: p.Version, AppVersion: p.AppVersion,
			Namespace: p.Namespace},
		Argo:        found.PrereqArgo,
		Application: app,
	}
	if found.State != api.ArgoFound {
		out.Helm = &api.PrereqHelm{Release: p.Chart, Repo: p.RepoURL, Chart: p.Chart, Version: p.Version,
			Namespace: p.Namespace}
	}
	because := func(code api.ReasonCode, message string) *api.Reason {
		reason := api.Because(code, message).Retry(false)
		return &reason
	}
	switch {
	case !writable:
		out.Reason = because(api.CodeVerbDenied, readOnly)
	case found.State == api.ArgoAbsent:
		out.Reason = because(api.CodeNotInstalled, "Argo CD is not installed: "+found.Note)
	case found.State == api.ArgoAmbiguous:
		out.Reason = because(api.CodeNotSupportedHere, "Argo CD runs in more than one namespace: "+found.Note)
	case found.State != api.ArgoFound:
		out.Reason = because(api.CodeAccessUnreviewable, found.Note)
	case app == nil || app.State == api.ApplicationUnknown:
		note := ""
		if app != nil {
			note = ": " + app.Note
		}
		out.Reason = because(api.CodeAccessUnreviewable, "whether the Application exists could not be read"+note)
	case app.State == api.ApplicationPresent:
		out.Reason = because(api.CodeNotSupportedHere,
			fmt.Sprintf("the Application %s already exists in %s", app.Name, app.Namespace))
	case create != nil && !create.Permitted:
		out.Reason = create.Reason
	default:
		out.Installable = true
		if create != nil {
			// The desktop's "the cluster decides" note, where there is one.
			out.Reason = create.Reason
		}
	}
	return out
}

// PreviewPrereq finds Argo CD again, then runs the installer's check: the
// Application exactly as it will be sent, through a dry run of the create
// reviewed for the person.
func (p apiPrereqs) PreviewPrereq(ctx context.Context, name, key string) (api.PlanFacts, error) {
	if p.s == nil || p.s.writer == nil {
		return api.PlanFacts{}, fmt.Errorf("%w: this build has no write path", api.ErrNotInProfile)
	}
	if _, ok := prereq.Lookup(key); !ok {
		return api.PlanFacts{}, fmt.Errorf("%w: %q is not something this console installs", api.ErrNotFound, key)
	}
	probe, err := p.probeFor(ctx, name)
	if err != nil {
		return api.PlanFacts{}, err
	}
	found := probe.find(ctx)
	if found.State != api.ArgoFound {
		return api.PlanFacts{}, fmt.Errorf("%w: nothing is installed without one Argo CD to install through "+
			"(Argo CD %s: %s)", api.ErrInvalid, found.State, found.Note)
	}
	plan, err := prereq.New(p.s.writer).Check(ctx, prereq.Request{
		Cluster: name, Prereq: key, ArgoNamespace: found.Namespace, Confirm: "",
	})
	if err != nil {
		return api.PlanFacts{}, writeError(err)
	}
	return prereqPlanFacts(plan), nil
}

// prereqPlanFacts is the confirmation: one Application, the YAML that is sent
// as `request`, and the Application's name typed back - an install puts a
// cluster-wide scanner on somebody's cluster, which a reflex click should not.
func prereqPlanFacts(plan prereq.Plan) api.PlanFacts {
	return api.PlanFacts{
		Verb: "install",
		// No note: the dry run's acceptance is what a plan existing means.
		Targets:     []api.PlanTarget{{Kind: prereq.ApplicationKind, Namespace: plan.ArgoNamespace, Name: plan.Name}},
		Count:       1,
		Destructive: false,
		Headline: fmt.Sprintf("Creates the Argo CD Application %s in %s, which installs %s %s into %s.",
			plan.Name, plan.ArgoNamespace, plan.Chart, plan.Version, plan.Namespace),
		Button:  "Install " + plan.ChartRef(),
		Request: plan.YAML,
		Confirmation: &api.Confirmation{Kind: "name", Expected: plan.Name,
			Label: "Type " + plan.Name + " to install it"},
	}
}

// InstallPrereq creates the Application in the namespace the confirmed plan
// named. The installer redoes every check first, dry run included; the
// handler has already compared a fresh preview with the plan.
func (p apiPrereqs) InstallPrereq(ctx context.Context, name, key, argoNamespace string) (api.ActOutcome, error) {
	if p.s == nil || p.s.writer == nil {
		return api.ActOutcome{}, fmt.Errorf("%w: this build has no write path", api.ErrNotInProfile)
	}
	done, err := prereq.New(p.s.writer).Install(ctx, prereq.Request{
		Cluster: name, Prereq: key, ArgoNamespace: argoNamespace, Confirm: confirmWord,
	})
	if err != nil {
		return api.ActOutcome{}, writeError(err)
	}
	target := api.PlanTarget{Kind: prereq.ApplicationKind, Namespace: done.ArgoNamespace, Name: done.Name}
	return api.ActOutcome{
		Succeeded: 1,
		Results:   []api.ActResult{{Target: target, OK: true, Message: done.Message}},
		Summary:   done.Message,
		Next: []api.Ref{{Text: done.Name,
			Href: detailURL(name, prereq.ApplicationKind, done.ArgoNamespace, done.Name)}},
	}, nil
}

// confirmWord is the word the installer checks: the handler has checked the
// typed name against the plan before it got here.
const confirmWord = actions.ConfirmToken
