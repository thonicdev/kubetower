package edit

import (
	"encoding/base64"
	"unicode/utf8"
)

// A Secret stores base64 and a ConfigMap stores text; the panel shows and
// takes text for both. These two functions are the whole difference.

// encode is what goes into a Secret's `data`.
func encode(text string) string {
	return base64.StdEncoding.EncodeToString([]byte(text))
}

// decode undoes it, and reports whether the stored value was base64 at all.
func decode(stored string, encoded bool) ([]byte, bool) {
	if !encoded {
		return []byte(stored), true
	}
	out, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		return nil, false
	}
	return out, true
}

// textValue reports whether a stored value is something a text area can hold
// without destroying it. A TLS key is text; a gzipped bundle in a Secret is not,
// and typing over it in a browser would replace it with its own mojibake.
func textValue(stored string, encoded bool) bool {
	decoded, ok := decode(stored, encoded)
	return ok && utf8.Valid(decoded)
}
