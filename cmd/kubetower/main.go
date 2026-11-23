// Command kubetower is the console, with every extra it ships.
//
// All start-up is app.Run; this main only hands it the extras this build
// carries. The list is written out here rather than built by self-registration
// so that one place, read by a reviewer, says what ships. Keep it this small:
// anything else belongs in internal/launch, behind app.Run.
package main

import (
	"context"
	"log"
	"os"

	"github.com/thonicdev/kubetower/app"
	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/extras/argocd"
	"github.com/thonicdev/kubetower/extras/certmanager"
	"github.com/thonicdev/kubetower/extras/cnpg"
	"github.com/thonicdev/kubetower/extras/dns"
	"github.com/thonicdev/kubetower/extras/events"
	"github.com/thonicdev/kubetower/extras/kubeadjust"
	"github.com/thonicdev/kubetower/extras/kubecron"
	"github.com/thonicdev/kubetower/extras/netinspect"
	"github.com/thonicdev/kubetower/extras/pending"
	"github.com/thonicdev/kubetower/extras/redis"
	"github.com/thonicdev/kubetower/extras/runtimeinspect"
	"github.com/thonicdev/kubetower/extras/security"
)

// extras is what this binary carries, in menu order. It names the same keys as
// web/app/extras/index.ts. A function rather than a variable so the test reads
// the list main hands over without starting anything.
func extras() []extension.Extension {
	return []extension.Extension{
		certmanager.New(),
		argocd.New(),
		events.New(),
		dns.New(),
		runtimeinspect.New(),
		kubeadjust.New(),
		kubecron.New(),
		netinspect.New(),
		pending.New(),
		cnpg.New(),
		redis.New(),
		security.New(),
	}
}

func main() {
	// Called on main's own goroutine: the desktop window's message loop is
	// bound to the thread the process started on.
	if err := app.Run(context.Background(), app.Options{Args: os.Args[1:]}, extras()...); err != nil {
		log.Fatalf("[kubetower] %v", err)
	}
}
