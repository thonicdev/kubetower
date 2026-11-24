// Package extras is one question asked four times: which of the pages that are
// not resource kinds does this installation want?
//
// The Delivery Center, right-sizing, scheduled-job health and the Redis viewer
// are all *additive*. None of them is part of what a Kubernetes console
// has to be, each of them reads custom resources or metrics that most clusters
// do not serve, and each of them costs a heading in a menu that is already long.
// So each is switched per cluster, and **every one is off until somebody turns
// it on there, on every profile** - and
// this package is the one place that knows whether a given one is on, and keeps
// the settings each one offers.
//
// It is its own package for the reason `internal/nodeshell`, `internal/hotbars`,
// `internal/columns` and `internal/nameplates` are theirs: it is a decision of
// the operator's, it outlives any one page, it is written to the state
// directory, and keeping every such thing in a package of its own is what makes
// "this is all KubeTower writes to disk" a sentence somebody can check by listing
// a directory rather than by reading code.
//
// # Two ways to decide, and why compose wins
//
// There are two ways an extra gets turned on, and they answer two different
// questions.
//
//   - `KT_EXTRAS` in the deployment's environment answers "what does this
//     *deployment* run?". It is declarative, it is in version control beside
//     the rest of the stack, and redeploying reproduces it.
//   - The switches on the Settings page's Extra tab answer "what does the person
//     at this console want to look at today?". **Every extra is off until
//     somebody turns it on, on every profile**: the tab lists
//     every extra the build carries with a button to turn it on, so a page
//     nobody chose is one click away rather than in front of everybody.
//
// **Under the server profile (`--incluster`) KT_EXTRAS means something else**:
// it is the chart's values saying which extras
// this installation *may* turn on (InCluster). It pins nothing there. An extra
// it names is still off on a cluster until a person allowed the verb `use` on
// `extras.kubetower.dev/<key>` turns it on in Settings > Extra; an extra it does
// not name is listed with its
// button disabled, and every write about it is refused (ErrNotInValues).
//
// On the desktop and in a container, when both speak about the same extra,
// **the deployment wins, per extra**. Not a global mode switch: an
// installation that pins the Delivery Center on because the platform team
// requires it has said nothing about whether this operator wants right-sizing,
// and a mode switch would make it say so by accident. What the variable does
// not mention stays the operator's to choose.
//
// A pinned extra is still *drawn* on the settings page — on or off as the
// deployment set it, with no switch, and labeled with where the decision came
// from. A switch that silently refuses to move is how an operator concludes the
// page is broken; one that says "the deployment decided this" sends them to the
// place that did.
//
// # What this package does not know
//
// It knows four keys and nothing else: not what a Redis key is, not what Argo
// CD serves, not what a page looks like. `internal/web` owns the routes and the
// menu, and asks this only "is this key on?". That is what keeps the four extras
// removable one at a time.
package extras

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/thonicdev/kubetower/internal/statefile"
)

// FileName is the setting, inside the state directory, beside the four files the
// other stores keep.
const FileName = "extras.json"

// EnvExtras is the deployment's say. See ParseEnv for the grammar and Store for
// what it wins over.
const EnvExtras = "KT_EXTRAS"

// A page that was published under an older key carries the old spelling in
// `Extra.Legacy`, and Register folds it into the alias map in catalogue.go.
//
// **The failure this exists to prevent is silent.** Drop an alias and an
// operator who had the page on gets it back off after an upgrade, with no error
// and nothing on the page saying why; a `KT_EXTRAS=<old key>` in a compose file
// becomes "which is not an extra"; and a bookmark 404s. None of those reads as a
// rename — they read as the feature having been removed.
//
// They are one-way on purpose. Nothing writes an old spelling back: `adopt`
// folds it to the new key on read, so the next save of `extras.json` is in the
// new vocabulary and the alias becomes dead weight rather than a second
// permanent name for the same page.

// Canonical turns a key that arrived from a file, an environment variable or a
// URL into the key this build uses.
//
// An unrecognized key is returned unchanged rather than rejected, because the
// caller's own Known() check is what decides whether a key is real — two places
// answering that question is how they come to disagree.
func Canonical(key string) string {
	mu.RLock()
	defer mu.RUnlock()
	if to, ok := legacy[key]; ok {
		return to
	}
	return key
}

// LegacyOf is every earlier key the extra key was published under, sorted, or
// none. The `use` review asks about them too (internal/web), so a ClusterRole
// written for an old name keeps granting the page after it is renamed.
func LegacyOf(key string) []string {
	mu.RLock()
	defer mu.RUnlock()
	var out []string
	for old, to := range legacy {
		if to == key {
			out = append(out, old)
		}
	}
	sort.Strings(out)
	return out
}

// Extra is one of them, as the settings page needs to draw it.
type Extra struct {
	// Key is the stable identifier — also the page's URL segment.
	Key string
	// Label is what the menu and the settings page call it.
	Label string
	// Summary is one sentence saying what turning it on adds, because a
	// checkbox reading "argocd" tells an operator who has not used it nothing.
	Summary string
	// Needs names what the cluster must be running for the page to say anything
	// useful. Drawn beside the switch: an operator turning a page on against a
	// cluster that does not run what it reads gets an honest empty page, and
	// being told why *before* the click is cheaper than after.
	Needs string
	// Legacy is every earlier key this page was published under. Register folds
	// each one onto Key, so a stored extras.json, a bookmark and a KT_EXTRAS
	// line written against the old spelling keep working. One-way: nothing ever
	// writes an old key back.
	Legacy []string
	// Settings is what the extension offers beyond its switch
	// (extension.Configurable), in the order the tile draws them.
	Settings []Setting
}

// Setting is one boolean an extra offers, as the store needs to know it: its
// key, its words, and its value where nobody has chosen. See
// extension.Setting, which it is copied from at registration.
type Setting struct {
	Key     string
	Label   string
	Summary string
	Default bool

	// Text, Placeholder, Env and Check are a line of text's
	// (extension.Setting.Text).
	Text        bool
	Placeholder string
	Choices     []Choice
	Env         string
	Check       func(string) error
}

// Choice is one value a choice setting accepts (extension.Choice).
type Choice struct {
	Value string
	Label string
}

// Catalog is the extras in menu order.
//
// Returns a copy, because the caller is a template and a template that could
// mutate the catalog would change what every other page draws.
func Catalogue() []Extra {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Extra, len(catalogue))
	copy(out, catalogue)
	// The settings too: a shallow copy shares their backing array, and a caller
	// that wrote to one would change every later answer.
	for i := range out {
		out[i].Settings = append([]Setting(nil), out[i].Settings...)
	}
	return out
}

