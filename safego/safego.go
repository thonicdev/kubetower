// Package safego is the one way this console starts a goroutine that runs
// cluster-derived work: anything whose input came from a cluster and could be
// shaped in a way nobody tested.
//
// A panic on a request's own goroutine is caught by net/http and costs one
// request. A panic on a goroutine that request started is caught by nobody and
// **ends the process**: under `--incluster`, every person's console at once.
// guard_test.go therefore refuses a `go` statement outside this package unless
// it is allowlisted there with its reason, and refuses the calls that start a
// goroutine without one (`time.AfterFunc`, `context.AfterFunc`, a group's
// `.Go` or `.TryGo`, apimachinery's `wait.Group`) as well as `wait.*Until*`
// and `wait.Forever`, whose HandleCrash re-panics. The guard is syntactic; what
// it does not see is listed in countUnrecoveredCalls.
//
// A recovered panic is logged **once** with its stack and handed back as a
// *Panic, so the caller turns it into that task's failure. It never re-panics.
package safego

import (
	"fmt"
	"log"
	"runtime/debug"
	"time"
)

// Panic is a recovered panic, as an error. What names the work, Value is what
// was panicked with. The stack goes to the log, not into the error: an error
// travels to a page, where a stack trace is a map of the binary.
type Panic struct {
	What  string
	Value any
}

// Error names the work and the panic value, without the stack.
func (p *Panic) Error() string {
	return fmt.Sprintf("%s stopped on an internal error (a panic, recovered and logged): %v", p.What, p.Value)
}

// logf is where the one line goes. A variable so a test can watch it.
var logf = log.Printf

// Call runs f on the caller's goroutine and returns its error, or a *Panic if
// it panicked. The panic is logged once, with its stack.
//
// `what` is a short description of the work ("overview: the machines"), never
// a value read from a cluster.
func Call(what string, f func() error) (err error) {
	defer func() {
		if crash := recover(); crash != nil {
			logf("panic: %s: %v (recovered; the process goes on)\n%s", what, crash, debug.Stack())
			err = &Panic{What: what, Value: crash}
		}
	}()
	return f()
}

// Go runs f on a new goroutine, through Call, and returns at once.
//
// done, when not nil, is called exactly once on that goroutine after f has
// returned or panicked, with what Call returned. It is where a caller releases
// what it holds (a WaitGroup, a waiters' channel) **after** the error is known,
// so nobody released early reads a result the panic never wrote. A panic in
// done is recovered and logged too.
func Go(what string, f func() error, done func(error)) {
	go func() {
		err := Call(what, f)
		if done != nil {
			_ = Call(what+" (after)", func() error { done(err); return nil })
		}
	}()
}

// AfterFunc is time.AfterFunc through Call: a panic in f is recovered and
// logged once instead of ending the process. The returned timer is
// time.AfterFunc's, so Stop and Reset keep their meaning. What a panic leaves
// behind (a lock held, a slot not given back) is still f's to avoid.
func AfterFunc(what string, d time.Duration, f func()) *time.Timer {
	return time.AfterFunc(d, func() {
		_ = Call(what, func() error { f(); return nil })
	})
}
