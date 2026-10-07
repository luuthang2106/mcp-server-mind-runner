// Package project suy ra nhãn project từ thư mục làm việc: tên thư mục gốc git
// (worktree → repo chính), không có git → tên chính thư mục đó. Thư mục home,
// "/" hoặc rỗng → "" (không thuộc project nào, ví dụ Claude Desktop).
package project

import (
	"os"
	"path/filepath"
	"strings"
)

// Of trả nhãn project của dir ("" nếu không xác định).
func Of(dir string) string {
	if dir == "" {
		return ""
	}
	dir = filepath.Clean(dir)
	home, _ := os.UserHomeDir()
	if dir == "/" || (home != "" && dir == filepath.Clean(home)) {
		return ""
	}
	for d := dir; ; d = filepath.Dir(d) {
		if home != "" && d == filepath.Clean(home) || d == "/" || d == "." {
			break
		}
		fi, err := os.Lstat(filepath.Join(d, ".git"))
		if err != nil {
			continue
		}
		if fi.IsDir() {
			return filepath.Base(d)
		}
		// worktree / submodule: ".git" là file "gitdir: <repo>/.git/worktrees/<x>"
		if b, err := os.ReadFile(filepath.Join(d, ".git")); err == nil {
			gd := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
			if i := strings.Index(gd, string(filepath.Separator)+".git"+string(filepath.Separator)+"worktrees"+string(filepath.Separator)); i > 0 {
				return filepath.Base(gd[:i])
			}
		}
		return filepath.Base(d)
	}
	return filepath.Base(dir)
}