// setting returns one setting an extra declares. Callers hold no lock.
func setting(key, name string) (Setting, bool) {
	e, ok := lookup(key)
	if !ok {
		return Setting{}, false
	}
	for _, one := range e.Settings {
		if one.Key == name {
			return one, true
		}
	}
	return Setting{}, false
}

// Known reports whether a key is one of them.
//
// Every entry point checks it. An unknown key must never be stored: extras.json
// is hand-editable, and a typo silently kept would be an extra that can be
// turned on in a file and never off on the page.
func Known(key string) bool {
	mu.RLock()
	defer mu.RUnlock()
	return indexOf(key) >= 0
}

// lookup returns the catalog entry for a key.
func lookup(key string) (Extra, bool) {
	mu.RLock()
	defer mu.RUnlock()
	if i := indexOf(key); i >= 0 {
		return catalogue[i], true
	}
	return Extra{}, false
}

// State is one extra as the settings page draws it: what it is, whether it is
// on, and whether the operator is allowed to change that.
type State struct {
	Extra
	// On is the answer in effect — the resolved one, compose included.
	On bool
	// Pinned means the deployment's KT_EXTRAS decided, so there is no switch.
	// Drawn, not hidden: see the package comment on why a pinned extra still
	// appears. Never true under InCluster, where KT_EXTRAS pins nothing.
	Pinned bool
	// Allowed is false only under InCluster, for an extra the chart's values
	// (KT_EXTRAS) do not name: listed, off, and every write about it refused.
	Allowed bool
	// Note is what to say beside a disabled switch, and it is empty for the
	// ordinary case. It lives on the row rather than being fetched per row by the
	// template, because "which of the two sources decided this" is a fact about
	// the store and the page should not have to ask a second question to draw one
	// line.
	Note string
	// Values is each of Extra.Settings as it stands on this cluster, in the
	// same order: what KT_EXTRA_SETTINGS pins, what was chosen there, or the
	// setting's default.
	Values []bool
	// SettingsPinned is, in the same order, whether KT_EXTRA_SETTINGS decides
	// each one - or, for a line of text, whether its Env is set - drawn with no
	// control on the Settings page.
	SettingsPinned []bool
	// Texts is, in the same order, each text setting's value on this cluster,
	// empty where nothing is set and for a checkbox.
	Texts []string
}

// formatChoices is the `format` a file is written in: every entry of `on` is a
// choice, true or false, and a cluster or an extra the file does not name has
// had none made, so it is off.
//
// A file without it is the previous format, where only ticks were written and
// the absence of one meant off. Since every extra is off by default, absent and
// off are one answer, so such a file is read by its ticks alone. See adoptTicks.
const formatChoices = 2

// stored is the file's shape.
//
// A map rather than a list of enabled keys, because the difference between "off"
// and "never mentioned" has to survive a round trip: an operator who turns
// something off has said something, and a future default that flipped to on
// would otherwise silently override them.
type stored struct {
	// Format is formatChoices on every file this build writes, and absent from
	// a file in the previous format.
	Format int `json:"format,omitempty"`

	// On is cluster -> extra -> the choice made there. Both answers are
	// written: false is "turned off here", and an extra absent from a cluster's
	// row - or a cluster absent from the map - is "never chosen", which the
	// store's default answers, so a future default cannot answer for a choice
	// written down. A binary that knows only the ticks-only format reads every
	// true as on and skips every false, which is the answer it would give.
	//
	// **Per cluster, not a global `enabled` plus per-cluster `off` exclusions.**
	// A global switch would make adding a cluster cheaper, but a page turned on
	// "here" would be silently turned on everywhere, including on clusters added
	// later, including production. Ticking per cluster makes the blast radius of
	// a tick exactly one cluster, and a new context arrives with nothing on it.
	// That is more clicks and less surprise, and for a console that reaches
	// production clusters it is the right trade.
	On map[string]map[string]bool `json:"on,omitempty"`

	// Enabled and Off are the previous format, read once and migrated. **Never
	// written.**
	//
	// Kept as fields rather than dropped so an existing file is converted rather
	// than silently read as "nothing is ticked" — which would take every page
	// away from an operator who had them, at a restart they did not connect to
	// it. See migrate.
	Enabled map[string]bool            `json:"enabled,omitempty"`
	Off     map[string]map[string]bool `json:"off,omitempty"`

	// Settings is cluster -> extra -> setting -> the value chosen there
	// (extension.Configurable). A setting absent is at its default.
	Settings map[string]map[string]map[string]bool `json:"settings,omitempty"`

	// Texts is the same for the settings that are a line of text
	//. A setting absent is not set; an empty value is
	// never written.
	Texts map[string]map[string]map[string]string `json:"texts,omitempty"`
}

// ParseEnv reads KT_EXTRAS.
//
// The grammar is a comma-separated list, each item either `key` (meaning on) or
// `key=value` where value is any of on/off, true/false, yes/no, 1/0. Case and
// surrounding space are ignored, because this is written by a person in a YAML
// file and `KT_EXTRAS=security, argocd=off` is what they will write.
//
// It returns what it understood and the complaints separately rather than an
// error, and the caller logs them: an environment variable is not a place an
// operator sees a refusal, and refusing to start over a typo in an optional page
// would take the whole console away over one misspelled word. The same bargain
// nodeshell.defaultFromEnv is on.
func ParseEnv(value string) (map[string]bool, []string) {
	pinned := map[string]bool{}
	var complaints []string

	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key, raw, hasValue := strings.Cut(item, "=")
		key = Canonical(strings.ToLower(strings.TrimSpace(key)))
		if !Known(key) {
			// Worded from the catalog rather than around it, because a
			// console with nothing registered has an **empty** catalog and
			// "names \"x\", which is not an extra; the 0 are " is not a
			// sentence. A build with no extras says so, which is the actual
			// answer to why KT_EXTRAS did nothing.
			if keys := Keys(); len(keys) == 0 {
				complaints = append(complaints, fmt.Sprintf("%s names %q, and this build of "+
					"KubeTower carries no extra pages at all", EnvExtras, key))
			} else {
				complaints = append(complaints, fmt.Sprintf("%s names %q, which is not an extra; "+
					"the %d are %s", EnvExtras, key, len(keys), strings.Join(keys, ", ")))
			}
			continue
		}
		on := true
		if hasValue {
			parsed, ok := parseBool(raw)
			if !ok {
				// The switch named is the deployment's, because it is the one that
				// exists on every profile - this runs before anything knows whether
				// the process is one of several replicas, which have no other.
				complaints = append(complaints, fmt.Sprintf("%s sets %s=%q, which is neither on nor off, "+
					"so it pins nothing and the per-cluster choice decides. To pin it, write %s=on or "+
					"%s=off in %s %s and restart KubeTower", EnvExtras, key, strings.TrimSpace(raw),
					key, key, EnvExtras, WhereSet))
				continue
			}
			on = parsed
		}
		pinned[key] = on
	}
	return pinned, complaints
}

