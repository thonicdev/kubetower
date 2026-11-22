package overview

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery"
)

// panickyDiscovery is a discovery client with no ServerVersionWithContext - so
// discoverServerVersion takes its goroutine - whose ServerVersion panics, as
// a client handed an answer it did not expect would.
type panickyDiscovery struct {
	discovery.DiscoveryInterface
}

func (panickyDiscovery) ServerVersion() (*version.Info, error) { panic("an answer nobody expected") }

// TestAPanicAskingTheVersionIsThatReadsError: a panic on cluster.go's
// fallback goroutine must not end the process. It comes back as the read's
// error and the process - this test binary - lives, rather than dying with
// "panic: an answer nobody expected" from a goroutine nothing recovered.
func TestAPanicAskingTheVersionIsThatReadsError(t *testing.T) {
	var d discovery.DiscoveryInterface = panickyDiscovery{}
	if _, ok := d.(discovery.ServerVersionInterfaceWithContext); ok {
		t.Skip("this client-go's DiscoveryInterface carries ServerVersionWithContext, so the fallback goroutine is unreachable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := discoverServerVersion(ctx, d)
	if err == nil {
		t.Fatalf("a panicking read answered %+v and no error", info)
	}
	if ctx.Err() != nil {
		t.Fatalf("the read was waited out to its deadline rather than answered: %v", err)
	}
	if !strings.Contains(err.Error(), "an answer nobody expected") {
		t.Errorf("the error does not carry the panic: %v", err)
	}
}
