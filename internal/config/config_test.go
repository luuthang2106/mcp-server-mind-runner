package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleTOML = `data_dir = "~/Library/Application Support/mind-runner"
log_level = "debug"

[gateway]
base_url = "https://gateway.example/v1"
[gateway.models]
extract = "qwen3-extract"
embed   = "text-embedding-v4"
rerank  = "qwen3-rerank"
omni    = "qwen-omni"

[spaces]
default = "personal"
[spaces.policy]
work = "local"
[spaces.local]
base_url = "http://127.0.0.1:11434/v1"
[spaces.local.models]
embed   = "nomic-embed-text"
extract = "qwen3:8b"
[spaces.match]
rules = [
  { glob = "~/work/**",      space = "work" },
  { glob = "~/Projects/**",  space = "personal" },
]

[retention]
events_days = 365
raw_days    = 90
jobs_days   = 30
[briefing]
token_budget = 1500
[media]
keep_originals = true
watch_dirs = []
[backup]
keep = 7
`

func writeFile(t *testing.T, dir, name, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMissingFileGivesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "none.toml"))
	if err != nil {
		t.Fatal(err)
	}
	def := Default()
	if cfg.DataDir != def.DataDir || cfg.LogLevel != "info" {
		t.Fatalf("DataDir=%q LogLevel=%q", cfg.DataDir, cfg.LogLevel)
	}
	if cfg.Retention != (Retention{EventsDays: 365, RawDays: 90, JobsDays: 30}) {
		t.Fatalf("Retention=%+v", cfg.Retention)
	}
	if cfg.Gateway.Models.Embed != "text-embedding-v4" || cfg.Gateway.Models.Rerank != "qwen3-rerank" {
		t.Fatalf("Models=%+v", cfg.Gateway.Models)
	}
	if cfg.Spaces.Local.BaseURL != "" || cfg.Spaces.Local.Models != (Models{}) {
		t.Fatalf("Local=%+v, mặc định phải zero (policy local chỉ chạy khi cấu hình)", cfg.Spaces.Local)
	}
	if !cfg.Media.KeepOriginals || cfg.Backup.Keep != 7 || cfg.Briefing.TokenBudget != 1500 {
		t.Fatalf("Media=%+v Backup=%+v Briefing=%+v", cfg.Media, cfg.Backup, cfg.Briefing)
	}
	if w := strings.Join(cfg.Warnings(), "\n"); !strings.Contains(w, "chưa tồn tại") {
		t.Fatalf("warnings=%q", w)
	}
}

func TestFullTOMLParses(t *testing.T) {
	p := writeFile(t, t.TempDir(), "config.toml", sampleTOML, 0o600)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "~/Library/Application Support/mind-runner" || cfg.LogLevel != "debug" {
		t.Fatalf("DataDir=%q LogLevel=%q", cfg.DataDir, cfg.LogLevel)
	}
	if cfg.Gateway.BaseURL != "https://gateway.example/v1" || cfg.Gateway.APIKey != "" {
		t.Fatalf("Gateway=%+v", cfg.Gateway)
	}
	if cfg.Gateway.Models.Extract != "qwen3-extract" || cfg.Gateway.Models.Omni != "qwen-omni" {
		t.Fatalf("Models=%+v", cfg.Gateway.Models)
	}
	if cfg.Spaces.Default != "personal" || cfg.Spaces.Local.BaseURL != "http://127.0.0.1:11434/v1" {
		t.Fatalf("Spaces=%+v", cfg.Spaces)
	}
	if cfg.Spaces.Local.Models.Embed != "nomic-embed-text" || cfg.Spaces.Local.Models.Extract != "qwen3:8b" {
		t.Fatalf("Local.Models=%+v", cfg.Spaces.Local.Models)
	}
	if len(cfg.Spaces.Match.Rules) != 2 || cfg.Spaces.Match.Rules[0].Glob != "~/work/**" || cfg.Spaces.Match.Rules[0].Space != "work" {
		t.Fatalf("Rules=%+v", cfg.Spaces.Match.Rules)
	}
	if got := cfg.SpacePolicy("work"); got != "local" {
		t.Fatalf("work policy=%q", got)
	}
	if got := cfg.SpacePolicy("personal"); got != "cloud" {
		t.Fatalf("personal policy=%q", got)
	}
	if got := cfg.SpacePolicy("unknown"); got != "cloud" {
		t.Fatalf("unknown policy=%q", got)
	}
	if len(cfg.Warnings()) != 0 {
		t.Fatalf("warnings=%q", cfg.Warnings())
	}
}

