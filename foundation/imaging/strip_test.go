package imaging

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"testing"
)

// segment is a JPEG marker segment: 0xFF, the marker, a length that counts
// itself, and the payload.
func segment(marker byte, payload ...string) []byte {
	var p []byte
	for _, s := range payload {
		p = append(p, s...)
	}

	return append([]byte{0xFF, marker, byte((len(p) + 2) >> 8), byte(len(p) + 2)}, p...)
}

// afterSOI is jpg with segs put straight after its start marker, in order.
func afterSOI(jpg []byte, segs ...[]byte) []byte {
	out := append([]byte{}, jpg[:2]...)
	for _, s := range segs {
		out = append(out, s...)
	}

	return append(out, jpg[2:]...)
}

// markers is the marker of each segment in a JPEG, up to the end of image,
// with the image data skipped as Stripped skips it.
func markers(t *testing.T, b []byte) []byte {
	t.Helper()

	var ms []byte
	for i := 2; i+1 < len(b); {
		m := b[i+1]
		if m == 0xD9 {
			return append(ms, m)
		}

		n := int(binary.BigEndian.Uint16(b[i+2:]))
		ms = append(ms, m)
		i += 2 + n

		if m == 0xDA {
			for b[i] != 0xFF || b[i+1] == 0x00 || (b[i+1] >= 0xD0 && b[i+1] <= 0xD7) {
				i++
			}
		}
	}

	t.Fatal("no end of image")

	return nil
}

// scanData is everything from the first start of scan to the end of image:
// the picture, which Stripped must copy byte for byte.
func scanData(t *testing.T, b []byte) []byte {
	t.Helper()

	for i := 2; i+4 <= len(b); {
		if b[i+1] == 0xDA {
			end := bytes.LastIndex(b, []byte{0xFF, 0xD9})
			return b[i : end+2]
		}
		i += 2 + int(binary.BigEndian.Uint16(b[i+2:]))
	}

	t.Fatal("no start of scan")

	return nil
}

var (
	xmp     = segment(0xE1, "http://ns.adobe.com/xap/1.0/\x00", `<x:xmpmeta><exif:GPSLatitude>30,18.6N</exif:GPSLatitude></x:xmpmeta>`)
	comment = segment(0xFE, "taken by a steward on the north path")
	iptc    = segment(0xED, "Photoshop 3.0\x00", "8BIM\x04\x04 a caption")
	mpf     = segment(0xE2, "MPF\x00", "an index of the gain map")
	icc     = segment(0xE2, "ICC_PROFILE\x00", "\x01\x01", "a colour profile's bytes")
	adobe   = segment(0xEE, "Adobe", "\x00\x64\x00\x00\x00\x00\x01")
	jfxx    = segment(0xE0, "JFXX\x00", "\x10 a thumbnail")
	gainMap = []byte("\xFF\xD8 a second picture, after the first one's end \xFF\xD9")
)

func TestAPhonePhotoIsStrippedToItsPicture(t *testing.T) {
	plain := jpegOf(t, picture(400, 300))
	located := gpsEXIF(plain, 'N', fieldLat, 'W', fieldLon, "2026:10:03 16:45:09", "-05:00")

	// As a phone writes it: EXIF with the place and the moment, XMP saying
	// the place again, a colour profile, a multi-picture index, and a gain
	// map after the end of the image.
	src := append(afterSOI(located, xmp, icc, mpf, iptc, comment, adobe), gainMap...)

	if m := readEXIF(src, "jpeg"); !m.located {
		t.Fatal("the fixture has no position to strip")
	}

	out, err := Stripped(src, "jpeg")
	if err != nil {
		t.Fatal(err)
	}

	if m := readEXIF(out, "jpeg"); m.located || m.taken != (Taken{}) || m.orientation != 0 {
		t.Errorf("something of the EXIF survived: %+v", m)
	}

	for name, gone := range map[string][]byte{"the XMP": xmp, "the comment": comment, "the IPTC": iptc, "the multi-picture index": mpf, "the gain map": gainMap[2:]} {
		if bytes.Contains(out, gone) {
			t.Errorf("%s survived", name)
		}
	}

	for _, word := range []string{"GPS", "steward", "north path", "Exif"} {
		if bytes.Contains(out, []byte(word)) {
			t.Errorf("%q is still in the file", word)
		}
	}

	if !bytes.Contains(out, icc) || !bytes.Contains(out, adobe) {
		t.Error("what draws the picture's colours did not survive")
	}

	if !bytes.Equal(scanData(t, out), scanData(t, plain)) {
		t.Error("the picture is not the original's, byte for byte")
	}

	if !bytes.HasSuffix(out, []byte{0xFF, 0xD9}) {
		t.Error("the file does not end at the end of the image")
	}

	if got, want := decode(t, out), decode(t, plain); !samePixels(got, want) {
		t.Error("it does not decode to the original's pixels")
	}
}

