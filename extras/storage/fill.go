package storage

import (
	"errors"
	"net/http"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/textutil"
)

// The lines the fill source says when its figures are not all there. Each
// names what grants the read: under the chart, `rbac.nodeProxy` is the switch
// that adds nodes/proxy to the console's role, off by default.
const (
	needsNodeProxy = "Fill unreadable · needs nodes/proxy (chart: rbac.nodeProxy)"
	kubeletsFailed = "Fill unreadable · the kubelets did not answer"
	needsNodes     = "Fill unreadable · needs list nodes"
	notAsked       = "Fill unreadable · not asked"
	someNodes      = "Fill partial · some kubelets did not answer"
)

// fill measures every claim a running pod mounts and grades it. The cluster's
// Prometheus (or VictoriaMetrics) is asked first (#91): the kubelets' own
// kubelet_volume_stats_* series, through Host.VolumeFill. The claims it holds
// no figure for are asked of the kubelets of the nodes their pods run on, as
// before. A claim no running pod mounts has no figure anywhere and is
// FillUnmounted; one nothing gave a figure for is FillUnread. Never a zero
// drawn as empty.
func fill(r *http.Request, h extension.Host, cluster string, claims []Claim, mounting map[string][]podInfo, incomplete func(string, string)) FillSource {
	for i := range claims {
		claims[i].Fill = Fill{State: FillUnmounted}
	}
	var refs []extension.ClaimRef
	for _, c := range claims {
		if running(mounting[c.Namespace+"/"+c.Name]) {
			refs = append(refs, extension.ClaimRef{Namespace: c.Namespace, Name: c.Name})
		}
	}
	if len(refs) == 0 {
		return FillSource{State: SourceNone}
	}

	// The metrics backend. Its answer is a state, never a failure of the
	// page: what it does not cover is the kubelets' to answer.
	var via []string
	covered := map[string]bool{}
	if got, err := h.VolumeFill(r, cluster, refs); err == nil && got.State == extension.WindowRead {
		byKey := map[string]extension.ClaimFill{}
		for _, c := range got.Claims {
			byKey[c.Namespace+"/"+c.Name] = c
		}
		for i := range claims {
			c := &claims[i]
			if f, ok := byKey[c.Namespace+"/"+c.Name]; ok && running(mounting[c.Namespace+"/"+c.Name]) {
				c.Fill = measure(f.UsedBytes, f.CapacityBytes, c.CapacityBytes, extension.Trustworthy)
				covered[c.Namespace+"/"+c.Name] = true
			}
		}
		if len(covered) > 0 {
			engine := got.Engine
			if engine == "" {
				engine = extension.EnginePrometheus
			}
			via = append(via, engine)
		}
		if !got.Complete {
			incomplete("Fill", got.Note)
		}
	}
	if len(covered) == len(refs) {
		return FillSource{State: SourceRead, Via: via}
	}

	src := kubeletFill(r, h, cluster, claims, mounting, covered, incomplete)
	if src.State != SourceUnreadable {
		src.Via = append(via, ViaKubelet)
	} else {
		src.Via = via
		// Some claims were measured from the backend: the rest is partial,
		// not the whole source unreadable.
		if len(covered) > 0 {
			src.State = SourcePartial
		}
	}
	return src
}

// kubeletFill measures the claims not covered, from the kubelets of the nodes
// their running pods are on, through nodes/proxy.
func kubeletFill(r *http.Request, h extension.Host, cluster string, claims []Claim, mounting map[string][]podInfo,
	covered map[string]bool, incomplete func(string, string)) FillSource {
	nodes := map[string]bool{}
	for _, c := range claims {
		if covered[c.Namespace+"/"+c.Name] {
			continue
		}
		for _, p := range mounting[c.Namespace+"/"+c.Name] {
			if p.running && p.node != "" {
				nodes[p.node] = true
			}
		}
	}
	asked := make([]string, 0, len(nodes))
	for n := range nodes {
		asked = append(asked, n)
	}
	sort.Strings(asked)
	unread := func() {
		for i := range claims {
			key := claims[i].Namespace + "/" + claims[i].Name
			if !covered[key] && running(mounting[key]) {
				claims[i].Fill = Fill{State: FillUnread}
			}
		}
	}

	usage, err := h.Usage(r, cluster, asked)
	if err != nil {
		unread()
		src := FillSource{State: SourceUnreadable, Asked: len(asked), Detail: shortError(err)}
		switch {
		case errors.Is(err, extension.ErrDenied):
			src.Message = needsNodes
		case errors.Is(err, extension.ErrUnreviewable):
			src.Message = notAsked
		default:
			src.Message = unreadableLine(err)
		}
		return src
	}
	src := FillSource{State: SourceRead, Asked: usage.Asked, Answered: usage.Answered}
	if !usage.Complete {
		incomplete("Fill", usage.Note)
	}
	measured := map[string]extension.FS{}
	for _, node := range usage.Nodes {
		for _, p := range node.Pods {
			for _, v := range p.Volumes {
				if v.PVCRef != "" {
					measured[v.PVCRef] = v.FS
				}
			}
		}
	}
	for i := range claims {
		c := &claims[i]
		key := c.Namespace + "/" + c.Name
		if covered[key] || !running(mounting[key]) {
			continue
		}
		fs, ok := measured[key]
		if !ok {
			c.Fill = Fill{State: FillUnread}
			continue
		}
		c.Fill = measure(fs.UsedBytes, fs.CapacityBytes, c.CapacityBytes, extension.Trustworthy)
	}
	switch {
	case usage.Answered == 0:
		src.State = SourceUnreadable
		src.Message = unreadableLine(usage.FirstErr)
		if usage.FirstErr != nil {
			src.Detail = shortError(usage.FirstErr)
		}
	case usage.Answered < usage.Asked:
		src.State = SourcePartial
		src.Message = someNodes
		if usage.FirstErr != nil {
			src.Detail = shortError(usage.FirstErr)
		}
	}
	return src
}

// unreadableLine tells a refusal - the proxy forbidden, which a grant fixes -
// from a failure - a kubelet that timed out or a node that is down, which no
// grant fixes. Two answers, never one (R5 §5).
func unreadableLine(err error) string {
	if err != nil && (apierrors.IsForbidden(err) || errors.Is(err, extension.ErrConsoleNotGranted)) {
		return needsNodeProxy
	}
	return kubeletsFailed
}

func running(pods []podInfo) bool {
	for _, p := range pods {
		if p.running && p.node != "" {
			return true
		}
	}
	return false
}

// shortError is an error's first line, bounded: a hover, not a dump.
func shortError(err error) string {
	if err == nil {
		return ""
	}
	return textutil.FirstLineN(err.Error(), 300)
}
