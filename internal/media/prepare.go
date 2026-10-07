// Package media chuẩn bị file media cho egress omni: trích audio từ video,
// giảm bitrate audio theo ladder, chuẩn hoá ảnh; copy bản gốc vào store.
package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mind-runner/internal/execx"
	"mind-runner/internal/imaging"
)

// maxBase64Payload là trần base64 mọi model omni chấp nhận (spec §8: 10MB).
const maxBase64Payload = 10 * 1024 * 1024

// imageEgressDim / imageEgressQuality theo D11: cạnh dài ≤ 1280px, JPEG q85.
const (
	imageEgressDim     = 1280
	imageEgressQuality = 85
)

// ErrTooLarge: audio vẫn vượt trần sau khi thử hết ladder — job phải chết hẳn (dead).
var ErrTooLarge = errors.New("audio vượt trần base64 sau ladder")

// Trần thời gian subprocess: treo thì không giữ worker mãi.
const (
	audioTimeout = 10 * time.Minute
	sipsTimeout  = 2 * time.Minute
)

// shrinkLadder: các mức bitrate thử lần lượt (AAC mono 16kHz).
var shrinkLadder = []int{64000, 32000, 16000}

// SHA256File trả hex sha256 của file.
func SHA256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Base64Len trả độ dài base64 chuẩn (kèm padding) của rawLen byte.
func Base64Len(rawLen int) int { return (rawLen + 2) / 3 * 4 }

// Preparer shell-out qua Runner (fake được khi test); output tạm nằm trong WorkDir.
type Preparer struct {
	R       execx.Runner
	WorkDir string
}

// CopyOriginal copy file gốc as-is (mọi định dạng kể cả HEIC — D9) vào
// dataDir/media/<sha[:2]>/<sha><ext>; trả rel (đường dẫn tương đối trong media/)
// và số byte. Convert chỉ xảy ra lúc egress, không đụng file lưu.
func (p *Preparer) CopyOriginal(dataDir, src, ext string, sha string) (string, int64, error) {
	rel := filepath.Join(sha[:2], sha+ext)
	dst := filepath.Join(dataDir, "media", rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", 0, err
	}
	in, err := os.Open(src)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	// ghi file tạm cùng thư mục rồi rename — không để lại file dở dang ở dst.
	out, err := os.CreateTemp(filepath.Dir(dst), ".copy-*")
	if err != nil {
		return "", 0, err
	}
	tmp := out.Name()
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o600)
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
		return "", 0, err
	}
	return rel, n, nil
}

// ExtractAudio trích audio từ video thành m4a (AAC) trong outDir; KHÔNG copy container.
// Lệnh thật đã verify trên máy (Task 6.2 Step 0):
//
//	avconvert --preset PresetAppleM4A --source <video> --output <out.m4a>
//
// avconvert in "avconvert completed with error:0." và exit 0 khi thành công.
func (p *Preparer) ExtractAudio(ctx context.Context, src, outDir string) (string, error) {
	out := filepath.Join(outDir, strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))+".m4a")
	ctx, cancel := context.WithTimeout(ctx, audioTimeout)
	defer cancel()
	if _, err := p.R.Run(ctx,
		"avconvert", "--preset", "PresetAppleM4A", "--source", src, "--output", out); err != nil {
		return "", fmt.Errorf("avconvert trích audio từ %s: %w", src, err)
	}
	return out, nil
}

// ShrinkAudio giảm dung lượng audio bằng afconvert (AAC mono 16kHz) theo ladder
// [64k, 32k, 16k] — chọn mức đầu tiên có base64 ≤ limitBytes; hết ladder → ErrTooLarge.
// Lệnh thật đã verify (Step 0):
//
//	afconvert -f m4af -d aac -b <bitrate> -c 1 -r 16000 <in> <out>
func (p *Preparer) ShrinkAudio(ctx context.Context, src string, limitBytes int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, audioTimeout)
	defer cancel()
	for _, br := range shrinkLadder {
		out := filepath.Join(p.WorkDir, fmt.Sprintf("shrink-%dk.m4a", br/1000))
		if _, err := p.R.Run(ctx,
			"afconvert", "-f", "m4af", "-d", "aac", "-b", strconv.Itoa(br), "-c", "1", "-r", "16000",
			src, out); err != nil {
			return "", fmt.Errorf("afconvert %s @%dbps: %w", src, br, err)
		}
		st, err := os.Stat(out)
		if err != nil {
			return "", fmt.Errorf("afconvert không tạo %s: %w", out, err)
		}
		if Base64Len(int(st.Size())) <= limitBytes {
			return out, nil
		}
	}
	return "", fmt.Errorf("%w (đã thử 64k/32k/16k mono 16kHz, trần %d bytes base64)", ErrTooLarge, limitBytes)
}

// PrepareImageEgress chuẩn bị bản ảnh gửi omni: HEIC → JPEG qua sips rồi
// imaging.Prepare (downscale ≤1280 + strip EXIF/GPS — D11). Lệnh thật đã verify (Step 0):
//
//	sips -s format jpeg <in> --out <out.jpg>
func (p *Preparer) PrepareImageEgress(ctx context.Context, src string) ([]byte, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(filepath.Ext(src), ".heic") {
		out := filepath.Join(p.WorkDir, strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))+".jpg")
		sctx, cancel := context.WithTimeout(ctx, sipsTimeout)
		_, err = p.R.Run(sctx, "sips", "-s", "format", "jpeg", src, "--out", out)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("sips HEIC→JPEG %s: %w", src, err)
		}
		if data, err = os.ReadFile(out); err != nil {
			return nil, fmt.Errorf("sips không tạo %s: %w", out, err)
		}
	}
	return imaging.Prepare(data, imageEgressDim, imageEgressQuality)
}
