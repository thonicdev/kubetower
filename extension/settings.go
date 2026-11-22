package extension

// Configurable is an Extension that offers settings of its own: a few choices
// the Settings page draws in the extension's tile, beside its on/off switch, and
// the console keeps per cluster exactly as it keeps the switch.
//
// **Optional, and that is why it is an interface of its own** rather than a
// seventh method on Extension: an extension that offers nothing compiles
// unchanged, and adding a method to Extension would be a breaking change to
// every one of them.
//
// **The console keeps the value; the extension's front end reads it.** The
// choices travel with the extension's row in `GET /api/c/{cluster}/extensions`
// and are written through `PUT
// /api/c/{cluster}/extensions/{key}/settings/{setting}` wherever the switch is:
// on every profile with one replica, and under the server profile only by a
// person holding the `use` permission on the extension, who may turn it on and
// so may configure it. A checkbox is a display choice read in the browser
// (Argo CD's column in the lists). A line of text is read by the extension's
// own handlers through Host.Setting, on every request, so a change applies
// without a restart; it is written on the desktop and in a container only,
// because it can name what the console reads with its own credentials.
type Configurable interface {
	Extension
	// Settings is what the extension offers, in the order the tile draws them.
	// Read once, when the extension is registered. A key is refused at start-up
	// when it is empty, not lower-case and hyphenated, or declared twice.
	Settings() []Setting
}

// Choice is one value a choice setting accepts, and the words that name it.
type Choice struct {
	Value string
	Label string
}

// Setting is one choice an extension offers: a checkbox, or with Text a line of
// text.
type Setting struct {
	// Key is stable within the extension and is stored per cluster under it, so
	// it is not free to change: renaming one returns every saved choice to its
	// default. Lower-case and hyphenated, as an extension key is, because it is
	// the last segment of the setting's write.
	Key string
	// Label is the checkbox's own words.
	Label string
	// Summary is one sentence saying what turning it off changes, drawn under
	// the label. Optional.
	Summary string
	// Default is the value on a cluster where nobody has chosen. A checkbox's
	// only: a text setting's default is empty, which the extension reads as
	// "not set" and answers with its own (Placeholder says which).
	Default bool

	// Text makes it a line of text, edited behind the tile's Edit button and
	// read by the extension's handlers through Host.Setting. Empty is "not set",
	// and saving an empty value returns the setting to it.
	Text bool
	// Placeholder is drawn in an empty text field: what the extension uses
	// when nothing is set. Text settings only, optional.
	Placeholder string
	// Choices makes a text setting a choice: one of these values, drawn as a
	// list instead of a line. The write refuses any other value (400), an
	// empty one still returns the setting to not set, and the first choice is
	// the one the extension answers with then. Text settings only, optional.
	Choices []Choice
	// Env is the environment variable that decides this setting on every
	// cluster when it is set, ahead of anything chosen on a cluster - the
	// deployment's own configuration, as KT_EXTRA_SETTINGS is for a checkbox.
	// The write refuses it then, and the tile says so. Text settings only,
	// optional; the extension reads the variable itself, as it always did.
	Env string
	// Check refuses a text value that cannot be used, with a sentence saying
	// why, before it is stored. Text settings only, optional; nil accepts any
	// value of one line.
	Check func(value string) error
}
