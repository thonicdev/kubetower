// What bounds a namespace: its ResourceQuotas and its LimitRanges.
//
// Both are objects in the namespace, so the page lists them and draws each as a
// row under Related, with the lists' own figures from internal/resources
// (`pods 2/10`, `Container/cpu min=50m max=2 def=200m`). Something is rows;
// nothing is no row, since no quota is the ordinary case; a refused list is a
// sentence in the relations note, since an unread list does not prove the
// namespace unbounded.
package detail

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/resources"
)

// maxNamespaceBounds caps each of the two lists. A namespace holds a handful of
// quotas at most; past this the operator is better served by the list page.
const maxNamespaceBounds = 20

// addNamespaceBounds lists the quotas and limit ranges in namespace and adds a
// row for each. It never fails the panel: a refused list is a note.
func (r *Reader) addNamespaceBounds(ctx context.Context, client *cluster.Client, set *relationSet, namespace string) {
	if namespace == "" {
		return
	}
	var refused []string
	var reason error

	refuse := func(what string, err error) {
		refused = append(refused, what)
		if reason == nil {
			reason = err
		}
	}

	if quotas, more, err := r.listQuotas(ctx, client, namespace); err != nil {
		refuse("ResourceQuotas", err)
	} else {
		for _, q := range quotas {
			detail := strings.Join(resources.QuotaFigures(&q), ", ")
			if detail == "" {
				detail = "no hard limits"
			}
			set.add(Relation{
				Role: RoleResourceQuota, Kind: resResourceQuotas, Namespace: namespace, Name: q.Name,
				Detail: detail,
			})
		}
		if more {
			set.note(fmt.Sprintf("The namespace holds more than %d ResourceQuotas; the first %d are listed.",
				maxNamespaceBounds, maxNamespaceBounds))
		}
	}

	if ranges, more, err := r.listLimitRanges(ctx, client, namespace); err != nil {
		refuse("LimitRanges", err)
	} else {
		for _, l := range ranges {
			detail := strings.Join(resources.LimitRangeFigures(&l), ", ")
			if detail == "" {
				detail = "no constraints"
			}
			set.add(Relation{
				Role: RoleLimitRange, Kind: resLimitRanges, Namespace: namespace, Name: l.Name,
				Detail: detail,
			})
		}
		if more {
			set.note(fmt.Sprintf("The namespace holds more than %d LimitRanges; the first %d are listed.",
				maxNamespaceBounds, maxNamespaceBounds))
		}
	}

	// One sentence for both refusals: an identity refused one list is usually
	// refused the other for the same reason.
	if len(refused) > 0 {
		set.note(andList(refused) + " in this namespace could not be listed: " + firstLine(reason.Error()) + ".")
	}
}

// listQuotas is one list in the namespace, reviewed for the person behind ctx.
// more reports that the cap cut it.
func (r *Reader) listQuotas(ctx context.Context, client *cluster.Client, namespace string) (items []corev1.ResourceQuota, more bool, err error) {
	if client == nil || client.Clients == nil {
		return nil, false, fmt.Errorf("no client for cluster")
	}
	question := access.Attributes{Verb: "list", Version: "v1", Resource: resResourceQuotas, Namespace: namespace}
	_, err = r.gate.Read(ctx, client.Context, question, func(ctx context.Context) error {
		list, err := client.Clients.CoreV1().ResourceQuotas(namespace).List(ctx, metav1.ListOptions{
			// One more than the cap tells "there are more" without a count.
			Limit: maxNamespaceBounds + 1,
		})
		if err != nil {
			return err
		}
		items = list.Items
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if len(items) > maxNamespaceBounds {
		return items[:maxNamespaceBounds], true, nil
	}
	return items, false, nil
}

// listLimitRanges is listQuotas for the other kind.
func (r *Reader) listLimitRanges(ctx context.Context, client *cluster.Client, namespace string) (items []corev1.LimitRange, more bool, err error) {
	if client == nil || client.Clients == nil {
		return nil, false, fmt.Errorf("no client for cluster")
	}
	question := access.Attributes{Verb: "list", Version: "v1", Resource: resLimitRanges, Namespace: namespace}
	_, err = r.gate.Read(ctx, client.Context, question, func(ctx context.Context) error {
		list, err := client.Clients.CoreV1().LimitRanges(namespace).List(ctx, metav1.ListOptions{
			Limit: maxNamespaceBounds + 1,
		})
		if err != nil {
			return err
		}
		items = list.Items
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if len(items) > maxNamespaceBounds {
		return items[:maxNamespaceBounds], true, nil
	}
	return items, false, nil
}
