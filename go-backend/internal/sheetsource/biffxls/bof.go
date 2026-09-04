package biffxls

import (
	"encoding/binary"
	"io"
	"unicode/utf16"
)

// the information unit in xls file
type bof struct {
	Id   uint16
	Size uint16
}

// maxUTF16Chars caps the character count a single record may claim. FORK
// ADDITION: count comes straight out of an untrusted file, so an unbounded
// make() is a one-record out-of-memory vector; 64Ki UTF-16 chars is far beyond
// any real hyperlink field.
const maxUTF16Chars = 1 << 16

// read the utf16 string from reader
//
// FORK FIX: upstream sliced bts[:len(bts)-1] unconditionally, which panics for
// count == 0, and sized the slice from a file-controlled uint32.
func (b *bof) utf16String(buf io.ReadSeeker, count uint32) string {
	if count == 0 {
		return ""
	}
	if count > maxUTF16Chars {
		// Refuse to decode, but consume the bytes the record claims so the
		// next record header is still read from the right offset.
		buf.Seek(int64(count)*2, 1)
		return ""
	}
	var bts = make([]uint16, count)
	binary.Read(buf, binary.LittleEndian, &bts)
	runes := utf16.Decode(bts[:len(bts)-1])
	return string(runes)
}

type biffHeader struct {
	Ver     uint16
	Type    uint16
	Id_make uint16
	Year    uint16
	Flags   uint32
	Min_ver uint32
}
