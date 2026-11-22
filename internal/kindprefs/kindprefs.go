// Package kindprefs keeps each person's choices about the kinds column:
// whether it shows the kinds holding no object they may see, and their
// favorite kinds per cluster. It is per person because under the server
// profile several people share one console.
//
// # The file
//
// One file, FileName, for everybody, written whole through statefile.Write
// under the store's mutex:
//
//	{"version":1,"people":{"<sha256 hex of Who.Key>":{"showUnused":false,
//	 "favourites":{"<cluster>":["<kind slug>"]},"written":"<RFC 3339>"}}}
//
// A person is keyed by a digest of their identity, never by the name, so the
// backed-up file does not list who uses the console.
//
// # Unknown is not empty
//
// Unlike the other stores, a file that cannot be read or parsed, or that a
// newer build wrote, is an error to every read and write until it reads again,
// and it is left as it is: resetting and overwriting it would silently lose
// everybody's favorites.
package kindprefs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/statefile"
)

// FileName is the file in the state directory.
const FileName = "kindprefs.json"

// formatVersion is what this build writes. A file with a higher one is
// refused rather than rewritten in this build's shape.
const formatVersion = 1

// MaxPeople is how many people the file holds. Past it, the person written
// longest ago is forgotten and finds the defaults again.
const MaxPeople = 1000

// MaxPersonBytes bounds one person's entry, encoded, so the file, rewritten
// whole on every write, stays cheap to rewrite: 64 MiB at worst for MaxPeople.
const MaxPersonBytes = 64 << 10

var (
	// ErrNobody is a request carrying no person, refused rather than kept under
	// one key for everybody.
	ErrNobody = errors.New("no person to keep the kinds column's choices for")
	// ErrTooLarge is one person's choices past MaxPersonBytes. Nothing changed.
	ErrTooLarge = errors.New("these choices are larger than one person's share of the file")
	// ErrUnreadable is the file that exists and cannot be read, parsed, or was
	// written by a newer build. Nothing is read or written until it reads again.
	ErrUnreadable = errors.New("the kinds column's choices could not be read")
)

// Prefs is one person's choices. The zero value, with an empty Favorites, is
// what a person never seen gets.
type Prefs struct {
	ShowUnused bool                `json:"showUnused"`
	Favourites map[string][]string `json:"favourites"`
}

type entry struct {
	Prefs
	Written time.Time `json:"written"`
}

type persisted struct {
	Version int               `json:"version"`
	People  map[string]*entry `json:"people"`
}

// Store is the file and its contents in memory.
type Store struct {
	path string
	say  *statefile.Complainer
	now  func() time.Time

	mu     sync.Mutex
	people map[string]*entry
	// broken is why the file could not be read, nil when it was (or did not
	// exist). Retried on the next call, so a file somebody repaired is read
	// without a restart.
	broken error
}

// New opens the store in dir, reading FileName if there is one. A file that
// cannot be read does not stop the console: the store answers ErrUnreadable
// until it can, and says so once in the log.
func New(dir string, logf func(string, ...any)) *Store {
	return newStore(dir, logf, time.Now)
}

func newStore(dir string, logf func(string, ...any), now func() time.Time) *Store {
	s := &Store{
		path: filepath.Join(strings.TrimSpace(dir), FileName),
		say:  statefile.NewComplainer(logf),
		now:  now,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		s.say.Log("kindprefs: %v; left as it is, and the kinds column's choices are refused until it reads", err)
	}
	return s
}

// Path is the file, said at startup beside the other stores'.
func (s *Store) Path() string { return s.path }

