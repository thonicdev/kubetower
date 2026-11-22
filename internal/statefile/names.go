package statefile

// The four operations an ordered list of names needs: the hotbars' context
// names and the columns' titles are both rearranged by one gesture, a drag that
// ends "beside that one, on this side", so both use these and cannot drift.
//
// Remove allocates rather than filtering in place: an in-place filter writes
// over its input, which quietly corrupts a layout the caller is also reading.

// Index is where want sits, or -1.
func Index(names []string, want string) int {
	for i, name := range names {
		if name == want {
			return i
		}
	}
	return -1
}

// Contains is Index without the arithmetic.
func Contains(names []string, want string) bool { return Index(names, want) >= 0 }

// Remove is names without want (every occurrence), in a fresh slice; the input
// is left alone.
func Remove(names []string, want string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name != want {
			out = append(out, name)
		}
	}
	return out
}

// RemoveOne takes out the first occurrence and leaves any others alone.
//
// Remove takes out every occurrence, which is right for a set of names and
// wrong for an ordered list that may repeat: moving one of two columns titled
// `Status` (a CRD's printer columns may) with Remove and Insert would lose a
// column. What this guarantees is the count.
func RemoveOne(names []string, want string) []string {
	for i, name := range names {
		if name != want {
			continue
		}
		out := make([]string, 0, len(names)-1)
		out = append(out, names[:i]...)
		return append(out, names[i+1:]...)
	}
	return names
}

// Insert puts want at index at, which may be len(names).
//
// Out of range is clamped rather than refused: the index comes from a list the
// caller has just edited, and a layout one place off beats a panic in a
// handler.
func Insert(names []string, at int, want string) []string {
	if at < 0 {
		at = 0
	}
	if at > len(names) {
		at = len(names)
	}
	out := make([]string, 0, len(names)+1)
	out = append(out, names[:at]...)
	out = append(out, want)
	return append(out, names[at:]...)
}