func TestAPhotoTakenInPortraitKeepsWhichWayUpItIs(t *testing.T) {
	// A phone stores a portrait photo sideways and says so in the EXIF. With
	// the EXIF gone and nothing in its place, it would be shown sideways.
	for _, o := range []uint16{3, 6, 8} {
		src := withEXIF(jpegOf(t, picture(400, 300)), o, "2027:04:18 09:30:00")

		out, err := Stripped(src, "jpeg")
		if err != nil {
			t.Fatal(err)
		}

		m := readEXIF(out, "jpeg")
		if m.orientation != int(o) || m.taken != (Taken{}) {
			t.Errorf("orientation %d: read back %+v", o, m)
		}

		// The new EXIF is the orientation and nothing else: no make, no
		// date, no position (withEXIF writes all three).
		if !bytes.Equal(segmentsOf(out, 0xE1)[0], orientationEXIF(int(o))) || len(segmentsOf(out, 0xE1)) != 1 {
			t.Errorf("orientation %d: the EXIF is not the orientation alone", o)
		}
	}

	// Upright needs no EXIF at all.
	out, err := Stripped(withEXIF(jpegOf(t, picture(400, 300)), 1, "2027:04:18 09:30:00"), "jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if len(segmentsOf(out, 0xE1)) != 0 {
		t.Error("an upright photo was given an EXIF")
	}
}

func TestTheJFIFHeaderStaysFirst(t *testing.T) {
	plain := jpegOf(t, picture(400, 300)) // Go writes no JFIF header; give it one
	jfif := segment(0xE0, "JFIF\x00", "\x01\x02\x00\x00\x01\x00\x01\x00\x00")
	src := afterSOI(withEXIF(plain, 6, "2027:04:18 09:30:00"), jfif, jfxx)

	out, err := Stripped(src, "jpeg")
	if err != nil {
		t.Fatal(err)
	}

	ms := markers(t, out)
	if ms[0] != 0xE0 || ms[1] != 0xE1 {
		t.Errorf("segments in the order %x; want the JFIF header, then the EXIF", ms)
	}
	if bytes.Contains(out, jfxx) {
		t.Error("the JFXX thumbnail survived")
	}
}

// segmentsOf is every segment with this marker before the start of scan.
func segmentsOf(b []byte, marker byte) [][]byte {
	var out [][]byte
	for i := 2; i+4 <= len(b) && b[i+1] != 0xDA; {
		n := int(binary.BigEndian.Uint16(b[i+2:]))
		if b[i+1] == marker {
			out = append(out, b[i:i+2+n])
		}
		i += 2 + n
	}

	return out
}

