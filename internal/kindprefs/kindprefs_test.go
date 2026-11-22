package kindprefs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/statefile"
)

var (
	alice = identity.Who{Name: "alice", Method: identity.SSO}
	bob   = identity.Who{Name: "bob", Method: identity.SSO}
)

func TestTwoPeopleKeepTwoDifferentChoices(t *testing.T) {
	s := New(t.TempDir(), nil)
	if _, err := s.Set(alice, Prefs{ShowUnused: true, Favourites: map[string][]string{"prod": {"pods"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set(bob, Prefs{Favourites: map[string][]string{"prod": {"secrets", "deployments"}}}); err != nil {
		t.Fatal(err)
	}
	a, err := s.Get(alice)
	if err != nil || !a.ShowUnused || strings.Join(a.Favourites["prod"], ",") != "pods" {
		t.Errorf("alice reads %+v, %v", a, err)
	}
	b, err := s.Get(bob)
	if err != nil || b.ShowUnused || strings.Join(b.Favourites["prod"], ",") != "secrets,deployments" {
		t.Errorf("bob reads %+v, %v", b, err)
	}
}

func TestAPersonNeverSeenGetsTheDefault(t *testing.T) {
	got, err := New(t.TempDir(), nil).Get(alice)
	if err != nil || got.ShowUnused || got.Favourites == nil || len(got.Favourites) != 0 {
		t.Errorf("read %+v, %v; want showUnused false and an empty, non-nil favorites", got, err)
	}
}

// An SSO name equal to the shared account's user name is somebody else.
func TestTheSameNameByAnotherMethodIsAnotherPerson(t *testing.T) {
	s := New(t.TempDir(), nil)
	if _, err := s.Set(identity.Who{Name: "admin", Method: identity.Password}, Prefs{ShowUnused: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(identity.Who{Name: "admin", Method: identity.SSO}); got.ShowUnused {
		t.Error("an SSO person named admin reads the shared account's choices")
	}
}

// The shared account is one key for everybody who holds it, through the typed
// password or the desktop window's launch code alike.
func TestTheSharedAccountIsOneKeyWhicheverDoor(t *testing.T) {
	s := New(t.TempDir(), nil)
	typed := identity.Who{Name: "admin", Method: identity.Password}
	window := identity.Who{Name: "admin", Method: identity.Launch, Window: true}
	if _, err := s.Set(typed, Prefs{ShowUnused: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(window); !got.ShowUnused {
		t.Error("the window does not read what the password set")
	}
}

func TestTheChoicesSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(dir, nil).Set(alice, Prefs{ShowUnused: true, Favourites: map[string][]string{"dev": {"pods"}}}); err != nil {
		t.Fatal(err)
	}
	got, err := New(dir, nil).Get(alice)
	if err != nil || !got.ShowUnused || strings.Join(got.Favourites["dev"], ",") != "pods" {
		t.Errorf("after a restart alice reads %+v, %v", got, err)
	}
}

func TestTheFileNeverHoldsTheName(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(dir, nil).Set(identity.Who{Name: "alice@example.org", Method: identity.SSO}, Prefs{}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "alice") {
		t.Errorf("the file names the person:\n%s", raw)
	}
}

// Unknown is not empty: a file that does not parse is an error to every read
// and write, and is left as it is.
func TestACorruptFileIsAnErrorAndIsNotOverwritten(t *testing.T) {
	for name, content := range map[string]string{
		"not JSON":           "{\"version\":1,\"people\":{",
		"empty":              "",
		"from a newer build": `{"version":2,"people":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, FileName)
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			var logged []string
			s := New(dir, func(f string, a ...any) { logged = append(logged, f) })
			if len(logged) == 0 {
				t.Error("nothing was said at start-up")
			}
			if _, err := s.Get(alice); !errors.Is(err, ErrUnreadable) {
				t.Errorf("the read answered %v; want ErrUnreadable", err)
			}
			if _, err := s.Set(alice, Prefs{ShowUnused: true}); !errors.Is(err, ErrUnreadable) {
				t.Errorf("the write answered %v; want ErrUnreadable", err)
			}
			if raw, _ := os.ReadFile(path); string(raw) != content {
				t.Errorf("the file was rewritten to %q", raw)
			}

			// Repaired, it is read without a restart.
			if err := os.WriteFile(path, []byte(`{"version":1,"people":{}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Get(alice); err != nil {
				t.Errorf("after the repair the read answered %v", err)
			}
		})
	}
}

func TestARequestWithNobodyIsRefused(t *testing.T) {
	s := New(t.TempDir(), nil)
	if _, err := s.Get(identity.Who{}); !errors.Is(err, ErrNobody) {
		t.Errorf("the read answered %v", err)
	}
	if _, err := s.Set(identity.Who{}, Prefs{}); !errors.Is(err, ErrNobody) {
		t.Errorf("the write answered %v", err)
	}
}

func TestOnePersonPastTheirShareIsRefusedAndNothingChanges(t *testing.T) {
	s := New(t.TempDir(), nil)
	kinds := make([]string, 0, 512)
	for i := 0; i < 512; i++ {
		kinds = append(kinds, strings.Repeat("k", 200))
	}
	if _, err := s.Set(alice, Prefs{ShowUnused: true, Favourites: map[string][]string{"prod": kinds}}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("answered %v; want ErrTooLarge", err)
	}
	if got, _ := s.Get(alice); got.ShowUnused {
		t.Error("a refused write changed something")
	}
}

func TestPastMaxPeopleTheOneWrittenLongestAgoIsForgotten(t *testing.T) {
	clock := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	s := newStore(t.TempDir(), nil, func() time.Time { clock = clock.Add(time.Second); return clock })
	for i := 0; i < MaxPeople; i++ {
		s.people[keyOf(identity.Who{Name: string(rune('a'+i%26)) + strings.Repeat("x", i/26), Method: identity.SSO})] =
			&entry{Prefs: Prefs{ShowUnused: true}, Written: clock.Add(time.Duration(i) * time.Second)}
	}
	first := identity.Who{Name: "a", Method: identity.SSO} // i == 0, the oldest
	if _, err := s.Set(alice, Prefs{ShowUnused: true}); err != nil {
		t.Fatal(err)
	}
	if len(s.people) != MaxPeople {
		t.Errorf("the file holds %d people; want %d", len(s.people), MaxPeople)
	}
	if got, _ := s.Get(first); got.ShowUnused {
		t.Error("the person written longest ago is still there")
	}
	if got, _ := s.Get(alice); !got.ShowUnused {
		t.Error("the newcomer was not kept")
	}
}

// Memory first, then the file: a write that fails is in effect and says so.
func TestAWriteThatCannotBeSavedIsInEffectAndSaysSo(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, "file-not-dir"), nil)
	if err := os.WriteFile(filepath.Join(dir, "file-not-dir"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set(alice, Prefs{ShowUnused: true}); !errors.Is(err, statefile.ErrNotSaved) {
		t.Fatalf("answered %v; want statefile.ErrNotSaved", err)
	}
	if got, _ := s.Get(alice); !got.ShowUnused {
		t.Error("the change is not in effect")
	}
}

func TestTheCallerCannotChangeWhatIsKept(t *testing.T) {
	s := New(t.TempDir(), nil)
	given := map[string][]string{"prod": {"pods"}}
	if _, err := s.Set(alice, Prefs{Favourites: given}); err != nil {
		t.Fatal(err)
	}
	given["prod"][0] = "secrets"
	got, _ := s.Get(alice)
	got.Favourites["prod"][0] = "nodes"
	if again, _ := s.Get(alice); again.Favourites["prod"][0] != "pods" {
		t.Errorf("kept %v", again.Favourites)
	}
}
