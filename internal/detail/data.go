package detail

// The key/value block of a ConfigMap or a Secret. A Secret's values are read
// one key at a time on an explicit request, so only a deliberate click can put
// a credential on screen.

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/textutil"
)

// Field names of a data block. A ConfigMap's `data` is plain text and its
// `binaryData` base64; a Secret's `data` is base64 whatever it holds.
const (
	FieldData       = "data"
	FieldBinaryData = "binaryData"
)

// previewLimit is how many runes of a value's first line the collapsed row
// shows.
const previewLimit = 90

// How much of a ConfigMap's value the panel draws without being asked: a block
// of lines with a byte ceiling, both needed (one minified line can be 40 KB,
// and 2 KiB of short lines is a thousand). Sixteen lines is about what fits
// above the next key.
const (
	inlineLines = 16
	inlineBytes = 2 << 10
)

// DataEntry is one key. Value is empty unless this key was the one asked for,
// so a Secret's contents are not in an ordinary drawer's view model at all.
type DataEntry struct {
	Key   string
	Field string // FieldData or FieldBinaryData
	// Size is the size of the *decoded* value in bytes, not of its base64.
	Size int
	// Stored is the length as the API holds it: the base64 for an encoded
	// value, the same as Size for plain text.
	Stored int
	// Lines is how many lines the value has, 0 when it is binary.
	Lines int
	// Binary reports a value that is not valid UTF-8: said, not shown as
	// mojibake.
	Binary bool
	// Secret marks a value that must not be rendered until it is asked for by
	// name.
	Secret bool
	// Encoded reports that the stored value is base64, so showing it is a
	// decode (the control is labeled Decode): every key of a Secret and a
	// ConfigMap's `binaryData`.
	Encoded bool
	// Preview is the first line, clipped, and always empty for a Secret. It is
	// the one line that decides whether a value is ever rendered, for the data
	// panel and the environment table alike.
	Preview string
	// Clipped reports that Preview is shorter than the value, so the
	// environment row offers a control for the rest only when there is one.
	Clipped bool
	// Inline is the value as far as the panel draws it unasked, filled for a
	// ConfigMap's `data` keys only: a ConfigMap's value is what it exists for
	// and readable to anyone with `get`, so it needs no Show control.
	//
	// Like Preview it decides whether a value is rendered, so it is filled
	// under three independent conditions — not a Secret, not base64, in `data`
	// — and no single edit can let a Secret's value through.
	Inline string
	// InlineClipped reports that Inline is shorter than the value, so the
	// Expand control is offered.
	InlineClipped bool
	// Drawn reports that this value is on screen unasked. Not the same as a
	// non-empty Inline: an empty-string value is drawn as the word "empty",
	// not as nothing.
	Drawn bool
	// Editable reports whether this key can be changed from the panel: a text
	// value in `data`, on an object that is not immutable. Adding and deleting
	// keys is not offered.
	Editable bool
	// Value is filled for the one revealed key and empty for every other.
	Value string
}

// DataSet is the whole block, as one panel needs it.
type DataSet struct {
	// Kind is "ConfigMap" or "Secret", as a person writes it.
	Kind   string
	Secret bool
	// Type is a Secret's `type`: Opaque, kubernetes.io/tls, and so on. Empty on
	// a ConfigMap.
	Type string
	// Immutable is the object's `immutable: true`; the API refuses every data
	// change on it, so the panel refuses first.
	Immutable bool
	// ResourceVersion is the precondition a save from this panel carries.
	ResourceVersion string
	Entries         []DataEntry
	// Revealed is the key whose Value is filled, or "".
	Revealed string
	// Note explains an empty list, so it does not read as a failed draw.
	Note string
}

// Total is the sum of the decoded sizes, worth showing because the API caps a
// ConfigMap at 1 MiB.
func (d DataSet) Total() int {
	total := 0
	for _, entry := range d.Entries {
		total += entry.Size
	}
	return total
}