// A progressive JPEG is many scans, with tables between them, and its image
// data holds stuffed 0xFF bytes and restart markers that are not the end.
func TestEveryScanIsCopied(t *testing.T) {
	scan1 := []byte{0x12, 0xFF, 0x00, 0x34, 0xFF, 0xD0, 0x56}
	scan2 := []byte{0x78, 0xFF, 0x00, 0xFF, 0xD7, 0x9A}

	var src, want []byte
	add := func(b []byte, keep bool) {
		src = append(src, b...)
		if keep {
			want = append(want, b...)
		}
	}

	add([]byte{0xFF, 0xD8}, true)
	add(withEXIF([]byte{0xFF, 0xD8}, 1, "2027:04:18 09:30:00")[2:], false)
	add(segment(0xDB, "\x00", string(make([]byte, 64))), true)
	add(segment(0xC2, "\x08\x00\x10\x00\x10\x01\x01\x11\x00"), true)
	add(segment(0xC4, "\x00", string(make([]byte, 16))), true)
	add(segment(0xDA, "\x01\x01\x00\x00\x3f\x00"), true)
	add(scan1, true)
	add([]byte{0xFF, 0xFF}, false) // fill before the next marker
	add(segment(0xC4, "\x10", string(make([]byte, 16))), true)
	add(comment, false)
	add(segment(0xDA, "\x01\x01\x00\x00\x3f\x00"), true)
	add(scan2, true)
	add([]byte{0xFF, 0xD9}, true)
	add(gainMap, false)

	out, err := Stripped(src, "jpeg")
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(out, want) {
		t.Errorf("got\n%x\nwant\n%x", out, want)
	}
}

func TestWhatCannotBeFollowedToItsEndIsNotServed(t *testing.T) {
	whole := gpsEXIF(jpegOf(t, picture(64, 48)), 'N', fieldLat, 'W', fieldLon, "2026:10:03 16:45:09", "-05:00")

	// Cut short anywhere, it has no end of image, and nothing comes back:
	// not the part before the cut, which would be the EXIF.
	for n := range len(whole) - 1 {
		if out, err := Stripped(whole[:n], "jpeg"); !errors.Is(err, ErrUnreadable) || out != nil {
			t.Fatalf("cut at %d of %d: %d bytes, %v", n, len(whole), len(out), err)
		}
	}

	for name, tc := range map[string]struct {
		data   []byte
		format string
	}{
		"empty":                     {nil, "jpeg"},
		"a PNG called a JPEG":       {[]byte("\x89PNG\r\n\x1a\n"), "jpeg"},
		"a JPEG called a PNG":       {whole, "png"},
		"a format it does not know": {whole, "heic"},
		"a length of zero":          {[]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x00, 0xFF, 0xD9}, "jpeg"},
		"a length past the end":     {[]byte{0xFF, 0xD8, 0xFF, 0xE1, 0xFF, 0xFF, 0x00}, "jpeg"},
		"not a marker":              {[]byte{0xFF, 0xD8, 0x00, 0x00, 0xFF, 0xD9}, "jpeg"},
	} {
		if out, err := Stripped(tc.data, tc.format); !errors.Is(err, ErrUnreadable) || out != nil {
			t.Errorf("%s: %d bytes, %v", name, len(out), err)
		}
	}
}

// ------------------------------------------------------------------ PNG

// chunk is a PNG chunk: length, type, data and the CRC of type and data.
func chunk(kind, data string) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	b = append(b, kind...)
	b = append(b, data...)

	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE([]byte(kind+data)))
}

// beforeIDAT is a PNG with chunks put in just before its first IDAT.
func beforeIDAT(p []byte, chunks ...[]byte) []byte {
	at := bytes.Index(p, []byte("IDAT")) - 4
	out := append([]byte{}, p[:at]...)
	for _, c := range chunks {
		out = append(out, c...)
	}

	return append(out, p[at:]...)
}

