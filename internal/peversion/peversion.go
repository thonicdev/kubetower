// Package peversion reads the strings of a Windows executable's version
// resource - ProductName, ProductVersion and the rest of what Explorer shows
// in a file's Properties - without running it.
//
// Two readers need it and neither may run what it reads:
//
//   - `cmd/ktbuild` reads the setup it built back, and refuses it unless the
//     version resource says exactly the version asked for;
//   - `kubetower --uninstall` on Windows starts the setup's `uninstall.exe`
//     only if it names itself KubeTower, so a file that merely has the right
//     name in the right folder is not run.
//
// # What is parsed, and what is not
//
// A VS_VERSIONINFO block stores each string as a header, a UTF-16LE key ending
// in a zero unit, zero padding to a 32-bit boundary, then the UTF-16LE value
// ending in a zero unit. Value finds the key at an even offset of the
// resource section and reads the value after it. It does not walk the
// resource directory tree: the section is small, the keys are distinctive,
// and a reader that reached the wrong block would read a value that does not
// match, which every caller treats as a refusal.
package peversion

import (
	"bytes"
	"debug/pe"
	"errors"
	"fmt"
	"unicode/utf16"
)

// Value is the version-resource string named key in the executable at path:
// "ProductName", "ProductVersion", "FileVersion", "FileDescription".
func Value(path, key string) (string, error) {
	f, err := pe.Open(path)
	if err != nil {
		return "", fmt.Errorf("not a PE file: %w", err)
	}
	defer f.Close()
	rsrc := f.Section(".rsrc")
	if rsrc == nil {
		return "", errors.New("no resource section, so no version resource")
	}
	data, err := rsrc.Data()
	if err != nil {
		return "", fmt.Errorf("reading the resource section: %w", err)
	}
	value, ok := Find(data, key)
	if !ok {
		return "", fmt.Errorf("the version resource has no %s", key)
	}
	return value, nil
}

// Find is Value on the bytes of a resource section. Split out so the parsing
// is tested on bytes written by hand.
func Find(data []byte, key string) (string, bool) {
	needle := append(utf16LE(key), 0, 0)
	for from := 0; ; {
		i := bytes.Index(data[from:], needle)
		if i < 0 {
			return "", false
		}
		at := from + i
		from = at + 1
		if at%2 != 0 {
			continue
		}
		// The padding after the key's terminator, then the value.
		j := at + len(needle)
		for j+1 < len(data) && data[j] == 0 && data[j+1] == 0 {
			j += 2
		}
		var units []uint16
		for ; j+1 < len(data); j += 2 {
			u := uint16(data[j]) | uint16(data[j+1])<<8
			if u == 0 {
				return string(utf16.Decode(units)), true
			}
			units = append(units, u)
		}
		return "", false
	}
}

func utf16LE(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 0, len(units)*2)
	for _, unit := range units {
		out = append(out, byte(unit), byte(unit>>8))
	}
	return out
}
