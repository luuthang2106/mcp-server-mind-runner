package imaging

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"testing"
)

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decodeJPEG(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// TestPrepareDownscales: 4000×2000 → cạnh dài đúng 1280, tỉ lệ giữ.
func TestPrepareDownscales(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4000, 2000))
	out, err := Prepare(encodeJPEG(t, img), 1280, 85)
	if err != nil {
		t.Fatal(err)
	}
	b := decodeJPEG(t, out).Bounds()
	if b.Dx() != 1280 || b.Dy() != 640 {
		t.Fatalf("dims=%dx%d, muốn 1280x640", b.Dx(), b.Dy())
	}
}

// TestPrepareNoUpscaleStripsExif: 800×600 giữ nguyên kích thước nhưng re-encode
// (bytes khác gốc, mất chuỗi Exif).
func TestPrepareNoUpscaleStripsExif(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 800, 600))
	in := jpegWithOrientation(t, img, 1)
	if !bytes.Contains(in, []byte("Exif")) {
		t.Fatal("fixture thiếu EXIF")
	}
	out, err := Prepare(in, 1280, 85)
	if err != nil {
		t.Fatal(err)
	}
	b := decodeJPEG(t, out).Bounds()
	if b.Dx() != 800 || b.Dy() != 600 {
		t.Fatalf("dims=%dx%d, muốn 800x600", b.Dx(), b.Dy())
	}
	if bytes.Contains(out, []byte("Exif")) {
		t.Fatal("output còn EXIF")
	}
	if bytes.Equal(out, in) {
		t.Fatal("output giống hệt input — chưa re-encode")
	}
}

// TestPrepareBakesOrientation6: trái đỏ / phải xanh + orientation=6 → xoay 90°
// CW: w/h đổi chiều, hàng trên đỏ, hàng dưới xanh.
func TestPrepareBakesOrientation6(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 32, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 32; x++ {
			if x < 16 {
				img.Set(x, y, color.RGBA{255, 0, 0, 255})
			} else {
				img.Set(x, y, color.RGBA{0, 0, 255, 255})
			}
		}
	}
	out, err := Prepare(jpegWithOrientation(t, img, 6), 1280, 85)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeJPEG(t, out)
	b := got.Bounds()
	if b.Dx() != 16 || b.Dy() != 32 {
		t.Fatalf("dims=%dx%d, muốn 16x32", b.Dx(), b.Dy())
	}
	r, _, bl, _ := got.At(8, 2).RGBA()
	if r < 0x8000 || bl > 0x4000 {
		t.Fatalf("hàng trên phải đỏ: r=%d b=%d", r, bl)
	}
	r, _, bl, _ = got.At(8, 29).RGBA()
	if bl < 0x8000 || r > 0x4000 {
		t.Fatalf("hàng dưới phải xanh: r=%d b=%d", r, bl)
	}
}

// TestPrepareGarbage: input không decode được → error.
func TestPrepareGarbage(t *testing.T) {
	if _, err := Prepare([]byte("không phải ảnh"), 1280, 85); err == nil {
		t.Fatal("rác phải lỗi")
	}
}

// TestPrepareRejectsHugeDimensions: header PNG khai 20000×20000 → từ chối trước khi decode.
func TestPrepareRejectsHugeDimensions(t *testing.T) {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 20000)
	binary.BigEndian.PutUint32(ihdr[4:], 20000)
	ihdr[8], ihdr[9] = 8, 2 // 8-bit RGB
	var buf bytes.Buffer
	buf.WriteString("\x89PNG\r\n\x1a\n")
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(ihdr)))
	chunk := append([]byte("IHDR"), ihdr...)
	buf.Write(chunk)
	_ = binary.Write(&buf, binary.BigEndian, crc32.ChecksumIEEE(chunk))

	_, err := Prepare(buf.Bytes(), 1280, 85)
	if err == nil || !strings.Contains(err.Error(), "quá lớn") {
		t.Fatalf("err=%v, muốn từ chối ảnh quá lớn", err)
	}
}
