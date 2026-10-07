package imaging

import (
	"bytes"
	"image"
	"image/jpeg"
	"testing"
)

// exifAPP1 khối APP1 EXIF tối thiểu: TIFF little-endian, IFD0 đúng 1 entry —
// Orientation (0x0112, SHORT, count 1).
func exifAPP1(o int) []byte {
	tiff := []byte{
		'I', 'I', 0x2A, 0x00,
		0x08, 0x00, 0x00, 0x00, // IFD0 tại offset 8
		0x01, 0x00, // 1 entry
		0x12, 0x01, 0x03, 0x00, // tag Orientation, type SHORT
		0x01, 0x00, 0x00, 0x00, // count 1
		byte(o), 0x00, 0x00, 0x00, // value
		0x00, 0x00, 0x00, 0x00, // next IFD = 0
	}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	return append([]byte{0xFF, 0xE1, 0x00, byte(len(payload) + 2)}, payload...)
}

// jpegWithOrientation encode ảnh rồi chèn APP1 ngay sau SOI.
func jpegWithOrientation(t *testing.T, img image.Image, o int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	out := make([]byte, 0, len(raw)+32)
	out = append(out, raw[:2]...) // SOI
	out = append(out, exifAPP1(o)...)
	out = append(out, raw[2:]...)
	return out
}

func TestReadOrientation(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))

	if o := ReadOrientation(jpegWithOrientation(t, img, 6)); o != 6 {
		t.Fatalf("EXIF orientation=6 → %d", o)
	}
	if o := ReadOrientation(jpegWithOrientation(t, img, 8)); o != 8 {
		t.Fatalf("EXIF orientation=8 → %d", o)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	if o := ReadOrientation(buf.Bytes()); o != 1 {
		t.Fatalf("không EXIF → %d", o)
	}
	if o := ReadOrientation([]byte("không phải jpeg")); o != 1 {
		t.Fatalf("rác → %d", o)
	}
	if o := ReadOrientation(nil); o != 1 {
		t.Fatalf("nil → %d", o)
	}
}
