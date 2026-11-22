package web

import (
	"strings"

	"github.com/thonicdev/kubetower/internal/nameplates"
)

// The cluster rail. A tile reads on three levels: color is the identity,
// initials the aim point, the name confirms. A nameplate (`internal/nameplates`)
// can override all three, because EKS context names are ARNs that hash to
// neighboring colors and near-identical labels. What a person types is only a
// label: every URL, lookup and sentence checked against a kubeconfig uses the
// context name.

// plateLabel, plateInitials, plateColourKey: the three things a nameplate can
// replace. They are separate functions so every place that shows a cluster
// derives it the same way.
func plateLabel(short string, plate nameplates.Nameplate) string {
	if plate.Name != "" {
		return plate.Name
	}
	return short
}

// plateInitials derives from the name a person gave when there is one, so a
// renamed cluster does not keep the initials of its context name.
func plateInitials(short string, plate nameplates.Nameplate) string {
	if plate.Initials != "" {
		return plate.Initials
	}
	return initials(plateLabel(short, plate))
}

// shortName reduces an EKS ARN to the cluster it names.
func shortName(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.LastIndexByte(name, ':'); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// initials: the first letter, plus the trailing number when there is one, which
// is what tells -1 from -2.
func initials(short string) string {
	parts := strings.FieldsFunc(short, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == ' '
	})
	if len(parts) == 0 {
		return "?"
	}
	out := string([]rune(parts[0])[0])
	if digit := lastDigit(short); digit != "" {
		out += digit
	} else if len(parts) > 1 {
		out += string([]rune(parts[1])[0])
	}
	return strings.ToUpper(out)
}

func lastDigit(s string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] >= '0' && s[i] <= '9' {
			return string(s[i])
		}
	}
	return ""
}