func TestEnvOverridesFile(t *testing.T) {
	p := writeFile(t, t.TempDir(), "config.toml", sampleTOML, 0o600)
	t.Setenv("MIND_RUNNER_GATEWAY_BASE_URL", "http://x")
	t.Setenv("MIND_RUNNER_MODEL_EXTRACT", "env-extract")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Gateway.BaseURL != "http://x" {
		t.Fatalf("BaseURL=%q", cfg.Gateway.BaseURL)
	}
	if cfg.Gateway.Models.Extract != "env-extract" {
		t.Fatalf("Extract=%q", cfg.Gateway.Models.Extract)
	}
	if cfg.Gateway.APIKey != "" {
		t.Fatalf("APIKey=%q, không set env thì phải rỗng", cfg.Gateway.APIKey)
	}
}

func TestLoosePermsWarns(t *testing.T) {
	p := writeFile(t, t.TempDir(), "config.toml", sampleTOML, 0o644)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if w := strings.Join(cfg.Warnings(), "\n"); !strings.Contains(w, "0600") {
		t.Fatalf("warnings=%q", w)
	}
}

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := ExpandHome("~/x/y"); got != filepath.Join(home, "x/y") {
		t.Fatalf("got %q", got)
	}
	if got := ExpandHome("/abs/path"); got != "/abs/path" {
		t.Fatalf("got %q", got)
	}
}

func TestAPIKeyOnlyFromEnv(t *testing.T) {
	p := writeFile(t, t.TempDir(), "config.toml", "[gateway]\nbase_url = \"http://x\"\napi_key = \"sk-file\"\n", 0o600)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Gateway.APIKey != "" {
		t.Fatalf("key trong file phải bị bỏ qua, có %q", cfg.Gateway.APIKey)
	}
	if !cfg.HasLegacyAPIKey() || !strings.Contains(strings.Join(cfg.Warnings(), "\n"), "api_key") {
		t.Fatalf("thiếu cảnh báo legacy: %q", cfg.Warnings())
	}
	t.Setenv("MIND_RUNNER_GATEWAY_API_KEY", "sk-env")
	if cfg, _ = Load(p); cfg.Gateway.APIKey != "sk-env" {
		t.Fatalf("APIKey=%q", cfg.Gateway.APIKey)
	}
	if f, _ := LoadFile(p); f.Gateway.APIKey != "" {
		t.Fatal("LoadFile không được áp env")
	}
}

func TestGlobMatchDoubleStar(t *testing.T) {
	cases := []struct {
		pat, name string
		want      bool
	}{
		{"/u/work/**", "/u/work", true},
		{"/u/work/**", "/u/work/repo", true},
		{"/u/work/**", "/u/work/repo/sub/deep", true},
		{"/u/work/**", "/u/workshop", false},
		{"/u/*/notes", "/u/a/notes", true},
		{"/u/*/notes", "/u/a/b/notes", false},
		{"/u/**/notes", "/u/a/b/notes", true},
		{"/u/work/*", "/u/work/a/b", false},
	}
	for _, c := range cases {
		if got := GlobMatch(c.pat, c.name); got != c.want {
			t.Errorf("GlobMatch(%q,%q)=%v", c.pat, c.name, got)
		}
	}
}

func TestDayStart(t *testing.T) {
	c := Default()
	loc := time.FixedZone("ICT", 7*3600)
	if got := c.DayStart(time.Date(2026, 10, 7, 2, 0, 0, 0, loc)); got.Day() != 6 || got.Hour() != 4 {
		t.Fatalf("2h sáng phải thuộc ngày hôm trước: %v", got)
	}
	if got := c.DayStart(time.Date(2026, 10, 7, 9, 0, 0, 0, loc)); got.Day() != 7 || got.Hour() != 4 {
		t.Fatalf("9h: %v", got)
	}
}

func TestEnvPlaceholderIgnored(t *testing.T) {
	t.Setenv("MIND_RUNNER_GATEWAY_BASE_URL", "${user_config.base_url}")
	t.Setenv("MIND_RUNNER_MODEL_EXTRACT", "  m-x  ")
	c := Default()
	c.Gateway.BaseURL = "https://from-file/v1"
	ApplyEnv(&c)
	if c.Gateway.BaseURL != "https://from-file/v1" {
		t.Fatalf("placeholder đè base_url: %q", c.Gateway.BaseURL)
	}
	if c.Gateway.Models.Extract != "m-x" {
		t.Fatalf("extract=%q", c.Gateway.Models.Extract)
	}
}
