// Package textutil holds the string trimming this console does on its way to a
// screen, a log line or a JSONL record.
//
// One implementation, so every caller agrees on the cases that matter: a
// `\r`-only line break is a break, a long admission-webhook denial is bounded
// before it reaches a table cell, and a bound counts runes rather than bytes.
// It is its own package because packages that must not import each other need
// it. Nothing here reaches a cluster and nothing here has state.
package textutil

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Ellipsis is what a truncation leaves behind. One rune, so a caller reasoning
// about width can count it as one.
const Ellipsis = "…"

// FirstLine reduces a multi-line message to its first line, trimmed.
//
// `\r` counts as a break: an error from a Windows-authored controller, or from
// a CRLF log stream, has its break there. Uncapped; see FirstLineN and Clip.
func FirstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		s = s[:i]
	}
	return s
}

// FirstLineN is FirstLine with a ceiling of max runes.
//
// The bound is in **runes, not bytes**: cutting bytes splits a CJK character
// or an emoji, which renders as a replacement glyph and would be written into
// the event archive as such. A max of zero or less means no ceiling.
func FirstLineN(s string, max int) string {
	return Clip(FirstLine(s), max)
}

// Clip shortens a string to max runes, marking that it did so.
//
// The ellipsis is *inside* the budget: Clip(s, 10) never returns more than ten
// runes, so a column sized to its bound never overflows by one character.
func Clip(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	if max == 1 {
		return Ellipsis
	}
	keep := max - 1
	for i := range s {
		if keep == 0 {
			return s[:i] + Ellipsis
		}
		keep--
	}
	return s
}

// Bytes is a size as a page prints it: binary units, one decimal above a
// kibibyte (`8.0 GiB`), and a plain byte count below one.
//
// The largest unit is TiB, so 1 PiB reads `1024.0 TiB`. A negative clamps to
// `0 B`: a size is never negative, but the value can be when a walk races a
// delete.
func Bytes(n int64) string { return bytes(n, false) }

// BytesCompact is Bytes without a trailing ".0" (`8 GiB`), for a figure a
// reader compares at a glance rather than adds up.
func BytesCompact(n int64) string { return bytes(n, true) }

// byteUnits is indexed by the exponent the loop in bytes stops at, so its
// length is the ceiling.
var byteUnits = [...]string{"KiB", "MiB", "GiB", "TiB"}

func bytes(n int64, compact bool) string {
	const unit = 1024
	if n < 0 {
		n = 0
	}
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for size := n / unit; size >= unit && exp < len(byteUnits)-1; size /= unit {
		div *= unit
		exp++
	}
	value := strconv.FormatFloat(float64(n)/float64(div), 'f', 1, 64)
	if compact {
		value = strings.TrimSuffix(value, ".0")
	}
	return value + " " + byteUnits[exp]
}