// Facts is what the Details tab says *about* the block — type, key count,
// size, immutability — and never its keys: nothing here iterates the entries,
// so no key name or value can reach the Details tab. Immutability is said both
// ways, since it explains why Data's Edit control is missing.
func (d DataSet) Facts() []Fact {
	var facts []Fact
	if d.Type != "" {
		facts = append(facts, Fact{Label: "Type", Value: d.Type})
	}
	facts = append(facts, Fact{Label: "Keys", Value: fmt.Sprint(len(d.Entries))})
	// The decoded size, a floor for the API's 1 MiB cap: metadata counts
	// towards it too and is not here.
	facts = append(facts, Fact{Label: "Size", Value: textutil.BytesCompact(int64(d.Total()))})
	if d.Immutable {
		facts = append(facts, Fact{Label: "Immutable", Value: "yes", Class: "warn", Pill: true})
	} else {
		facts = append(facts, Fact{Label: "Immutable", Value: "no"})
	}
	return facts
}

// Entry finds one key.
func (d DataSet) Entry(key string) (DataEntry, bool) {
	for _, entry := range d.Entries {
		if entry.Key == key {
			return entry, true
		}
	}
	return DataEntry{}, false
}

// HasData reports whether this kind carries a data block at all.
func HasData(kind string) bool {
	return strings.EqualFold(kind, "ConfigMap") || strings.EqualFold(kind, "Secret")
}

// Data reads one object's key/value block, revealing at most one key, in one
// read so the list and the value cannot disagree.
func (r *Reader) Data(ctx context.Context, contextName string, req Request, reveal string) (DataSet, error) {
	where, err := r.Locate(ctx, contextName, req.Kind)
	if err != nil {
		return DataSet{}, err
	}
	if !HasData(where.GroupVersionKind.Kind) {
		return DataSet{}, fmt.Errorf("%s has no data block", where.GroupVersionKind.Kind)
	}
	iface, err := where.Interface(req.Namespace)
	if err != nil {
		return DataSet{}, err
	}
	// Reviewed by name, so `resourceNames` on a Role holds.
	var raw *unstructured.Unstructured
	_, err = r.gate.Read(ctx, where.Cluster, where.Attributes("get", req.Namespace, req.Name), func(ctx context.Context) error {
		var err error
		raw, err = iface.Get(ctx, req.Name, metav1.GetOptions{})
		return err
	})
	if err != nil {
		return DataSet{}, err
	}

	set := dataOf(raw, where.GroupVersionKind.Kind)
	if set == nil {
		return DataSet{}, fmt.Errorf("%s has no data block", where.GroupVersionKind.Kind)
	}
	set.revealInto(raw, reveal)
	return *set, nil
}

// dataOf builds the block from an object already in hand. nil for any kind that
// does not have one.
func dataOf(raw *unstructured.Unstructured, kind string) *DataSet {
	if !HasData(kind) {
		return nil
	}
	secret := strings.EqualFold(kind, "Secret")

	set := &DataSet{
		Kind:            kind,
		Secret:          secret,
		ResourceVersion: raw.GetResourceVersion(),
	}
	if secret {
		set.Type, _, _ = unstructured.NestedString(raw.Object, "type")
	}
	set.Immutable, _, _ = unstructured.NestedBool(raw.Object, "immutable")

	// A ConfigMap's `data` is text and its `binaryData` base64; a Secret has
	// only `data`, base64 either way.
	fields := []struct {
		name     string
		encoded  bool
		editable bool
	}{
		{FieldData, secret, true},
		{FieldBinaryData, true, false},
	}
	for _, field := range fields {
		if field.name == FieldBinaryData && secret {
			continue
		}
		block, found, err := unstructured.NestedStringMap(raw.Object, field.name)
		if err != nil || !found {
			continue
		}
		for key, stored := range block {
			set.Entries = append(set.Entries, entryOf(key, field.name, stored, field.encoded, field.editable, secret, set.Immutable))
		}
	}

	// Sorted so a refresh does not reshuffle; text keys before binary ones.
	sort.Slice(set.Entries, func(i, j int) bool {
		if set.Entries[i].Field != set.Entries[j].Field {
			return set.Entries[i].Field == FieldData
		}
		return set.Entries[i].Key < set.Entries[j].Key
	})

	if len(set.Entries) == 0 {
		set.Note = "This " + kind + " has no keys."
	}
	return set
}