// parseBool accepts the spellings a person writes in a compose file.
func parseBool(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on", "true", "yes", "1", "enabled":
		return true, true
	case "off", "false", "no", "0", "disabled":
		return false, true
	}
	return false, false
}

// Keys is the four keys in menu order, for a message that has to list them.
func Keys() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(catalogue))
	for _, e := range catalogue {
		out = append(out, e.Key)
	}
	return out
}

// Store is the resolved answer: what the deployment pinned, what the operator
// chose, and the file the second of those lives in.
type Store struct {
	// path is empty when there is nowhere to persist to. A supported state and
	// not an error — the choices then live as long as the process, which is what
	// a test wants and is better than refusing to start.
	path string
	// say is the log, and the once-only half of it. See statefile.Complainer.
	say *statefile.Complainer

	// pinned is what KT_EXTRAS said, on the desktop and in a container. Empty
	// under InCluster. Read-only after New: an environment variable does not
	// change under a running process, and making it immutable is what lets
	// Enabled read it without the lock.
	pinned map[string]bool

	// inCluster is the InCluster option, and allowed what KT_EXTRAS names there:
	// the extras this installation may turn on. Read-only after New,
	// for the reason pinned is. Nil allowed - every profile but InCluster - is
	// every extra.
	inCluster bool
	allowed   map[string]bool

	// pinnedSettings is what KT_EXTRA_SETTINGS said, extra -> setting ->
	// value. Read-only after New, for the reason pinned is.
	pinnedSettings map[string]map[string]bool

	mu sync.RWMutex
	// on[cluster][key] — see stored.On: true or false where a choice was made,
	// absent where none was.
	on map[string]map[string]bool
	// settings[cluster][key][setting] — see stored.Settings.
	settings map[string]map[string]map[string]bool
	// texts[cluster][key][setting] — see stored.Texts.
	texts map[string]map[string]map[string]string

	// keptOn and keptSettings are what the file says about extensions this
	// build does not carry: never honored, written back unchanged, so a binary
	// that no longer carries an extra keeps the operator's choices for the one
	// that does again.
	keptOn       map[string]map[string]bool
	keptSettings map[string]map[string]map[string]bool
	keptTexts    map[string]map[string]map[string]string

	// clusters is what the kubeconfig held when this store was opened, used to
	// convert a file written in the oldest format, where "on" meant "on
	// everywhere". A context that has gone keeps its row, the same way the old
	// format kept its exclusions, because a kubeconfig that loses a cluster for
	// an afternoon must not lose what was said about it.
	clusters []string
}

// Option adjusts New.
type Option func(*Store)

// InCluster is the server profile's reading of KT_EXTRAS: the chart's values
// naming the extras this installation
// **may** turn on, not a pin. `key` or `key=on` allows it; `key=off`, or a key
// the variable does not name, does not.
//
// An allowed extra is still off on a cluster until somebody turns it on there,
// and the core's review of the verb `use` decides who may; one that is
// not allowed is off on every cluster and refuses every write (ErrNotInValues).
//
// There is no option for a default-on: off is every profile's default, and it
// needs nothing running to hold it.
func InCluster() Option { return func(s *Store) { s.inCluster = true } }

// New opens the store in dir, which may be empty, and reads KT_EXTRAS.
//
// It never fails, for the reason nodeshell.New does not: a state directory that
// cannot be read is a forgotten preference, not a console that will not start.
//
// clusters is the contexts the kubeconfig holds. It converts a file written in
// the oldest format - see migrate. Pass nil and such a file, with nothing to
// convert it against, is read as nothing chosen, which is why the caller that
// has the list should pass it.
func New(dir string, clusters []string, logf func(string, ...any), opts ...Option) *Store {
	s := &Store{
		say:      statefile.NewComplainer(logf),
		on:       map[string]map[string]bool{},
		settings: map[string]map[string]map[string]bool{},
		texts:    map[string]map[string]map[string]string{},

		keptOn:       map[string]map[string]bool{},
		keptSettings: map[string]map[string]map[string]bool{},
		keptTexts:    map[string]map[string]map[string]string{},
		clusters:     clusters,
	}
	for _, opt := range opts {
		opt(s)
	}

	pinned, complaints := ParseEnv(os.Getenv(EnvExtras))
	if s.inCluster {
		// What the chart's values name may be turned on; nothing is pinned.
		s.allowed = map[string]bool{}
		for key, on := range pinned {
			if on {
				s.allowed[key] = true
			}
		}
		pinned = map[string]bool{}
	}
	s.pinned = pinned
	pinnedSettings, settingComplaints := ParseSettingsEnv(os.Getenv(EnvExtraSettings))
	s.pinnedSettings = pinnedSettings
	for _, c := range append(complaints, settingComplaints...) {
		// Not Once: each complaint is about a different word in the variable, and
		// collapsing them would hide the second typo behind the first.
		s.say.Log("extras: %s", c)
	}

	if dir == "" {
		return s
	}
	s.path = filepath.Join(dir, FileName)
	s.load()
	return s
}

// Dir is where the setting is kept: KT_STATE_DIR, or a per-user path when
// nothing says otherwise. The same resolution as the four stores beside it, so
// all of KubeTower's state is in one directory.
func Dir() string { return statefile.Dir() }

// Path is the file, or empty when there is none. The startup log says it out
// loud, because "my extras came back" is otherwise diagnosed by guesswork.
func (s *Store) Path() string { return s.path }

// IsInCluster reports whether KT_EXTRAS was read as the installation's values
// rather than as pins (InCluster).
func (s *Store) IsInCluster() bool { return s.inCluster }

// Allowed reports whether this installation may turn an extra on: always, but
// under InCluster for an extra the chart's values (KT_EXTRAS) do not name.
func (s *Store) Allowed(key string) bool {
	return s.allowed == nil || s.allowed[key]
}

