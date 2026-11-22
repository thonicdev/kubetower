package web

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/safego"
)

// errNobodyForTheConsole is a console tunnel asked for a request that carries
// nobody, under the server profile: there is no person to act for, and so no
// one for the audit record to name.
var errNobodyForTheConsole = errors.New("nobody is signed in on this request, so the console opens no tunnel on anyone's behalf")

// ForwardAsConsole is Forward dug as the console (access.AsConsole), for a
// backend the console talks to on the person's behalf: argocd-server, the
// KubeCron collector. It is the pattern of the console's own Prometheus tunnel
// (internal/metrics/prom.go), handed to an extension.
//
// **Everything Forward does still happens, as the console.** The forwarder
// reads the Service and its pods and digs the tunnel inside Gate.Perform, as
// `create pods/portforward` on the pod it lands on. The gate reviews it as the
// console's own credentials and records it as the console's, with the person
// ctx carries as `onBehalfOf` (Gate.Record). A review the console's
// ServiceAccount is refused, or one that cannot be made, opens nothing.
//
// **Under the server profile a request with nobody on it is refused first**:
// AsConsole would otherwise review it as the console and record it as nobody's.
// On the desktop there is always somebody behind a request, and the console's
// credentials are the person's own kubeconfig.
//
// **The slot is the person's**: AsConsole leaves the identity on ctx, so the
// counted tunneller (tunnelcap.go) takes it from the same ceiling and the same
// per-person share as Forward.
//
// **It ends with r at the latest.** What comes down it is the console's read,
// and the extension serves it only to the person whose request it is for; a
// tunnel kept past that request would be the console's socket held for
// nobody. Stop releases it sooner, and either way only once.
func (h host) ForwardAsConsole(r *http.Request, name, namespace, service string, port int) (extension.Tunnel, error) {
	if r == nil {
		return extension.Tunnel{}, errors.New("a tunnel opened as the console needs the request it is opened for")
	}
	ctx := r.Context()
	if _, ok := identity.From(ctx); !ok && h.s != nil && h.s.inCluster {
		return extension.Tunnel{}, errNobodyForTheConsole
	}
	tunnel, err := h.Forward(access.AsConsole(ctx), name, namespace, service, port)
	if err != nil {
		return extension.Tunnel{}, err
	}
	var once sync.Once
	stop := tunnel.Stop
	closeOnce := func() { once.Do(stop) }
	// Through safego: context.AfterFunc runs closeOnce on a goroutine of its
	// own, where a panic in the tunnel's teardown would end the process.
	unhook := context.AfterFunc(ctx, func() {
		_ = safego.Call("console tunnel: closing it as its request ends", func() error {
			closeOnce()
			return nil
		})
	})
	tunnel.Stop = func() {
		unhook()
		closeOnce()
	}
	return tunnel, nil
}