// entryOf measures one value without keeping it.
func entryOf(key, field, stored string, encoded, editable, secret, immutable bool) DataEntry {
	decoded, ok := decodeValue(stored, encoded)
	entry := DataEntry{
		Key:     key,
		Field:   field,
		Size:    len(decoded),
		Secret:  secret,
		Encoded: encoded,
		Stored:  len(stored),
	}
	// Base64 that does not decode cannot be shown either: read as binary.
	entry.Binary = !ok || !utf8.Valid(decoded)
	if entry.Binary {
		return entry
	}

	text := string(decoded)
	entry.Lines = strings.Count(strings.TrimSuffix(text, "\n"), "\n") + 1
	if text == "" {
		entry.Lines = 0
	}
	entry.Editable = editable && !immutable
	// A Secret's value is measured above and then dropped: it reaches no
	// structure that outlives this call.
	if !secret {
		entry.Preview, entry.Clipped = preview(text)
	}
	// Three conditions where one would do; see DataEntry.Inline. binaryData
	// keeps its Decode control.
	if !secret && !encoded && field == FieldData {
		entry.Drawn = true
		entry.Inline, entry.InlineClipped = inlineBlock(text)
	}
	return entry
}

// InlineLines is how many lines of the value the panel is drawing, derived
// from Inline so the "first N of M" it prints cannot go stale.
func (e DataEntry) InlineLines() int {
	if e.Inline == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(e.Inline, "\n"), "\n") + 1
}

// revealInto fills the one value that was asked for.
func (d *DataSet) revealInto(raw *unstructured.Unstructured, key string) {
	if key == "" {
		return
	}
	for i, entry := range d.Entries {
		if entry.Key != key || entry.Binary {
			continue
		}
		stored, found, err := unstructured.NestedString(raw.Object, entry.Field, key)
		if err != nil || !found {
			return
		}
		decoded, ok := decodeValue(stored, d.Secret || entry.Field == FieldBinaryData)
		if !ok {
			return
		}
		d.Entries[i].Value = string(decoded)
		d.Revealed = key
		return
	}
}

// decodeValue undoes the base64 the wire form uses, or passes plain text
// through. The bool is false when the stored value claims to be base64 and is
// not.
func decodeValue(stored string, encoded bool) ([]byte, bool) {
	if !encoded {
		return []byte(stored), true
	}
	decoded, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		return nil, false
	}
	return decoded, true
}

// preview is the first line, clipped, with tabs and carriage returns taken out
// so one value cannot break its row. The bool says whether anything was left
// behind; it cannot be derived from the string (a value may end in "…", and a
// cut at a line break leaves no mark).
func preview(text string) (string, bool) {
	line := text
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	clipped := len(line) != len(text)
	line = strings.ReplaceAll(line, "\t", " ")
	if utf8.RuneCountInString(line) > previewLimit {
		runes := []rune(line)
		return string(runes[:previewLimit]) + "…", true
	}
	return line, clipped
}

// inlineBlock is the value as far as the panel will draw it unasked, and
// whether anything was left behind (computed from the text, as in preview).
// Unlike preview it keeps line breaks and tabs, for a `<pre>`, and appends no
// ellipsis: the panel says "16 of 240 lines", and an ellipsis would read as
// part of the configuration.
func inlineBlock(text string) (string, bool) {
	block := text
	// Both cuts run: lines, then bytes.
	if lines := strings.SplitAfterN(block, "\n", inlineLines+1); len(lines) > inlineLines {
		block = strings.Join(lines[:inlineLines], "")
	}
	if len(block) > inlineBytes {
		// On a rune boundary, so no replacement character ends the block.
		cut := inlineBytes
		for cut > 0 && !utf8.RuneStart(block[cut]) {
			cut--
		}
		block = block[:cut]
	}
	return block, len(block) != len(text)
}
