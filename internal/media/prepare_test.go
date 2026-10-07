package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"mind-runner/internal/execx"
)

// fakeRunner ghi lại lệnh và có thể tạo file output như công cụ thật.
type fakeRunner struct {
	calls [][]string
	fn    func(name string, args []string) ([]byte, error)
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.fn != nil {
		return f.fn(name, args)
	}
	return nil, nil
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestExtractAudioReal: avconvert thật (darwin) trên fixture .mov → .m4a > 0 bytes trong outDir.
func TestExtractAudioReal(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("cần macOS (avconvert)")
	}
	const src = "testdata/video_short.mov"
	if _, err := os.Stat(src); err != nil {
		t.Skipf("thiếu fixture: %v", err)
	}
	p := &Preparer{R: execx.OS{}, WorkDir: t.TempDir()}
	outDir := t.TempDir()
	m4a, err := p.ExtractAudio(context.Background(), src, outDir)
	if err != nil {
		t.Fatalf("ExtractAudio: %v", err)
	}
	st, err := os.Stat(m4a)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() == 0 {
		t.Fatalf("m4a rỗng: %s", m4a)
	}
	if filepath.Dir(m4a) != outDir {
		t.Fatalf("m4a không nằm trong outDir: %s", m4a)
	}
}

// TestPrepareImageEgressBoundsAndStripsEXIF: ảnh 2000x1000 → JPEG cạnh dài 1280, không còn Exif.
func TestPrepareImageEgressBoundsAndStripsEXIF(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2000, 1000))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 40, G: 90, B: 200, A: 255}}, image.Point{}, draw.Src)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "big.jpg")
	writeFile(t, src, buf.Bytes())

	p := &Preparer{R: &fakeRunner{}, WorkDir: t.TempDir()}
	out, err := p.PrepareImageEgress(context.Background(), src)
	if err != nil {
		t.Fatalf("PrepareImageEgress: %v", err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 1280 || cfg.Height != 640 {
		t.Fatalf("kích thước %dx%d, muốn 1280x640", cfg.Width, cfg.Height)
	}
	if bytes.Contains(out, []byte("Exif")) {
		t.Fatal("JPEG egress còn dấu vết Exif")
	}
}

// TestShrinkAudioLadderPicksFirstFit: fake runner tạo output theo bitrate —
// chọn đúng mức đầu tiên có base64 ≤ limit và truyền đúng lệnh afconvert.
func TestShrinkAudioLadderPicksFirstFit(t *testing.T) {
	src := filepath.Join(t.TempDir(), "big.m4a")
	writeFile(t, src, []byte("nguồn"))

	// Args afconvert: [afconvert -f m4af -d aac -b <br> -c 1 -r 16000 src out] → br ở index 5.
	sizes := map[string]int{"64000": 50, "32000": 100, "16000": 100}
	fr := &fakeRunner{fn: func(name string, args []string) ([]byte, error) {
		out := args[len(args)-1]
		writeFile(t, out, bytes.Repeat([]byte{7}, sizes[args[5]]))
		return nil, nil
	}}
	p := &Preparer{R: fr, WorkDir: t.TempDir()}

	// limit đủ chỗ cho 64k (50 bytes → base64 68 ≤ 100) → dừng ngay mức 1.
	out, err := p.ShrinkAudio(context.Background(), src, 100)
	if err != nil {
		t.Fatalf("ShrinkAudio: %v", err)
	}
	if len(fr.calls) != 1 {
		t.Fatalf("số lệnh = %d, muốn 1", len(fr.calls))
	}
	want := []string{"afconvert", "-f", "m4af", "-d", "aac", "-b", "64000", "-c", "1", "-r", "16000", src, out}
	if !reflect.DeepEqual(fr.calls[0], want) {
		t.Fatalf("lệnh %v, muốn %v", fr.calls[0], want)
	}
	if st, _ := os.Stat(out); st == nil || st.Size() != 50 {
		t.Fatalf("out %s không phải file 50 bytes", out)
	}

	// limit chỉ đủ cho 32k (100 bytes → base64 136 ≤ 150 < 64k của lần này) → chạy 2 mức.
	sizes["64000"] = 200 // base64 268 > 150 → trượt mức 1
	fr.calls = nil
	out, err = p.ShrinkAudio(context.Background(), src, 150)
	if err != nil {
		t.Fatalf("ShrinkAudio lần 2: %v", err)
	}
	if len(fr.calls) != 2 || fr.calls[1][6] != "32000" {
		t.Fatalf("lệnh = %v, muốn 2 lệnh dừng ở 32000", fr.calls)
	}
	if fr.calls[1][len(fr.calls[1])-1] != out {
		t.Fatal("out trả về không khớp tham số cuối lệnh 2")
	}
}

// TestShrinkAudioTooLarge: mọi mức đều vượt limit → ErrTooLarge sau đủ 3 lần thử.
func TestShrinkAudioTooLarge(t *testing.T) {
	src := filepath.Join(t.TempDir(), "big.m4a")
	writeFile(t, src, []byte("nguồn"))
	fr := &fakeRunner{fn: func(_ string, args []string) ([]byte, error) {
		writeFile(t, args[len(args)-1], bytes.Repeat([]byte{7}, 10000))
		return nil, nil
	}}
	p := &Preparer{R: fr, WorkDir: t.TempDir()}

	_, err := p.ShrinkAudio(context.Background(), src, 100)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, muốn ErrTooLarge", err)
	}
	if len(fr.calls) != 3 {
		t.Fatalf("số lệnh = %d, muốn thử đủ 3 mức", len(fr.calls))
	}
}

