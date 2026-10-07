package media

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mind-runner/internal/config"
)

// watchFresh: file mtime mới hơn ngưỡng này coi là đang ghi dở → bỏ qua lượt này
// (spec §8: "Bỏ qua file mtime < 60s").
const watchFresh = 60 * time.Second

// WatchReport đếm từng nhóm để maintenance in đủ (kể cả 0 — không im lặng).
// Failed: file Ingest lỗi (đã log, quét tiếp file sau).
type WatchReport struct {
	Seen, Ingested, SkippedFresh, SkippedType, AlreadyHave, Failed int
}

// ScanWatchDirs quét KHÔNG đệ quy file trong từng dir: mtime < 60s → bỏ qua
// (đang ghi dở); đuôi không nằm trong kindByExt → bỏ qua (SkippedType); còn lại
// → Ingest với source "watch:<dir>" (dedupe sha256 tự nhiên → AlreadyHave).
// Space của file theo [spaces.match] (Config.MatchSpace).
func (m *Media) ScanWatchDirs(ctx context.Context, dirs []string, now time.Time) (WatchReport, error) {
	var rep WatchReport
	for _, dir := range dirs {
		dir = config.ExpandHome(dir)
		ents, err := os.ReadDir(dir)
		if err != nil {
			return rep, fmt.Errorf("watch %s: %w", dir, err)
		}
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			path := filepath.Join(dir, e.Name())
			rep.Seen++
			fi, err := e.Info()
			if err != nil {
				return rep, fmt.Errorf("watch %s: %w", path, err)
			}
			if now.Sub(fi.ModTime()) < watchFresh {
				rep.SkippedFresh++
				continue
			}
			if _, ok := kindByExt[strings.ToLower(filepath.Ext(path))]; !ok {
				rep.SkippedType++
				continue
			}
			spaceID, err := m.st.SpaceByName(ctx, m.cfg.MatchSpace(path))
			if err != nil {
				return rep, fmt.Errorf("watch %s: %w", path, err)
			}
			res, err := m.Ingest(ctx, path, spaceID, "watch:"+dir)
			if err != nil {
				if ctx.Err() != nil {
					return rep, fmt.Errorf("watch %s: %w", path, err)
				}
				// 1 file hỏng không được chặn cả lượt quét (ReadDir sắp xếp → kẹt mãi)
				rep.Failed++
				slog.Warn("watch: ingest lỗi, bỏ qua file", "path", path, "err", err)
				continue
			}
			if res.Created {
				rep.Ingested++
			} else {
				rep.AlreadyHave++
			}
		}
	}
	return rep, nil
}
