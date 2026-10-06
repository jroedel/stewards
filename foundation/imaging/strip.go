package imaging

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// ErrUnreadable is a photo whose structure Stripped could not follow to its
// end. Its original is not served at all rather than served as it is: what
// could not be read could not be stripped either.
var ErrUnreadable = errors.New("that photo's file could not be read to its end")

// Stripped is a photo's original with everything its camera wrote about it
// taken out, and its picture left exactly as it was: the full-size copy a
// person can be shown.
//
// The original is kept as it arrived and never served, because it carries
// the place it was taken, the camera, sometimes the phone's serial number,
// and on some phones a second, hidden picture. The large and small copies
// are re-encoded from the pixels and carry none of it. A full-size copy
// re-encoded the same way would cost a decode at full size -- 170 MB for a
// 48 MP photo, on a host that kills the app near 300 -- and a generation of
// quality. So this decodes nothing: it copies the file's structure, leaving
// out the segments that describe rather than draw, and copies the image
// data byte for byte.
//
// What is kept is what changes how the picture looks: for a JPEG the
// quantisation and Huffman tables, the frame and scans, the JFIF header, the
// colour profile (an iPhone's photos are Display P3, and without it they
// show dull), Adobe's colour-transform marker, and the orientation, written
// fresh as the only thing in a new EXIF -- without it a photo taken in
// portrait, which the phone stores sideways, would be shown sideways. For a
// PNG, the chunks that draw and colour it. Everything else goes: EXIF, XMP,
// Photoshop and IPTC blocks, comments, thumbnails, text, and any bytes after
// the end of the image, where a phone's HDR gain map lives.
//
// format is "jpeg" or "png", as Prepare named it.
func Stripped(data []byte, format string) ([]byte, error) {
	switch format {
	case "jpeg":
		return strippedJPEG(data)
	case "png":
		return strippedPNG(data)
	}

	return nil, ErrUnreadable
}

func strippedJPEG(b []byte) ([]byte, error) {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return nil, ErrUnreadable
	}

	orientation := readEXIF(b, "jpeg").orientation

	out := make([]byte, 0, len(b))
	out = append(out, 0xFF, 0xD8)

	// The new EXIF goes after the JFIF header, which must come first when
	// there is one, and before everything else.
	exifWritten := orientation <= 1

	for i := 2; ; {
		if i+1 >= len(b) || b[i] != 0xFF {
			return nil, ErrUnreadable
		}

		marker := b[i+1]

		switch {
		case marker == 0xFF: // fill byte before a marker
			i++

			continue
		case marker == 0xD9: // end of image: and of what is served
			return append(out, 0xFF, 0xD9), nil
		case marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7): // stand alone, no length
			out = append(out, 0xFF, marker)
			i += 2

			continue
		}

		if i+4 > len(b) {
			return nil, ErrUnreadable
		}

		n := int(binary.BigEndian.Uint16(b[i+2:]))
		if n < 2 || i+2+n > len(b) {
			return nil, ErrUnreadable
		}

		seg := b[i : i+2+n]
		i += 2 + n

		if !keepJPEG(marker, seg[4:]) {
			continue
		}

		if !exifWritten && marker != 0xE0 {
			out = append(out, orientationEXIF(orientation)...)
			exifWritten = true
		}

		out = append(out, seg...)

		if marker != 0xDA {
			continue
		}

		// Start of scan: the image data follows, up to the next marker. In
		// it 0xFF is followed by 0x00 (a stuffed byte) or a restart marker,
		// and anything else ends it. A progressive JPEG has many scans, so
		// the walk goes on from there.
		j := i
		for {
			if j+1 >= len(b) {
				return nil, ErrUnreadable
			}

			if b[j] == 0xFF {
				if c := b[j+1]; c != 0x00 && (c < 0xD0 || c > 0xD7) {
					break
				}

				j += 2

				continue
			}

			j++
		}

		out = append(out, b[i:j]...)
		i = j
	}
}

// keepJPEG is whether a segment changes how the picture looks. payload is
// the segment after its length.
func keepJPEG(marker byte, payload []byte) bool {
	switch {
	case marker == 0xE0: // APP0: the JFIF header, not JFXX's thumbnail
		return bytes.HasPrefix(payload, []byte("JFIF\x00"))
	case marker == 0xE2: // APP2: the colour profile, not the multi-picture index
		return bytes.HasPrefix(payload, []byte("ICC_PROFILE\x00"))
	case marker == 0xEE: // APP14: Adobe's colour transform, which decoding needs
		return bytes.HasPrefix(payload, []byte("Adobe"))
	case marker >= 0xE0 && marker <= 0xEF: // every other APPn: EXIF, XMP, IPTC, ...
		return false
	case marker == 0xFE: // a comment
		return false
	}

	return true // tables, frame, scans, restart interval: the picture itself
}

// orientationEXIF is an APP1 EXIF segment holding the orientation and
// nothing else: big-endian TIFF, IFD0 with one SHORT entry, no IFD1.
func orientationEXIF(orientation int) []byte {
	tiff := []byte{
		'M', 'M', 0x00, 0x2A, // big-endian TIFF
		0x00, 0x00, 0x00, 0x08, // IFD0 at offset 8
		0x00, 0x01, // one entry
		0x01, 0x12, 0x00, 0x03, 0x00, 0x00, 0x00, 0x01, // Orientation, SHORT, count 1
		byte(orientation >> 8), byte(orientation), 0x00, 0x00, // its value
		0x00, 0x00, 0x00, 0x00, // no IFD1
	}

	payload := append([]byte("Exif\x00\x00"), tiff...)
	n := len(payload) + 2

	return append([]byte{0xFF, 0xE1, byte(n >> 8), byte(n)}, payload...)
}

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

func strippedPNG(b []byte) ([]byte, error) {
	if !bytes.HasPrefix(b, pngSignature) {
		return nil, ErrUnreadable
	}

	out := make([]byte, 0, len(b))
	out = append(out, pngSignature...)

	// Each chunk is a length, a four-letter type, that many bytes, and a CRC.
	for i := len(pngSignature); ; {
		if i+12 > len(b) {
			return nil, ErrUnreadable
		}

		n := int(binary.BigEndian.Uint32(b[i:]))
		if n < 0 || n > len(b)-i-12 {
			return nil, ErrUnreadable
		}

		kind := string(b[i+4 : i+8])
		end := i + 12 + n

		if keepPNG(kind) {
			out = append(out, b[i:end]...)
		}

		if kind == "IEND" {
			return out, nil
		}

		i = end
	}
}

// keepPNG is whether a chunk changes how the picture looks: every critical
// chunk (upper-case first letter), and the ancillary ones about colour,
// transparency and pixel size. Text, EXIF, timestamps and anything unknown
// go.
func keepPNG(kind string) bool {
	if kind[0] >= 'A' && kind[0] <= 'Z' {
		return true
	}

	switch kind {
	case "tRNS", "gAMA", "cHRM", "sRGB", "iCCP", "sBIT", "bKGD", "pHYs", "cICP":
		return true
	}

	return false
}
