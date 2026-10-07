// Package logging: slog JSON vào logs/<name>.log, rotate theo size (giữ 2 file).
package logging

import (
	"log/slog"
	"os"
	"path/filepath"
)

// maxLogBytes là ngưỡng rotate; unexported để test hạ nhỏ.
var maxLogBytes int64 = 5 << 20

// New mở dataDir/logs/<name>.log (append). Trước khi mở: nếu file hiện tại
// > maxLogBytes → xoá .1 cũ, đổi tên file hiện tại thành .1 (giữ 2 file).
// level: debug|info|warn|error, lạ → info.
func New(dataDir, name, level string) (*slog.Logger, error) {
	dir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name+".log")
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLogBytes {
		if err := os.Remove(path + ".1"); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err := os.Rename(path, path+".1"); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: parseLevel(level)})), nil
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
