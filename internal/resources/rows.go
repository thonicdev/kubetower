package resources

// Row functions turn an object into cells, one per kind, in the rows_*.go
// files by kind family; nothing there fetches, filters or sorts. This file
// holds what they share.

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// --- shared bits ----------------------------------------------------------------

// replicasOf reads a *int32 spec field. Nil means one, per the API defaults.
func replicasOf(n *int32) int32 {
	if n == nil {
		return 1
	}
	return *n
}

func gradePhase(phase string, classes map[string]string) (string, int) {
	class, known := classes[phase]
	if !known {
		// An unknown phase is neither known-good nor known-bad.
		return ClassWarn, RankWarn
	}
	switch class {
	case ClassOK:
		return ClassOK, RankOK
	case ClassWarn:
		return ClassWarn, RankWarn
	case ClassBad:
		return ClassBad, RankBad
	}
	return ClassNone, 0
}

// quantityCell prints a capacity the way the manifest wrote it and sorts it by
// value, not by text.
func quantityCell(list corev1.ResourceList, name corev1.ResourceName) Cell {
	value, ok := list[name]
	if !ok {
		return Cell{Text: Missing}
	}
	return Cell{Text: value.String(), Value: float64(value.Value())}
}

func accessModes(modes []corev1.PersistentVolumeAccessMode) string {
	// The abbreviations kubectl prints.
	short := map[corev1.PersistentVolumeAccessMode]string{
		corev1.ReadWriteOnce:    "RWO",
		corev1.ReadOnlyMany:     "ROX",
		corev1.ReadWriteMany:    "RWX",
		corev1.ReadWriteOncePod: "RWOP",
	}
	out := make([]string, 0, len(modes))
	for _, mode := range modes {
		if s, ok := short[mode]; ok {
			out = append(out, s)
			continue
		}
		out = append(out, string(mode))
	}
	return strings.Join(out, ",")
}

// storageClass distinguishes "the default class" from "no class at all": two
// different reasons for a PVC to stay Pending.
func storageClass(name *string) string {
	if name == nil {
		return ""
	}
	if *name == "" {
		return "(none)"
	}
	return *name
}

// imagesOf answers "what is actually deployed here", without the registry
// prefix where the rest is unambiguous.
func imagesOf(containers []corev1.Container) string {
	images := make([]string, 0, len(containers))
	for _, c := range containers {
		image := c.Image
		if i := strings.LastIndexByte(image, '/'); i >= 0 {
			image = image[i+1:]
		}
		images = append(images, image)
	}
	return JoinCapped(images, 2)
}

func sortedKeys[T any](data map[string]T) []string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// selectorText prints a label selector the way a manifest writes it, and says
// what an empty one means (`everything`): every pod in the namespace, not none.
//
// Built term by term rather than from Selector.String(), whose set-based
// expressions contain commas of their own.
func selectorText(s *metav1.LabelSelector, everything string) string {
	if s == nil {
		// Nil selects nothing at all.
		return ""
	}
	if len(s.MatchLabels) == 0 && len(s.MatchExpressions) == 0 {
		return everything
	}

	parts := make([]string, 0, len(s.MatchLabels)+len(s.MatchExpressions))
	for _, key := range sortedKeys(s.MatchLabels) {
		parts = append(parts, key+"="+s.MatchLabels[key])
	}
	for _, e := range s.MatchExpressions {
		switch e.Operator {
		case metav1.LabelSelectorOpExists:
			parts = append(parts, e.Key)
		case metav1.LabelSelectorOpDoesNotExist:
			parts = append(parts, "!"+e.Key)
		default:
			parts = append(parts, fmt.Sprintf("%s %s (%s)",
				e.Key, strings.ToLower(string(e.Operator)), strings.Join(e.Values, " ")))
		}
	}
	return JoinCapped(parts, 2)
}

// labelsText is a plain label map as `key=value` pairs, sorted so the cell does
// not reshuffle between refreshes.
func labelsText(labels map[string]string) string {
	parts := make([]string, 0, len(labels))
	for _, key := range sortedKeys(labels) {
		parts = append(parts, key+"="+labels[key])
	}
	return JoinCapped(parts, 3)
}

// defaultClass marks the class a cluster falls back to when a manifest names
// none, reading every spelling of the annotation. Exactly one marked row is
// healthy.
func defaultClass(annotations map[string]string, keys ...string) Cell {
	for _, key := range keys {
		if annotations[key] == "true" {
			return Cell{Text: "default", Class: ClassOK}
		}
	}
	return Cell{Text: Missing}
}

// distinct keeps the first of each value, in the order seen, for a row
// summarizing a list whose entries repeat.
func distinct(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func sortedNames(refs []corev1.ObjectReference) []string {
	names := make([]string, 0, len(refs))
	for _, r := range refs {
		names = append(names, r.Name)
	}
	sort.Strings(names)
	return names
}
