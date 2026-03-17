// Package image implements reading and writing ImageUSB .bin file format.
// The .bin format consists of a 512-byte header followed by a raw disk image,
// with MD5 and SHA1 checksums stored as UTF-16LE hex strings in the header.
package image

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/lazaroagomez/wusbkit/internal/encoding"
)

// HeaderSize is the fixed size of the ImageUSB .bin file header in bytes.
const HeaderSize = 512

// Signature is the magic string at the start of every ImageUSB .bin file.
const Signature = "imageUSB"

// Field offsets and sizes within the 512-byte header.
const (
	signatureSize  = 32  // [16]uint16
	versionSize    = 16  // 4 x uint32
	imageLenSize   = 8   // uint64
	deprecatedSize = 8   // uint64 (legacy checksum)
	md5FieldSize   = 66  // [33]uint16
	sha1FieldSize  = 82  // [41]uint16
	reservedSize   = 300 // zero-filled padding

	offsetSignature  = 0
	offsetVersion    = 32
	offsetImageLen   = 48
	offsetDeprecated = 56
	offsetMD5        = 64
	offsetSHA1       = 130
	offsetReserved   = 212
)

// Header represents the ImageUSB .bin file header.
type Header struct {
	VersionMajor    uint32
	VersionMinor    uint32
	VersionBuild    uint32
	VersionRevision uint32
	ImageLength     uint64
	MD5             string // hex string (32 chars)
	SHA1            string // hex string (40 chars)
}

// ReadHeader reads and validates a .bin header from a reader.
// The reader position must be at offset 0 (or will be seeked to 0).
// Returns nil header (no error) if the file does not have a valid imageUSB signature.
func ReadHeader(r io.ReadSeeker) (*Header, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek to start: %w", err)
	}

	var buf [HeaderSize]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	// Validate signature
	sig := encoding.DecodeUTF16LE(buf[offsetSignature:offsetSignature+signatureSize])
	if sig != Signature {
		return nil, nil
	}

	h := &Header{
		VersionMajor:    binary.LittleEndian.Uint32(buf[offsetVersion:]),
		VersionMinor:    binary.LittleEndian.Uint32(buf[offsetVersion+4:]),
		VersionBuild:    binary.LittleEndian.Uint32(buf[offsetVersion+8:]),
		VersionRevision: binary.LittleEndian.Uint32(buf[offsetVersion+12:]),
		ImageLength:     binary.LittleEndian.Uint64(buf[offsetImageLen:]),
		MD5:             encoding.DecodeUTF16LE(buf[offsetMD5 : offsetMD5+md5FieldSize]),
		SHA1:            encoding.DecodeUTF16LE(buf[offsetSHA1 : offsetSHA1+sha1FieldSize]),
	}

	return h, nil
}

// WriteHeader writes a .bin header to a writer at offset 0.
// The writer is seeked to position 0 before writing.
func WriteHeader(w io.WriteSeeker, h *Header) error {
	if _, err := w.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek to start: %w", err)
	}

	var buf [HeaderSize]byte

	// Signature: "imageUSB" as UTF-16LE, null-padded to 32 bytes
	encoding.EncodeUTF16LE(buf[offsetSignature:offsetSignature+signatureSize], Signature)

	// Version fields
	binary.LittleEndian.PutUint32(buf[offsetVersion:], h.VersionMajor)
	binary.LittleEndian.PutUint32(buf[offsetVersion+4:], h.VersionMinor)
	binary.LittleEndian.PutUint32(buf[offsetVersion+8:], h.VersionBuild)
	binary.LittleEndian.PutUint32(buf[offsetVersion+12:], h.VersionRevision)

	// Image length
	binary.LittleEndian.PutUint64(buf[offsetImageLen:], h.ImageLength)

	// Deprecated checksum field stays zero

	// MD5 hex string as UTF-16LE
	encoding.EncodeUTF16LE(buf[offsetMD5:offsetMD5+md5FieldSize], h.MD5)

	// SHA1 hex string as UTF-16LE
	encoding.EncodeUTF16LE(buf[offsetSHA1:offsetSHA1+sha1FieldSize], h.SHA1)

	// Reserved area is already zero from array initialization

	_, err := w.Write(buf[:])
	if err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	return nil
}

// IsImageUSBFile checks if a file has a valid imageUSB .bin header.
func IsImageUSBFile(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	h, err := ReadHeader(f)
	if err != nil {
		return false, err
	}
	return h != nil, nil
}

