package egress

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
)

// OmniRequest: nội dung đa hình gửi omni — ảnh JPEG (bản egress đã resize/strip
// EXIF) và/hoặc audio (m4a/wav/…); Text được redact như mọi text rời máy.
type OmniRequest struct {
	Text        string
	ImageJPEG   []byte
	Audio       []byte
	AudioFormat string // khớp định dạng file gửi: "m4a" | "wav" | …
}

// OmniModel trả model omni theo policy (lưu vào media.model sau khi chạy).
func (e *Egress) OmniModel(pol Policy) string {
	if pol == PolicyLocal {
		return e.cfg.Spaces.Local.Models.Omni
	}
	return e.cfg.Gateway.Models.Omni
}

// Omni gọi model omni qua chat completions với content array (shape verified
// trên gateway thật 2026-10-06 — Step 0 Task 6.3):
//
//	audio: {"type":"input_audio","input_audio":{"data":"data:audio/m4a;base64,…","format":"m4a"}}
//	image: {"type":"image_url","image_url":{"url":"data:image/jpeg;base64,…"}}
//
// m4a/wav đã xác nhận được chấp nhận; trần base64 10MB mọi model omni do caller
// ép trước khi gọi (spec §8).
func (e *Egress) Omni(ctx context.Context, pol Policy, req OmniRequest) (string, error) {
	client, models, err := e.route(pol)
	if err != nil {
		return "", err
	}
	if models.Omni == "" {
		return "", fmt.Errorf("%s thiếu model omni", pol)
	}
	txt := Redact(req.Text)
	logRedactions("omni", txt.Counts)
	var parts []map[string]any
	if len(req.ImageJPEG) > 0 {
		parts = append(parts, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(req.ImageJPEG),
			},
		})
	}
	if len(req.Audio) > 0 {
		parts = append(parts, map[string]any{
			"type": "input_audio",
			"input_audio": map[string]any{
				"data":   "data:audio/" + req.AudioFormat + ";base64," + base64.StdEncoding.EncodeToString(req.Audio),
				"format": req.AudioFormat,
			},
		})
	}
	parts = append(parts, map[string]any{"type": "text", "text": txt.Text})
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	ci, err := client.doJSON(ctx, "/chat/completions", map[string]any{
		"model":    models.Omni,
		"messages": []map[string]any{{"role": "user", "content": parts}},
	}, &resp)
	// media base64 không tính vào ngưỡng request lớn
	e.meter.record(ctx, "omni", models.Omni, pol, ci, err, base64.StdEncoding.EncodedLen(len(req.ImageJPEG))+base64.StdEncoding.EncodedLen(len(req.Audio)))
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("omni: response không có choices")
	}
	return resp.Choices[0].Message.Content, nil
}
