package launch

import (
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/thonicdev/kubetower/internal/web"
)

// printNotices writes the third-party notices the bundle carries, for
// `--notices`. bundle is Run's, nil for the embedded tree.
//
// A build without them is an error that says so, never an empty page: an
// empty answer and a failed one are different answers.
func printNotices(w io.Writer, bundle fs.FS) error {
	text, err := web.Notices(bundle)
	if errors.Is(err, fs.ErrNotExist) {
		return errors.New("this build carries no third-party notices: it was not built by a release " +
			"(release.yml, the Dockerfile, or `task build`), and a release ships them as THIRD_PARTY_NOTICES.txt")
	}
	if err != nil {
		return fmt.Errorf("reading the third-party notices: %w", err)
	}
	_, err = w.Write(text)
	return err
}
