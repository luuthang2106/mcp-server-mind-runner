package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mind-runner/internal/config"
	"mind-runner/internal/store"
)

func TestSetupNonInteractive(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cfgPath := filepath.Join(tmp, "cfg", "config.toml")
	t.Setenv("MIND_RUNNER_CONFIG", cfgPath)
	dataDir := filepath.Join(tmp, "data")

	var out, errb bytes.Buffer
	code := RunSetup([]string{"--non-interactive", "--gateway-key=k", "--skip-launchd", "--data-dir", dataDir}, &out, &errb, os.Getenv)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}

	fi, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("config mode=%v, muốn 0600", fi.Mode().Perm())
	}
	for _, sub := range []string{"media", "backups", "logs"} {
		if fi, err := os.Stat(filepath.Join(dataDir, sub)); err != nil || !fi.IsDir() {
			t.Fatalf("thiếu %s/ (%v)", sub, err)
		}
	}

	dbPath := filepath.Join(dataDir, "mind-runner.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	v, err := st.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != store.LatestSchema {
		t.Fatalf("version=%d", v)
	}

	// chạy lại → idempotent, in "đã cập nhật config"
	out.Reset()
	errb.Reset()
	code = RunSetup([]string{"--non-interactive", "--gateway-key=k", "--skip-launchd", "--data-dir", dataDir}, &out, &errb, os.Getenv)
	if code != 0 {
		t.Fatalf("exit lần 2=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "đã cập nhật config") {
		t.Fatalf("out=%q", out.String())
	}
}

// TestSetupKeyNeverWrittenToConfig: key (flag hoặc env) không bao giờ vào
// config.toml; thiếu key cũng không làm setup thất bại; api_key cũ bị gỡ.
func TestSetupKeyNeverWrittenToConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cfgPath := filepath.Join(tmp, "cfg", "config.toml")
	t.Setenv("MIND_RUNNER_CONFIG", cfgPath)
	t.Setenv("MIND_RUNNER_GATEWAY_API_KEY", "env-secret")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("[gateway]\napi_key = \"legacy-secret\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	code := RunSetup([]string{"--non-interactive", "--gateway-key=flag-secret", "--skip-launchd",
		"--data-dir", filepath.Join(tmp, "data")}, &out, &errb, os.Getenv)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	data, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(data), "api_key =") {
		t.Fatalf("config còn api_key:\n%s", data)
	}
	for _, secret := range []string{"env-secret", "flag-secret", "legacy-secret"} {
		if strings.Contains(string(data), secret) || strings.Contains(out.String(), secret) {
			t.Fatalf("lộ %q:\nconfig=%s\nout=%s", secret, data, out.String())
		}
	}
	if !strings.Contains(out.String(), "đã gỡ [gateway].api_key") {
		t.Fatalf("out=%s", out.String())
	}

	// không có key → vẫn exit 0
	t.Setenv("MIND_RUNNER_GATEWAY_API_KEY", "")
	out.Reset()
	if code := RunSetup([]string{"--non-interactive", "--skip-launchd", "--data-dir", filepath.Join(tmp, "data")},
		&out, &errb, os.Getenv); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
}

func TestSetupInteractive(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cfgPath := filepath.Join(tmp, "cfg", "config.toml")
	t.Setenv("MIND_RUNNER_CONFIG", cfgPath)

	old := stdin
	stdin = strings.NewReader("\n\n\n\n\n\n")
	t.Cleanup(func() { stdin = old })

	var out, errb bytes.Buffer
	code := RunSetup([]string{"--skip-launchd", "--data-dir", filepath.Join(tmp, "data")}, &out, &errb, os.Getenv)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Gateway.BaseURL != "" {
		t.Fatalf("base_url=%q", cfg.Gateway.BaseURL)
	}
	if cfg.Gateway.Models.Extract != "" || cfg.Gateway.Models.Omni != "" {
		t.Fatalf("models=%+v", cfg.Gateway.Models)
	}
	if cfg.Spaces.Default != "personal" || cfg.Spaces.Policy["work"] != "cloud" {
		t.Fatalf("spaces=%+v", cfg.Spaces)
	}
}

// TestSetupInteractiveLocal: chọn work=local → wizard hỏi endpoint + 2 model,
// ghi đủ [spaces.local] và [spaces.local.models].
func TestSetupInteractiveLocal(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cfgPath := filepath.Join(tmp, "cfg", "config.toml")
	t.Setenv("MIND_RUNNER_CONFIG", cfgPath)

	old := stdin
	// 4 câu đầu nhận default (base URL, extract, omni, space) — không hỏi key
	// khi không có --claude-code; câu 5 = local, 3 câu local nhận default.
	stdin = strings.NewReader("\n\n\n\nlocal\n\n\n")
	t.Cleanup(func() { stdin = old })

	var out, errb bytes.Buffer
	code := RunSetup([]string{"--skip-launchd", "--data-dir", filepath.Join(tmp, "data")}, &out, &errb, os.Getenv)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Spaces.Policy["work"] != "local" {
		t.Fatalf("policy=%+v", cfg.Spaces.Policy)
	}
	if cfg.Spaces.Local.BaseURL != "http://127.0.0.1:11434/v1" {
		t.Fatalf("local base_url=%q", cfg.Spaces.Local.BaseURL)
	}
	if cfg.Spaces.Local.Models.Embed != "nomic-embed-text" || cfg.Spaces.Local.Models.Extract != "qwen3:8b" {
		t.Fatalf("local models=%+v", cfg.Spaces.Local.Models)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[spaces.local]") {
		t.Fatalf("config thiếu [spaces.local]\n%s", data)
	}
}

// TestSetupNonInteractiveLocalFlags: flags --local-* ghi thẳng vào config.
func TestSetupNonInteractiveLocalFlags(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cfgPath := filepath.Join(tmp, "cfg", "config.toml")
	t.Setenv("MIND_RUNNER_CONFIG", cfgPath)

	var out, errb bytes.Buffer
	code := RunSetup([]string{
		"--non-interactive", "--gateway-key=k", "--skip-launchd",
		"--data-dir", filepath.Join(tmp, "data"),
		"--local-url=http://127.0.0.1:9999/v1",
		"--local-embed-model=my-embed",
		"--local-chat-model=my-chat",
	}, &out, &errb, os.Getenv)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Spaces.Local.BaseURL != "http://127.0.0.1:9999/v1" ||
		cfg.Spaces.Local.Models.Embed != "my-embed" ||
		cfg.Spaces.Local.Models.Extract != "my-chat" {
		t.Fatalf("local=%+v", cfg.Spaces.Local)
	}
}

// TestSetupPrintsSnippet: cuối luồng setup in hướng dẫn client (instructions
// tự động, env key) + dòng consent ghi âm.
func TestSetupPrintsSnippet(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("MIND_RUNNER_CONFIG", filepath.Join(tmp, "cfg", "config.toml"))

	var out, errb bytes.Buffer
	code := RunSetup([]string{
		"--non-interactive", "--gateway-key=k", "--skip-launchd",
		"--data-dir", filepath.Join(tmp, "data"),
	}, &out, &errb, os.Getenv)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	s := out.String()
	for _, want := range []string{
		"MCP instructions", "MIND_RUNNER_GATEWAY_API_KEY", ".mcpb", "consent",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("out thiếu %q\nout=%s", want, s)
		}
	}
}
