// Package capture đọc transcript delta (Claude Code JSONL), lưu raw gzip,
// spool fallback khi DB bận.
package capture

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"mind-runner/internal/store"
)

// ReadDelta đọc phần transcript từ offset. File ngắn hơn offset (bị ghi
// lại/rotate) → đọc từ 0 với reset=true (raw cũ đã lưu, không mất dữ liệu).
func ReadDelta(path string, offset int64) ([]byte, int64, bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, offset, false, err
	}
	reset := false
	if fi.Size() < offset {
		offset = 0
		reset = true
	}
	if fi.Size() == offset {
		return nil, offset, reset, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, reset, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, reset, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, offset, reset, err
	}
	// Claude Code có thể đang ghi dở dòng cuối: chỉ lấy tới '\n' cuối cùng,
	// phần còn lại để lần sau (tránh cắt đôi một bản ghi JSONL).
	i := bytes.LastIndexByte(data, '\n')
	if i < 0 {
		return nil, offset, reset, nil
	}
	data = data[:i+1]
	return data, offset + int64(len(data)), reset, nil
}

// Stop chốt delta hiện tại của session (hook Stop/session-end):
// ExtractDebounce: job extract của phiên chờ chừng này kể từ lượt đầu chưa
// xử lý, gom mọi lượt tới trong khoảng đó thành MỘT request (ít call hơn,
// model thấy ngữ cảnh liền mạch). Phiên kết thúc → chạy ngay (ExpediteExtract).
var ExtractDebounce = 10 * time.Minute

// ReadDelta → gzip → AppendRaw (một tx: raw + offset + enqueue extract_session).
// Không có delta mới, hoặc process khác vừa chốt cùng delta → (nil, nil).
// DB bận quá busy_timeout → ghi spool kèm offset để maintenance hợp nhất sau
// (trả nil — capture best-effort).
func Stop(ctx context.Context, st *store.Store, sess *store.Session, rawDays int, spoolDir string, now time.Time) (*int64, error) {
	if sess.TranscriptPath == nil {
		return nil, nil
	}
	data, newOffset, _, err := ReadDelta(*sess.TranscriptPath, sess.TranscriptOffset)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	blob := buf.Bytes()

	id, err := st.AppendRaw(ctx, sess.ID, blob, rawDays, now, now.Add(ExtractDebounce), sess.TranscriptOffset, newOffset)
	if errors.Is(err, store.ErrOffsetMoved) {
		return nil, nil
	}
	if err != nil {
		if werr := WriteWithOffset(spoolDir, sess.ID, blob, sess.TranscriptOffset, newOffset); werr != nil {
			return nil, fmt.Errorf("insert raw: %v; spool: %w", err, werr)
		}
		return nil, nil
	}
	return &id, nil
}
