package api

import (
	"encoding/json"
	"strconv"
	"time"
)

// The functions and methods the payload types carry; the types_*.go files hold
// declarations only.

// MarshalJSON writes the list's own arm, with `readAt` where there is one.
func (n NamespaceList) MarshalJSON() ([]byte, error) {
	state, reason, items := n.arm()
	return json.Marshal(struct {
		State  State        `json:"state"`
		Reason *Reason      `json:"reason,omitempty"`
		Items  *[]Namespace `json:"items,omitempty"`
		ReadAt *time.Time   `json:"readAt,omitempty"`
	}{State: state, Reason: reason, Items: items, ReadAt: n.readAt})
}

// MarshalJSON writes the list's own arm, with `accessNote` where the list was
// narrowed and the column arrangement beside the rows. A failed list carries
// neither: it has no rows to qualify or to draw.
func (h HelmList) MarshalJSON() ([]byte, error) {
	state, reason, items := h.arm()
	note := h.accessNote
	columns := h.columns
	customised := h.columnsCustomised
	if items == nil {
		note = ""
		columns = nil
		customised = false
	}
	return json.Marshal(struct {
		State             State          `json:"state"`
		Reason            *Reason        `json:"reason,omitempty"`
		Items             *[]HelmRelease `json:"items,omitempty"`
		AccessNote        string         `json:"accessNote,omitempty"`
		Columns           []Column       `json:"columns,omitempty"`
		ColumnsCustomised bool           `json:"columnsCustomised,omitempty"`
	}{State: state, Reason: reason, Items: items, AccessNote: note, Columns: columns, ColumnsCustomised: customised})
}

// helmList turns a backend answer into the envelope.
func helmList(reading HelmReleaseReading, f *Failure) HelmList {
	return HelmList{
		List:              listOf(reading.Releases, f),
		accessNote:        reading.AccessNote,
		columns:           reading.Columns,
		columnsCustomised: reading.ColumnsCustomised,
	}
}

// namespaceList turns a backend answer into the envelope, the one place the
// answers are told apart:
//
//	a failure                 → that state, a reason, no items
//	stale, with names         → ok, the names, and `stale_reading` beside them
//	stale, with no names      → the failure it is stale because of
//	read, with names          → ok
//	read, with no names       → empty
//
// A kept reading with nothing in it is not a kept reading: it is the failure.
func namespaceList(reading NamespaceReading, f *Failure) NamespaceList {
	out := NamespaceList{}
	if !reading.ReadAt.IsZero() {
		stamp := reading.ReadAt
		out.readAt = &stamp
	}
	items := make([]Namespace, 0, len(reading.Names))
	for _, name := range reading.Names {
		items = append(items, Namespace{Name: name})
	}
	switch {
	case f != nil:
		out.List = ListFailed[Namespace](f.State, f.Reason)
		// A failed read has no moment of its own to report.
		out.readAt = nil
	case reading.Stale && len(items) > 0:
		out.List = Kept(items, staleBecause(reading.Why))
	case reading.Stale:
		fallback := reading.Why
		if fallback == nil {
			fallback = Unreadable("this cluster's namespaces could not be read")
		}
		out.List = ListFailed[Namespace](fallback.State, fallback.Reason)
		out.readAt = nil
	default:
		out.List = Items(items)
	}
	return out
}

// staleBecause words the qualifying reason so that "kept from earlier" still
// says what stopped working.
func staleBecause(why *Failure) Reason {
	reason := Because(CodeStaleReading,
		"this cluster has stopped answering; these namespaces were read earlier").Retry(true)
	if why != nil && why.Reason.Message != "" {
		reason = reason.WithDetail(why.Reason.Message)
	}
	return reason
}

// HelmRevisionSecret is the Secret Helm stores one revision in. A reveal of its
// values or notes is reviewed and recorded as `get` on it, by name.
func HelmRevisionSecret(release string, revision int) string {
	return "sh.helm.release.v1." + release + ".v" + strconv.Itoa(revision)
}

// normalized fills the two members the schema requires and a backend may leave
// nil, so the front end always gets an array and an object, never `null`. Done
// here so no backend has to remember it.
func (p Preferences) normalised() Preferences {
	// Rebuilt rather than patched in place: the store may still hold the slice,
	// and handlers run concurrently.
	bars := make([][]string, 0, len(p.Rail.Bars))
	for _, bar := range p.Rail.Bars {
		if bar == nil {
			bar = []string{}
		}
		bars = append(bars, bar)
	}
	p.Rail.Bars = bars
	if p.Columns.Kinds == nil {
		p.Columns.Kinds = map[string]KindColumns{}
	}
	return p
}

// ReadingOf builds a Reading from a float that may not be finite and may not be
// trustworthy. Upstream sources encode a gap as NaN and `encoding/json` refuses
// to marshal NaN or an infinity, so one bad sample would fail the whole
// response; the conversion to null happens here, in one place.
func ReadingOf(value float64, trustworthy bool, text string) Reading {
	r := Reading{Trustworthy: trustworthy, Text: text}
	if trustworthy && isFinite(value) {
		v := value
		r.Value = &v
		return r
	}
	// Not trustworthy, or not a number at all: null, and the flag says which.
	r.Trustworthy = trustworthy && isFinite(value)
	return r
}

// NoReading is a measurement that could not be taken. Null and untrustworthy —
// never 0, which reads as an idle machine.
func NoReading(text string) Reading { return Reading{Trustworthy: false, Text: text} }

// Sample is one point of a Series, null where there is no sample.
func Sample(value float64) *float64 {
	if !isFinite(value) {
		return nil
	}
	v := value
	return &v
}

func isFinite(f float64) bool {
	// A value not equal to itself is NaN; beyond ±1e308 is treated as
	// infinite. Both fail encoding/json.
	return f == f && f < 1e308 && f > -1e308
}

// boolPtr, intPtr and strPtr exist because the spec has optional booleans whose
// false is meaningful, and `omitempty` cannot tell "false" from "not said".
func boolPtr(b bool) *bool    { return &b }
func intPtr(i int) *int       { return &i }
func strPtr(s string) *string { return &s }

// normalized is the shape the contract requires: favorites an object, never
// null, and no cluster holding a null list.
func (p KindPreferences) normalised() KindPreferences {
	out := KindPreferences{ShowUnused: p.ShowUnused, Favourites: make(map[string][]string, len(p.Favourites))}
	for cluster, kinds := range p.Favourites {
		if kinds == nil {
			kinds = []string{}
		}
		out.Favourites[cluster] = kinds
	}
	return out
}