// TestCopyOriginalByteExactNoConvert: file gốc (kể cả .heic) được copy byte-exact
// vào media/<sha[:2]>/<sha><ext>, không shell-out, không convert (D9).
func TestCopyOriginalByteExactNoConvert(t *testing.T) {
	dataDir := t.TempDir()
	fr := &fakeRunner{}
	p := &Preparer{R: fr, WorkDir: t.TempDir()}

	cases := []struct {
		name string
		ext  string
		data []byte
	}{
		{"jpeg", ".jpg", []byte("\xff\xd8 jpeg-giả \x00\x01\x02")},
		{"heic", ".heic", []byte("heic-giả-không-convert")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := filepath.Join(t.TempDir(), "in"+tc.ext)
			writeFile(t, src, tc.data)
			sha, err := SHA256File(src)
			if err != nil {
				t.Fatal(err)
			}
			rel, n, err := p.CopyOriginal(dataDir, src, tc.ext, sha)
			if err != nil {
				t.Fatalf("CopyOriginal: %v", err)
			}
			want := sha[:2] + "/" + sha + tc.ext
			if rel != want {
				t.Fatalf("rel = %q, muốn %q", rel, want)
			}
			got, err := os.ReadFile(filepath.Join(dataDir, "media", want))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tc.data) {
				t.Fatal("nội dung không byte-exact")
			}
			if n != int64(len(tc.data)) {
				t.Fatalf("bytes = %d, muốn %d", n, len(tc.data))
			}
		})
	}
	if len(fr.calls) != 0 {
		t.Fatalf("CopyOriginal phải không shell-out, có %d lệnh", len(fr.calls))
	}
}

// TestSHA256FileAndBase64Len: hash khớp crypto/sha256; Base64Len đúng công thức có padding.
func TestSHA256FileAndBase64Len(t *testing.T) {
	src := filepath.Join(t.TempDir(), "x.bin")
	data := []byte("mind-runner")
	writeFile(t, src, data)
	got, err := SHA256File(src)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if want := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("sha = %s, muốn %s", got, want)
	}

	for _, tc := range []struct{ in, want int }{{0, 0}, {1, 4}, {2, 4}, {3, 4}, {4, 8}, {6, 8}, {7, 12}} {
		if got := Base64Len(tc.in); got != tc.want {
			t.Fatalf("Base64Len(%d) = %d, muốn %d", tc.in, got, tc.want)
		}
	}
	if got := Base64Len(maxBase64Payload); got <= maxBase64Payload {
		t.Fatalf("Base64Len trần = %d, phải > %d", got, maxBase64Payload)
	}
}
