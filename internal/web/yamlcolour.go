package web

// Coloring the YAML this console shows, on the server, as data. A read-only
// view is colored here; the editor is CodeMirror
// (`web/ui/src/pages/object/ManifestCode.tsx`), which colors with the same
// tokens, because a colored layer under a textarea drifts out of register with
// the caret.
//
// Three rules, each held by yamlcolour_test.go:
//
//   - Every byte of the document survives: the tokens' text, joined, is the
//     input exactly.
//   - The output is data, never markup: a class and a run of text, escaped by
//     whoever draws it, since a ConfigMap value may hold `<script>`.
//   - A block scalar's body is not YAML (it may be JSON, an nginx.conf, a
//     script), so the lexer tracks its indent and stops coloring.

import (
	"strings"
)

// noBlock is "not inside a block scalar". Negative, because a block scalar
// opened by a top-level key has indent 0.
const noBlock = -1

// yamlKeywords are the plain scalars YAML reads as something other than text,
// 1.1 spellings included (`on`, `no`), since manifests are full of them.
var yamlKeywords = map[string]bool{
	"true": true, "false": true, "null": true, "~": true,
	"yes": true, "no": true, "on": true, "off": true,
}

// colourYAMLLine writes one line and reports the block-scalar state after it:
// the indent of the line that opened a block scalar, or noBlock.
func colourYAMLLine(out yamlSink, line string, block int) int {
	indent := leadingBlanks(line)
	rest := line[indent:]

	// Inside a block scalar nothing is YAML; only a line indented back to the opener
	// ends it.
	if block != noBlock {
		if rest == "" {
			out.plain(line) // blank lines belong to the block and are blank
			return block
		}
		if indent > block {
			out.plain(line[:indent])
			out.span("y-lit", rest)
			return block
		}
		block = noBlock
	}

	if rest == "" {
		out.plain(line)
		return noBlock
	}
	// Indentation is written through untouched: it is what the document means.
	out.plain(line[:indent])

	// A directive or a document marker owns its whole line.
	if strings.HasPrefix(rest, "%") || rest == "---" || rest == "..." ||
		strings.HasPrefix(rest, "--- ") || strings.HasPrefix(rest, "... ") {
		out.span("y-doc", rest)
		return noBlock
	}

	// Sequence markers, possibly several (`- - a`), each drawn so nesting can be
	// counted without counting spaces.
	for strings.HasPrefix(rest, "- ") || rest == "-" {
		out.span("y-dash", "-")
		if rest == "-" {
			return noBlock
		}
		rest = rest[1:]
		gap := leadingBlanks(rest)
		out.plain(rest[:gap])
		rest = rest[gap:]
		if rest == "" {
			return noBlock
		}
	}

	if strings.HasPrefix(rest, "#") {
		out.span("y-com", rest)
		return noBlock
	}

	if key, after, ok := splitYAMLKey(rest); ok {
		out.span("y-key", key)
		out.plain(":")
		rest = after
		gap := leadingBlanks(rest)
		out.plain(rest[:gap])
		rest = rest[gap:]
	}
	if rest == "" {
		return noBlock
	}
	return colourYAMLValue(out, rest, indent)
}

// colourYAMLValue writes what is to the right of a key, and reports a block
// scalar it opened.
func colourYAMLValue(out yamlSink, rest string, indent int) int {
	// `key:   # comment`: taken first, because splitYAMLComment needs whitespace
	// before the `#` and the gap has already been written.
	if strings.HasPrefix(rest, "#") {
		out.span("y-com", rest)
		return noBlock
	}

	value, gap, comment := splitYAMLComment(rest)
	block := noBlock
	if value != "" {
		block = colourYAMLScalar(out, value, indent)
	}
	out.plain(gap)
	if comment != "" {
		out.span("y-com", comment)
	}
	return block
}

// colourYAMLScalar writes one value, already stripped of any trailing comment.
func colourYAMLScalar(out yamlSink, value string, indent int) int {
	// An anchor, an alias or a tag precedes the value, possibly two of them
	// (`&name !!str hello`).
	for len(value) > 0 && (value[0] == '&' || value[0] == '*' || value[0] == '!') {
		token := value
		if i := strings.IndexAny(value, " \t"); i >= 0 {
			token = value[:i]
		}
		out.span("y-tag", token)
		value = value[len(token):]
		gap := leadingBlanks(value)
		out.plain(value[:gap])
		value = value[gap:]
		if value == "" {
			return noBlock
		}
	}

	// `|`, `>`, `|-`, `>+2`: the value is the indented block below. The one token
	// that changes how later lines are read.
	if isBlockIndicator(value) {
		out.span("y-blk", value)
		return indent
	}

	switch {
	case value[0] == '"' || value[0] == '\'':
		out.span("y-str", value)
	case yamlKeywords[strings.ToLower(value)]:
		out.span("y-bool", value)
	case isYAMLNumber(value):
		out.span("y-num", value)
	default:
		// A plain scalar stays in the ordinary text color, so color keeps meaning
		// something.
		out.plain(value)
	}
	return noBlock
}