func TestAPNGIsStrippedToItsPicture(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, picture(120, 80)); err != nil {
		t.Fatal(err)
	}
	plain := buf.Bytes()

	text := chunk("tEXt", "Author\x00a steward")
	itxt := chunk("iTXt", "XML:com.adobe.xmp\x00\x00\x00\x00\x00<x:xmpmeta/>")
	exif := chunk("eXIf", "MM\x00*\x00\x00\x00\x08\x00\x00")
	when := chunk("tIME", "\x07\xea\x0a\x05\x14\x1c\x26")
	gamma := chunk("gAMA", "\x00\x00\xb1\x8f")

	// Anything after the end is a second file stuck on, and goes too.
	src := append(beforeIDAT(plain, text, itxt, exif, when, gamma), chunk("zTXt", "after\x00\x00x")...)

	out, err := Stripped(src, "png")
	if err != nil {
		t.Fatal(err)
	}

	for name, gone := range map[string][]byte{"tEXt": text, "iTXt": itxt, "eXIf": exif, "tIME": when} {
		if bytes.Contains(out, gone) {
			t.Errorf("the %s chunk survived", name)
		}
	}

	if !bytes.Contains(out, gamma) {
		t.Error("the gAMA chunk, which changes how it looks, did not survive")
	}

	if !bytes.Equal(out, beforeIDAT(plain, gamma)) {
		t.Error("what is left is not the original's chunks, byte for byte")
	}

	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if !samePixels(img, picture(120, 80)) {
		t.Error("it does not decode to the original's pixels")
	}

	// Cut anywhere before its end, nothing comes back. (A cut in what comes
	// after IEND leaves a whole PNG, which is served.)
	for n := range bytes.Index(src, []byte("IEND")) + 8 {
		if out, err := Stripped(src[:n], "png"); !errors.Is(err, ErrUnreadable) || out != nil {
			t.Fatalf("cut at %d of %d: %d bytes, %v", n, len(src), len(out), err)
		}
	}
}

func samePixels(a, b image.Image) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}

	for y := a.Bounds().Min.Y; y < a.Bounds().Max.Y; y++ {
		for x := a.Bounds().Min.X; x < a.Bounds().Max.X; x++ {
			r1, g1, b1, a1 := a.At(x, y).RGBA()
			r2, g2, b2, a2 := b.At(x, y).RGBA()
			if r1 != r2 || g1 != g2 || b1 != b2 || a1 != a2 {
				return false
			}
		}
	}

	return true
}

// ------------------------------------------------------------------ fuzzing

// Whatever bytes it is given, Stripped neither panics nor reads outside them,
// and what it gives back carries no EXIF but an orientation. The seeds run
// with every `go test`; `go test -fuzz=FuzzStripped ./foundation/imaging`
// looks further.
func FuzzStripped(f *testing.F) {
	plain := jpegOfF(f)
	f.Add(plain, "jpeg")
	f.Add(append(afterSOI(gpsEXIF(plain, 'N', fieldLat, 'W', fieldLon, "2026:10:03 16:45:09", "-05:00"), xmp, icc, comment), gainMap...), "jpeg")
	f.Add(withEXIF(plain, 6, "2027:04:18 09:30:00"), "jpeg")
	f.Add([]byte("\x89PNG\r\n\x1a\n"), "png")
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xDA, 0x00, 0x02, 0xFF}, "jpeg")

	f.Fuzz(func(t *testing.T, data []byte, format string) {
		out, err := Stripped(data, format)
		if err != nil {
			if out != nil {
				t.Fatal("an error with bytes")
			}

			return
		}

		if format == "jpeg" {
			if !bytes.HasPrefix(out, []byte{0xFF, 0xD8}) || !bytes.HasSuffix(out, []byte{0xFF, 0xD9}) {
				t.Fatal("not a JPEG from start to end")
			}

			if m := readEXIF(out, "jpeg"); m.located || m.taken != (Taken{}) {
				t.Fatalf("EXIF survived: %+v", m)
			}
		}
	})
}

func jpegOfF(f *testing.F) []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, picture(32, 24), &jpeg.Options{Quality: 95}); err != nil {
		f.Fatal(err)
	}

	return buf.Bytes()
}
