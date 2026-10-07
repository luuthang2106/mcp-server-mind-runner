package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"mind-runner/internal/brain"
	"mind-runner/internal/config"
	"mind-runner/internal/egress"
	"mind-runner/internal/execx"
	"mind-runner/internal/extract"
	"mind-runner/internal/media"
	"mind-runner/internal/store"
)

// Deps: phụ thuộc các handler dùng chung (maintenance + mcp sweep cùng dựng).
type Deps struct {
	Store   *store.Store
	Brain   *brain.Brain
	Egress  *egress.Egress
	Config  *config.Config
	Execx   execx.Runner // media: avconvert/afconvert/sips
	DataDir string       // gốc dữ liệu (media/ nằm trong đây)
}

// RegisterAll: embed_chunk; extract_session/summarize_session; transcribe_media.
func RegisterAll(reg *Registry, d Deps) {
	reg.Register("embed_chunk", func(ctx context.Context, payload json.RawMessage) error {
		var p struct {
			NoteID int64 `json:"note_id"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("payload embed_chunk: %w", err)
		}
		return d.Brain.EmbedNote(ctx, p.NoteID)
	})

	x := extract.New(d.Store, d.Brain, d.Egress, d.Config)
	reg.Register("extract_session", func(ctx context.Context, payload json.RawMessage) error {
		var p struct {
			SessionID string `json:"session_id"`
			RawID     int64  `json:"raw_id"` // job dạng cũ (trước schema 5)
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("payload extract_session: %w", err)
		}
		if p.SessionID != "" {
			return x.RunSession(ctx, p.SessionID)
		}
		return x.RunRaw(ctx, p.RawID)
	})
	reg.Register("summarize_session", func(ctx context.Context, payload json.RawMessage) error {
		var p struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("payload summarize_session: %w", err)
		}
		return x.SummarizeSession(ctx, p.SessionID)
	})

	md := media.New(d.Store, d.Brain, d.Egress, d.Config, d.Execx, d.DataDir)
	reg.Register("transcribe_media", func(ctx context.Context, payload json.RawMessage) error {
		var p struct {
			MediaID int64 `json:"media_id"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("payload transcribe_media: %w", err)
		}
		return md.Transcribe(ctx, p.MediaID)
	})
}
