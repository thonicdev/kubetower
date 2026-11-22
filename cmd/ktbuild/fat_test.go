package main

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// thinMachO is the smallest Mach-O debug/macho reads: a 64-bit header with no
// load commands, for the CPU given.
func thinMachO(t *testing.T, dir string, cpu macho.Cpu, sub uint32, body string) string {
	t.Helper()
	var b bytes.Buffer
	for _, v := range []uint32{macho.Magic64, uint32(cpu), sub, uint32(macho.TypeExec), 0, 0, 0, 0} {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString(body)
	path := filepath.Join(dir, cpu.String())
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAUniversalBinaryCarriesBothSlicesAlignedAndWhole(t *testing.T) {
	dir := t.TempDir()
	amd := thinMachO(t, dir, macho.CpuAmd64, 3, "the amd64 slice")
	arm := thinMachO(t, dir, macho.CpuArm64, 0, "the arm64 slice")
	out := filepath.Join(dir, "fat")
	if err := writeFat(out, []string{amd, arm}); err != nil {
		t.Fatalf("writeFat: %v", err)
	}
	if err := checkFat(out, []macho.Cpu{macho.CpuAmd64, macho.CpuArm64}); err != nil {
		t.Fatalf("checkFat on what writeFat wrote: %v", err)
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.BigEndian.Uint32(raw); got != fatMagic {
		t.Fatalf("the file starts %#x; want FAT_MAGIC %#x, big-endian", got, fatMagic)
	}
	f, err := macho.OpenFat(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for i, arch := range f.Arches {
		if arch.Offset%(1<<fatAlign) != 0 || arch.Align != fatAlign {
			t.Errorf("slice %d (%s) is at %d with align %d; want a multiple of 2^%d", i, arch.Cpu, arch.Offset, arch.Align, fatAlign)
		}
		src, _ := os.ReadFile(map[macho.Cpu]string{macho.CpuAmd64: amd, macho.CpuArm64: arm}[arch.Cpu])
		if !bytes.Equal(raw[arch.Offset:arch.Offset+arch.Size], src) {
			t.Errorf("the %s slice is not its source byte for byte", arch.Cpu)
		}
	}
	if f.Arches[0].SubCpu != 3 {
		t.Errorf("the amd64 slice's subtype is %d; want the 3 its header carries", f.Arches[0].SubCpu)
	}
}

func TestAUniversalBinaryMissingASliceIsRefused(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "fat")
	if err := writeFat(out, []string{thinMachO(t, dir, macho.CpuArm64, 0, "only arm64")}); err != nil {
		t.Fatal(err)
	}
	if err := checkFat(out, []macho.Cpu{macho.CpuAmd64, macho.CpuArm64}); err == nil {
		t.Error("a universal binary with no amd64 slice was accepted")
	}
}

func TestAThinMachOIsTheOnlySliceAccepted(t *testing.T) {
	dir := t.TempDir()
	notMachO := filepath.Join(dir, "x")
	_ = os.WriteFile(notMachO, []byte("#!/bin/sh\n"), 0o644)
	if err := writeFat(filepath.Join(dir, "fat"), []string{notMachO}); err == nil {
		t.Error("a shell script was accepted as a slice")
	}
}

func TestUniversalIsForDarwinOnly(t *testing.T) {
	if err := (target{goos: "linux", goarch: universalArch}).validate(); err == nil {
		t.Error("-arch universal was accepted for linux")
	}
	if err := (target{goos: "darwin", goarch: universalArch}).validate(); err != nil {
		t.Errorf("-arch universal was refused for darwin: %v", err)
	}
}
