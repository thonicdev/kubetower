package web

import (
	"strings"

	"github.com/thonicdev/kubetower/internal/resources"
)

// namespaceColumn finds the namespace column after the person's arrangement has
// been applied, or -1.
func namespaceColumn(columns []resources.Column) int {
	for i, c := range columns {
		if strings.EqualFold(strings.TrimSpace(c.Title), namespaceColumnTitle) {
			return i
		}
	}
	return -1
}

// namespaceColumnTitle is what the listing layer calls the column, compared
// case-insensitively because a CRD's printer columns may spell it "Namespace".
// A mismatch is silent: no column matches and no link is drawn.
const namespaceColumnTitle = "namespace"
