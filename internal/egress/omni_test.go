package egress_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"mind-runner/internal/egress"
	"mind-runner/internal/egress/egressfake"
)

// TestOmniAudioShape: request audio đúng shape đã verify với gateway thật
// (Step 0 Task 6.3): input_audio data-URL base64 + format m4a; prompt bị redact.
func TestOmniAudioShape(t *testing.T) {
	fake := egressfake.New(t, egressfake.Options{OmniResp: func([]map[string]any) string {
		return "xin chào mind runner"
	}})
	audio := []byte("\x00m4a-giả\x01")
	out, err := fake.Egress.Omni(context.Background(), egress.PolicyCloud, egress.OmniRequest{
		Text: "chép lại nguyên văn (sk-abcdefghijklmnopqrstuvwxyz)", Audio: audio, AudioFormat: "m4a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "xin chào mind runner" {
		t.Fatalf("out=%q", out)
	}
	if fake.OmniCalls != 1 || fake.OmniModel != "fake-omni" {
		t.Fatalf("calls=%d model=%q", fake.OmniCalls, fake.OmniModel)
	}
	if len(fake.OmniParts) != 2 {
		t.Fatalf("parts=%v", fake.OmniParts)
	}
	pa := fake.OmniParts[0]
	if pa["type"] != "input_audio" {
		t.Fatalf("part[0]=%v", pa)
	}
	ia, _ := pa["input_audio"].(map[string]any)
	if ia["format"] != "m4a" {
		t.Fatalf("format=%v", ia["format"])
	}
	if want := "data:audio/m4a;base64," + base64.StdEncoding.EncodeToString(audio); ia["data"] != want {
		t.Fatalf("data=%v\nmuốn %v", ia["data"], want)
	}
	pt := fake.OmniParts[1]
	if pt["type"] != "text" {
		t.Fatalf("part[1]=%v", pt)
	}
	s, _ := pt["text"].(string)
	if strings.Contains(s, "sk-abc") || !strings.Contains(s, "[REDACTED:api-key]") {
		t.Fatalf("prompt chưa redact: %q", s)
	}
}

// TestOmniImageShape: ảnh JPEG → part image_url data-URL, không có part audio.
func TestOmniImageShape(t *testing.T) {
	fake := egressfake.New(t, egressfake.Options{OmniResp: func([]map[string]any) string { return "mô tả ảnh" }})
	img := []byte("\xff\xd8jpeg-giả\xff\xd9")
	out, err := fake.Egress.Omni(context.Background(), egress.PolicyCloud,
		egress.OmniRequest{Text: "mô tả ảnh", ImageJPEG: img})
	if err != nil || out != "mô tả ảnh" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if len(fake.OmniParts) != 2 {
		t.Fatalf("parts=%v", fake.OmniParts)
	}
	pi := fake.OmniParts[0]
	if pi["type"] != "image_url" {
		t.Fatalf("part[0]=%v", pi)
	}
	iu, _ := pi["image_url"].(map[string]any)
	if want := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(img); iu["url"] != want {
		t.Fatalf("url=%v muốn %v", iu["url"], want)
	}
	for _, p := range fake.OmniParts {
		if p["type"] == "input_audio" {
			t.Fatalf("lẫn part audio: %v", fake.OmniParts)
		}
	}
}

// TestOmniMissingModel: policy cloud thiếu model omni → lỗi rõ, không gọi mạng.
func TestOmniMissingModel(t *testing.T) {
	fake := egressfake.New(t, egressfake.Options{})
	cfg := egressfake.Config(fake, nil)
	cfg.Gateway.Models.Omni = ""
	eg := egress.New(cfg, nil)
	_, err := eg.Omni(context.Background(), egress.PolicyCloud,
		egress.OmniRequest{Text: "x", Audio: []byte("a"), AudioFormat: "m4a"})
	if err == nil || !strings.Contains(err.Error(), "thiếu model omni") {
		t.Fatalf("err=%v", err)
	}
	if fake.OmniCalls != 0 {
		t.Fatalf("đã gọi mạng: %d", fake.OmniCalls)
	}
}
