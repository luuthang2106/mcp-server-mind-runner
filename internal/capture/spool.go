package capture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mind-runner/internal/store"
)

// Write ghi blob vào spool dir khi DB bận (định dạng cũ, không kèm offset):
// <dir>/<sessionID>-<unixnano>.gz.
func Write(dir, sessionID string, blob []byte) error {
	return writeSpool(dir, sessionID+"-"+strconv.FormatInt(time.Now().UnixNano(), 10)+".gz", blob)
}

// WriteWithOffset ghi spool kèm offset mong đợi/offset mới:
// <dir>/<sessionID>~<expect>~<new>~<unixnano>.gz. Khi merge, raw chỉ được
// chèn nếu offset trong DB vẫn == expect — nếu một Stop sau đó đã chốt lại
// cùng đoạn transcript thì file spool bị bỏ (không nhân bản raw).
func WriteWithOffset(dir, sessionID string, blob []byte, expect, newOffset int64) error {
	name := fmt.Sprintf("%s~%d~%d~%d.gz", sessionID, expect, newOffset, time.Now().UnixNano())
	return writeSpool(dir, name, blob)
}

// writeSpool ghi atomic (tmp + rename) để MergeSpool không đọc file dở.
func writeSpool(dir, name string, blob []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+name+".tmp")
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}

// parseSpoolName trả sessionID và offset (expect=-1 nếu định dạng cũ).
func parseSpoolName(name string) (sessionID string, expect, newOffset int64, ok bool) {
	base := strings.TrimSuffix(name, ".gz")
	if parts := strings.Split(base, "~"); len(parts) == 4 {
		e, err1 := strconv.ParseInt(parts[1], 10, 64)
		n, err2 := strconv.ParseInt(parts[2], 10, 64)
		if parts[0] == "" || err1 != nil || err2 != nil {
			return "", 0, 0, false
		}
		return parts[0], e, n, true
	}
	i := strings.LastIndex(base, "-") // sessionID có thể chứa '-' (uuid)
	if i <= 0 {
		return "", 0, 0, false
	}
	return base[:i], -1, 0, true
}

// MergeSpool hợp nhất mọi file spool vào DB: InsertRaw + enqueue extract_session
// rồi xoá file. Idempotent theo tên file: file đã merge = đã xoá; crash giữa
// chừng để lại file → merge lại, chấp nhận raw trùng lặp hiếm gặp (extract
// idempotent theo content_hash nên không nhân bản note).
// Dir chưa tồn tại → (0, nil).
func MergeSpool(ctx context.Context, st *store.Store, dir string, rawDays int, now time.Time) (int, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	merged := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".gz") || strings.HasPrefix(name, ".") {
			continue
		}
		sessionID, expect, newOffset, ok := parseSpoolName(name)
		if !ok {
			continue
		}
		path := filepath.Join(dir, name)
		blob, err := os.ReadFile(path)
		if err != nil {
			return merged, err
		}
		_, err = st.AppendRaw(ctx, sessionID, blob, rawDays, now, now.Add(ExtractDebounce), expect, newOffset)
		if err != nil && !errors.Is(err, store.ErrOffsetMoved) {
			return merged, err
		}
		if err := os.Remove(path); err != nil {
			return merged, err
		}
		if err == nil {
			merged++
		}
	}
	return merged, nil
}
