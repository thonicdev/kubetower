package web

// An empty ConfigMap value is empty, not withheld.
//
// # What it guards
//
// `Data.tsx` draws *"Held back until you ask for it."* wherever an entry carries
// no `value`, so a ConfigMap key set to the empty string must still reach the
// client with a `value` member, or it would draw exactly that on an object
// nothing about which is masked. Two things stack:
//
//   - `api.DataEntry.Value` is a `string` with `json:"value,omitempty"`, so an
//     empty value and an absent one are **one spelling on the wire**. The Go
//     zero value of the field is the empty string, which is also what a real
//     empty value is, and `omitempty` drops both.
//   - `DataPanel` must not re-derive "is there a value to draw" as
//     `!entry.Secret`. `internal/detail` answers that question in
//     `DataEntry.Drawn`, whose own comment names the empty-value case and which
//     `inline_test.go` covers in `TestAnEmptyConfigMapValueIsStillDrawn`; a
//     narrower question is right for three cases out of four.
//
// # Why it is worse than a wrong sentence
//
// The sentence is a **security affordance**. It tells an operator that this
// console is withholding a value for their protection. Said over a value that is
// simply empty, it teaches the wrong thing about what masking means, on the one
// surface where masking is the entire point. An empty answer and an absent one
// are different answers, in a panel whose job is to keep them apart.
//
// # Why these assertions are on the JSON and not on the struct
//
// Because that is where the two states are one. With `Value string` there is no
// Go value that means *absent* — the check has to be "did the member reach the
// wire", and the only honest way to ask that is to marshal. A test that asserted
// `out.Value == ""` would pass against the defect.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/detail"
)

// wireOf marshals one adapted entry and says whether `value` reached it.
//
// `json.Marshal` and then a decode into a map, rather than a substring search:
// `"value"` occurs in a key name, in a nested string and in this file's own
// prose, and a test that greps its own subject is a test that passes on the
// wrong evidence.
func wireOf(t *testing.T, entry detail.DataEntry) (raw string, value string, present bool) {
	t.Helper()
	out := dataEntryOf(entry)
	blob, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshalling the adapted entry: %v", err)
	}
	var back map[string]json.RawMessage
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatalf("reading the entry back: %v", err)
	}
	member, ok := back["value"]
	if !ok {
		return string(blob), "", false
	}
	if err := json.Unmarshal(member, &value); err != nil {
		t.Fatalf("the `value` member is not a string: %s", member)
	}
	return string(blob), value, true
}

// A ConfigMap key set to the empty string arrives as an empty value.
//
// The fixture is `internal/detail`'s own answer for such a key, taken from
// `entryOf`: `Drawn` true, `Inline` empty, `Secret` false, `Binary` false. Charts
// write keys like this constantly.
func TestAnEmptyConfigMapValueArrivesAsEmptyAndNotAsAbsent(t *testing.T) {
	raw, value, present := wireOf(t, detail.DataEntry{
		Key:      "A_EMPTY",
		Field:    detail.FieldData,
		Editable: true,
		Drawn:    true,
		Inline:   "",
	})

	if !present {
		t.Fatalf("the `value` member did not reach the wire: %s\n"+
			"A client cannot tell this key from a masked one, and `Data.tsx` draws "+
			"\"Held back until you ask for it.\" over a ConfigMap that withholds nothing.", raw)
	}
	if value != "" {
		t.Errorf("value = %q, want the empty string it actually holds", value)
	}
}

// And the state it must stay different from: a Secret's key, withheld.
//
// This is the assertion that stops the obvious repair. Removing `omitempty`
// makes the empty case work and makes **this** one send `"value": ""` — a
// withheld credential and an empty string wearing the same face, which is the
// same defect pointed the other way and the worse direction to be wrong in.
func TestAMaskedKeyCarriesNoValueMemberAtAll(t *testing.T) {
	raw, value, present := wireOf(t, detail.DataEntry{
		Key:     "PASSWORD",
		Field:   detail.FieldData,
		Secret:  true,
		Encoded: true,
	})

	if present {
		t.Errorf("a masked key carried `value: %q` (%s). Absent is what tells a client to "+
			"offer Show; an empty string tells it the credential is empty.", value, raw)
	}
}

// A ConfigMap's `binaryData` is not drawn either, and for its own reason.
//
// `internal/detail` keeps it behind a control because showing it is a decode,
// and `TestAConfigMapsBinaryDataIsNotDrawnUnasked` guards that. It is asserted
// again here because a bare `!entry.Secret` condition is true for such a key,
// which would reach the wire as `value: ""` truncated away by `omitempty` - the
// right answer by accident, which a repair for the empty case would undo.
func TestAConfigMapsBinaryDataCarriesNoValueMember(t *testing.T) {
	raw, value, present := wireOf(t, detail.DataEntry{
		Key:     "blob",
		Field:   detail.FieldBinaryData,
		Encoded: true,
	})

	if present {
		t.Errorf("a binaryData key carried `value: %q` (%s); it is base64 in the object and "+
			"this panel does not decode it", value, raw)
	}
}

// The ordinary case, so that none of the above is achieved by sending nothing.
func TestAnOrdinaryConfigMapValueStillArrives(t *testing.T) {
	const held = "carnets-postgresql-rw"
	raw, value, present := wireOf(t, detail.DataEntry{
		Key:      "DB_HOST",
		Field:    detail.FieldData,
		Editable: true,
		Drawn:    true,
		Inline:   held,
	})

	if !present || value != held {
		t.Errorf("value = %q (present=%v), want %q: %s", value, present, held, raw)
	}
	if !strings.Contains(raw, held) {
		t.Errorf("the value is not in the payload at all: %s", raw)
	}
}
