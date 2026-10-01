package imaging

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// Fixtures are drawn here, never a real photo: a real one has a garden, a
// house or a person in it (CLAUDE.md).

// picture is a w×h image, red in its top-left quarter and blue elsewhere, so
// that which way up it came out is visible in one pixel.
func picture(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.RGBA{0, 0, 255, 255}
			if x < w/2 && y < h/2 {
				c = color.RGBA{255, 0, 0, 255}
			}
			img.Set(x, y, c)
		}
	}

	return img
}

func jpegOf(t *testing.T, img image.Image) []byte {
	t.Helper()

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

// withEXIF inserts an APP1 segment after the JPEG's start marker: a
// little-endian TIFF with the orientation, the camera's make (the kind of
// thing that must not survive), and an Exif directory holding the date and a
// made-up GPS-ish string.
func withEXIF(jpg []byte, orientation uint16, taken string) []byte {
	le := binary.LittleEndian
	var t []byte
	put16 := func(v uint16) { t = le.AppendUint16(t, v) }
	put32 := func(v uint32) { t = le.AppendUint32(t, v) }

	// An entry is a tag, a type, a count and a four-byte value field. A
	// SHORT sits in the first two bytes of the field, which in little-endian
	// is the same as the field read as a number.
	entry := func(tag, kind uint16, count, value uint32) {
		put16(tag)
		put16(kind)
		put32(count)
		put32(value)
	}

	t = append(t, "II*\x00"...)
	put32(8) // IFD0 at 8

	// IFD0: three entries, then the next-IFD offset.
	const ifd0Len = 2 + 3*12 + 4
	makeAt := uint32(8 + ifd0Len)
	makeStr := "SecretPhone\x00"
	exifAt := makeAt + uint32(len(makeStr))

	put16(3)
	entry(0x0112, 3, 1, uint32(orientation))
	entry(0x010F, 2, uint32(len(makeStr)), makeAt)
	entry(0x8769, 4, 1, exifAt)
	put32(0)
	t = append(t, makeStr...)

	// The Exif directory: the date, out of line.
	dateAt := exifAt + 2 + 12 + 4
	put16(1)
	entry(0x9003, 2, 20, dateAt)
	put32(0)
	t = append(t, taken+"\x00"...)

	seg := append([]byte("Exif\x00\x00"), t...)
	app1 := []byte{0xFF, 0xE1}
	app1 = binary.BigEndian.AppendUint16(app1, uint16(len(seg)+2))
	app1 = append(app1, seg...)

	out := append([]byte{}, jpg[:2]...)
	out = append(out, app1...)

	return append(out, jpg[2:]...)
}

func decode(t *testing.T, b []byte) image.Image {
	t.Helper()

	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("the output is not a JPEG: %v", err)
	}

	return img
}

func isRed(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r > 0xC000 && g < 0x4000 && b < 0x4000
}

// A phone held upright: the pixels are landscape and the tag says turn them
// a quarter clockwise. The result is portrait, red now top-right, and nothing
// of the EXIF is left in either size.
func TestAnUprightPhonePhotoComesOutUprightAndClean(t *testing.T) {
	src := withEXIF(jpegOf(t, picture(2000, 1000)), 6, "2027:04:18 09:30:00")

	p, err := Prepare(src)
	if err != nil {
		t.Fatal(err)
	}

	if p.Large.Width != 800 || p.Large.Height != 1600 {
		t.Errorf("large is %d×%d, want 800×1600", p.Large.Width, p.Large.Height)
	}

	if p.Small.Width != 400 || p.Small.Height != 800 {
		t.Errorf("small is %d×%d, want 400×800", p.Small.Width, p.Small.Height)
	}

	img := decode(t, p.Large.JPEG)
	if b := img.Bounds(); b.Dx() != 800 || b.Dy() != 1600 {
		t.Errorf("the large JPEG decodes as %v", b)
	}

	// The red corner was top-left of the landscape pixels; a quarter turn
	// clockwise puts it top-right.
	if !isRed(img.At(700, 100)) || isRed(img.At(100, 100)) {
		t.Error("the photo was not turned a quarter clockwise")
	}

	if p.Taken != (Taken{Year: 2027, Month: 4}) {
		t.Errorf("taken %+v, want April 2027", p.Taken)
	}

	for name, b := range map[string][]byte{"large": p.Large.JPEG, "small": p.Small.JPEG} {
		if bytes.Contains(b, []byte("Exif")) || bytes.Contains(b, []byte("SecretPhone")) || bytes.Contains(b, []byte("2027:04")) {
			t.Errorf("the %s picture still carries the EXIF", name)
		}
	}
}

