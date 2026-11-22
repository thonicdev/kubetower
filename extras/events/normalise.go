package events

import "strings"

// maxMessage is the most bytes of a normalized message kept as a cause's key.
const maxMessage = 240

// normalize is an Event's message with what is particular to one object
// replaced by `*`, so that the same fault on thirty pods is one cause:
//
//   - the involved object's own name (a pod's name sits in its BackOff
//     message, `... in pod crasher-7474496469-kzs5j_demo(<uid>)`);
//   - UIDs (8-4-4-4-12 hex);
//   - sha256 digests (`sha256:` and 64 hex);
//   - IPv4 addresses, with a port when there is one;
//   - every other run of digits.
//
// Adjacent replacements collapse into one. It is one pass over the bytes, not
// a chain of regular expressions: it runs once per Event.
func normalise(message, name string) string {
	if name != "" {
		message = strings.ReplaceAll(message, name, "*")
	}
	var b strings.Builder
	b.Grow(len(message))
	star := func() {
		if s := b.String(); len(s) == 0 || s[len(s)-1] != '*' {
			b.WriteByte('*')
		}
	}
	for i := 0; i < len(message); {
		c := message[i]
		switch {
		case isHex(c) && isUID(message[i:]):
			star()
			i += 36
		case c == 's' && strings.HasPrefix(message[i:], "sha256:") && isHexRun(message[i+7:], 64):
			star()
			i += 7 + 64
		case isDigit(c):
			j := i
			if end, ok := ipv4(message[i:]); ok {
				j = i + end
			} else {
				for j < len(message) && isDigit(message[j]) {
					j++
				}
			}
			star()
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	out := b.String()
	if len(out) > maxMessage {
		out = out[:maxMessage]
	}
	return out
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHex(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func isHexRun(s string, n int) bool {
	if len(s) < n {
		return false
	}
	for i := 0; i < n; i++ {
		if !isHex(s[i]) {
			return false
		}
	}
	return true
}

// isUID is whether s starts with a UID: 8-4-4-4-12 hex digits.
func isUID(s string) bool {
	if len(s) < 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	for i := 0; i < 36; i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !isHex(s[i]) {
			return false
		}
	}
	return true
}

// ipv4 is the length of an IPv4 address (with an optional :port) at the start
// of s, and whether there is one.
func ipv4(s string) (int, bool) {
	i := 0
	for group := 0; group < 4; group++ {
		if group > 0 {
			if i >= len(s) || s[i] != '.' {
				return 0, false
			}
			i++
		}
		start := i
		for i < len(s) && isDigit(s[i]) && i-start < 3 {
			i++
		}
		if i == start {
			return 0, false
		}
	}
	// A fifth digit or group means this was a longer number, not an address.
	if i < len(s) && (isDigit(s[i]) || (s[i] == '.' && i+1 < len(s) && isDigit(s[i+1]))) {
		return 0, false
	}
	if i < len(s) && s[i] == ':' {
		j := i + 1
		for j < len(s) && isDigit(s[j]) {
			j++
		}
		if j > i+1 {
			i = j
		}
	}
	return i, true
}
