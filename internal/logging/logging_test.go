package logging

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("mở %s: %v", path, err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

func TestNewWritesJSONLines(t *testing.T) {
	dir := t.TempDir()
	lg, err := New(dir, "app", "info")
	if err != nil {
		t.Fatal(err)
	}
	lg.Info("xin chào", "k", "v")
	lg.Warn("cẩn thận")

	lines := readLines(t, filepath.Join(dir, "logs", "app.log"))
	if len(lines) != 2 {
		t.Fatalf("lines=%d, muốn 2", len(lines))
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatalf("không phải JSON: %v (%s)", err, lines[0])
	}
	for _, k := range []string{"time", "level", "msg"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("thiếu khoá %q trong %s", k, lines[0])
		}
	}
	if m["msg"] != "xin chào" || m["k"] != "v" {
		t.Fatalf("m=%v", m)
	}
}

func TestLevelFilter(t *testing.T) {
	dir := t.TempDir()
	lg, err := New(dir, "info", "info")
	if err != nil {
		t.Fatal(err)
	}
	lg.Debug("ẩn")
	if lines := readLines(t, filepath.Join(dir, "logs", "info.log")); len(lines) != 0 {
		t.Fatalf("level info không được ghi debug: %v", lines)
	}

	dir2 := t.TempDir()
	lg2, err := New(dir2, "debug", "debug")
	if err != nil {
		t.Fatal(err)
	}
	lg2.Debug("hiện")
	if lines := readLines(t, filepath.Join(dir2, "logs", "debug.log")); len(lines) != 1 {
		t.Fatalf("level debug phải ghi debug: %v", lines)
	}

	// level lạ → mặc định info: debug ẩn, info hiện
	dir3 := t.TempDir()
	lg3, err := New(dir3, "lạ", "xyz")
	if err != nil {
		t.Fatal(err)
	}
	lg3.Debug("ẩn")
	lg3.Info("hiện")
	if lines := readLines(t, filepath.Join(dir3, "logs", "lạ.log")); len(lines) != 1 {
		t.Fatalf("level lạ phải mặc định info: %v", lines)
	}
}

func TestRotateKeepsTwoFiles(t *testing.T) {
	old := maxLogBytes
	maxLogBytes = 200
	t.Cleanup(func() { maxLogBytes = old })

	dir := t.TempDir()
	lg, err := New(dir, "rot", "info")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		lg.Info(strings.Repeat("x", 50)) // ~80 bytes/dòng, vượt 200 sau vài dòng
	}

	// Mở lại → file cũ > 200 bytes bị đổi tên .1
	lg2, err := New(dir, "rot", "info")
	if err != nil {
		t.Fatal(err)
	}
	lg2.Info("sau rotate")
	if _, err := os.Stat(filepath.Join(dir, "logs", "rot.log.1")); err != nil {
		t.Fatalf("thiếu .1: %v", err)
	}
	if lines := readLines(t, filepath.Join(dir, "logs", "rot.log")); len(lines) != 1 {
		t.Fatalf("file mới phải trống trước dòng đầu: %v", lines)
	}

	// Rotate lần nữa → .1 cũ bị xoá, không có .2
	for i := 0; i < 10; i++ {
		lg2.Info(strings.Repeat("y", 50))
	}
	lg3, err := New(dir, "rot", "info")
	if err != nil {
		t.Fatal(err)
	}
	lg3.Info("lần 3")
	if _, err := os.Stat(filepath.Join(dir, "logs", "rot.log.2")); !os.IsNotExist(err) {
		t.Fatalf("không được giữ .2: %v", err)
	}
	lines := readLines(t, filepath.Join(dir, "logs", "rot.log"))
	if len(lines) != 1 || !strings.Contains(lines[0], "lần 3") {
		t.Fatalf("rot.log=%v", lines)
	}
}
