// Package registry holds the extensions this process carries, and the refusals
// that decide whether one can be carried at all.
//
// It is `app.Register`'s state, held here rather than in `app` for one reason:
// `app.Run` builds the console, the console's router mounts what is registered,
// and the router is `internal/web`. Were the list in `app`, `app` would import
// the console and the console would import `app` — a cycle Go refuses. So the
// list lives here, below both, and `app` is the public door onto it. Nothing
// outside the core module can import this package; an extra calls
// `app.Register` or hands its extensions to `app.Run`, and both arrive here.
//
// # What it is
//
// The registry, and the refusals that go with it. `cmd/kubetower` hands over
// the extras it ships (`extras/`, each a package of this module) once, before
// the server is built; a
// process that registers nothing gets a console with no additive pages, which
// is what most of this repository's tests start.
package registry

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/extras"
)

var (
	mu         sync.RWMutex
	registered []extension.Extension
)

// Register hands the console the extensions this binary carries.
//
// Called once, before the server is built. It does three things and the order
// matters: it refuses what cannot be mounted, it records the extension so the
// console can mount its routes, and it puts the page in the catalog so the
// menu and the settings page can offer it.
//
// # The refusals, and why each is a refusal rather than a log line
//
//   - **A key that cannot be a URL segment.** The key is the URL segment and
//     the state-file key, so it is held to the spec's pattern
//     (extras.KeyPattern: lower case, digits and inner hyphens, 2 to 40
//     characters). An empty key would mount the page at the console's own
//     root; a `/`, `{`, `}` or a space would reach the router as something
//     other than one literal segment, where ServeMux panics or mounts a
//     wildcard.
//   - **A duplicate Key**, against what is registered *and* against the other
//     extensions of the same call — `app.Run` hands every extra over at once.
//     Two pages under one key is one of them silently shadowing the other — at
//     the router, in the catalog, and in the operator's stored per-cluster
//     state, which cannot then be told apart. Refused at startup because the
//     alternative is discovering it as a page that comes back off after an
//     upgrade.
//   - **A Legacy key that collides with a live one, or with another page's old
//     key**, either way round, in the same call or an earlier one. An alias
//     folds an old spelling onto a new key on read; one that names a page
//     somebody still has would silently redirect that page's stored state onto
//     another page's, and one two pages share folds onto whichever came first.
//     An old key is held to the key's own shape too, since every reader folds
//     a key lower-cased and could never match one outside it.
//   - **A route the router cannot mount**: one that does not begin with a
//     slash or holds whitespace, a repeated wildcard name (the prefix already
//     holds `{cluster}`), or two routes ServeMux finds in conflict. Each is a
//     panic inside the router otherwise, with a stack trace in place of the
//     error `app.Run` promises.
//   - **A setting that could not be stored or written** (settingsOf).
//
// A refusal is an error and not a panic: the caller is a `main` that can print
// it and exit with something an operator can read. Nothing of a refused call is
// registered, whichever of its extensions was refused.
//
// **The routes are asked for before the lock is taken**, because Routes is the
// extension's code and may read the registry (app.Extensions, app.Keys): asked
// under the write lock, that read would wait on this call forever. The keys are
// then checked and recorded under the lock in one step, so two concurrent calls
// cannot both claim one key.
func Register(exts ...extension.Extension) error {
	type candidate struct {
		e        extension.Extension
		key      string
		label    string
		legacy   []string
		settings []extras.Setting
	}
	candidates := make([]candidate, 0, len(exts))
	for _, e := range exts {
		if e == nil {
			return fmt.Errorf("an extension is nil, so there is nothing to mount")
		}
		key := e.Key()
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("an extension has no key, and the key is its URL segment and the name its per-cluster switch is stored under")
		}
		if !extras.KeyPattern.MatchString(key) {
			return fmt.Errorf("extension %s has the key %q, and a key is lower case, digits and inner hyphens, 2 to 40 characters: it is a segment of every URL the page answers on and the name its switch is stored under",
				e.Label(), key)
		}
		settings, err := settingsOf(e)
		if err != nil {
			return fmt.Errorf("extension %q: %w", key, err)
		}
		if err := checkRoutes(key, e); err != nil {
			return err
		}
		olds := legacyKeys(e)
		for _, old := range olds {
			// Every reader folds a key lower-cased (KT_EXTRAS, the state file,
			// the URL), so an old key outside the key's own shape would be an
			// alias nothing can ever match.
			if !extras.KeyPattern.MatchString(old) {
				return fmt.Errorf("extension %q lists %q as an old key, and an old key is held to the key's own shape (lower case, digits and inner hyphens, 2 to 40 characters), or nothing that reads one could match it",
					key, old)
			}
		}
		candidates = append(candidates, candidate{e: e, key: key, label: e.Label(), legacy: olds, settings: settings})
	}

	mu.Lock()
	defer mu.Unlock()

	// owner is every key the registry would hold after this call, live and
	// legacy, with who holds it: what is registered, then this call's
	// extensions as each is accepted.
	owner := map[string]holder{}
	for _, e := range registered {
		owner[e.Key()] = holder{label: e.Label()}
	}
	for _, e := range registered {
		for _, old := range legacyKeys(e) {
			if _, taken := owner[old]; !taken {
				owner[old] = holder{label: e.Label(), legacy: true}
			}
		}
	}

	for _, c := range candidates {
		if found, taken := owner[c.key]; taken {
			if found.legacy {
				return fmt.Errorf("extension %s claims the key %q, which %s lists as an old key: the alias would fold this page's stored state onto %s",
					c.label, c.key, found.label, found.label)
			}
			return fmt.Errorf("two extensions claim the key %q: %s and %s. One would shadow the other at the router and in the operator's stored state, where they could not be told apart",
				c.key, found.label, c.label)
		}
		for _, old := range c.legacy {
			if found, taken := owner[old]; taken {
				if found.legacy {
					return fmt.Errorf("extension %q lists %q as an old key, and so does %s: a state stored under it would fold onto whichever of the two came first",
						c.key, old, found.label)
				}
				return fmt.Errorf("extension %q lists %q as an old key, but %s is registered under it now: the alias would fold that page's stored state onto this one",
					c.key, old, found.label)
			}
		}
		owner[c.key] = holder{label: c.label}
		for _, old := range c.legacy {
			owner[old] = holder{label: c.label, legacy: true}
		}
	}

	for _, c := range candidates {
		registered = append(registered, c.e)
		extras.Register(extras.Extra{
			Key:      c.key,
			Label:    c.label,
			Summary:  c.e.Summary(),
			Needs:    c.e.Needs(),
			Legacy:   c.legacy,
			Settings: c.settings,
		})
	}
	return nil
}

