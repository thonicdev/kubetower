package resources

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/labels"
)

// InvalidSelector is a label selector that does not parse. Its own type, so a
// caller answers it as a request to correct rather than as a read that failed:
// asking again cannot make it parse.
type InvalidSelector struct {
	Selector string
	Err      error
}

func (e *InvalidSelector) Error() string {
	return fmt.Sprintf("the label selector %q is not valid: %v", e.Selector, e.Err)
}

func (e *InvalidSelector) Unwrap() error { return e.Err }

// ParseSelector checks a label selector in kubectl's syntax and returns it in
// the canonical form the API server is sent. Blank is everything, and comes
// back empty.
//
// Parsed here because the API server's 400 reads like any failed read.
func ParseSelector(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parsed, err := labels.Parse(raw)
	if err != nil {
		return "", &InvalidSelector{Selector: raw, Err: err}
	}
	return parsed.String(), nil
}
