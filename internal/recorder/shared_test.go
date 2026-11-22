package recorder

// What this package writes to disk, at the cases a reading can get wrong.
// These matter more than the equivalents in a render: a mangled cell
// is fixed by a redeploy, and a mangled JSONL record is what `get_event_history`
// reads back for as long as the retention window.

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A reading that stops at LastTimestamp then EventTime misses
// series.lastObservedTime, dating a repeating events.k8s.io event from its
// first occurrence.
func TestARepeatingEventIsArchivedAtItsLastObservation(t *testing.T) {
	started := time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC)
	lastSeen := started.Add(2 * time.Hour)

	got := convert("prod", []corev1.Event{{
		ObjectMeta:     metav1.ObjectMeta{Name: "repeating", CreationTimestamp: metav1.NewTime(started)},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "api-1"},
		Reason:         "BackOff",
		EventTime:      metav1.NewMicroTime(started),
		Series:         &corev1.EventSeries{Count: 240, LastObservedTime: metav1.NewMicroTime(lastSeen)},
	}})

	if len(got) != 1 {
		t.Fatalf("got %d events", len(got))
	}
	if !got[0].Last.Equal(lastSeen) {
		t.Errorf("Last is %s, want %s — series.lastObservedTime is where the newer API puts the "+
			"last occurrence, and an event still firing was archived as two hours stale",
			got[0].Last, lastSeen)
	}
	if !got[0].First.Equal(started) {
		t.Errorf("First is %s, want %s", got[0].First, started)
	}
}

// The other half of the same chain: an event carrying none of the three stamps
// is archived at its creation, never as 1970.
func TestAnEventWithNoStampsAtAllIsArchivedAtItsCreation(t *testing.T) {
	created := time.Date(2026, 8, 20, 2, 15, 0, 0, time.UTC)

	got := convert("prod", []corev1.Event{{
		ObjectMeta:     metav1.ObjectMeta{Name: "bare", CreationTimestamp: metav1.NewTime(created)},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "api-1"},
		Reason:         "Killing",
	}})

	if !got[0].Last.Equal(created) {
		t.Errorf("Last is %s, want the creation time %s — 1970 sorts before everything, forever",
			got[0].Last, created)
	}
	if got[0].Last.Year() == 1970 {
		t.Error("archived at the epoch")
	}
}

// The count has the same trap as the timestamp: `count` is zero on every
// cluster writing through events.k8s.io, and get_event_history prints the
// repetition only when the count is above one, so the bare field would drop
// the one figure that turns "it happened" into "it has been happening".
func TestARepeatingEventIsArchivedWithItsRealCount(t *testing.T) {
	got := convert("prod", []corev1.Event{{
		ObjectMeta:     metav1.ObjectMeta{Name: "repeating"},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "api-1"},
		EventTime:      metav1.NewMicroTime(time.Now().Add(-time.Hour)),
		Series:         &corev1.EventSeries{Count: 240, LastObservedTime: metav1.NewMicroTime(time.Now())},
		// Count deliberately zero, which is what the core/v1 view reports.
	}})

	if got[0].Count != 240 {
		t.Errorf("Count = %d, want 240 — series.count is where the newer API puts it", got[0].Count)
	}
}

// A byte-wise cut splits a multi-byte rune in half, and what it damages goes
// to disk.
func TestALongMessageIsCutBetweenCharactersAndNotInsideOne(t *testing.T) {
	// Three bytes per character, so a byte-wise cut at 2000 lands inside one.
	long := strings.Repeat("配", 3000)

	got := convert("c", []corev1.Event{{
		InvolvedObject: corev1.ObjectReference{Name: "a"}, Message: long,
	}})

	stored := got[0].Message
	if utf8.RuneCountInString(stored) > maxMessage {
		t.Errorf("stored %d runes, want at most %d", utf8.RuneCountInString(stored), maxMessage)
	}
	if !utf8.ValidString(stored) {
		t.Error("the stored message is not valid UTF-8 — a rune was cut in half on its way to disk")
	}
	if strings.ContainsRune(stored, '�') {
		t.Error("the stored message contains a replacement character")
	}
	if !strings.HasSuffix(stored, "…") {
		t.Errorf("the stored message does not say it was cut: %q", stored[len(stored)-8:])
	}
}