// envName is the shape of the variable a text setting names (Setting.Env).
var envName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// settingsOf reads what an extension offers (extension.Configurable), refusing
// what could not be stored or written:
//
//   - **a key that is not lower-case and hyphenated** (extras.KeyPattern, the
//     extension key's own shape, because it is the last segment of the
//     setting's write, `PUT /api/c/{cluster}/extensions/{key}/settings/{setting}`),
//     which the API's path would refuse, so the checkbox could be drawn and
//     never saved;
//   - **a key declared twice**, which would be two checkboxes writing one value;
//   - **no label**, which would be a checkbox with nothing beside it.
func settingsOf(e extension.Extension) ([]extras.Setting, error) {
	configurable, ok := e.(extension.Configurable)
	if !ok {
		return nil, nil
	}
	declared := configurable.Settings()
	out := make([]extras.Setting, 0, len(declared))
	seen := map[string]bool{}
	for _, one := range declared {
		switch {
		case !extras.KeyPattern.MatchString(one.Key):
			return nil, fmt.Errorf("setting %q is not a key the settings write can carry: lower-case letters, digits and hyphens, 2 to 40 of them", one.Key)
		case seen[one.Key]:
			return nil, fmt.Errorf("setting %q is declared twice, so two checkboxes would write one value", one.Key)
		case strings.TrimSpace(one.Label) == "":
			return nil, fmt.Errorf("setting %q has no label, so its checkbox would say nothing", one.Key)
		case one.Text && one.Default:
			return nil, fmt.Errorf("setting %q is a line of text with an on/off default; a text setting's default is empty", one.Key)
		case !one.Text && (one.Placeholder != "" || one.Env != "" || one.Check != nil):
			return nil, fmt.Errorf("setting %q is a checkbox with a placeholder, a variable or a check, which are a line of text's", one.Key)
		case len(one.Choices) > 0 && !one.Text:
			return nil, fmt.Errorf("setting %q has choices but is not a line of text, which is what a choice is stored as", one.Key)
		case one.Env != "" && !envName.MatchString(one.Env):
			return nil, fmt.Errorf("setting %q names %q as its variable, which is not an environment variable's name", one.Key, one.Env)
		}
		seen[one.Key] = true
		out = append(out, extras.Setting{Key: one.Key, Label: one.Label, Summary: one.Summary, Default: one.Default,
			Text: one.Text, Placeholder: one.Placeholder, Choices: choicesOf(one.Choices), Env: one.Env, Check: one.Check})
	}
	return out, nil
}

func choicesOf(in []extension.Choice) []extras.Choice {
	out := make([]extras.Choice, 0, len(in))
	for _, one := range in {
		out = append(out, extras.Choice{Value: one.Value, Label: one.Label})
	}
	return out
}

// Extensions is what was registered, in registration order — which is menu
// order, because a page's place in the menu is the caller's decision and not
// something to be re-derived from a label.
func Extensions() []extension.Extension {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]extension.Extension, len(registered))
	copy(out, registered)
	return out
}

// Keys is the registered keys, sorted, for a message that has to list them.
func Keys() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(registered))
	for _, e := range registered {
		out = append(out, e.Key())
	}
	sort.Strings(out)
	return out
}

// Reset empties the registry. Tests only: nothing in the product unregisters a
// page, and a running console whose extensions changed underneath it would draw
// a menu that disagrees with its own routes.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	registered = nil
	extras.Reset()
}

// holder is who holds a key in Register's check: an extension's label, and
// whether the key is that extension's live key or one of its old ones.
type holder struct {
	label  string
	legacy bool
}

// legacyKeys is e's old keys as Register checks them and as it hands them to
// the catalog: trimmed, each once, without the empty ones and without e's
// own live key.
func legacyKeys(e extension.Extension) []string {
	var out []string
	seen := map[string]bool{e.Key(): true}
	for _, old := range e.Legacy() {
		if old = strings.TrimSpace(old); old != "" && !seen[old] {
			seen[old] = true
			out = append(out, old)
		}
	}
	return out
}