// nothingChosen is what the store does with a file it cannot use, in words.
func (s *Store) nothingChosen() string {
	return "no extra is enabled"
}

func (s *Store) load() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		// A file that is not there is the ordinary first run, and the ordinary
		// first run is nothing chosen anywhere: the default answers.
		if !os.IsNotExist(err) {
			s.say.Log("cannot read %s, %s: %v", s.path, s.nothingChosen(), err)
		}
		return
	}
	var doc stored
	if err := json.Unmarshal(raw, &doc); err != nil {
		s.say.Log("%s is unreadable, %s: %v", s.path, s.nothingChosen(), err)
		return
	}

	s.adoptSettings(doc.Settings)
	s.adoptTexts(doc.Texts)

	// Nothing is written back here: absent and off are one answer on every
	// profile, so a file in an older format reads exactly as it was
	// meant, and the next choice writes the current format.
	switch {
	case doc.Format >= formatChoices:
		// The current format wins outright. A file holding it and an older one
		// too - which only a hand-edit produces - is read as the current one,
		// because that is the one this process writes and the page reflects.
		s.adopt(doc.On)
	case len(doc.On) > 0:
		s.adoptTicks(doc.On)
	case len(doc.Enabled) > 0:
		s.migrate(doc)
	}
}

// adopt takes the choices from a file in the current format.
//
// Filtered on the extra key and **not** on the cluster: a context that is not in
// this kubeconfig today may be tomorrow, and dropping its row would quietly turn
// a page back on — or off — somewhere the operator had decided. The rail forgets
// a cluster it cannot see; this remembers what was said about it.
func (s *Store) adopt(on map[string]map[string]bool) {
	for cluster, keys := range on {
		for key, chosen := range keys {
			// Before Known(), not after: a file written by an older build names
			// the page by the key that build used, and that is not an unknown
			// key — it is this one, spelled the way it used to be spelled.
			key = Canonical(key)
			if !Known(key) {
				s.keep(cluster, key, chosen)
				continue
			}
			s.choose(cluster, key, chosen)
		}
	}
}

// adoptTicks reads a file in the ticks-only format, whose rows hold only ticks:
// on a cluster it names, an extra it does not tick was off.
//
// **Every saved choice is honored exactly**: what was ticked stays on, and
// everything else is off - which is also what a cluster nobody chose on has.
// So the ticks are all there is to read.
func (s *Store) adoptTicks(on map[string]map[string]bool) {
	for cluster, keys := range on {
		for key, tick := range keys {
			if !tick {
				continue
			}
			key = Canonical(key)
			if !Known(key) {
				// Kept as the tick it was: the build that carries it reads a
				// file of the current format, where true is on.
				s.keep(cluster, key, true)
				continue
			}
			s.choose(cluster, key, true)
		}
	}
}

// adoptSettings takes the settings from the file, dropping - out loud - what
// names no extra or no setting this build declares.
func (s *Store) adoptSettings(in map[string]map[string]map[string]bool) {
	for cluster, keys := range in {
		for key, values := range keys {
			key = Canonical(key)
			for name, value := range values {
				if !Known(key) {
					s.keepSetting(cluster, key, name, value)
					continue
				}
				if declared, ok := setting(key, name); !ok || declared.Text {
					s.say.Log("%s sets %q of %q on %q, which this build does not offer as a checkbox; ignoring it",
						s.path, name, key, cluster)
					continue
				}
				s.set(cluster, key, name, value)
			}
		}
	}
}

// adoptTexts takes the text settings from the file, as adoptSettings does the
// checkboxes. A value its setting's Check refuses is dropped out loud: the file
// is hand-editable, and a value that cannot be used must not be read as one.
func (s *Store) adoptTexts(in map[string]map[string]map[string]string) {
	for cluster, keys := range in {
		for key, values := range keys {
			key = Canonical(key)
			for name, value := range values {
				if !Known(key) {
					s.keepText(cluster, key, name, value)
					continue
				}
				declared, ok := setting(key, name)
				if !ok || !declared.Text {
					s.say.Log("%s sets %q of %q on %q, which this build does not offer as a line of text; ignoring it",
						s.path, name, key, cluster)
					continue
				}
				if err := checkText(declared, value); err != nil {
					s.say.Log("%s sets %s.%s on %q to a value that cannot be used, so it is not set: %v",
						s.path, key, name, cluster, err)
					continue
				}
				s.setText(cluster, key, name, strings.TrimSpace(value))
			}
		}
	}
}

// keep holds a choice about an extension this build does not carry, said out
// loud: a key that is not an extra is never honored, and a hand-edited typo is
// kept rather than silently rewritten, which the log line makes findable.
// Called from New alone.
func (s *Store) keep(cluster, key string, on bool) {
	s.say.Log("%s names %q on %q, which this build does not carry; it is kept in the "+
		"file for a build that does, and not honored", s.path, key, cluster)
	if s.keptOn[cluster] == nil {
		s.keptOn[cluster] = map[string]bool{}
	}
	s.keptOn[cluster][key] = on
}

// keepSetting holds a setting of an extension this build does not carry. Called
// from New alone.
func (s *Store) keepSetting(cluster, key, name string, value bool) {
	if s.keptSettings[cluster] == nil {
		s.keptSettings[cluster] = map[string]map[string]bool{}
	}
	if s.keptSettings[cluster][key] == nil {
		s.keptSettings[cluster][key] = map[string]bool{}
	}
	s.keptSettings[cluster][key][name] = value
}

// keepText holds a text setting of an extension this build does not carry.
// Called from New alone.
func (s *Store) keepText(cluster, key, name, value string) {
	if s.keptTexts[cluster] == nil {
		s.keptTexts[cluster] = map[string]map[string]string{}
	}
	if s.keptTexts[cluster][key] == nil {
		s.keptTexts[cluster][key] = map[string]string{}
	}
	s.keptTexts[cluster][key][name] = value
}

// choose records one choice. Callers hold mu, or own s alone (New).
func (s *Store) choose(cluster, key string, on bool) {
	if s.on[cluster] == nil {
		s.on[cluster] = map[string]bool{}
	}
	s.on[cluster][key] = on
}

// set records one setting. Callers hold mu, or own s alone (New).
func (s *Store) set(cluster, key, name string, value bool) {
	if s.settings[cluster] == nil {
		s.settings[cluster] = map[string]map[string]bool{}
	}
	if s.settings[cluster][key] == nil {
		s.settings[cluster][key] = map[string]bool{}
	}
	s.settings[cluster][key][name] = value
}

