// Package selfsync: binary đóng gói trong .mcpb (Claude Desktop) tự cập nhật
// bản cài cho Claude Code/hook ở ~/.local/bin/mind-runner khi bản đó cũ hơn.
// Nhờ vậy người dùng chỉ cần kéo .mcpb mới vào Desktop là cập nhật cả hai.
//
// Quy tắc an toàn: chỉ cập nhật bản đã cài sẵn (không tự tạo mới), không bao
// giờ hạ cấp, bỏ qua build dev (ở cả hai phía), ghi file tạm rồi rename —
// inode mới nên macOS không giữ cache chữ ký của file cũ.
package selfsync

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Result mô tả việc Sync đã làm (để log).
type Result struct {
	Copied    bool
	Installed string // phiên bản bản cài trước khi sync ("" nếu không đọc được)
	Reason    string // lý do bỏ qua
}

// VersionFunc chạy `<path> version` và trả stdout.
type VersionFunc func(ctx context.Context, path string) (string, error)

// Sync chép self → target nếu target tồn tại và cũ hơn cur.
func Sync(ctx context.Context, self, target, cur string, version VersionFunc) (Result, error) {
	curV, ok := parseVersion(cur)
	if !ok {
		return Result{Reason: "bản đang chạy không có số phiên bản (dev)"}, nil
	}
	if same(self, target) {
		return Result{Reason: "đang chạy chính bản cài"}, nil
	}
	if _, err := os.Stat(target); err != nil {
		if os.IsNotExist(err) {
			return Result{Reason: "chưa cài cho Claude Code"}, nil
		}
		return Result{}, err
	}
	out, err := version(ctx, target)
	if err != nil {
		return Result{Reason: "không đọc được phiên bản bản cài"}, nil
	}
	inst := versionField(out)
	instV, ok := parseVersion(inst)
	if !ok {
		return Result{Installed: inst, Reason: "bản cài là build dev"}, nil
	}
	if !less(instV, curV) {
		return Result{Installed: inst, Reason: "bản cài không cũ hơn"}, nil
	}
	if err := copyAtomic(self, target); err != nil {
		return Result{Installed: inst}, err
	}
	return Result{Copied: true, Installed: inst}, nil
}

func same(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	if ra == rb {
		return true
	}
	sa, err1 := os.Stat(ra)
	sb, err2 := os.Stat(rb)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
}

// versionField lấy token phiên bản từ "mind-runner 0.1.4 (abc123)".
func versionField(out string) string {
	f := strings.Fields(out)
	if len(f) >= 2 && f[0] == "mind-runner" {
		return f[1]
	}
	if len(f) == 1 {
		return f[0]
	}
	return ""
}

// parseVersion nhận "0.1.4", "v0.1.4"; bỏ hậu tố pre-release/build
// ("0.1.4-rc1" → 0.1.4). "dev" hoặc lạ → false.
func parseVersion(s string) ([3]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var v [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

func less(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func copyAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".mind-runner.selfsync-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmpName)
		}
	}()
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return fmt.Errorf("chép binary: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}
	ok = true
	return nil
}
