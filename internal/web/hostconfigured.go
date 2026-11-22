package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/registry"
)

// The two doors through which an extension reads, with the console's own
// credentials, what the console's configuration names: the token and the
// Service of a backend the console talks to on the person's behalf (Argo CD's
// server, the KubeCron collector).
//
// Everything else an extension reads goes through a door reviewed for the
// person (List, Reveal, Usage...), and Client answers discovery only. These two
// are the exception, and three things hold them to it:
//
//  1. **The name is the configuration's**: the extension's
//     extension.Configured answer for that cluster, made from the environment,
//     the cluster's settings and its defaults. Anything else is
//     extension.ErrNotConfigured before anything is reviewed or read.
//  2. **Read as the console** (access.AsConsole): reviewed as the console's own
//     credentials, as ForwardAsConsole's tunnel is, and under the server profile
//     refused for a request with nobody signed in.
//  3. **A Secret's read is recorded** as a reveal by the console on the person's
//     behalf, attempt before the read; the value is in no record, line or
//     error.

// configuration is what the extension behind h names on cluster: nothing for
// the door with no extension behind it, or an extension that does not
// implement extension.Configured.
func (h host) configuration(r *http.Request, cluster string) extension.Configuration {
	if h.ext == "" {
		return extension.Configuration{}
	}
	for _, e := range registry.Extensions() {
		if e.Key() != h.ext {
			continue
		}
		configured, ok := e.(extension.Configured)
		if !ok {
			return extension.Configuration{}
		}
		return configured.Configured(cluster, func(key string) string { return h.Setting(r, cluster, key) })
	}
	return extension.Configuration{}
}

// names is whether namespace/name is among objects. Exact, as Kubernetes
// names are: a case or a space of difference is another object.
func names(objects []extension.ObjectName, namespace, name string) bool {
	for _, o := range objects {
		if o.Namespace == namespace && o.Name == name {
			return true
		}
	}
	return false
}

// asConsoleFor is r's context marked to be reviewed and recorded as the
// console, or the refusal of a request with nobody on it under the server
// profile: AsConsole would otherwise review it as the console and record it as
// nobody's.
func (h host) asConsoleFor(r *http.Request) (context.Context, error) {
	if r == nil {
		return nil, errors.New("a read made as the console needs the request it is made for")
	}
	ctx := r.Context()
	if _, ok := identity.From(ctx); !ok && h.s != nil && h.s.inCluster {
		return nil, errors.New("nobody is signed in on this request, so the console reads nothing on anyone's behalf")
	}
	return access.AsConsole(ctx), nil
}

