package netinspect

import (
	"fmt"
	"strings"
)

// Calico's selector language, as projectcalico.org documents it for the
// `selector`, `notSelector` and `namespaceSelector` fields:
//
//	all()  global()  has(k)  k == "v"  k != "v"  k in {"a", "b"}
//	k not in {"a"}  k contains "s"  k starts with "s"  k ends with "s"
//	!expr  expr && expr  expr || expr  ( expr )
//
// `!` binds tighter than `&&`, which binds tighter than `||`. Strings take
// single or double quotes. A label that is absent satisfies `!=` and
// `not in`, and fails every other comparison. An empty selector selects
// everything. `global()` selects global network sets only, so no pod.
//
// Nothing here panics on a selector it cannot read: the parser returns an
// error and the caller treats what carries it as not evaluated.

// calicoSel is a parsed selector.
type calicoSel interface {
	match(get func(key string) (string, bool)) bool
}

// parseCalicoSelector parses text; an empty or blank text is all().
func parseCalicoSelector(text string) (calicoSel, error) {
	if strings.TrimSpace(text) == "" {
		return selAll{}, nil
	}
	toks, err := lexCalico(text)
	if err != nil {
		return nil, err
	}
	p := &calParser{toks: toks}
	sel, err := p.or()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.toks) {
		return nil, fmt.Errorf("unexpected %q after the expression", p.toks[p.pos].text)
	}
	return sel, nil
}

// --- nodes -----------------------------------------------------------------------

type selAll struct{}
type selGlobal struct{}
type selHas struct{ key string }
type selNot struct{ in calicoSel }
type selAnd struct{ a, b calicoSel }
type selOr struct{ a, b calicoSel }
type selCmp struct {
	key string
	op  string // ==, !=, in, notin, contains, starts, ends
	val []string
}

func (selAll) match(func(string) (string, bool)) bool    { return true }
func (selGlobal) match(func(string) (string, bool)) bool { return false }
func (s selHas) match(get func(string) (string, bool)) bool {
	_, ok := get(s.key)
	return ok
}
func (s selNot) match(get func(string) (string, bool)) bool { return !s.in.match(get) }
func (s selAnd) match(get func(string) (string, bool)) bool { return s.a.match(get) && s.b.match(get) }
func (s selOr) match(get func(string) (string, bool)) bool  { return s.a.match(get) || s.b.match(get) }
func (s selCmp) match(get func(string) (string, bool)) bool {
	v, ok := get(s.key)
	switch s.op {
	case "==":
		return ok && v == s.val[0]
	case "!=":
		return !ok || v != s.val[0]
	case "in":
		return ok && contains(s.val, v)
	case "notin":
		return !ok || !contains(s.val, v)
	case "contains":
		return ok && strings.Contains(v, s.val[0])
	case "starts":
		return ok && strings.HasPrefix(v, s.val[0])
	case "ends":
		return ok && strings.HasSuffix(v, s.val[0])
	}
	return false
}

// matchSet evaluates the selector over a label map.
func matchSet(sel calicoSel, set map[string]string) bool {
	return sel.match(func(k string) (string, bool) { v, ok := set[k]; return v, ok })
}

// --- lexer -----------------------------------------------------------------------

type calTok struct {
	kind string // word, str, op, ( ) { } ,
	text string
}

func lexCalico(s string) ([]calTok, error) {
	var out []calTok
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(' || c == ')' || c == '{' || c == '}' || c == ',':
			out = append(out, calTok{kind: string(c), text: string(c)})
			i++
		case c == '"' || c == '\'':
			end := strings.IndexByte(s[i+1:], c)
			if end < 0 {
				return nil, fmt.Errorf("a string is not closed at position %d", i)
			}
			out = append(out, calTok{kind: "str", text: s[i+1 : i+1+end]})
			i += end + 2
		case strings.HasPrefix(s[i:], "&&"), strings.HasPrefix(s[i:], "||"), strings.HasPrefix(s[i:], "=="), strings.HasPrefix(s[i:], "!="):
			out = append(out, calTok{kind: "op", text: s[i : i+2]})
			i += 2
		case c == '!':
			out = append(out, calTok{kind: "op", text: "!"})
			i++
		case isCalWord(c):
			j := i
			for j < len(s) && isCalWord(s[j]) {
				j++
			}
			out = append(out, calTok{kind: "word", text: s[i:j]})
			i = j
		default:
			return nil, fmt.Errorf("unexpected character %q at position %d", c, i)
		}
	}
	return out, nil
}

