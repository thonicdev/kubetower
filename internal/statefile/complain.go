package statefile

// Complainer is a store's log, and the once-only half of it.
//
// Log is for something that happened once (a file that would not parse, an
// entry dropped). Once is for a standing condition: an unwritable state
// directory stays unwritable while the operator keeps dragging, and one line
// per attempt would bury the first, useful one. Clear, after a write that
// works, arms the next complaint, so a volume that comes back and goes again is
// reported again.
//
// A type rather than a bool per store because pairing the flag with its reset
// on success is the part easy to copy wrongly.
type Complainer struct {
	logf func(string, ...any)
	said bool
}

// NewComplainer wraps a log function, which may be nil: stores are built
// without a logger in tests, and a preference store must not panic.
func NewComplainer(logf func(string, ...any)) *Complainer {
	return &Complainer{logf: logf}
}

// Log says it every time.
func (c *Complainer) Log(format string, args ...any) {
	if c == nil || c.logf == nil {
		return
	}
	c.logf(format, args...)
}

// Once says it, and then says nothing more until Clear.
//
// The caller holds the store's lock.
func (c *Complainer) Once(format string, args ...any) {
	if c == nil || c.said {
		return
	}
	c.said = true
	c.Log(format, args...)
}

// Clear arms the next Once. Called after a write that worked.
func (c *Complainer) Clear() {
	if c != nil {
		c.said = false
	}
}