// setText records one text setting, or forgets it when value is empty. Callers
// hold mu, or own s alone (New).
func (s *Store) setText(cluster, key, name, value string) {
	if value == "" {
		delete(s.texts[cluster][key], name)
		return
	}
	if s.texts[cluster] == nil {
		s.texts[cluster] = map[string]map[string]string{}
	}
	if s.texts[cluster][key] == nil {
		s.texts[cluster][key] = map[string]string{}
	}
	s.texts[cluster][key][name] = value
}

// migrate converts a file in the oldest format, where a page was enabled
// globally and clusters could be excluded from it, and reports whether it did.
//
// **The conversion needs the cluster list, and that is why New takes one.** The
// old format's "on" meant "on everywhere", and everywhere is only nameable from
// the kubeconfig. Without it the honest reading of an old file is *nothing*
// chosen — which, off by default, would take six pages away from an operator
// who had them, at a restart they did not connect to it. So the list is asked
// for, and when it is absent the loss is said out loud rather than performed
// quietly.
//
// Only the ticks are recorded, and the file is not written back here: the next
// choice writes the new format, and until then the file still opens in an older
// binary.
func (s *Store) migrate(doc stored) {
	if len(s.clusters) == 0 {
		// HowToTurnOn's desktop form, and knowingly so: in-cluster always has
		// its one context, so a process with no cluster list is never one run
		// under the server profile.
		s.say.Log("%s is in the previous format and this process has no cluster list to "+
			"convert it with, so nothing is ticked. %s The file is rewritten in the new "+
			"format on the first per-cluster write", s.path, HowToTurnOn("<cluster>", "", Desktop))
		return
	}
	for key := range doc.Enabled {
		if !Known(key) {
			s.say.Log("%s names %q, which is not an extra; ignoring it", s.path, key)
		}
	}
	converted := 0
	for _, key := range Keys() {
		for _, cluster := range s.clusters {
			// The operator had taken this page away here when Off says so.
			// Carried across, because the whole point of converting rather
			// than dropping is that a decision already made is not made again.
			if doc.Enabled[key] && !doc.Off[cluster][key] {
				s.choose(cluster, key, true)
				converted++
			}
		}
	}
	s.say.Log("%s was in the previous format: %d page/cluster ticks carried across %d "+
		"contexts. Extras are now chosen per cluster", s.path, converted, len(s.clusters))
}

