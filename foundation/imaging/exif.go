package imaging

import (
	"encoding/binary"
	"strconv"
)

// exifMeta is the two things read from a photo's EXIF.
type exifMeta struct {
	orientation int // 1 to 8; 0 when the photo does not say
	taken       Taken
}

// readEXIF finds a JPEG's EXIF and reads the orientation and the date taken
// from it. A PNG, a JPEG with no EXIF, and EXIF that does not parse all give
// the zero value: a photo shown upright with no date, which is what a browser
// would have done with it.
//
// Written here rather than imported. The two tags needed are a few dozen
// lines of TIFF directory walking, and every EXIF library is far more code
// than that facing bytes a stranger chose. Every offset below is checked
// against the length before it is used; a crafted file can make this return
// nothing, never read outside the slice.
func readEXIF(data []byte, format string) exifMeta {
	if format != "jpeg" {
		return exifMeta{}
	}

	tiff := app1(data)
	if tiff == nil {
		return exifMeta{}
	}

	var order binary.ByteOrder

	switch string(tiff[:4]) {
	case "II*\x00":
		order = binary.LittleEndian
	case "MM\x00*":
		order = binary.BigEndian
	default:
		return exifMeta{}
	}

	t := tiffReader{b: tiff, order: order}

	var meta exifMeta

	ifd0 := t.u32(4)
	exifIFD := uint32(0)

	t.entries(ifd0, func(tag, kind uint16, count, value, at uint32) {
		switch {
		case tag == 0x0112 && kind == 3: // Orientation, a SHORT
			meta.orientation = int(t.u16(at))
		case tag == 0x8769 && kind == 4: // the Exif sub-directory, a LONG offset
			exifIFD = value
		}
	})

	if exifIFD != 0 {
		t.entries(exifIFD, func(tag, kind uint16, count, value, _ uint32) {
			// DateTimeOriginal, ASCII "2006:01:02 15:04:05" and a NUL.
			if tag == 0x9003 && kind == 2 && count >= 10 {
				meta.taken = parseTaken(t.bytes(value, 7))
			}
		})
	}

	return meta
}

// app1 returns the TIFF data in a JPEG's EXIF segment, or nil.
//
// A JPEG is a series of marker segments, each 0xFF, a marker byte and a
// two-byte length that counts itself. EXIF is the APP1 segment that starts
// "Exif\0\0"; it comes before the image data, so the walk stops at the start
// of scan.
func app1(data []byte) []byte {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil
	}

	for i := 2; i+4 <= len(data); {
		if data[i] != 0xFF {
			return nil
		}

		marker := data[i+1]
		if marker == 0xDA || marker == 0xD9 { // start of scan, end of image
			return nil
		}

		n := int(binary.BigEndian.Uint16(data[i+2:]))
		if n < 2 || i+2+n > len(data) {
			return nil
		}

		seg := data[i+4 : i+2+n]
		if marker == 0xE1 && len(seg) >= 14 && string(seg[:6]) == "Exif\x00\x00" {
			return seg[6:]
		}

		i += 2 + n
	}

	return nil
}

// tiffReader reads a TIFF structure, answering zero for anything out of
// range instead of panicking.
type tiffReader struct {
	b     []byte
	order binary.ByteOrder
}

func (t tiffReader) u16(at uint32) uint16 {
	if uint64(at)+2 > uint64(len(t.b)) {
		return 0
	}

	return t.order.Uint16(t.b[at:])
}

func (t tiffReader) u32(at uint32) uint32 {
	if uint64(at)+4 > uint64(len(t.b)) {
		return 0
	}

	return t.order.Uint32(t.b[at:])
}

func (t tiffReader) bytes(at, n uint32) []byte {
	if uint64(at)+uint64(n) > uint64(len(t.b)) {
		return nil
	}

	return t.b[at : at+n]
}

// entries calls fn for each entry of the directory at off: its tag, type,
// count, the value field read as a LONG, and where the value field sits.
//
// At most 512 entries, which is more than any camera writes and stops a
// directory that claims 65,535 from being walked in a loop of reads that
// each answer zero.
func (t tiffReader) entries(off uint32, fn func(tag, kind uint16, count, value, at uint32)) {
	if off == 0 {
		return
	}

	n := min(uint32(t.u16(off)), 512)

	for i := range n {
		e := off + 2 + i*12
		if uint64(e)+12 > uint64(len(t.b)) {
			return
		}

		fn(t.u16(e), t.u16(e+2), t.u32(e+4), t.u32(e+8), e+8)
	}
}

// parseTaken reads "2006:01" from the front of an EXIF date. A camera whose
// clock was never set writes zeros or spaces, which parse to nothing.
func parseTaken(b []byte) Taken {
	if len(b) < 7 || b[4] != ':' {
		return Taken{}
	}

	y, err1 := strconv.Atoi(string(b[:4]))
	m, err2 := strconv.Atoi(string(b[5:7]))

	if err1 != nil || err2 != nil || y < 1990 || m < 1 || m > 12 {
		return Taken{}
	}

	return Taken{Year: y, Month: m}
}
