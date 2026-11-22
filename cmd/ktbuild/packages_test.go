package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestTheReleaseChecksTheLinuxPackagesItShips. The .deb and the .rpm are made
// by linux-packages.yml, which runs only from a tag or a full run by hand, so
// reading the workflows is the only guard on what a pull request cannot see:
//
//   - nfpm is fetched at a pinned version and checked against a pinned SHA-256
//     before it is run, never a floating download;
//   - on a tag, the Linux archive is checked against the archives job's own
//     checksum output before the packages are built around its binary;
//   - each package is installed in its distribution's container and asked its
//     version;
//   - the draft checks each package against the packages job's own output,
//     then lists it in checksums.txt, and the image is pushed only once the
//     packages exist;
//   - ci.yml's full tier runs the same file, and only the full tier.
func TestTheReleaseChecksTheLinuxPackagesItShips(t *testing.T) {
	pkgs := workflowJobs(t, "linux-packages.yml")
	job, ok := pkgs["packages"]
	if !ok {
		t.Fatal("linux-packages.yml has no packages job")
	}
	if outputs, _ := job["outputs"].(map[string]any); outputs["sha256_deb"] == nil || outputs["sha256_rpm"] == nil {
		t.Error("the packages job has no sha256_deb and sha256_rpm outputs for the draft to check against")
	}
	env, _ := job["env"].(map[string]any)
	if v := fmt.Sprint(env["NFPM_VERSION"]); !semverish(v) {
		t.Errorf("linux-packages.yml pins nfpm to %q, not a version", v)
	}
	if sum := fmt.Sprint(env["NFPM_SHA256"]); len(sum) != 64 || strings.Trim(sum, "0123456789abcdef") != "" {
		t.Errorf("linux-packages.yml pins nfpm's archive to %q, not a SHA-256", sum)
	}
	text := jobText(job)
	order := []string{
		`"https://github.com/goreleaser/nfpm/releases/download/v${NFPM_VERSION}/$name"`,
		`echo "$NFPM_SHA256  $dir/$name" | sha256sum --check --strict`,
		`echo "$SUMS  checksums.txt" | sha256sum --check --strict`,
		`grep -F "  ./kubetower_${VERSION}_linux_amd64.tar.gz" checksums.txt | sha256sum --check --strict`,
		`packaging/linux/packages.sh build "$VERSION" "in/kubetower_${VERSION}_linux_amd64.tar.gz" out`,
		`packaging/linux/packages.sh try "$VERSION" out`,
	}
	last := -1
	for _, want := range order {
		at := strings.Index(text, want)
		switch {
		case at < 0:
			t.Errorf("linux-packages.yml's job does not carry %q", want)
		case at < last:
			t.Errorf("linux-packages.yml's job runs %q before the step that has to come first", want)
		default:
			last = at
		}
	}
	upload := stepWhere(job, moves("upload-artifact", "kubetower-linux-packages-${{ inputs.version }}"))
	if upload < 0 {
		t.Error("linux-packages.yml does not upload kubetower-linux-packages-<version>, which the draft downloads")
	}

	// The script the job runs installs each package and asks it its version.
	script, err := os.ReadFile(filepath.Join("..", "..", "packaging", "linux", "packages.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`apt-get install -y -qq --no-install-recommends "/pkg/$DEB"`,
		`dnf install -y -q "/pkg/$RPM"`,
		`says_version "${log##*$'\n'}" "$version" "$DEBIAN_IMAGE`,
		`says_version "${log##*$'\n'}" "$version" "$FEDORA_IMAGE`,
		`local deb="$out/kubetower-${version}-amd64.deb"`,
		`local rpm="$out/kubetower-${version}-x86_64.rpm"`,
	} {
		if !strings.Contains(string(script), want) {
			t.Errorf("packaging/linux/packages.sh does not carry %q", want)
		}
	}

	release := workflowJobs(t, "release.yml")
	caller, ok := release["packages"]
	if !ok {
		t.Fatal("release.yml has no packages job")
	}
	if got := fmt.Sprint(caller["uses"]); got != "./.github/workflows/linux-packages.yml" {
		t.Errorf("release.yml's packages job uses %q", got)
	}
	with, _ := caller["with"].(map[string]any)
	for key, want := range map[string]string{
		"sums": "${{ needs.archives.outputs.sums }}",
		"ref":  "${{ needs.verify.outputs.sha }}",
	} {
		if got := fmt.Sprint(with[key]); got != want {
			t.Errorf("release.yml's packages job passes %s %q, not %q", key, got, want)
		}
	}
	dtext := jobText(release["draft"])
	for _, want := range []string{
		"DEB: ${{ needs.packages.outputs.sha256_deb }}",
		"RPM: ${{ needs.packages.outputs.sha256_rpm }}",
		`for pair in "amd64.deb:$DEB" "x86_64.rpm:$RPM"; do`,
		`echo "$want  ./kubetower-${VERSION}-${name}" | sha256sum --check --strict`,
		`echo "$want  ./kubetower-${VERSION}-${name}" >> checksums.txt`,
		" DEB RPM",
	} {
		if !strings.Contains(dtext, want) {
			t.Errorf("the draft job does not carry %q, so the packages it publishes are unchecked", want)
		}
	}
	if stepWhere(release["draft"], moves("download-artifact", "kubetower-linux-packages-${{ needs.verify.outputs.version }}")) < 0 {
		t.Error("the draft job does not download the packages")
	}
	for _, j := range []string{"image", "draft"} {
		if !slices.Contains(needs(release[j]), "packages") {
			t.Errorf("release.yml's %s job does not need packages: %v", j, needs(release[j]))
		}
	}

	ci := workflowJobs(t, "ci.yml")
	full, ok := ci["packages"]
	if !ok {
		t.Fatal("ci.yml has no packages job: the full tier would not prove what a tag runs")
	}
	if got := fmt.Sprint(full["uses"]); got != "./.github/workflows/linux-packages.yml" {
		t.Errorf("ci.yml's packages job uses %q", got)
	}
	if got, want := fmt.Sprint(full["if"]), "needs.detect.outputs.go == 'yes' && needs.detect.outputs.full == 'yes'"; got != want {
		t.Errorf("ci.yml's packages job runs if %q, not the full tier's %q", got, want)
	}
	// The hand-off carries what an archive carries, which the packages install.
	if !strings.Contains(jobText(ci["platforms"]), `cp LICENSE LICENSING.md web/dist/ui/THIRD_PARTY_NOTICES.txt "$out/$t/"`) {
		t.Error("ci.yml's platforms hand-off does not carry the license files beside each binary")
	}
}

func semverish(v string) bool {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" || strings.Trim(p, "0123456789") != "" {
			return false
		}
	}
	return true
}
