package main

// The macOS universal binary.
//
// Written here rather than by lipo, which exists only on macOS. A fat Mach-O
// is a short big-endian header followed by aligned slices (<mach-o/fat.h>), and
// debug/macho.OpenFat reads it back.
//
// Each slice is go build's output byte for byte. The arm64 slice's ad-hoc code
// signature, which Apple Silicon requires, covers the slice and not the
// container, so it stays valid.

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// fatMagic is FAT_MAGIC from <mach-o/fat.h>: the header and every fat_arch
	// after it are big-endian, whatever the slices are.
	fatMagic uint32 = 0xcafebabe

	// fatAlign is the slices' alignment as a power of two: 16 KiB, the arm64
	// macOS page, which lipo also uses and which suits x86_64's 4 KiB pages.
	fatAlign = 14

	// universalArch is the -arch value that asks for the fat binary.
	universalArch = "universal"
)

// universalSlices are the architectures a universal binary carries, in the
// order `lipo -create` writes them for this pair.
var universalSlices = []string{"amd64", "arm64"}

// writeFat writes the slices at paths into one universal Mach-O at out. Each
// slice must be a thin Mach-O; their CPU types and subtypes are read from them
// rather than assumed from their names.
func writeFat(out string, paths []string) error {
	if len(paths) == 0 {
		return errors.New("no slices to put in a universal binary")
	}
	type slice struct {
		cpu, sub uint32
		data     []byte
	}
	slices := make([]slice, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := macho.NewFile(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("%s is not a thin Mach-O: %w", path, err)
		}
		slices = append(slices, slice{cpu: uint32(f.Cpu), sub: f.SubCpu, data: data})
		f.Close()
	}

	align := int64(1) << fatAlign
	header := int64(8 + 20*len(slices)) // fat_header, then one fat_arch per slice
	var buf bytes.Buffer
	put := func(v uint32) { _ = binary.Write(&buf, binary.BigEndian, v) }
	put(fatMagic)
	put(uint32(len(slices)))
	offsets := make([]int64, len(slices))
	next := roundUp(header, align)
	for i, s := range slices {
		offsets[i] = next
		if next+int64(len(s.data)) > 1<<32-1 {
			return errors.New("a universal binary this large needs the 64-bit fat format, which this does not write")
		}
		put(s.cpu)
		put(s.sub)
		put(uint32(next))
		put(uint32(len(s.data)))
		put(fatAlign)
		next = roundUp(next+int64(len(s.data)), align)
	}
	for i, s := range slices {
		buf.Write(make([]byte, offsets[i]-int64(buf.Len())))
		buf.Write(s.data)
	}

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return os.WriteFile(out, buf.Bytes(), 0o755)
}

func roundUp(n, to int64) int64 { return (n + to - 1) / to * to }

// checkFat reads a universal binary back and refuses it unless it carries
// exactly the architectures asked for, each slice a Mach-O executable.
func checkFat(path string, want []macho.Cpu) error {
	f, err := macho.OpenFat(path)
	if err != nil {
		return fmt.Errorf("not a universal Mach-O: %w", err)
	}
	defer f.Close()
	have := map[macho.Cpu]bool{}
	for _, arch := range f.Arches {
		if arch.File.Type != macho.TypeExec {
			return fmt.Errorf("the %s slice is a Mach-O of type %v, not an executable", arch.Cpu, arch.File.Type)
		}
		have[arch.Cpu] = true
	}
	if len(f.Arches) != len(want) {
		return fmt.Errorf("it carries %d slices, want %d", len(f.Arches), len(want))
	}
	for _, cpu := range want {
		if !have[cpu] {
			return fmt.Errorf("it carries no %s slice", cpu)
		}
	}
	return nil
}

// goarchCPU is the Mach-O CPU type each slice's GOARCH links for.
var goarchCPU = map[string]macho.Cpu{"amd64": macho.CpuAmd64, "arm64": macho.CpuArm64}

// buildUniversal links every universal slice through build, into a directory
// of its own, and writes the fat binary at out.
func buildUniversal(t target, out string) error {
	tmp, err := os.MkdirTemp("", "ktbuild-universal-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	paths := make([]string, 0, len(universalSlices))
	want := make([]macho.Cpu, 0, len(universalSlices))
	for _, arch := range universalSlices {
		slice := t
		slice.goarch = arch
		path := filepath.Join(tmp, "kubetower-"+arch)
		if err := build(slice, path); err != nil {
			return err
		}
		paths = append(paths, path)
		want = append(want, goarchCPU[arch])
	}
	out, err = filepath.Abs(out)
	if err != nil {
		return err
	}
	if err := writeFat(out, paths); err != nil {
		return err
	}
	if err := checkFat(out, want); err != nil {
		return fmt.Errorf("%s: %w", out, err)
	}
	fmt.Printf("built universal darwin binary (%v) -> %s\n", universalSlices, out)
	return nil
}
