package detail

// Two readings the drawer takes from shared code, pinned at the cases that
// would show in the same drawer: the Events panel disagreeing with the Event
// *detail* panel beside it, and a message losing its last character.

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// One reading of an event's time. A chain stopping at EventTime would show a
// repeating events.k8s.io event at its first occurrence — while event.go, the
// panel for an Event object itself, reads series.lastObservedTime. Two panels
// in one drawer must not give two answers.
func TestTheEventsPanelDatesASeriesEventFromItsLastObservation(t *testing.T) {
	started := time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC)
	lastSeen := started.Add(90 * time.Minute)

	got := eventFrom(&corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "repeating", CreationTimestamp: metav1.NewTime(started)},
		Reason:     "BackOff",
		Type:       corev1.EventTypeWarning,
		EventTime:  metav1.NewMicroTime(started),
		Series:     &corev1.EventSeries{Count: 180, LastObservedTime: metav1.NewMicroTime(lastSeen)},
	})

	if !got.Last.Equal(lastSeen) {
		t.Errorf("Last = %s, want %s — an event still firing must not read as 90 minutes stale",
			got.Last, lastSeen)
	}
	if !got.First.Equal(started) {
		t.Errorf("First = %s, want %s", got.First, started)
	}
}

// The other end of the same chain: nothing but a creation timestamp is still not
// 1970.
func TestTheEventsPanelFallsBackToTheCreationTime(t *testing.T) {
	created := time.Date(2026, 8, 20, 2, 0, 0, 0, time.UTC)

	got := eventFrom(&corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "bare", CreationTimestamp: metav1.NewTime(created)},
		Reason:     "Killing",
	})
	if !got.Last.Equal(created) {
		t.Errorf("Last = %s, want the creation time %s", got.Last, created)
	}
	if got.Last.Year() == 1970 {
		t.Error("the panel dated it to the epoch")
	}
}

// Messages are cut in runes: `s[:limit] + "…"` would show a CJK or emoji
// message with a replacement glyph in the drawer.
func TestALongEventMessageIsCutBetweenCharacters(t *testing.T) {
	got := eventFrom(&corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "long"},
		Message:    strings.Repeat("配", 600),
	})

	if utf8.RuneCountInString(got.Message) > maxEventMessage {
		t.Errorf("kept %d runes, want at most %d", utf8.RuneCountInString(got.Message), maxEventMessage)
	}
	if !utf8.ValidString(got.Message) || strings.ContainsRune(got.Message, '�') {
		t.Error("a rune was cut in half, which the drawer draws as a replacement glyph")
	}
	if !strings.HasSuffix(got.Message, "…") {
		t.Error("the message does not say it was cut")
	}
}

// The reporting controller is the newer API's field for what the older one calls
// source.component; kept here because eventFrom owns that fallback.
func TestTheReportingControllerStandsInForTheSource(t *testing.T) {
	got := eventFrom(&corev1.Event{
		ObjectMeta:          metav1.ObjectMeta{Name: "modern"},
		ReportingController: "deployment-controller",
	})
	if got.From != "deployment-controller" {
		t.Errorf("From = %q, want deployment-controller", got.From)
	}
}
