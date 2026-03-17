// Package encoding provides shared UTF-16LE encoding and decoding routines
// used throughout wusbkit for reading and writing Windows-format strings.
package encoding

import (
	"encoding/binary"
	"unicode/utf16"
)

// DecodeUTF16LE decodes a byte slice containing a UTF-16LE encoded string.
// The returned string is trimmed at the first null character.
// Returns an empty string if b has fewer than 2 bytes.
func DecodeUTF16LE(b []byte) string {
	if len(b) < 2 {
		return ""
	}

	n := len(b) / 2
	codeUnits := make([]uint16, n)
	for i := range n {
		codeUnits[i] = binary.LittleEndian.Uint16(b[i*2:])
	}

	// Trim at first null terminator
	for i, cu := range codeUnits {
		if cu == 0 {
			codeUnits = codeUnits[:i]
			break
		}
	}

	return string(utf16.Decode(codeUnits))
}

// EncodeUTF16LE writes a Go string into a fixed-size byte slice as UTF-16LE.
// The destination is zero-filled first, so shorter strings are null-padded.
func EncodeUTF16LE(dst []byte, s string) {
	clear(dst)

	encoded := utf16.Encode([]rune(s))
	for i, codeUnit := range encoded {
		offset := i * 2
		if offset+1 >= len(dst) {
			break
		}
		binary.LittleEndian.PutUint16(dst[offset:], codeUnit)
	}
}
