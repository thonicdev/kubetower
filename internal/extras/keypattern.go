package extras

import "regexp"

// KeyPattern is what an extension key, and an extension setting's key, may be:
// lower case, digits and inner hyphens, 2 to 40 characters. It is the OpenAPI
// spec's `ExtensionKey` pattern.
//
// The registry refuses a key that does not match (internal/registry) and the
// API refuses a switch or setting write whose path segment does not
// (internal/api), from this one variable, so a key cannot be accepted by one
// and refused by the other. The web `KEY_PATTERN` is held to it by
// internal/api/extensionkey_test.go.
//
// It also keeps a key one literal path segment: a `/` would move the extension
// under another prefix, a `{` or `}` would make it a ServeMux wildcard, and a
// space ends the pattern where ServeMux reads a host.
var KeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$`)
