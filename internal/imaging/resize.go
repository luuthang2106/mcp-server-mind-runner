package imaging

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // decode PNG (PNG không có EXIF → orientation bỏ qua)
	"math"

	"golang.org/x/image/draw"
)

// maxPixels: trần số điểm ảnh trước khi decode (chặn decompression bomb trong watch folder).
const maxPixels = 100_000_000

// Prepare chuẩn bị ảnh để gửi egress: decode JPEG/PNG → bake orientation →
// chỉ downscale khi cạnh dài > maxDim (CatmullRom) → encode JPEG quality.
// Re-encode tự bỏ EXIF/GPS.
func Prepare(src []byte, maxDim, quality int) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("decode ảnh: %w", err)
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return nil, fmt.Errorf("ảnh quá lớn: %dx%d vượt %d điểm ảnh", cfg.Width, cfg.Height, maxPixels)
	}
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("decode ảnh: %w", err)
	}
	if o := ReadOrientation(src); o != 1 {
		img = applyOrientation(img, o)
	}
	if maxDim > 0 {
		img = downscale(img, maxDim)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// applyOrientation biến đổi pixel theo 8 giá trị EXIF Orientation (2–8).
func applyOrientation(img image.Image, o int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var nx, ny int
			switch o {
			case 2:
				nx, ny = w-1-x, y
			case 3:
				nx, ny = w-1-x, h-1-y
			case 4:
				nx, ny = x, h-1-y
			case 5:
				nx, ny = y, x
			case 6:
				nx, ny = h-1-y, x
			case 7:
				nx, ny = h-1-y, w-1-x
			case 8:
				nx, ny = y, w-1-x
			default:
				nx, ny = x, y
			}
			dst.Set(nx, ny, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// downscale chỉ thu nhỏ khi cạnh dài vượt maxDim, giữ tỉ lệ.
func downscale(img image.Image, maxDim int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	long := w
	if h > long {
		long = h
	}
	if long <= maxDim {
		return img
	}
	s := float64(maxDim) / float64(long)
	nw := int(math.Round(float64(w) * s))
	nh := int(math.Round(float64(h) * s))
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}
