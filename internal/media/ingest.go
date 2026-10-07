package media

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mind-runner/internal/brain"
	"mind-runner/internal/config"
	"mind-runner/internal/egress"
	"mind-runner/internal/execx"
	"mind-runner/internal/store"
)

// kindByExt: định dạng M6.3 xử lý (audio | image | video).
var kindByExt = map[string]string{
	".m4a": "audio", ".mp3": "audio", ".wav": "audio", ".ogg": "audio",
	".png": "image", ".jpg": "image", ".jpeg": "image", ".heic": "image",
	".mp4": "video", ".mov": "video", ".m4v": "video",
}

// unsupportedVideo: container AVFoundation không đọc — hướng dẫn chuyển mp4/mov.
var unsupportedVideo = map[string]bool{
	".mkv": true, ".avi": true, ".flv": true, ".wmv": true, ".webm": true,
}

// unsupportedOther: đuôi media khác chưa hỗ trợ.
var unsupportedOther = map[string]bool{".aac": true, ".flac": true, ".gif": true, ".webp": true}

// IsMedia: path có phải media (kể cả đuôi chưa hỗ trợ — để CLI/tool báo lỗi
// rõ thay vì nạp như file text).
func IsMedia(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if _, ok := kindByExt[ext]; ok {
		return true
	}
	return unsupportedVideo[ext] || unsupportedOther[ext]
}

// Media là tầng nghiệp vụ Media/Omni (ingest + transcribe theo queue).
type Media struct {
	st      *store.Store
	b       *brain.Brain
	eg      *egress.Egress
	cfg     *config.Config
	p       *Preparer
	dataDir string
}

func New(st *store.Store, b *brain.Brain, eg *egress.Egress, cfg *config.Config, r execx.Runner, dataDir string) *Media {
	return &Media{st: st, b: b, eg: eg, cfg: cfg, p: &Preparer{R: r}, dataDir: dataDir}
}

// IngestResult: Created=false nghĩa là "đã có" (sha256 file nguồn trùng trong space).
type IngestResult struct {
	MediaID int64
	Status  string
	Created bool
}

// tempPreparer: thư mục tạm riêng cho từng thao tác — mcp sweep và maintenance
// có thể chạy job song song ở hai process nên không dùng chung một WorkDir.
func (m *Media) tempPreparer() (*Preparer, func(), error) {
	dir, err := os.MkdirTemp("", "mind-runner-media-*")
	if err != nil {
		return nil, nil, err
	}
	return &Preparer{R: m.p.R, WorkDir: dir}, func() { os.RemoveAll(dir) }, nil
}

// Ingest nạp 1 file media: dedupe sha256 file nguồn; video → trích audio
// trước (không copy container — D12); copy bản dùng vào media/<sha[:2]>/…
// rồi enqueue transcribe_media. Trả ngay, không chờ model.
func (m *Media) Ingest(ctx context.Context, path string, spaceID int64, source string) (IngestResult, error) {
	ext := strings.ToLower(filepath.Ext(path))
	kind, ok := kindByExt[ext]
	if !ok {
		if unsupportedVideo[ext] {
			return IngestResult{}, fmt.Errorf("đuôi video %s chưa hỗ trợ — chuyển sang mp4/mov rồi ingest lại", ext)
		}
		return IngestResult{}, fmt.Errorf("đuôi %s chưa hỗ trợ (audio: m4a mp3 wav ogg; ảnh: png jpg jpeg heic; video: mp4 mov m4v)", ext)
	}
	sha, err := SHA256File(path)
	if err != nil {
		return IngestResult{}, fmt.Errorf("đọc file: %w", err)
	}
	if have, err := m.st.MediaBySHA(ctx, spaceID, sha); err != nil {
		return IngestResult{}, err
	} else if have != nil {
		return IngestResult{MediaID: have.ID, Status: have.Status, Created: false}, nil
	}

	prep, cleanup, err := m.tempPreparer()
	if err != nil {
		return IngestResult{}, err
	}
	defer cleanup()

	storedSrc, storedExt := path, ext
	if kind == "video" {
		m4a, err := prep.ExtractAudio(ctx, path, prep.WorkDir)
		if err != nil {
			return IngestResult{}, err
		}
		storedSrc, storedExt = m4a, ".m4a"
	}
	rel, n, err := prep.CopyOriginal(m.dataDir, storedSrc, storedExt, sha)
	if err != nil {
		return IngestResult{}, err
	}

	now := time.Now()
	id, created, err := m.st.InsertMedia(ctx, &store.Media{
		SpaceID: spaceID, SHA256: sha, Kind: kind, Path: rel, Bytes: n,
		Status: "queued", Source: source,
	}, now)
	if err != nil {
		return IngestResult{}, err
	}
	if !created { // thua race UNIQUE(space, sha) → dùng row của process kia
		have, err := m.st.MediaBySHA(ctx, spaceID, sha)
		if err != nil {
			return IngestResult{}, err
		}
		if have == nil {
			return IngestResult{}, fmt.Errorf("media sha %s: insert báo trùng nhưng không tìm thấy row", sha)
		}
		return IngestResult{MediaID: have.ID, Status: have.Status, Created: false}, nil
	}
	if _, err := m.st.Enqueue(ctx, "transcribe_media", map[string]int64{"media_id": id}, now); err != nil {
		return IngestResult{}, err
	}
	return IngestResult{MediaID: id, Status: "queued", Created: true}, nil
}
