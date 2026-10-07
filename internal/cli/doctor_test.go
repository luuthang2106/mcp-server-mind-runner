package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mind-runner/internal/config"
	"mind-runner/internal/store"
)

// setupFresh chạy setup non-interactive vào HOME tạm, trả về (cfgPath, dataDir).
func setupFresh(t *testing.T) (string, string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cfgPath := filepath.Join(tmp, "cfg", "config.toml")
	t.Setenv("MIND_RUNNER_CONFIG", cfgPath)
	// key do MCP client cấp qua env (không nằm trong config.toml)
	t.Setenv("MIND_RUNNER_GATEWAY_API_KEY", "k")
	dataDir := filepath.Join(tmp, "data")
	var out, errb bytes.Buffer
	if code := RunSetup([]string{
		"--non-interactive", "--gateway-key=k", "--data-dir", dataDir, "--skip-launchd",
	}, &out, &errb, os.Getenv); code != 0 {
		t.Fatalf("setup exit=%d stderr=%s", code, errb.String())
	}
	return cfgPath, dataDir
}

func TestDoctorHealthyThenFailures(t *testing.T) {
	cfgPath, dataDir := setupFresh(t)

	var out, errb bytes.Buffer
	code := RunDoctor(nil, &out, &errb, os.Getenv)
	if code != 0 {
		t.Fatalf("doctor exit=%d\nstderr=%s\nout=%s", code, errb.String(), out.String())
	}
	if !strings.Contains(out.String(), "ok: db") {
		t.Fatalf("out=%q", out.String())
	}

	// phá perms → warn, vẫn exit 0
	if err := os.Chmod(cfgPath, 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	code = RunDoctor(nil, &out, &errb, os.Getenv)
	if code != 0 {
		t.Fatalf("perms warn phải exit 0, code=%d", code)
	}
	if !strings.Contains(out.String(), "warn: config perms") {
		t.Fatalf("out=%q", out.String())
	}

	// xoá db → fail exit 1
	if err := os.Remove(filepath.Join(dataDir, "mind-runner.db")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	code = RunDoctor(nil, &out, &errb, os.Getenv)
	if code != 1 {
		t.Fatalf("missing db phải exit 1, code=%d out=%s", code, out.String())
	}
	if !strings.Contains(out.String(), "fail: db") {
		t.Fatalf("out=%q", out.String())
	}
}

// TestDoctorLocalPolicyWarnings: policy local chỉ warn, không fail — thiếu
// base_url, endpoint chết, thiếu model; endpoint sống + đủ model → ok.
func TestDoctorLocalPolicyWarnings(t *testing.T) {
	cfgPath, _ := setupFresh(t)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Spaces.Policy = map[string]string{"work": "local"}
	if err := writeConfig(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	// thiếu base_url + thiếu model hai bên
	var out, errb bytes.Buffer
	code := RunDoctor(nil, &out, &errb, os.Getenv)
	if code != 0 {
		t.Fatalf("warn-only phải exit 0, code=%d out=%s", code, out.String())
	}
	s := out.String()
	for _, want := range []string{
		"policy local nhưng [spaces.local].base_url chưa cấu hình",
		"[spaces.local.models].embed",
		"[spaces.local.models].extract",
		"[gateway.models].extract",
		"[gateway.models].omni",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("out thiếu %q\nout=%s", want, s)
		}
	}

	// endpoint chết → warn không phản hồi
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close()
	cfg.Spaces.Local.BaseURL = dead.URL
	if err := writeConfig(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	code = RunDoctor(nil, &out, &errb, os.Getenv)
	if code != 0 || !strings.Contains(out.String(), "local endpoint không phản hồi") {
		t.Fatalf("code=%d out=%s", code, out.String())
	}

	// endpoint sống + đủ model local → ok, hết warn local
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer live.Close()
	cfg.Spaces.Local.BaseURL = live.URL
	cfg.Spaces.Local.Models = config.Models{Embed: "nomic-embed-text", Extract: "qwen3:8b"}
	if err := writeConfig(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	code = RunDoctor(nil, &out, &errb, os.Getenv)
	if code != 0 {
		t.Fatalf("code=%d out=%s", code, out.String())
	}
	s = out.String()
	if !strings.Contains(s, "ok: local endpoint phản hồi") {
		t.Fatalf("thiếu ok endpoint\nout=%s", s)
	}
	for _, noWant := range []string{"không phản hồi", "[spaces.local.models].embed", "[spaces.local.models].extract"} {
		if strings.Contains(s, noWant) {
			t.Fatalf("còn %q\nout=%s", noWant, s)
		}
	}
}

// TestDoctorWarnsMissingMediaFile (6.5): row path non-empty mà file mất → warn
// đúng path; path=” (đã xoá chủ đích) không warn; warn-only nên exit 0.
func TestDoctorWarnsMissingMediaFile(t *testing.T) {
	_, dataDir := setupFresh(t)
	st, err := store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := "2026-10-06T10:00:00Z"
	mustWrite(t, filepath.Join(dataDir, "media", "aa", "có.m4a"), 10)
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO media(space_id, sha256, kind, path, bytes, status, source, created_at, updated_at) VALUES
		 (1,'h1','audio','aa/có.m4a',10,'done','cli','`+now+`','`+now+`'),
		 (1,'h2','audio','aa/thiếu.m4a',10,'done','cli','`+now+`','`+now+`'),
		 (1,'h3','audio','',10,'done','cli','`+now+`','`+now+`')`); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	code := RunDoctor(nil, &out, &errb, os.Getenv)
	if code != 0 {
		t.Fatalf("warn-only phải exit 0, code=%d out=%s", code, out.String())
	}
	s := out.String()
	if !strings.Contains(s, "warn: media file thiếu: aa/thiếu.m4a") {
		t.Fatalf("thiếu warn file mất\nout=%s", s)
	}
	if got := strings.Count(s, "warn: media file thiếu"); got != 1 {
		t.Fatalf("có %d warn, muốn đúng 1 (path='' và file còn không được warn)\nout=%s", got, s)
	}
	if strings.Contains(s, "ok: media files") {
		t.Fatalf("còn file thiếu mà vẫn in ok\nout=%s", s)
	}

	// tạo lại file thiếu → hết warn, in ok
	mustWrite(t, filepath.Join(dataDir, "media", "aa", "thiếu.m4a"), 10)
	out.Reset()
	if code = RunDoctor(nil, &out, &errb, os.Getenv); code != 0 {
		t.Fatalf("code=%d out=%s", code, out.String())
	}
	if s = out.String(); !strings.Contains(s, "ok: media files") || strings.Contains(s, "warn: media") {
		t.Fatalf("muốn ok: media files, không warn\nout=%s", s)
	}
}