// Enabled reports whether an extra is on on **any** cluster.
//
// Not "is this page available" — there is no such state. What this answers is
// the middle of `refuseExtraOff`'s three questions:
// this build has the page, and it is on nowhere. Which is a different sentence
// from "not on this cluster", and the two send an operator to different places.
//
// KT_EXTRAS still wins, and still wins globally, on the desktop and in a
// container: a deployment that pins a page on means it on every context,
// because that is what a platform team pinning it is saying. Under InCluster an
// extra the chart's values do not name is on nowhere.
func (s *Store) Enabled(key string) bool {
	if on, ok := s.pinned[key]; ok {
		return on
	}
	if !Known(key) || !s.Allowed(key) {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, keys := range s.on {
		if keys[key] {
			return true
		}
	}
	return false
}

// EnabledOn reports whether an extra is on on one cluster, which is the
// question every route and every menu actually asks.
//
// The whole resolution in a few lines, and the order is the design: what the
// deployment pinned, then whether this build has the page and this
// installation allows it (InCluster), then the choice made on this cluster,
// then - where none was - off. An unknown key is off — a route asking about a page this build does not carry
// gets the same answer as one nobody turned on, and `refuseExtraOff` is what
// tells those two apart in words.
//
// An empty cluster is **false**. There is no global question: a choice belongs
// to a cluster, and a caller that has not named one is asking something this
// store cannot answer, so a route that forgot to resolve its cluster is refused.
//
// With one exception, and it is the first line rather than a special case: a
// *pinned* extra answers before the cluster is looked at, because a pin is a
// statement about this deployment and not about a cluster. So `EnabledOn("", k)`
// is the pin where there is one and false everywhere else.
func (s *Store) EnabledOn(cluster, key string) bool {
	if on, ok := s.pinned[key]; ok {
		return on
	}
	if cluster == "" || !Known(key) || !s.Allowed(key) {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Absent is off: every extra is off where nobody chose.
	return s.on[cluster][key]
}

// There is no ExcludedOn and no exclusion: a cluster's row holds choices only,
// and `refuseExtraOff` asks EnabledOn and tells the cases apart itself.

// SetOn turns one extra on or off on one cluster. The only setter there is.
//
// **Both answers are recorded**, off as well as on, so "turned off here"
// survives a restart as a choice and a future default cannot answer for it.
//
// No "is it enabled globally" precondition — there is no second level. What
// remains is: the page must exist in this build, a cluster must be named, this
// installation must allow it (InCluster), and the deployment must not have
// pinned it. Who may make the choice is not this store's question: under the
// server profile the core reviews the verb `use` before calling it.
func (s *Store) SetOn(cluster, key string, on bool) error {
	if err := s.writable(cluster, key, map[bool]string{true: "turn on", false: "turn off"}[on]); err != nil {
		return err
	}
	if err := s.allowedHere(key); err != nil {
		return err
	}
	if s.Pinned(key) {
		label := key
		if e, ok := lookup(key); ok {
			label = e.Label
		}
		return fmt.Errorf("%w, so %s cannot be changed here", ErrPinned, label)
	}

	s.mu.Lock()
	s.choose(cluster, key, on)
	err := s.persistLocked()
	s.mu.Unlock()
	return err
}

// Setting is one setting's value on one cluster: what KT_EXTRA_SETTINGS pins,
// else what was chosen there, else the default the extension declares. A
// setting this build does not declare is false.
func (s *Store) Setting(cluster, key, name string) bool {
	declared, ok := setting(key, name)
	if !ok || declared.Text {
		return false
	}
	if on, pinned := s.pinnedSettings[key][name]; pinned {
		return on
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if value, chosen := s.settings[cluster][key][name]; chosen {
		return value
	}
	return declared.Default
}

// SetSetting writes one setting of one extra on one cluster
// (extension.Configurable).
//
// **Not refused for a pinned extra, nor for one that is off.** A pin decides
// whether the page is there, and a setting is how it is drawn; choosing it
// before turning the page on is a reasonable order to do things in. **Refused
// for a pinned setting** (KT_EXTRA_SETTINGS): the deployment decided it on
// every cluster, and a write that changed nothing visible would read as broken.
// **Refused for an extra this installation does not allow** (InCluster,
// ErrNotInValues): whoever may turn an extra on may configure it, and nobody
// may turn that one on.
func (s *Store) SetSetting(cluster, key, name string, value bool) error {
	if err := s.writable(cluster, key, "set"); err != nil {
		return err
	}
	if err := s.allowedHere(key); err != nil {
		return err
	}
	declared, ok := setting(key, name)
	if ok && declared.Text {
		return fmt.Errorf("%w: %s.%s is a line of text, not on or off", ErrSettingKind, key, name)
	}
	if !ok {
		e, _ := lookup(key)
		names := make([]string, 0, len(e.Settings))
		for _, one := range e.Settings {
			names = append(names, one.Key)
		}
		if len(names) == 0 {
			return fmt.Errorf("%w: %q. %s offers no settings", ErrUnknownSetting, name, e.Label)
		}
		return fmt.Errorf("%w: %q. %s offers %s", ErrUnknownSetting, name, e.Label, strings.Join(names, ", "))
	}
	if s.SettingPinned(key, name) {
		return fmt.Errorf("%w, so %s.%s cannot be changed here", ErrSettingPinned, key, name)
	}

	s.mu.Lock()
	s.set(cluster, key, name, value)
	err := s.persistLocked()
	s.mu.Unlock()
	return err
}

// ErrNotInValues is what SetOn and SetSetting answer under InCluster for an
// extra the chart's values (KT_EXTRAS) do not name. Its own error so
// a handler answers a refusal, not a malformed request.
var ErrNotInValues = errors.New("not enabled in this installation's chart values")

// NotInValues is the reason the Settings page draws beside the disabled button
// of such an extra, and the start of the refusal.
const NotInValues = "Not enabled in this installation's chart values."

// allowedHere refuses an extra this installation does not allow (InCluster).
func (s *Store) allowedHere(key string) error {
	if s.Allowed(key) {
		return nil
	}
	label := key
	if e, ok := lookup(key); ok {
		label = e.Label
	}
	return fmt.Errorf("%w: %s cannot be turned on or configured here. The chart's values "+
		"(%s) name the extras this installation may turn on", ErrNotInValues, label, EnvExtras)
}

// ErrUnknownSetting is what SetSetting answers for a setting the extension does
// not declare. Its own error so a handler answers it as a 404, like a key that
// names no extension.
var ErrUnknownSetting = fmt.Errorf("no such setting")

// ErrSettingKind is a checkbox written as text or a line of text written as on
// or off: a request the setting cannot take, answered as a 400.
var ErrSettingKind = fmt.Errorf("that setting is not of this kind")

// ErrTextInvalid is a text value the setting's Check refused, answered as a
// 400 with Check's sentence.
var ErrTextInvalid = fmt.Errorf("that value cannot be used")

// ErrTextPinned is a text setting its environment variable decides.
var ErrTextPinned = fmt.Errorf("this setting is set by an environment variable")

// ErrTextInCluster is a text setting written under the server profile, where
// the chart's values decide it: a line of text can name a Secret the console
// reads with its own credentials, which a person's `use` does not cover.
var ErrTextInCluster = fmt.Errorf("under the server profile this setting is the chart's values")

// Text is one text setting's value on one cluster: what
// was set there, or empty. The setting's Env is not read here - the extension
// reads its own variable and decides what wins - and a key that names no text
// setting is empty.
func (s *Store) Text(cluster, key, name string) string {
	declared, ok := setting(key, name)
	if !ok || !declared.Text {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.texts[cluster][key][name]
}

// SetText writes one text setting of one extra on one cluster, or forgets it
// when value is empty after trimming. Refused for a checkbox, for a value with
// a line break or that Check refuses, and while the setting's Env is set: the
// deployment decided it everywhere, and a write that changed nothing would
// read as broken.
func (s *Store) SetText(cluster, key, name, value string) error {
	if err := s.writable(cluster, key, "set"); err != nil {
		return err
	}
	if s.inCluster {
		return fmt.Errorf("%w, so %s.%s cannot be changed here", ErrTextInCluster, key, name)
	}
	declared, ok := setting(key, name)
	if !ok {
		e, _ := lookup(key)
		return fmt.Errorf("%w: %q. %s %s", ErrUnknownSetting, name, e.Label, offered(key))
	}
	if !declared.Text {
		return fmt.Errorf("%w: %s.%s is on or off, not a line of text", ErrSettingKind, key, name)
	}
	if textPinned(declared) {
		return fmt.Errorf("%w (%s), so %s.%s cannot be changed here", ErrTextPinned, declared.Env, key, name)
	}
	value = strings.TrimSpace(value)
	if err := checkText(declared, value); err != nil {
		return fmt.Errorf("%w: %v", ErrTextInvalid, err)
	}

	s.mu.Lock()
	s.setText(cluster, key, name, value)
	err := s.persistLocked()
	s.mu.Unlock()
	return err
}

// checkText is the setting's own Check, after the one rule every line of text
// shares. An empty value is "not set" and always accepted.
func checkText(declared Setting, value string) error {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		return nil
	case strings.ContainsAny(value, "\r\n"):
		return fmt.Errorf("it is more than one line")
	case len(value) > 253:
		return fmt.Errorf("it is longer than 253 characters")
	case len(declared.Choices) > 0:
		values := make([]string, 0, len(declared.Choices))
		for _, one := range declared.Choices {
			if one.Value == value {
				if declared.Check != nil {
					return declared.Check(value)
				}
				return nil
			}
			values = append(values, one.Value)
		}
		return fmt.Errorf("it is not one of %s", strings.Join(values, ", "))
	case declared.Check != nil:
		return declared.Check(value)
	}
	return nil
}

// textPinned is whether a text setting's environment variable is set. Read on
// every call rather than once in New: the catalog may be registered after
// the store is opened, and an environment does not change under a process.
func textPinned(declared Setting) bool {
	return declared.Env != "" && strings.TrimSpace(os.Getenv(declared.Env)) != ""
}

// writable is the two refusals SetOn and SetSetting share: a key this build
// does not carry, and no cluster.
func (s *Store) writable(cluster, key, what string) error {
	if !Known(key) {
		keys := Keys()
		if len(keys) == 0 {
			return fmt.Errorf("%w: %q. This build of KubeTower carries no extra pages", ErrUnknown, key)
		}
		return fmt.Errorf("%w: %q. The %d are %s", ErrUnknown, key, len(keys), strings.Join(keys, ", "))
	}
	if cluster == "" {
		return fmt.Errorf("no cluster was named, so there is nothing to %s", what)
	}
	return nil
}

// persistLocked writes the file, if there is one, and keeps the log honest.
// Callers hold mu.
//
// **The log calls are inside the critical section.**
// `Complainer` has no lock of its own and says so: *"The caller holds whatever
// lock guards the store; every call site is inside a save() that already
// does"*. The Settings page posts **one extra per request**, so ticking two
// boxes quickly is two goroutines read-modify-writing `c.said`; outside the
// lock that is a data race, and the race detector is run against this tree
// deliberately.
func (s *Store) persistLocked() error {
	if s.path == "" {
		return nil
	}
	err := s.write()
	if err != nil {
		s.say.Once("extras: %v", err)
	} else {
		s.say.Clear()
	}
	return err
}

// ErrNotAvailable is what SetOn answers for a page that is off everywhere.
var ErrNotAvailable = fmt.Errorf("that extra is not enabled on this KubeTower")

// Pinned reports whether the deployment decided this one (KT_EXTRAS), and so whether the
// settings page must draw its switch disabled.
func (s *Store) Pinned(key string) bool {
	_, ok := s.pinned[key]
	return ok
}

// Any reports whether anything is ticked anywhere, which is what decides
// whether the menu draws an Extra group at all.
func (s *Store) Any() bool {
	for _, e := range Catalogue() {
		if s.Enabled(e.Key) {
			return true
		}
	}
	return false
}

// State is the extras as the settings page draws them **for one cluster**, in
// menu order.
//
// The page is a block per cluster with a box per extra, so the cluster is the
// question and the extra is the answer — the axis where a tick's blast radius
// is visible from where the tick is made.
func (s *Store) State(cluster string) []State {
	list := Catalogue()
	out := make([]State, 0, len(list))
	for _, e := range list {
		out = append(out, State{
			Extra:   e,
			On:      s.EnabledOn(cluster, e.Key),
			Pinned:  s.Pinned(e.Key),
			Allowed: s.Allowed(e.Key),
			Note:    s.note(e.Key),
			Values:  s.values(cluster, e),
			Texts:   s.textValues(cluster, e),

			SettingsPinned: s.settingsPinned(e),
		})
	}
	return out
}

// settingsPinned is whether each of an extra's settings is pinned, in declared
// order.
func (s *Store) settingsPinned(e Extra) []bool {
	out := make([]bool, 0, len(e.Settings))
	for _, one := range e.Settings {
		if one.Text {
			out = append(out, textPinned(one))
			continue
		}
		out = append(out, s.SettingPinned(e.Key, one.Key))
	}
	return out
}

// textValues is each of an extra's text settings on one cluster, in declared
// order, empty for a checkbox.
func (s *Store) textValues(cluster string, e Extra) []string {
	out := make([]string, 0, len(e.Settings))
	for _, one := range e.Settings {
		out = append(out, s.Text(cluster, e.Key, one.Key))
	}
	return out
}

// values is each of an extra's settings on one cluster, in declared order.
func (s *Store) values(cluster string, e Extra) []bool {
	out := make([]bool, 0, len(e.Settings))
	for _, one := range e.Settings {
		out = append(out, s.Setting(cluster, e.Key, one.Key))
	}
	return out
}

// note is State.Note: why the switch is not this console's to move, or empty.
func (s *Store) note(key string) string {
	if !s.Allowed(key) {
		return NotInValues
	}
	return s.PinnedNote(key)
}

// PinnedNote is the sentence for a pinned switch, carried on State.Note, or
// empty when the operator owns the choice. The Settings page's Extra tab draws
// the reason the listing carries for each row (internal/web), which is this
// sentence for a pinned extra.
//
// Here rather than in the template because it states *what* the deployment said,
// and a template that had to work that out would be reimplementing Enabled.
func (s *Store) PinnedNote(key string) string {
	on, ok := s.pinned[key]
	if !ok {
		return ""
	}
	if on {
		return "Turned on by " + EnvExtras + " " + WhereSet + "; it cannot be turned off here."
	}
	return "Turned off by " + EnvExtras + " " + WhereSet + "; it cannot be turned on here."
}

// ErrPinned is what Set answers for an extra the deployment decided. Its own
// error so a handler can tell it from a typo and say the useful thing.
var ErrPinned = fmt.Errorf("this extra is set by %s %s", EnvExtras, WhereSet)

// WhereSet is where every sentence says KT_EXTRAS is set: the deployment's
// environment, whatever spells it - a Helm value, a manifest, a shell.
//
// One constant for PinnedNote, ErrPinned, the start-up line and HowToTurnOn
// below, so the sentences cannot drift apart: spelled separately, one of them
// keeps naming a place after the others have been corrected.
const WhereSet = "in the deployment's environment"

// Profile is which switches exist: the sentence HowToTurnOn writes, and with
// Switchable whether the per-cluster write is registered at all.
type Profile int

const (
	// Desktop is the desktop and the container: the per-cluster switch, and
	// KT_EXTRAS pinning for every cluster.
	Desktop Profile = iota
	// Server is the server profile with one replica: the chart's values say
	// what may be turned on, and a person allowed the verb `use` turns it on
	// per cluster.
	Server
	// Replicated is several replicas behind one address, always under the
	// server profile: no per-cluster switch is registered, so nothing can turn
	// an extra on.
	Replicated
	// Unkept is the server profile with one replica and no state volume
	// (KT_STATE_PERSISTENT=false): the per-cluster switch is not registered
	// either, since a choice would be gone at the next restart.
	Unkept
)

// ProfileOf is which switches exist for a console run in a cluster or not, as
// one of several replicas or not, and keeping its state directory or not. The
// start-up line and the console's own refusals both ask it, so the two cannot
// name different switches. Several replicas, and a state directory the server
// profile does not keep, both leave the per-cluster write unregistered
// (internal/api); each is named as what it is.
func ProfileOf(inCluster, several, ephemeral bool) Profile {
	switch {
	case several:
		return Replicated
	case inCluster && ephemeral:
		return Unkept
	case inCluster:
		return Server
	}
	return Desktop
}

// Switchable is whether the per-cluster write (`PUT /api/c/{cluster}/extensions/{key}`
// and the settings and suggestions beside it) is registered under this profile:
// on the desktop and under the server profile with one replica and its state
// volume, never on one of several replicas nor where the state does not survive
// a restart. internal/api's router and internal/web's listing both
// read it here, so neither spells the rule by hand.
func (p Profile) Switchable() bool {
	return p == Desktop || p == Server
}

// HowToTurnOn is the sentence naming the switches that turn an extra on, spelled
// so they can be typed: the Settings page's Extra tab and the per-cluster write
// behind it, `PUT /api/c/{cluster}/extensions/{key}` (`setExtensionEnabled` in
// `api/openapi.yaml`), and on the desktop the deployment's own say, KT_EXTRAS,
// which turns a page on for every cluster at once and which the per-cluster
// switch cannot override. Per-cluster first, because it is the narrower of the
// two and the one that does not need a restart. The header is the API's mark
// on every mutation, and a sentence that left it out would be a command that
// is refused when typed.
//
// clusterSegment is the cluster as the API's `{cluster}` takes it - the context
// name percent-encoded - or a placeholder such as `<cluster>` where there is no
// cluster to name. An empty key reads "an extra" and `<extra>`: a sentence about
// all of them, which is what the start-up line says when none is on.
//
// **Under the server profile** the sentence names who
// may press the switch - a person holding the verb `use` on
// `extras.kubetower.dev/<key>` - and that the chart's values must name
// the extra first, which is what KT_EXTRAS means there.
//
// **Where several replicas share one address the PUT is not named**, because
// it is not there: `internal/api` registers it only where `Deps.Replicated`
// does not hold (each write a file in one pod's directory). Nothing
// else turns an extra on in a cluster, so the sentence says to run one replica
// with its state volume.
func HowToTurnOn(clusterSegment, key string, profile Profile) string {
	subject, value := "it", key
	if key == "" {
		subject, value = "an extra", "<extra>"
	}
	put := fmt.Sprintf("PUT /api/c/%s/extensions/%s, the header X-KubeTower-Request: 1 and the "+
		"body {\"enabled\": true}", clusterSegment, value)
	switch profile {
	case Unkept:
		return fmt.Sprintf("This console keeps no state volume (KT_STATE_PERSISTENT=false), so it "+
			"has no per-cluster switch and no extra can be turned on: run it with its state volume, "+
			"and turn %s on in Settings > Extra.", subject)
	case Replicated:
		return fmt.Sprintf("This console runs as several replicas, which have no per-cluster "+
			"switch, so no extra can be turned on: run one replica with its state volume, and "+
			"turn %s on in Settings > Extra.", subject)
	case Server:
		return fmt.Sprintf("Turn %s on for this cluster in Settings > Extra, or with %s, as a "+
			"person holding the verb use on extras.kubetower.dev/%s - once the chart's values "+
			"name it in %s.", subject, put, value, EnvExtras)
	}
	return fmt.Sprintf("Turn %s on for one cluster in Settings > Extra, or with %s; or for every "+
		"cluster with %s=%s %s and a restart of KubeTower.",
		subject, put, EnvExtras, value, WhereSet)
}

// ErrUnknown is what Set answers for a key that is not an extra.
var ErrUnknown = fmt.Errorf("no such extra")

// There is no Set: ticking is per cluster, see SetOn.
//
// A setter that turned a page on everywhere at once would contradict the rule
// that a tick reaches exactly the cluster it was made on.

// write persists the choices. Called with the lock held.
//
// Through statefile.Write, so it is atomic and durable and says "in effect now,
// but it will not survive a restart" when it is neither.
//
// encoding/json writes a map's keys sorted, so the file does not reshuffle
// itself on every save - a state file whose diff is noise is one nobody reads.
func (s *Store) write() error {
	doc := stored{Format: formatChoices}
	// What this build does not carry first, so nothing it does carry can be
	// shadowed by it: the two key sets are disjoint by construction.
	for cluster, keys := range s.keptOn {
		for key, on := range keys {
			if doc.On == nil {
				doc.On = map[string]map[string]bool{}
			}
			if doc.On[cluster] == nil {
				doc.On[cluster] = map[string]bool{}
			}
			doc.On[cluster][key] = on
		}
	}
	for cluster, keys := range s.keptSettings {
		for key, values := range keys {
			if doc.Settings == nil {
				doc.Settings = map[string]map[string]map[string]bool{}
			}
			if doc.Settings[cluster] == nil {
				doc.Settings[cluster] = map[string]map[string]bool{}
			}
			row := map[string]bool{}
			for name, value := range values {
				row[name] = value
			}
			doc.Settings[cluster][key] = row
		}
	}
	for cluster, keys := range s.keptTexts {
		for key, values := range keys {
			for name, value := range values {
				doc.Texts = putText(doc.Texts, cluster, key, name, value)
			}
		}
	}
	for cluster, keys := range s.texts {
		for key, values := range keys {
			for name, value := range values {
				doc.Texts = putText(doc.Texts, cluster, key, name, value)
			}
		}
	}
	for cluster, keys := range s.on {
		if len(keys) == 0 {
			continue
		}
		if doc.On == nil {
			doc.On = map[string]map[string]bool{}
		}
		// Merged into the row the kept choices may have started, never
		// replacing it.
		if doc.On[cluster] == nil {
			doc.On[cluster] = make(map[string]bool, len(keys))
		}
		for key, on := range keys {
			doc.On[cluster][key] = on
		}
	}
	for cluster, keys := range s.settings {
		for key, values := range keys {
			if len(values) == 0 {
				continue
			}
			if doc.Settings == nil {
				doc.Settings = map[string]map[string]map[string]bool{}
			}
			if doc.Settings[cluster] == nil {
				doc.Settings[cluster] = map[string]map[string]bool{}
			}
			row := make(map[string]bool, len(values))
			for name, value := range values {
				row[name] = value
			}
			doc.Settings[cluster][key] = row
		}
	}
	// Enabled and Off are deliberately left zero: the previous format is read and
	// never written back, so the first write after an upgrade is what retires it.

	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return statefile.Write(s.path, raw)
}

// putText sets one value in a stored.Texts, making its rows as needed.
func putText(in map[string]map[string]map[string]string, cluster, key, name, value string) map[string]map[string]map[string]string {
	if in == nil {
		in = map[string]map[string]map[string]string{}
	}
	if in[cluster] == nil {
		in[cluster] = map[string]map[string]string{}
	}
	if in[cluster][key] == nil {
		in[cluster][key] = map[string]string{}
	}
	in[cluster][key][name] = value
	return in
}
