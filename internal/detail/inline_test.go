package detail

import (
	"fmt"
	"strings"
	"testing"
)

// A ConfigMap's value is drawn without being asked for. A Secret's is not.
//
// Show/hide on a ConfigMap is not useful, except to expand a long value — and
// the whole of it rests on one line in `entryOf` holding three conditions. These
// tests are that line from both sides.

// The property, and it is the one that matters: nothing about this feature can
// put a credential on a page.
//
// `Preview`'s comment calls itself "the one line that decides whether a value is
// ever rendered". `Inline` is a second, and a second such line is a second
// place to get it wrong — which is the argument for asserting it here rather
// than trusting the condition to keep reading the way it reads today.
//
// Asserted over **every** entry of a real Secret rather than over one key,
// because the failure this guards is a loop that fills a field it should not:
// a test naming one key passes the day a second field is filled beside it.
func TestNoSecretValueReachesTheInlineBlock(t *testing.T) {
	set := setOf(t, secretDoc, "Secret")
	if len(set.Entries) == 0 {
		t.Fatal("the Secret fixture produced no entries, so this test covers nothing")
	}
	for _, e := range set.Entries {
		if e.Drawn {
			t.Errorf("key %q of a Secret is marked Drawn, so the panel would put a credential "+
				"on screen with nobody having asked for it", e.Key)
		}
		if e.Inline != "" {
			t.Errorf("key %q of a Secret has an inline value of %d bytes. A Secret's value must "+
				"exist in this process for the length of the call that measures it and reach no "+
				"structure that outlives it.", e.Key, len(e.Inline))
		}
		if e.InlineClipped {
			t.Errorf("key %q of a Secret reports InlineClipped, which would draw an Expand "+
				"control over a value that is not there", e.Key)
		}
	}
}

// And a ConfigMap's `binaryData` keeps its control, because decoding is an
// operation and the label has to name it.
//
// This is the second of the three conditions, and it is the one a tidy-up would
// drop first: `!secret` alone reads as sufficient, and it is not. binaryData is
// base64 of arbitrary bytes — a value that happens to decode to text would then
// be drawn as though it were plain `data`, under a control that had disappeared.
func TestAConfigMapsBinaryDataIsNotDrawnUnasked(t *testing.T) {
	set := setOf(t, configMapDoc, "ConfigMap")
	found := false
	for _, e := range set.Entries {
		if e.Field != FieldBinaryData {
			continue
		}
		found = true
		if e.Drawn || e.Inline != "" {
			t.Errorf("key %q is in binaryData and is drawn unasked. Its stored form is base64: "+
				"showing it is a decode, and the panel's rule is that a control names its "+
				"own operation.", e.Key)
		}
	}
	if !found {
		t.Fatal("the ConfigMap fixture has no binaryData key any more, so this test covers nothing")
	}
}

// A ConfigMap's text value is drawn, and drawn whole when it fits.
//
// The second half matters: a control that only ever produces the same answer is
// noise. `InlineClipped` false is what keeps Expand off the key, so it is
// asserted rather than inferred from the value being short.
func TestAConfigMapValueThatFitsIsDrawnWholeWithNothingToPress(t *testing.T) {
	set := setOf(t, configMapDoc, "ConfigMap")
	got := entry(t, set, "Corefile")

	if !got.Drawn {
		t.Fatal("a ConfigMap's `data` key is not drawn unasked, which is the whole of this change")
	}
	if !strings.Contains(got.Inline, "errors") {
		t.Errorf("the inline value does not hold the value's own text; got %q", got.Inline)
	}
	// The indentation is half of what makes a config file readable, and `preview`
	// deliberately flattens it. This must not: it goes into a `<pre>`.
	if !strings.Contains(got.Inline, "\n") {
		t.Error("the inline value has no line breaks, so it has been flattened the way a table " +
			"cell's preview is. This one is drawn in a <pre> and its shape is the point.")
	}
	if got.InlineClipped {
		t.Errorf("a %d-line value reports itself clipped, so the panel would draw an Expand "+
			"control that produces exactly what is already on screen", got.Lines)
	}
	if lines := got.InlineLines(); lines != got.Lines {
		t.Errorf("InlineLines is %d and the value has %d lines. On an unclipped value the two are "+
			"the same figure, and the panel prints both in one sentence.", lines, got.Lines)
	}
}

// A value set to the empty string is drawn as "empty", not as nothing.
//
// `Drawn` exists for exactly this and would otherwise be derivable from `Inline`
// being non-empty. Charts write empty keys constantly, and an entry that renders
// as nothing at all is a panel that failed to draw wearing the same face as a
// value that is empty: an empty answer and a failed one are different answers.
func TestAnEmptyConfigMapValueIsStillDrawn(t *testing.T) {
	got := entry(t, setOf(t, configMapDoc, "ConfigMap"), "empty")
	if !got.Drawn {
		t.Error("an empty ConfigMap value is not marked Drawn, so the panel has no way to tell " +
			"it apart from a key whose value it declined to show")
	}
	if got.Inline != "" {
		t.Errorf("an empty value produced an inline block of %q", got.Inline)
	}
	if got.InlineClipped {
		t.Error("an empty value reports itself clipped, which would offer an Expand control " +
			"over nothing")
	}
}

// Both budgets bite, and each is tested where the other cannot reach it.
//
// When two guards cover the same case, neither is tested until one is tested
// somewhere the other cannot reach. A 400-line file of short lines is under the byte cap and over the line
// cap; one enormous single line is the reverse. A single fixture that is over
// both leaves either guard deletable with the suite still green.
func TestBothInlineBudgetsBite(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		guard string
	}{
		{
			name:  "many short lines is over the line budget and under the byte one",
			text:  strings.Repeat("a\n", inlineLines*4),
			guard: "the line budget",
		},
		{
			name:  "one long line is over the byte budget and under the line one",
			text:  strings.Repeat("x", inlineBytes*2),
			guard: "the byte budget",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			block, clipped := inlineBlock(tc.text)
			if !clipped {
				t.Errorf("%s did not fire: %d bytes of value came back whole", tc.guard, len(tc.text))
			}
			if len(block) >= len(tc.text) {
				t.Errorf("%s reported a cut and cut nothing: %d bytes in, %d out",
					tc.guard, len(tc.text), len(block))
			}
			if lines := strings.Count(block, "\n"); lines > inlineLines {
				t.Errorf("the block kept %d lines, over the %d-line budget", lines, inlineLines)
			}
			if len(block) > inlineBytes {
				t.Errorf("the block kept %d bytes, over the %d-byte budget", len(block), inlineBytes)
			}
		})
	}
}

// The cut lands on a rune boundary.
//
// A `<pre>` ending in half a UTF-8 sequence draws a replacement character, which
// reads as a corrupted value rather than as a value this panel cut — and the
// value it is most likely to happen to is somebody's translations ConfigMap,
// where every line is multi-byte.
func TestTheInlineCutDoesNotSplitARune(t *testing.T) {
	// A three-byte rune repeated puts a boundary at every third byte, so a cut at
	// a power-of-two budget lands mid-sequence unless something moves it back.
	block, clipped := inlineBlock(strings.Repeat("→", inlineBytes))
	if !clipped {
		t.Fatal("the fixture is not over the byte budget, so this test covers nothing")
	}
	if !isValidUTF8(block) {
		t.Errorf("the inline block is not valid UTF-8 after the cut: it ends %q",
			tailOf(block, 4))
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return fmt.Sprintf("…% x", s[len(s)-n:])
}
