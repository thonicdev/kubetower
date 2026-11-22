package jvm

import (
	"strings"
)

// Masking. A system property or an option whose name looks like a secret has
// its value replaced before the answer leaves the console: a person who may
// exec in the pod could read it in a shell, but what a page draws is what a
// shared screen shows. The page asks for one value on a click (reveal), which
// runs the command again and is recorded as an exec like any other.

// masked is what a hidden value is drawn as.
const masked = "••••••"

// The words that make a name a secret's, anywhere in it, whatever its case.
var secretWords = []string{"password", "passwd", "secret", "token", "credential", "apikey", "api_key", "api-key", "private"}

// secretName is whether name looks like a secret's: one of secretWords
// anywhere in it, or `key` as a whole segment between `.`, `_`, `-` or `/`
// (`tls.key`, `key.store.pass`, `api.key`; not `keyboard`, not `keyStore`).
func secretName(name string) bool {
	lower := strings.ToLower(name)
	for _, w := range secretWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	for _, segment := range strings.FieldsFunc(lower, func(r rune) bool {
		return r == '.' || r == '_' || r == '-' || r == '/'
	}) {
		if segment == "key" {
			return true
		}
	}
	return false
}

// arg is one token of a command line as the page draws it. With Secret set,
// Text is everything before the value and the value is hidden; Secret is the
// name the page reveals it by.
type arg struct {
	Text   string `json:"text"`
	Secret string `json:"secret,omitempty"`
}

// optionName is the name an option sets and the value it gives, for the
// forms `-Dname=value`, `--name=value` and `-name=value`; ok is false for a
// token that sets nothing (`-jar`, `app.jar`), and value is empty for `--name`.
func optionName(token string) (name, value, prefix string, ok bool) {
	switch {
	case strings.HasPrefix(token, "-D"):
		name, value, _ = strings.Cut(token[2:], "=")
		return name, value, "-D" + name + "=", name != ""
	case strings.HasPrefix(token, "--"):
		name, value, _ = strings.Cut(token[2:], "=")
		return name, value, "--" + name + "=", name != ""
	case strings.HasPrefix(token, "-"):
		name, value, _ = strings.Cut(token[1:], "=")
		return name, value, "-" + name + "=", name != ""
	}
	return "", "", "", false
}

// maskArgs is tokens with every value of a secret-looking option hidden: in
// `-Dname=value` and `--name=value`, the value; after a bare `--name` or
// `-name`, the next token unless it is itself an option.
func maskArgs(tokens []string) []arg {
	out := make([]arg, 0, len(tokens))
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		name, _, prefix, ok := optionName(tok)
		if !ok || !secretName(name) || strings.HasPrefix(name, "XX:") {
			out = append(out, arg{Text: tok})
			continue
		}
		if strings.Contains(tok, "=") {
			out = append(out, arg{Text: prefix, Secret: name})
			continue
		}
		out = append(out, arg{Text: tok})
		if i+1 < len(tokens) && !strings.HasPrefix(tokens[i+1], "-") {
			out = append(out, arg{Text: "", Secret: name})
			i++
		}
	}
	return out
}

// argValue is the value maskArgs hid under name in tokens, and whether it
// hid one.
func argValue(tokens []string, name string) (string, bool) {
	for i := 0; i < len(tokens); i++ {
		n, value, _, ok := optionName(tokens[i])
		if !ok || n != name || !secretName(n) {
			continue
		}
		if strings.Contains(tokens[i], "=") {
			return value, true
		}
		if i+1 < len(tokens) && !strings.HasPrefix(tokens[i+1], "-") {
			return tokens[i+1], true
		}
	}
	return "", false
}

// joined is a masked command line as one string, hidden values drawn masked.
func joined(args []arg) string {
	parts := make([]string, len(args))
	for i, a := range args {
		if a.Secret != "" {
			parts[i] = a.Text + masked
		} else {
			parts[i] = a.Text
		}
	}
	return strings.Join(parts, " ")
}

// propertyRow is one system property as the page draws it.
type propertyRow struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	// Masked is set when Value hides something: the whole value, for a key
	// that looks like a secret, or the secret options of a value that is a
	// command line (`sun.java.command`). The page reveals it by Key.
	Masked bool `json:"masked"`
}

// maskProperty is p as the page may draw it.
func maskProperty(p property) propertyRow {
	if secretName(p.Key) {
		return propertyRow{Key: p.Key, Value: masked, Masked: true}
	}
	tokens := strings.Fields(p.Value)
	args := maskArgs(tokens)
	for _, a := range args {
		if a.Secret != "" {
			return propertyRow{Key: p.Key, Value: joined(args), Masked: true}
		}
	}
	return propertyRow{Key: p.Key, Value: p.Value}
}