// Every orientation, on a 2×3 image whose six pixels are all different.
func TestEachOrientationIsTheSpecificationsPicture(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 3))
	for i := range 6 {
		src.Set(i%2, i/2, color.RGBA{uint8(i + 1), 0, 0, 255})
	}

	// Each row is the output read left to right, top to bottom, by the
	// number of the source pixel (1 is top-left, 2 top-right, 6 bottom-right).
	for o, want := range map[int][]uint8{
		1: {1, 2, 3, 4, 5, 6},
		2: {2, 1, 4, 3, 6, 5},
		3: {6, 5, 4, 3, 2, 1},
		4: {5, 6, 3, 4, 1, 2},
		5: {1, 3, 5, 2, 4, 6},
		6: {5, 3, 1, 6, 4, 2},
		7: {6, 4, 2, 5, 3, 1},
		8: {2, 4, 6, 1, 3, 5},
	} {
		out := orient(src, o)

		var got []uint8
		for y := range out.Bounds().Dy() {
			for x := range out.Bounds().Dx() {
				got = append(got, out.RGBAAt(x, y).R)
			}
		}

		if !bytes.Equal(got, want) {
			t.Errorf("orientation %d: %v, want %v", o, got, want)
		}
	}
}

// A small borrowed photo keeps its size rather than being blown up, and a
// PNG's transparency becomes white rather than black.
func TestASmallTransparentPNGIsNotEnlargedOrBlackened(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 500, 400))

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}

	p, err := Prepare(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	if p.Format != "png" || p.Large.Width != 500 || p.Large.Height != 400 || p.Small.Width != 500 {
		t.Errorf("got %s %d×%d, small %d", p.Format, p.Large.Width, p.Large.Height, p.Small.Width)
	}

	if r, g, b, _ := decode(t, p.Large.JPEG).At(10, 10).RGBA(); r < 0xF000 || g < 0xF000 || b < 0xF000 {
		t.Error("the transparent part did not come out white")
	}

	if p.Taken != (Taken{}) {
		t.Error("a PNG was given a date")
	}
}

func TestWhatIsNotAPhotoIsRefused(t *testing.T) {
	var gif bytes.Buffer
	gif.WriteString("GIF89a")

	tiny := jpegOf(t, picture(200, 100))

	for name, tc := range map[string]struct {
		data []byte
		want error
	}{
		"text":           {[]byte("hello, this is not a photo"), ErrNotAPhoto},
		"empty":          {nil, ErrNotAPhoto},
		"a gif":          {gif.Bytes(), ErrNotAPhoto},
		"a cut-off jpeg": {jpegOf(t, picture(600, 600))[:400], ErrNotAPhoto},
		"too small":      {tiny, ErrTooSmall},
		"a pixel bomb":   {pngClaiming(t, 100_000, 100_000), ErrTooLarge},
	} {
		if _, err := Prepare(tc.data); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
}

// pngClaiming is a tiny PNG whose header says it is w×h, with a correct
// checksum, so only the size gives it away.
func pngClaiming(t *testing.T, w, h uint32) []byte {
	t.Helper()

	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}

	b := buf.Bytes()

	// The IHDR chunk: 8-byte signature, 4 length, "IHDR", then width and
	// height, and the CRC over the type and data 13 bytes later.
	binary.BigEndian.PutUint32(b[16:], w)
	binary.BigEndian.PutUint32(b[20:], h)
	binary.BigEndian.PutUint32(b[29:], crc32.ChecksumIEEE(b[12:29]))

	return b
}

// Malformed EXIF never panics and never turns the photo: every way of
// cutting the segment short gives the zero answer.
func TestBrokenEXIFIsIgnored(t *testing.T) {
	good := withEXIF(jpegOf(t, picture(400, 300)), 6, "2027:04:18 09:30:00")

	for n := range 120 {
		cut := append([]byte{}, good...)
		// Shorten the APP1 segment's declared length; the walk must stop
		// at the bounds, not past them.
		binary.BigEndian.PutUint16(cut[4:], uint16(n))
		_ = readEXIF(cut, "jpeg")
	}

	if m := readEXIF(withEXIF(jpegOf(t, picture(400, 300)), 6, "0000:00:00 00:00:00"), "jpeg"); m.taken != (Taken{}) || m.orientation != 6 {
		t.Errorf("an unset camera clock: %+v", m)
	}
}
