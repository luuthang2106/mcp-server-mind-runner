package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"mind-runner/internal/config"
	"mind-runner/internal/launchd"
	"mind-runner/internal/store"
)

// RunDoctor kiểm tra sức khoẻ: config, data dir, DB, schema, launchd.
// Exit 1 nếu có dòng fail; warn không làm fail.
func RunDoctor(args []string, stdout, stderr io.Writer, env func(string) string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	fails := 0

	cfgPath := env("MIND_RUNNER_CONFIG")
	if cfgPath == "" {
		cfgPath = config.DefaultConfigPath()
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(stdout, "fail: config: %v\n", err)
		return 1
	}
	if w := cfg.Warnings(); len(w) > 0 {
		for _, line := range w {
			fmt.Fprintf(stdout, "warn: %s\n", line)
		}
	} else {
		fmt.Fprintf(stdout, "ok: config %s (0600)\n", cfgPath)
	}
	checkPolicyModels(cfg, stdout)

	base := config.ExpandHome(cfg.DataDir)
	if err := checkWritableDir(base); err != nil {
		fmt.Fprintf(stdout, "fail: data dir %s: %v\n", base, err)
		fails++
	} else {
		fmt.Fprintf(stdout, "ok: data dir %s (writable)\n", base)
	}

	dbPath := filepath.Join(base, "mind-runner.db")
	if err := checkDB(dbPath); err != nil {
		fmt.Fprintf(stdout, "fail: db: %v\n", err)
		fails++
	} else {
		fmt.Fprintf(stdout, "ok: db (quick_check ok, schema v%d)\n", store.LatestSchema)
		checkMediaFiles(dbPath, filepath.Join(base, "media"), stdout)
	}

	if runtime.GOOS == "darwin" {
		if _, err := os.Stat(launchd.PlistPath(config.ExpandHome("~"))); err != nil {
			fmt.Fprintln(stdout, "warn: launchd plist chưa có — chạy setup (hoặc setup đã dùng --skip-launchd)")
		} else {
			fmt.Fprintln(stdout, "ok: launchd plist")
		}
	}

	if fails > 0 {
		return 1
	}
	return 0
}

// checkPolicyModels: warn khi policy local thiếu endpoint/model hoặc endpoint
// không phản hồi; cloud thiếu model extract/omni. Warn-only — máy không dùng
// tính năng nào vẫn xanh.
func checkPolicyModels(cfg config.Config, stdout io.Writer) {
	hasLocal := false
	for _, pol := range cfg.Spaces.Policy {
		if pol == "local" {
			hasLocal = true
			break
		}
	}
	if hasLocal {
		if cfg.Spaces.Local.BaseURL == "" {
			fmt.Fprintln(stdout, "warn: policy local nhưng [spaces.local].base_url chưa cấu hình")
		} else if err := probeModels(cfg.Spaces.Local.BaseURL); err != nil {
			fmt.Fprintf(stdout, "warn: local endpoint không phản hồi: %v\n", err)
		} else {
			fmt.Fprintln(stdout, "ok: local endpoint phản hồi (/models)")
		}
		if cfg.Spaces.Local.Models.Embed == "" {
			fmt.Fprintln(stdout, "warn: policy local thiếu [spaces.local.models].embed")
		}
		if cfg.Spaces.Local.Models.Extract == "" {
			fmt.Fprintln(stdout, "warn: policy local thiếu [spaces.local.models].extract")
		}
	}
	if cfg.Gateway.BaseURL == "" {
		fmt.Fprintln(stdout, "warn: [gateway].base_url trống — embed/rerank/extract/omni qua gateway sẽ lỗi"+
			" (điền trong config.toml, `setup --gateway-url`, hoặc ô Gateway URL của extension Claude Desktop)")
	}
	if cfg.Gateway.APIKey == "" {
		fmt.Fprintln(stdout, "info: shell này không có MIND_RUNNER_GATEWAY_API_KEY — bình thường: key nằm trong"+
			" setting MCP của client (Claude Code/Desktop); job cloud do MCP server xử lý, launchd chỉ dọn dẹp")
	}
	if cfg.Gateway.Models.Extract == "" {
		fmt.Fprintln(stdout, "warn: [gateway.models].extract trống — extract_session sẽ lỗi khi chạy")
	}
	if cfg.Gateway.Models.Omni == "" {
		fmt.Fprintln(stdout, "warn: [gateway.models].omni trống — media/omni sẽ lỗi khi chạy")
	}
}

// probeModels: GET {base}/models, timeout 2s.
func probeModels(base string) error {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(strings.TrimSuffix(base, "/") + "/models")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("GET /models → %s", resp.Status)
	}
	return nil
}

func checkWritableDir(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("không phải thư mục")
	}
	f, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		return fmt.Errorf("không ghi được: %w", err)
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// checkMediaFiles warn từng path non-empty mà file không còn trong media/
// (6.5: path=” là gốc đã xoá chủ đích khi keep_originals=false — bình thường).
// Warn-only, không đụng exit code.
func checkMediaFiles(dbPath, mediaDir string, stdout io.Writer) {
	st, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(stdout, "warn: media: %v\n", err)
		return
	}
	defer st.Close()
	missing, err := st.MediaMissingPaths(context.Background(), mediaDir)
	if err != nil {
		fmt.Fprintf(stdout, "warn: media: %v\n", err)
		return
	}
	if len(missing) == 0 {
		fmt.Fprintln(stdout, "ok: media files")
		return
	}
	for _, p := range missing {
		fmt.Fprintf(stdout, "warn: media file thiếu: %s\n", p)
	}
}

func checkDB(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	st, err := store.Open(path)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	var qc string
	if err := st.DB().QueryRowContext(ctx, "PRAGMA quick_check").Scan(&qc); err != nil {
		return err
	}
	if qc != "ok" {
		return fmt.Errorf("quick_check: %s", qc)
	}
	v, err := st.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if v != store.LatestSchema {
		return fmt.Errorf("schema version %d != %d (chạy setup để migrate)", v, store.LatestSchema)
	}
	return nil
}
