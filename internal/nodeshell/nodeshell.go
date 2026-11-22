// Package nodeshell is one setting: which container image a shell on a node
// runs in, and the small JSON file in the state directory that remembers it.
//
// It knows nothing about Kubernetes: `internal/terminal` turns the image into a
// pod on a node, and `internal/web` draws the field. Keeping each persisted
// setting in a package of its own lets "everything KubeTower writes to disk" be
// checked by listing directories.
//
// `kubectl debug node/<name>` has no default image; a button that then asks for
// one saves nobody anything, so there is a default, stated and changeable on the
// settings page.
package nodeshell

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/thonicdev/kubetower/internal/statefile"
)

// FileName is the setting's file inside the state directory. The directory,
// the atomic write and the once-only logging are `internal/statefile`'s; what
// stays here is Validate.
const FileName = "nodeshell.json"

// EnvImage seeds the default, so a deployment can arrive with the decision
// already made. A stored value wins over it.
const EnvImage = "KT_NODE_SHELL_IMAGE"

// DefaultImage is what a node shell runs when nobody has said otherwise.
//
// alpine:3.24 is what this repository's Dockerfile builds from (ARG
// ALPINE_VERSION=3.24), so it is known to exist; it has `/bin/sh`, the whole
// requirement (the pod sleeps and the shell is exec-ed into it), plus `apk` for
// installing a tool on the spot. A default should be an image whose provenance
// an operator can verify at a glance, hence not a toolbox image.
//
// It is pinned by digest: the pod is privileged, shares the node's PID
// namespace, mounts its `/` and pulls `IfNotPresent`, so a bare tag would be
// whatever the registry or the node's cache answers. The digest is the
// multi-arch index's, so the kubelet resolves the node's architecture from it.
// The tag stays in front for the reader and for Renovate, which moves both
// together; `TestTheDefaultImageIsTheDockerfilesAlpine` holds the tag to the
// Dockerfile's `ALPINE_VERSION`.
const DefaultImage = "alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a"

// Settings is the whole of what this package remembers.
type Settings struct {
	// Image is the container image a node shell runs. Never empty as read: Set
	// turns an empty value into the default, since a pod cannot be created
	// without an image.
	Image string `json:"image"`
}

// imageRE is a deliberately permissive shape check for an image reference.
// The kubelet is the authority on whether an image exists; this only refuses
// what is certainly not one (spaces, control characters, a leading dash, shell
// syntax) before it reaches the API server as an invalid pod spec. It is not the
// distribution spec's grammar, which the registry already enforces.
var imageRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/@-]*$`)

// MaxImageLength is the ceiling: a reference with a registry, a long path and
// a digest runs to about 200 characters, and the field must not double as
// storage for anything else.
const MaxImageLength = 300

// Validate reports whether a value can be used as an image reference, and
// says what is wrong when it cannot: the sentence is shown next to the field
// on the settings page.
func Validate(image string) error {
	image = strings.TrimSpace(image)
	if image == "" {
		return errors.New("an image is required; leave it as " + DefaultImage + " if you have no preference")
	}
	if len(image) > MaxImageLength {
		return fmt.Errorf("that is %d characters long, and an image reference is not; the limit here is %d",
			len(image), MaxImageLength)
	}
	if !imageRE.MatchString(image) {
		return errors.New("that is not an image reference — it may contain letters, digits and " +
			"`.` `_` `-` `:` `/` `@`, and nothing else. Spaces and shell syntax are what this refuses")
	}
	return nil
}

// Store is the setting and the file under it. Everything goes through the
// mutex, the file write included, so two concurrent saves cannot interleave.
type Store struct {
	// path is empty when there is nowhere to persist to; the setting then lives
	// as long as the process.
	path string
	// say logs a persistence problem once. See statefile.Complainer.
	say *statefile.Complainer

	mu       sync.RWMutex
	settings Settings
}

// New opens the store in dir, which may be empty. It never fails: an
// unreadable state directory loses the setting, not the console.
func New(dir string, logf func(string, ...any)) *Store {
	s := &Store{say: statefile.NewComplainer(logf), settings: Settings{Image: defaultFromEnv()}}
	if dir == "" {
		return s
	}
	s.path = filepath.Join(dir, FileName)
	s.load()
	return s
}

// defaultFromEnv is the shipped default, or what EnvImage puts in front of
// it. An invalid value is ignored: an environment variable is not a place an
// operator sees a refusal.
func defaultFromEnv() string {
	image := strings.TrimSpace(os.Getenv(EnvImage))
	if image == "" || Validate(image) != nil {
		return DefaultImage
	}
	return image
}

// Dir is where the setting is kept: KT_STATE_DIR, or a per-user path. It is
// statefile.Dir, so all of KubeTower's state shares one directory.
func Dir() string { return statefile.Dir() }

// Path is the file, or empty when there is none. The startup log prints it.
func (s *Store) Path() string { return s.path }

func (s *Store) load() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		// A file that is not there is the ordinary first run.
		if !os.IsNotExist(err) {
			s.say.Log("cannot read %s, the node shell image is back at its default: %v", s.path, err)
		}
		return
	}
	var stored Settings
	if err := json.Unmarshal(raw, &stored); err != nil {
		s.say.Log("%s is unreadable, the node shell image is back at its default: %v", s.path, err)
		return
	}
	// Validated on the way in too: the file is editable by hand, and a bad
	// reference would otherwise surface only as a rejected pod spec.
	if Validate(stored.Image) != nil {
		s.say.Log("%s names %q, which is not an image reference; using %s",
			s.path, stored.Image, DefaultImage)
		return
	}
	s.settings = stored
}

// Settings is what is stored now.
func (s *Store) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// Image is the stored image alone.
func (s *Store) Image() string { return s.Settings().Image }

// Set applies a new image and persists it.
//
// It returns what is now in effect alongside any error, independently: a
// valid value is applied in memory even when the file cannot be written, so the
// caller can say "in effect, but it will not survive a restart".
func (s *Store) Set(image string) (Settings, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		image = DefaultImage
	}
	if err := Validate(image); err != nil {
		return s.Settings(), err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings.Image = image
	if s.path == "" {
		return s.settings, nil
	}
	if err := s.write(); err != nil {
		s.say.Once("nodeshell: %v", err)
		return s.settings, err
	}
	s.say.Clear()
	return s.settings, nil
}

// write persists the setting. Called with the lock held. statefile.Write
// replaces the file atomically, since a truncated file would be read back as
// unparseable and silently reset to the default image.
func (s *Store) write() error {
	raw, err := json.MarshalIndent(s.settings, "", "  ")
	if err != nil {
		// Unreachable for one string, but reported rather than ignored in case
		// Settings grows.
		return fmt.Errorf("%w: the setting could not be encoded (%v)", statefile.ErrNotSaved, err)
	}
	return statefile.Write(s.path, append(raw, '\n'))
}
