// Package launchd cài LaunchAgent chạy `mind-runner maintenance` hằng ngày (macOS).
// Ngoài darwin mọi hàm là no-op để setup/test chạy được cả ubuntu.
package launchd

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/template"

	"mind-runner/internal/execx"
)

// goos injectable để test cả 2 nhánh trên một máy.
var goos = runtime.GOOS

const Label = "com.mind-runner.maintenance"

//go:embed com.mind-runner.maintenance.plist.tmpl
var plistTmpl string

// PlistPath trả đường dẫn plist trong home.
func PlistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
}

// Install ghi plist, bootout (bỏ qua lỗi) rồi bootstrap.
// logDir cần thiết để plist trỏ log tuyệt đối theo data_dir (bổ sung nhỏ so với
// signature trong plan Install(ctx, r, binaryPath, home)).
func Install(ctx context.Context, r execx.Runner, binaryPath, home, logDir string) error {
	if goos != "darwin" {
		return nil
	}
	// escape XML: đường dẫn có &, <, ' … không được làm hỏng plist.
	t, err := template.New("plist").Parse(plistTmpl)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, struct{ BinaryPath, LogDir string }{xmlEscape(binaryPath), xmlEscape(logDir)}); err != nil {
		return err
	}
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return err
	}
	dst := PlistPath(home)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(dst, buf.Bytes(), 0o644); err != nil {
		return err
	}

	target := "gui/" + strconv.Itoa(os.Getuid())
	_, _ = r.Run(ctx, "launchctl", "bootout", target, dst) // chưa cài → lỗi, bỏ qua
	if _, err := r.Run(ctx, "launchctl", "bootstrap", target, dst); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w", err)
	}
	return nil
}

// Uninstall bootout + xoá plist.
func Uninstall(ctx context.Context, r execx.Runner, home string) error {
	if goos != "darwin" {
		return nil
	}
	dst := PlistPath(home)
	_, _ = r.Run(ctx, "launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid()), dst)
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