// loadLocked reads the file into memory, or records why it could not.
func (s *Store) loadLocked() error {
	s.broken = nil
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		s.people = map[string]*entry{}
		return nil
	}
	if err != nil {
		s.broken = fmt.Errorf("%w: %s (%v)", ErrUnreadable, s.path, err)
		return s.broken
	}
	var file persisted
	if err := json.Unmarshal(raw, &file); err != nil {
		s.broken = fmt.Errorf("%w: %s is not readable JSON (%v)", ErrUnreadable, s.path, err)
		return s.broken
	}
	if file.Version > formatVersion {
		s.broken = fmt.Errorf("%w: %s was written in format %d by a newer build, this one reads %d",
			ErrUnreadable, s.path, file.Version, formatVersion)
		return s.broken
	}
	s.people = make(map[string]*entry, len(file.People))
	for key, e := range file.People {
		if e != nil {
			s.people[key] = e
		}
	}
	return nil
}

// ready is nil when memory holds the file, retrying a broken read first.
func (s *Store) ready() error {
	if s.broken == nil {
		return nil
	}
	return s.loadLocked()
}

// keyOf is the digest a person is filed under.
// The shared account (identity.Who.Shared) is one key whichever door it came
// through, as for every other per-person limit: its name names nobody.
func keyOf(who identity.Who) string {
	key := who.Key()
	if who.Shared() {
		key = "shared\x00" + who.Name
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// Get is the choices of who, the default for a person never seen.
func (s *Store) Get(who identity.Who) (Prefs, error) {
	if who.Name == "" {
		return Prefs{}, ErrNobody
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return Prefs{}, err
	}
	if e, ok := s.people[keyOf(who)]; ok {
		return clone(e.Prefs), nil
	}
	return clone(Prefs{}), nil
}

// Set replaces the choices of who and answers what is kept.
// Memory first, then the file: a failed write wraps statefile.ErrNotSaved and
// the choices hold until the next start. A refusal (ErrNobody, ErrTooLarge,
// ErrUnreadable) changes nothing.
func (s *Store) Set(who identity.Who, prefs Prefs) (Prefs, error) {
	if who.Name == "" {
		return Prefs{}, ErrNobody
	}
	prefs = clone(prefs)
	encoded, err := json.Marshal(prefs)
	if err != nil {
		return Prefs{}, err
	}
	if len(encoded) > MaxPersonBytes {
		return Prefs{}, fmt.Errorf("%w (%d bytes, at most %d)", ErrTooLarge, len(encoded), MaxPersonBytes)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return Prefs{}, err
	}
	key := keyOf(who)
	if _, known := s.people[key]; !known {
		s.makeRoomLocked()
	}
	s.people[key] = &entry{Prefs: prefs, Written: s.now().UTC()}
	if err := s.saveLocked(); err != nil {
		return clone(prefs), err
	}
	return clone(prefs), nil
}

// makeRoomLocked forgets the people written longest ago until one more fits.
func (s *Store) makeRoomLocked() {
	for len(s.people) >= MaxPeople {
		oldest, at := "", time.Time{}
		for key, e := range s.people {
			// The key breaks a tie, so which one goes does not depend on map order.
			if oldest == "" || e.Written.Before(at) || (e.Written.Equal(at) && key < oldest) {
				oldest, at = key, e.Written
			}
		}
		delete(s.people, oldest)
	}
}

func (s *Store) saveLocked() error {
	// encoding/json writes a map's keys sorted, so two saves of the same
	// choices are the same bytes.
	raw, err := json.MarshalIndent(persisted{Version: formatVersion, People: s.people}, "", "  ")
	if err != nil {
		return err
	}
	if err := statefile.Write(s.path, append(raw, '\n')); err != nil {
		s.say.Once("kindprefs: %v", err)
		return err
	}
	s.say.Clear()
	return nil
}

// clone copies prefs so neither the caller nor the store holds the other's
// slices, with an empty map and empty lists where nil was given.
func clone(p Prefs) Prefs {
	out := Prefs{ShowUnused: p.ShowUnused, Favourites: make(map[string][]string, len(p.Favourites))}
	for cluster, kinds := range p.Favourites {
		out.Favourites[cluster] = append([]string{}, kinds...)
	}
	return out
}
