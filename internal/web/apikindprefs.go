package web

import (
	"context"
	"errors"
	"fmt"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/kindprefs"
	"github.com/thonicdev/kubetower/internal/statefile"
)

// apiKindPrefs keeps each person's choices about the kinds column (whether empty
// kinds show, favorite kinds per cluster) through internal/kindprefs. Where
// kindPrefsOffered is false both answer api.ErrNotInProfile and the browser
// keeps them.
type apiKindPrefs struct{ s *Server }

func (k apiKindPrefs) KindPreferences(ctx context.Context) (api.KindPreferences, error) {
	who, err := k.person(ctx)
	if err != nil {
		return api.KindPreferences{}, err
	}
	prefs, err := k.s.kindPrefs.Get(who)
	if err != nil {
		return api.KindPreferences{}, kindPrefsAnswer(err)
	}
	return api.KindPreferences(prefs), nil
}

func (k apiKindPrefs) SetKindPreferences(ctx context.Context, prefs api.KindPreferences) (api.KindPreferences, error) {
	who, err := k.person(ctx)
	if err != nil {
		return api.KindPreferences{}, err
	}
	kept, err := k.s.kindPrefs.Set(who, kindprefs.Prefs(prefs))
	if err != nil {
		return api.KindPreferences{}, kindPrefsAnswer(err)
	}
	return api.KindPreferences(kept), nil
}

// person is who the choices are kept for, or the refusal. A ctx with no person
// is refused rather than kept under one shared key: every route here is behind
// the sign-in, which always sets one.
func (k apiKindPrefs) person(ctx context.Context) (identity.Who, error) {
	if !k.s.kindPrefsOffered() {
		return identity.Who{}, api.ErrNotInProfile
	}
	who, ok := identity.From(ctx)
	if !ok {
		return identity.Who{}, fmt.Errorf("%w: %v", api.ErrRefused, kindprefs.ErrNobody)
	}
	return who, nil
}

// kindPrefsAnswer maps the store's errors onto the contract's. A file that
// cannot be read stays a 500 naming the file: unknown is not empty.
func kindPrefsAnswer(err error) error {
	switch {
	case errors.Is(err, statefile.ErrNotSaved):
		return storeAnswer(err)
	case errors.Is(err, kindprefs.ErrTooLarge):
		return fmt.Errorf("%w: %s", api.ErrInvalid, err.Error())
	case errors.Is(err, kindprefs.ErrNobody):
		return fmt.Errorf("%w: %s", api.ErrRefused, err.Error())
	}
	return err
}

// kindPrefsOffered is Capabilities.kindPreferences, so the capability and the
// store cannot disagree. It follows the extras' per-cluster switch: the desktop,
// or the server profile with one replica and a persistent state volume. Several
// replicas would each keep half a person's choices, and an emptyDir would forget
// them on restart.
func (s *Server) kindPrefsOffered() bool {
	return s.kindPrefs != nil && s.extensionSwitch()
}