// ConfiguredSecret reads one key of one Secret the extension's configuration
// names, as the console, recorded. See extension.Host.ConfiguredSecret.
func (h host) ConfiguredSecret(r *http.Request, name, namespace, secret, key string) (string, error) {
	if h.ext == "" {
		return "", errors.New("ConfiguredSecret is answered only to a mounted extension, so the read can name who asked; " +
			"this door was handed to none")
	}
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(secret) == "" || key == "" {
		return "", fmt.Errorf("a configured Secret is one key of one Secret in one namespace; asked for key %q of %q in %q",
			key, secret, namespace)
	}
	client, err := h.client(r, name)
	if err != nil {
		return "", err
	}
	if !names(h.configuration(r, name).Secrets, namespace, secret) {
		return "", fmt.Errorf("%w: the Secret %s in namespace %s is not one %s reads on cluster %q",
			extension.ErrNotConfigured, secret, namespace, h.ext, client.Context)
	}
	ctx, err := h.asConsoleFor(r)
	if err != nil {
		return "", fmt.Errorf("%w: %w", extension.ErrRevealUnreviewed, err)
	}
	attrs := access.Attributes{Verb: "get", Version: "v1", Resource: "secrets", Namespace: namespace, Name: secret}
	answer := h.s.gate.Review(ctx, client.Context, attrs)

	record := h.s.gate.Record(ctx, audit.Reveal, client.Context, attrs, answer.Decision)
	record.Key = key
	record.Extension = h.ext
	pending, unrecorded := h.s.gate.Begin(ctx, record)
	said := revealSaid{ctx: ctx, s: h.s, cluster: client.Context, attrs: attrs, key: key, ext: h.ext, console: true}

	if !answer.Allowed() && !h.s.gate.LetsThrough(answer) {
		h.s.gate.End(ctx, pending, audit.Refused, "")
		said.line(answer.Decision, "refused", "")
		return "", refusedReveal(&access.Refused{Cluster: client.Context, Attributes: attrs, Answer: answer})
	}
	if unrecorded != nil {
		said.line(answer.Decision, "refused", "unrecorded")
		return "", fmt.Errorf("%w: %w", extension.ErrRevealUnreviewed, unrecorded)
	}

	var data map[string][]byte
	read := false
	_, err = h.s.gate.Read(ctx, client.Context, attrs, func(ctx context.Context) error {
		read = true
		got, err := client.Clients.CoreV1().Secrets(namespace).Get(ctx, secret, metav1.GetOptions{})
		if err != nil {
			return err
		}
		data = got.Data
		return nil
	})

	var refused *access.Refused
	switch {
	case !read && errors.As(err, &refused):
		h.s.gate.End(ctx, pending, audit.Refused, "")
		said.line(refused.Answer.Decision, "refused", "")
		return "", refusedReveal(refused)
	case apierrors.IsNotFound(err):
		h.s.gate.End(ctx, pending, audit.Failed, string(metav1.StatusReasonNotFound))
		said.line(answer.Decision, "absent", string(metav1.StatusReasonNotFound))
		return "", fmt.Errorf("%w: %s in namespace %s on cluster %q", extension.ErrNoSuchSecret, secret, namespace, client.Context)
	case apierrors.IsForbidden(err):
		// The review let it through (the desktop, unreviewable) and the API
		// server said no: the console's own credentials may not read it.
		h.s.gate.End(ctx, pending, audit.Failed, string(metav1.StatusReasonForbidden))
		said.line(answer.Decision, "refused", string(metav1.StatusReasonForbidden))
		return "", fmt.Errorf("%w: the console's credentials may not get the Secret %s in namespace %s on cluster %q",
			extension.ErrRevealDenied, secret, namespace, client.Context)
	case err != nil:
		word := access.FailureWord(err)
		h.s.gate.End(ctx, pending, audit.Failed, word)
		said.line(answer.Decision, "failed", word)
		return "", fmt.Errorf("the Secret %s in namespace %s on cluster %q could not be read: %w", secret, namespace, client.Context, err)
	}
	value, ok := data[key]
	if !ok {
		h.s.gate.End(ctx, pending, audit.Failed, noKey)
		said.line(answer.Decision, "absent", noKey)
		return "", fmt.Errorf("%w: %q in Secret %s in namespace %s", extension.ErrNoSuchKey, key, secret, namespace)
	}
	h.s.gate.End(ctx, pending, audit.Applied, "")
	said.line(answer.Decision, "ok", "")
	return string(value), nil
}

// ConfiguredService reads one Service the extension's configuration names, as
// the console. See extension.Host.ConfiguredService.
func (h host) ConfiguredService(r *http.Request, name, namespace, service string) (corev1.Service, error) {
	if h.ext == "" {
		return corev1.Service{}, errors.New("ConfiguredService is answered only to a mounted extension; this door was handed to none")
	}
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(service) == "" {
		return corev1.Service{}, fmt.Errorf("a configured Service is one Service in one namespace; asked for %q in %q", service, namespace)
	}
	client, err := h.client(r, name)
	if err != nil {
		return corev1.Service{}, err
	}
	if !names(h.configuration(r, name).Services, namespace, service) {
		return corev1.Service{}, fmt.Errorf("%w: the Service %s in namespace %s is not one %s reads on cluster %q",
			extension.ErrNotConfigured, service, namespace, h.ext, client.Context)
	}
	ctx, err := h.asConsoleFor(r)
	if err != nil {
		return corev1.Service{}, &access.Refused{Cluster: client.Context,
			Attributes: access.Attributes{Verb: "get", Version: "v1", Resource: "services", Namespace: namespace, Name: service},
			Answer:     access.Answer{Decision: access.Unreviewable, Subject: access.Nobody, Reason: err.Error()}}
	}
	attrs := access.Attributes{Verb: "get", Version: "v1", Resource: "services", Namespace: namespace, Name: service}
	var got *corev1.Service
	_, err = h.s.gate.Read(ctx, client.Context, attrs, func(ctx context.Context) error {
		svc, err := client.Clients.CoreV1().Services(namespace).Get(ctx, service, metav1.GetOptions{})
		got = svc
		return err
	})
	switch {
	case apierrors.IsNotFound(err):
		return corev1.Service{}, fmt.Errorf("%w: %s in namespace %s on cluster %q", extension.ErrNoSuchService, service, namespace, client.Context)
	case apierrors.IsForbidden(err):
		// Let through by the review (the desktop, unreviewable) and refused by
		// the API server: the console's credentials may not read it.
		return corev1.Service{}, &access.Refused{Cluster: client.Context, Attributes: attrs,
			Answer: access.Answer{Decision: access.Denied, Subject: access.Console, Reason: err.Error()}}
	case err != nil:
		return corev1.Service{}, err
	case got == nil:
		return corev1.Service{}, fmt.Errorf("the Service %s in namespace %s on cluster %q could not be read", service, namespace, client.Context)
	}
	return *got, nil
}
