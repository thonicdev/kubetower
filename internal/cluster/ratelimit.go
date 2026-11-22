package cluster

import (
	"k8s.io/client-go/util/flowcontrol"

	"github.com/thonicdev/kubetower/internal/access"
)

// One rate limiter per cluster, shared by every client built for it.
//
// A fresh client from the same config shares client-go's cached transport and
// exec credential, but mints **its own rate limiter** when `RateLimiter` is nil
// (rest.RESTClientForConfigAndClient, kubernetes.NewForConfigAndClient), and
// `rest.CopyConfig` copies the field. Left nil, `clientQPS` would apply per
// client rather than per cluster. Setting it once per context makes the
// ceiling a property of the cluster. The streaming client shares it too: a
// bucket is charged when a request starts, so a followed log stream costs one
// token.

// defaultLimiter is the real one. A function, because each context needs its
// own bucket: one shared value would let a busy cluster throttle a quiet one.
func defaultLimiter() flowcontrol.RateLimiter {
	return flowcontrol.NewTokenBucketRateLimiter(clientQPS, clientBurst)
}

// limiterFor returns the bucket this context's clients share, making it on first
// use.
//
// Reload keeps it: a reload re-reads addresses and credentials, which the
// ceiling is not about, and a fresh bucket would hand out a fresh full burst.
// One entry per context ever seen, bounded by the kubeconfig.
func (m *Manager) limiterFor(context string) flowcontrol.RateLimiter {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.limiters == nil {
		m.limiters = map[string]flowcontrol.RateLimiter{}
	}
	if existing, ok := m.limiters[context]; ok {
		return existing
	}
	build := m.newLimiter
	if build == nil {
		build = defaultLimiter
	}
	// Marked by access.Limiter, so a reviewed write the bucket held back is known
	// not to have been sent: client-go wraps the refusal in an error that would
	// otherwise read as the API server's answer.
	limiter := access.Limiter(build())
	m.limiters[context] = limiter
	return limiter
}