// isCalWord is a character of a label key or keyword.
func isCalWord(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '/' || c == '-'
}

// --- parser ----------------------------------------------------------------------

type calParser struct {
	toks []calTok
	pos  int
}

func (p *calParser) peek() *calTok {
	if p.pos < len(p.toks) {
		return &p.toks[p.pos]
	}
	return nil
}

func (p *calParser) take(kind, text string) bool {
	t := p.peek()
	if t != nil && t.kind == kind && (text == "" || t.text == text) {
		p.pos++
		return true
	}
	return false
}

func (p *calParser) or() (calicoSel, error) {
	left, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.take("op", "||") {
		right, err := p.and()
		if err != nil {
			return nil, err
		}
		left = selOr{left, right}
	}
	return left, nil
}

func (p *calParser) and() (calicoSel, error) {
	left, err := p.not()
	if err != nil {
		return nil, err
	}
	for p.take("op", "&&") {
		right, err := p.not()
		if err != nil {
			return nil, err
		}
		left = selAnd{left, right}
	}
	return left, nil
}

func (p *calParser) not() (calicoSel, error) {
	if p.take("op", "!") {
		in, err := p.not()
		if err != nil {
			return nil, err
		}
		return selNot{in}, nil
	}
	return p.atom()
}

func (p *calParser) str() (string, error) {
	t := p.peek()
	if t == nil || t.kind != "str" {
		return "", fmt.Errorf("a quoted string was expected")
	}
	p.pos++
	return t.text, nil
}

func (p *calParser) atom() (calicoSel, error) {
	if p.take("(", "") {
		in, err := p.or()
		if err != nil {
			return nil, err
		}
		if !p.take(")", "") {
			return nil, fmt.Errorf("a closing parenthesis is missing")
		}
		return in, nil
	}
	t := p.peek()
	if t == nil {
		return nil, fmt.Errorf("the expression ends too early")
	}
	if t.kind != "word" {
		return nil, fmt.Errorf("unexpected %q", t.text)
	}
	p.pos++
	key := t.text
	switch key {
	case "all", "global":
		if p.take("(", "") && p.take(")", "") {
			if key == "all" {
				return selAll{}, nil
			}
			return selGlobal{}, nil
		}
		return nil, fmt.Errorf("%s needs its parentheses: %s()", key, key)
	case "has":
		if !p.take("(", "") {
			return nil, fmt.Errorf("has needs a key in parentheses")
		}
		k := p.peek()
		if k == nil || k.kind != "word" {
			return nil, fmt.Errorf("has needs a label key")
		}
		p.pos++
		if !p.take(")", "") {
			return nil, fmt.Errorf("a closing parenthesis is missing after has(%s", k.text)
		}
		return selHas{k.text}, nil
	}
	// key op value
	switch {
	case p.take("op", "=="):
		v, err := p.str()
		return selCmp{key: key, op: "==", val: []string{v}}, err
	case p.take("op", "!="):
		v, err := p.str()
		return selCmp{key: key, op: "!=", val: []string{v}}, err
	case p.take("word", "in"):
		vs, err := p.set()
		return selCmp{key: key, op: "in", val: vs}, err
	case p.take("word", "not"):
		if !p.take("word", "in") {
			return nil, fmt.Errorf("%q: \"not\" must be followed by \"in\"", key)
		}
		vs, err := p.set()
		return selCmp{key: key, op: "notin", val: vs}, err
	case p.take("word", "contains"):
		v, err := p.str()
		return selCmp{key: key, op: "contains", val: []string{v}}, err
	case p.take("word", "starts"):
		if !p.take("word", "with") {
			return nil, fmt.Errorf("%q: \"starts\" must be followed by \"with\"", key)
		}
		v, err := p.str()
		return selCmp{key: key, op: "starts", val: []string{v}}, err
	case p.take("word", "ends"):
		if !p.take("word", "with") {
			return nil, fmt.Errorf("%q: \"ends\" must be followed by \"with\"", key)
		}
		v, err := p.str()
		return selCmp{key: key, op: "ends", val: []string{v}}, err
	}
	return nil, fmt.Errorf("%q has no operator", key)
}

func (p *calParser) set() ([]string, error) {
	if !p.take("{", "") {
		return nil, fmt.Errorf("a set in braces was expected")
	}
	var out []string
	for {
		if p.take("}", "") {
			return out, nil
		}
		if len(out) > 0 && !p.take(",", "") {
			return nil, fmt.Errorf("a comma or a closing brace was expected in the set")
		}
		if p.take("}", "") {
			return out, nil
		}
		v, err := p.str()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
}
