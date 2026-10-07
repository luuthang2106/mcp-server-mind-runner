package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mind-runner/internal/brain"
	"mind-runner/internal/egress"
	"mind-runner/internal/store"
)

// ErrPermanent: lỗi retry không bao giờ khỏi — worker chuyển job thẳng sang
// dead thay vì chờ đủ MaxAttempts. Đặt ở media (không phải worker) vì chiều
// import là worker→media (register.go dựng handler); media không import worker.
var ErrPermanent = errors.New("lỗi vĩnh viễn")

// Prompts khớp curl verify 2026-10-06 (Step 0 Task 6.3).
const (
	transcribePrompt = "Chép lại nguyên văn nội dung ghi âm."
	captionPrompt    = "Mô tả ngắn gọn nội dung bức ảnh bằng tiếng Việt."
)

// Transcribe là handler job transcribe_media: media → omni → transcript/caption
// (bảng media) + note kind transcript|caption, source "media:<id>" (chunks +
// embed job chạy theo WriteNote). Retry an toàn: WriteNote idempotent theo text.
func (m *Media) Transcribe(ctx context.Context, mediaID int64) error {
	row, err := m.st.MediaByID(ctx, mediaID)
	if err != nil {
		return err
	}
	if row == nil {
		return fmt.Errorf("media %d không tồn tại", mediaID)
	}
	if err := m.st.UpdateMediaStatus(ctx, mediaID, "processing", nil, time.Now()); err != nil {
		return err
	}

	text, model, err := m.runOmni(ctx, row)
	if err != nil {
		status := "failed"
		if errors.Is(err, ErrPermanent) {
			status = "dead"
		}
		reason := err.Error()
		if uerr := m.st.UpdateMediaStatus(ctx, mediaID, status, &reason, time.Now()); uerr != nil {
			return errors.Join(err, fmt.Errorf("cập nhật status media %d: %w", mediaID, uerr))
		}
		return err
	}

	now := time.Now()
	if err := m.st.SetMediaTranscript(ctx, mediaID, text, model, now); err != nil {
		return err
	}
	kind := "transcript"
	if row.Kind == "image" {
		kind = "caption"
	}
	if _, err := m.b.WriteNote(ctx, brain.WriteParams{
		SpaceID: row.SpaceID, Kind: kind, Text: text, Source: fmt.Sprintf("media:%d", mediaID),
	}); err != nil {
		return err
	}
	// (6.5) keep_originals=false: transcript/caption là bản lưu chính — xoá file
	// gốc sau khi note đã ghi; path='' để doctor/purge phân biệt với file mất.
	if !m.cfg.Media.KeepOriginals {
		if err := m.dropOriginal(ctx, row); err != nil {
			return err
		}
	}
	return m.st.UpdateMediaStatus(ctx, mediaID, "done", nil, time.Now())
}

// dropOriginal xoá file gốc đã lưu (media/<path>); file không còn (đã xoá tay)
// vẫn coi là thành công rồi đánh dấu path=”. Lỗi xoá khác → trả lỗi để job
// retry (file còn thì lần sau xoá lại).
func (m *Media) dropOriginal(ctx context.Context, row *store.Media) error {
	full := filepath.Join(m.dataDir, "media", row.Path)
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("xoá gốc %s: %w", row.Path, err)
	}
	return m.st.ClearMediaPath(ctx, row.ID, time.Now())
}

// runOmni chuẩn bị bản egress rồi gọi model omni: ảnh → JPEG ≤1280 (D11) +
// caption; audio/video → file đã trích, vượt trần base64 thì giảm bitrate theo
// ladder, cạn ladder → ErrTooLarge bọc ErrPermanent (file quá dài, không retry).
func (m *Media) runOmni(ctx context.Context, row *store.Media) (string, string, error) {
	pol, err := m.policyForSpace(ctx, row.SpaceID)
	if err != nil {
		return "", "", err
	}
	src := filepath.Join(m.dataDir, "media", row.Path)
	req := egress.OmniRequest{Text: transcribePrompt}
	if row.Kind == "image" {
		prep, cleanup, err := m.tempPreparer()
		if err != nil {
			return "", "", err
		}
		defer cleanup()
		jpg, err := prep.PrepareImageEgress(ctx, src)
		if err != nil {
			return "", "", fmt.Errorf("chuẩn bị ảnh %s: %w", row.Path, err)
		}
		req.ImageJPEG = jpg
		req.Text = captionPrompt
	} else {
		fi, err := os.Stat(src)
		if err != nil {
			return "", "", fmt.Errorf("đọc %s: %w", row.Path, err)
		}
		send := src
		if Base64Len(int(fi.Size())) > maxBase64Payload {
			prep, cleanup, err := m.tempPreparer()
			if err != nil {
				return "", "", err
			}
			defer cleanup()
			shrunk, err := prep.ShrinkAudio(ctx, src, maxBase64Payload)
			if err != nil {
				if errors.Is(err, ErrTooLarge) {
					return "", "", fmt.Errorf(
						"%w: file quá dài — vượt trần 10MB base64 kể cả sau khi giảm bitrate (vượt ngưỡng thời lượng ~15 phút m4a); cắt ngắn rồi ingest lại: %w",
						ErrPermanent, err)
				}
				return "", "", err
			}
			send = shrunk
		}
		data, err := os.ReadFile(send)
		if err != nil {
			return "", "", fmt.Errorf("đọc %s: %w", row.Path, err)
		}
		req.Audio = data
		// format theo file thực gửi (ShrinkAudio đổi sang .m4a)
		req.AudioFormat = strings.ToLower(strings.TrimPrefix(filepath.Ext(send), "."))
	}
	out, err := m.eg.Omni(ctx, pol, req)
	if err != nil {
		return "", "", err
	}
	return out, m.eg.OmniModel(pol), nil
}

// policyForSpace resolve policy theo space: space_id → tên → config ([spaces.policy]).
func (m *Media) policyForSpace(ctx context.Context, spaceID int64) (egress.Policy, error) {
	sp, err := m.st.SpaceByID(ctx, spaceID)
	if err != nil {
		return "", err
	}
	return egress.Policy(m.cfg.SpacePolicy(sp.Name)), nil
}
