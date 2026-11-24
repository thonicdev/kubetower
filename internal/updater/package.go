package updater

import "errors"

// ErrNoPackageTool: neither pkexec nor a package manager that takes a file.
var ErrNoPackageTool = errors.New("no pkexec and package manager to install the package with")

// packageCommand is the command that installs a downloaded .deb or .rpm, as
// root through pkexec. The first manager found that installs a local file:
// apt-get, then dpkg, for a .deb; dnf, zypper, then rpm, for an .rpm.
func packageCommand(kind Kind, file string, look func(string) (string, error)) ([]string, error) {
	pkexec, err := look("pkexec")
	if err != nil {
		return nil, ErrNoPackageTool
	}
	type way struct {
		tool string
		args []string
	}
	var ways []way
	switch kind {
	case LinuxDeb:
		ways = []way{{"apt-get", []string{"install", "-y", file}}, {"dpkg", []string{"-i", file}}}
	case LinuxRPM:
		ways = []way{
			{"dnf", []string{"install", "-y", file}},
			// The .rpm is not signed; its hash was checked against the signed
			// checksums.txt, and zypper otherwise aborts on it (exit 8).
			{"zypper", []string{"--non-interactive", "install", "--allow-unsigned-rpm", file}},
			{"rpm", []string{"-U", file}},
		}
	default:
		return nil, ErrUnsupported
	}
	for _, w := range ways {
		if tool, err := look(w.tool); err == nil {
			return append([]string{pkexec, tool}, w.args...), nil
		}
	}
	return nil, ErrNoPackageTool
}
