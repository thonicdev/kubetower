package textutil

import (
	"regexp"
	"strings"
)

// URL matches the address a failed request was made to, wherever it sits in the
// message and whether or not client-go quoted it.
var URL = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"]*`)

// WithoutURL removes every address from a message.
//
// A destination is not a diagnosis: client-go formats a transport failure as
// `Get "<url>": <reason>` with the timeout as a query parameter, so a
// classifier reading the whole string would match the address instead of the
// failure.
func WithoutURL(s string) string {
	return URL.ReplaceAllString(s, "")
}

// clientGoFrame is what a stripped client-go transport error leaves in front of
// its reason: the HTTP verb and the empty quotes the address was inside.
// Anchored, so a `Get ""` later in a message, which is data, is kept.
var clientGoFrame = regexp.MustCompile(`^(?:Get|Head|Post|Put|Patch|Delete)\s*"?"?\s*:\s*`)

// ReasonN is FirstLineN for a message that may carry the address it was
// fetching: when it will not fit, **the destination gives way and the reason
// survives**.
//
//	Get "https://0123….gr7.eu-west-3.eks.amazonaws.com/apis/metrics.k8s.io/v1beta1?timeout=30s":
//	signing a token for "example-prod-eks-1": operation error STS: GetCallerIdentity, get i…
//
// The address is kept whenever the whole message fits, because on a name that
// does not resolve or a refused connection it is the useful half.
func ReasonN(s string, max int) string {
	line := FirstLine(s)
	if len([]rune(line)) <= max {
		return line
	}
	// The frame is removed after the address: the URL pattern stops at the
	// closing quote, so a bare strip would leave `Get "": …`, which reads as a
	// truncation of its own.
	stripped := strings.Join(strings.Fields(WithoutURL(line)), " ")
	stripped = strings.TrimSpace(clientGoFrame.ReplaceAllString(stripped, ""))
	if stripped == "" {
		// Nothing but an address: then the address is the message.
		return FirstLineN(line, max)
	}
	return FirstLineN(stripped, max)
}
