// Command lipo joins thin 64-bit Mach-O executables (macOS x86-64 and arm64)
// into one universal binary, as Apple's `lipo -create` does, so the release
// can build the macOS file on any system:
//
//	go run ./tools/lipo OUT IN...
package main

import (
	"encoding/binary"
	"fmt"
	"os"
)

const (
	fatMagic    = 0xcafebabe
	machMagic64 = 0xfeedfacf
	cpuX86_64   = 0x01000007
	cpuArm64    = 0x0100000c
)

type slice struct {
	cpu, sub, align uint32
	data            []byte
}

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: lipo OUT IN IN...")
		os.Exit(2)
	}
	var slices []slice
	for _, in := range os.Args[2:] {
		b, err := os.ReadFile(in)
		if err != nil {
			fail(err)
		}
		if len(b) < 12 || binary.LittleEndian.Uint32(b) != machMagic64 {
			fail(fmt.Errorf("%s is not a 64-bit Mach-O file", in))
		}
		s := slice{cpu: binary.LittleEndian.Uint32(b[4:]), sub: binary.LittleEndian.Uint32(b[8:]) & 0x00ffffff, data: b}
		switch s.cpu {
		case cpuArm64:
			s.align = 14 // 16 KB pages
		case cpuX86_64:
			s.align = 12 // 4 KB pages
		default:
			fail(fmt.Errorf("%s: unexpected CPU type %#x", in, s.cpu))
		}
		slices = append(slices, s)
	}
	header := make([]byte, 8+20*len(slices))
	binary.BigEndian.PutUint32(header, fatMagic)
	binary.BigEndian.PutUint32(header[4:], uint32(len(slices)))
	out := header
	for i, s := range slices {
		size := uint32(1) << s.align
		offset := (uint32(len(out)) + size - 1) / size * size
		out = append(out, make([]byte, offset-uint32(len(out)))...)
		out = append(out, s.data...)
		e := header[8+20*i:]
		binary.BigEndian.PutUint32(e, s.cpu)
		binary.BigEndian.PutUint32(e[4:], s.sub)
		binary.BigEndian.PutUint32(e[8:], offset)
		binary.BigEndian.PutUint32(e[12:], uint32(len(s.data)))
		binary.BigEndian.PutUint32(e[16:], s.align)
	}
	copy(out, header)
	if err := os.WriteFile(os.Args[1], out, 0o755); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "lipo:", err)
	os.Exit(1)
}