// splitYAMLKey finds the `key:` at the head of a line. The colon must be
// followed by a space or the end of the line, which separates a key from a value
// containing a colon (an image reference, a URL, a `sha256:` digest).
func splitYAMLKey(s string) (key, after string, ok bool) {
	if s == "" {
		return "", "", false
	}
	if s[0] == '"' || s[0] == '\'' {
		end := closingQuote(s)
		if end < 0 || end+1 >= len(s) || s[end+1] != ':' {
			return "", "", false
		}
		rest := s[end+2:]
		if rest == "" || rest[0] == ' ' || rest[0] == '\t' {
			return s[:end+1], rest, true
		}
		return "", "", false
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ':':
			if i+1 == len(s) || s[i+1] == ' ' || s[i+1] == '\t' {
				return s[:i], s[i+1:], true
			}
		case '#':
			// A comment starts here, so anything after it is not a key.
			if i > 0 && (s[i-1] == ' ' || s[i-1] == '\t') {
				return "", "", false
			}
		case '{', '[':
			// Flow style: left in the ordinary color rather than read badly.
			return "", "", false
		}
	}
	return "", "", false
}

// splitYAMLComment separates a trailing `# …` from the value, keeping the
// whitespace between. A `#` starts a comment only after whitespace and outside
// quotes: `image: nginx#latest` is one token.
func splitYAMLComment(s string) (value, gap, comment string) {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case quote == '"':
			if c == '\\' {
				i++
			} else if c == '"' {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '#' && i > 0 && (s[i-1] == ' ' || s[i-1] == '\t'):
			end := i
			for end > 0 && (s[end-1] == ' ' || s[end-1] == '\t') {
				end--
			}
			return s[:end], s[end:i], s[i:]
		}
	}
	return s, "", ""
}

// closingQuote is the index of the quote that closes the one at s[0], or -1.
func closingQuote(s string) int {
	quote := s[0]
	for i := 1; i < len(s); i++ {
		switch {
		case quote == '"' && s[i] == '\\':
			i++
		case s[i] == quote:
			// '' inside a single-quoted scalar is an escaped quote, not the end.
			if quote == '\'' && i+1 < len(s) && s[i+1] == '\'' {
				i++
				continue
			}
			return i
		}
	}
	return -1
}

// isBlockIndicator reports `|`, `>` and their chomping and indentation
// modifiers.
func isBlockIndicator(s string) bool {
	if s == "" || (s[0] != '|' && s[0] != '>') {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] == '-' || s[i] == '+' || (s[i] >= '0' && s[i] <= '9') {
			continue
		}
		return false
	}
	return true
}

// isYAMLNumber is deliberately narrow: an integer or a decimal with an optional
// sign and exponent, so `1.2.3-rc1` is not a number.
func isYAMLNumber(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '-' || s[0] == '+' {
		s = s[1:]
	}
	digits, dots, exponent := 0, 0, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c == '.':
			dots++
			if dots > 1 || exponent {
				return false
			}
		case (c == 'e' || c == 'E') && digits > 0 && !exponent:
			exponent = true
			if i+1 < len(s) && (s[i+1] == '-' || s[i+1] == '+') {
				i++
			}
			digits = 0
		default:
			return false
		}
	}
	return digits > 0
}

// leadingBlanks counts the spaces and tabs at the head of s. Tabs are illegal as
// YAML indentation and counted anyway: a document that will not parse is the one
// somebody is staring at.
func leadingBlanks(s string) int {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// yamlSink is what the colouriser writes through. The API returns the coloring
// as a token list (tokenSink), never as HTML: a contract that ships markup ships
// an injection surface.
type yamlSink interface {
	// plain is uncolored text: indentation, the colon after a key, the gap before a
	// comment, a plain scalar.
	plain(text string)
	// span is one colored token, under one of the y- classes.
	span(class, text string)
}

// yamlToken is one piece of a colored line, as data.
type yamlToken struct {
	Text string
	// Class is the contract's vocabulary (plain, key, string, number, boolean,
	// comment, anchor, punctuation), mapped one way from the y- classes by
	// tokenClass, so a new color never adds an enum member.
	Class string
}

// tokenSink is the rendering the JSON contract uses.
type tokenSink struct{ tokens []yamlToken }

// tokenClass maps a y- class onto the contract's enum. A class with no member is
// `plain`, so a generated client never meets a value it cannot type.
func tokenClass(class string) string {
	switch class {
	case "y-key":
		return "key"
	case "y-str":
		return "string"
	case "y-num":
		return "number"
	case "y-bool":
		return "boolean"
	case "y-com":
		return "comment"
	case "y-tag":
		// An anchor, an alias or a tag. The contract calls the family `anchor`.
		return "anchor"
	case "y-dash", "y-doc", "y-blk":
		return "punctuation"
	}
	return "plain"
}

// tokeniseYAML colors a document into lines of tokens.
func tokeniseYAML(document string) [][]yamlToken {
	if document == "" {
		return nil
	}
	lines := strings.Split(document, "\n")
	out := make([][]yamlToken, 0, len(lines))
	block := noBlock
	for _, line := range lines {
		sink := &tokenSink{}
		block = colourYAMLLine(sink, line, block)
		out = append(out, sink.tokens)
	}
	return out
}

func (t *tokenSink) plain(text string) {
	if text == "" {
		return
	}
	// Adjacent plain runs are merged, so `  key: ` is one token, not four.
	if n := len(t.tokens); n > 0 && t.tokens[n-1].Class == "plain" {
		t.tokens[n-1].Text += text
		return
	}
	t.tokens = append(t.tokens, yamlToken{Text: text, Class: "plain"})
}

func (t *tokenSink) span(class, text string) {
	if text == "" {
		return
	}
	t.tokens = append(t.tokens, yamlToken{Text: text, Class: tokenClass(class)})
}
