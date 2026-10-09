package config

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	DataDir     string      `toml:"data_dir"`
	LogLevel    string      `toml:"log_level"`
	Gateway     Gateway     `toml:"gateway"`
	Spaces      Spaces      `toml:"spaces"`
	Retention   Retention   `toml:"retention"`
	Briefing    Briefing    `toml:"briefing"`
	Recall      Recall      `toml:"recall"`
	Media       Media       `toml:"media"`
	Backup      Backup      `toml:"backup"`
	Consolidate Consolidate `toml:"consolidate"`

	path string
	// legacyAPIKey: config.toml còn [gateway].api_key — bị BỎ QUA (key chỉ lấy
	// từ env do MCP client cấp); Warnings nhắc dọn.
	legacyAPIKey bool
	legacyKeyVal string
}

type Gateway struct {
	BaseURL string `toml:"base_url"`
	// APIKey KHÔNG đọc/ghi từ config.toml: chỉ lấy từ env
	// MIND_RUNNER_GATEWAY_API_KEY — do MCP client cấp (Claude Code:
	// `claude mcp add -e …`; Claude Desktop: trường api_key của extension .mcpb).
	// Key không bao giờ nằm trên đĩa dưới dạng file config của mind-runner.
	APIKey string `toml:"-"`
	Models Models `toml:"models"`
}

type Models struct {
	Extract string `toml:"extract"`
	Embed   string `toml:"embed"`
	Rerank  string `toml:"rerank"`
	Omni    string `toml:"omni"`
}

type Spaces struct {
	Default string            `toml:"default"`
	Policy  map[string]string `toml:"policy"`
	Local   LocalEP           `toml:"local"`
	Match   MatchRules        `toml:"match"`
}

type LocalEP struct {
	BaseURL string `toml:"base_url"`
	// Models bắt buộc khi policy local — KHÔNG fallback sang [gateway.models]:
	// endpoint local (Ollama) có tên model riêng; thiếu key nào → lỗi rõ
	// "<policy> thiếu model <x>".
	Models Models `toml:"models"`
}

type MatchRules struct {
	Rules []MatchRule `toml:"rules"`
}

type MatchRule struct {
	Glob  string `toml:"glob"`
	Space string `toml:"space"`
}

type Retention struct {
	EventsDays int `toml:"events_days"`
	RawDays    int `toml:"raw_days"`
	JobsDays   int `toml:"jobs_days"`
}

type Briefing struct {
	TokenBudget int `toml:"token_budget"`
	// DayStartHour: giờ địa phương bắt đầu "ngày mới" cho recap đầu ngày
	// (mặc định 4 — làm việc qua nửa đêm vẫn tính là hôm trước).
	DayStartHour int `toml:"day_start_hour"`
}

// Recall: chống nhiễu kết quả recall.
type Recall struct {
	// Limit: số kết quả mặc định khi tool không truyền limit (mặc định 5).
	Limit int `toml:"limit"`
	// MinScore: ngưỡng điểm rerank (0..1) — kết quả thấp hơn bị bỏ. Chỉ áp khi
	// rerank chạy (điểm RRF không hiệu chuẩn). 0 = tắt. Mặc định 0.2.
	MinScore float64 `toml:"min_score"`
}

type Media struct {
	KeepOriginals bool     `toml:"keep_originals"`
	WatchDirs     []string `toml:"watch_dirs"`
}

type Backup struct {
	Keep int `toml:"keep"`
}

// Consolidate: job LLM định kỳ gộp note kiến thức trùng (decision/fact/
// preference/procedure). EveryDays <= 0 = tắt.
type Consolidate struct {
	EveryDays int `toml:"every_days"`
}

// DefaultConfigPath trả về đường dẫn config: env MIND_RUNNER_CONFIG nếu có,
// ngược lại ~/.config/mind-runner/config.toml.
func DefaultConfigPath() string {
	if p := os.Getenv("MIND_RUNNER_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(ExpandHome("~"), ".config", "mind-runner", "config.toml")
}

// ExpandHome đổi "~" hoặc "~/..." thành $HOME.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}

func Default() Config {
	return Config{
		DataDir:  "~/Library/Application Support/mind-runner",
		LogLevel: "info",
		Gateway: Gateway{
			Models: Models{Embed: "text-embedding-v4", Rerank: "qwen3-rerank"},
		},
		Spaces:      Spaces{Default: "personal"},
		Retention:   Retention{EventsDays: 365, RawDays: 90, JobsDays: 30},
		Briefing:    Briefing{TokenBudget: 1500, DayStartHour: 4},
		Recall:      Recall{Limit: 5, MinScore: 0.2},
		Media:       Media{KeepOriginals: true, WatchDirs: []string{}},
		Backup:      Backup{Keep: 7},
		Consolidate: Consolidate{EveryDays: 7},
		path:        DefaultConfigPath(),
	}
}

// Load đọc config từ path (rỗng → DefaultPath; file không tồn tại → defaults),
// parse TOML đè lên defaults, rồi áp env override MIND_RUNNER_*.
func Load(path string) (Config, error) {
	c, err := LoadFile(path)
	if err != nil {
		return c, err
	}
	ApplyEnv(&c)
	return c, nil
}

// LoadFile như Load nhưng KHÔNG áp env — dùng khi cần ghi lại file (setup)
// để env override (và API key) không bị lưu xuống đĩa.
func LoadFile(path string) (Config, error) {
	c := Default()
	if path != "" {
		c.path = path
	}
	data, err := os.ReadFile(c.path)
	if err != nil {
		if !os.IsNotExist(err) {
			return c, fmt.Errorf("đọc config: %w", err)
		}
		return c, nil
	}
	md, err := toml.Decode(string(data), &c)
	if err != nil {
		return c, fmt.Errorf("parse config %s: %w", c.path, err)
	}
	c.legacyAPIKey = md.IsDefined("gateway", "api_key")
	if c.legacyAPIKey {
		var legacy struct {
			Gateway struct {
				APIKey string `toml:"api_key"`
			} `toml:"gateway"`
		}
		if _, err := toml.Decode(string(data), &legacy); err == nil {
			c.legacyKeyVal = strings.TrimSpace(legacy.Gateway.APIKey)
		}
	}
	if c.Briefing.DayStartHour < 0 || c.Briefing.DayStartHour > 23 {
		c.Briefing.DayStartHour = 4
	}
	if c.Recall.Limit <= 0 || c.Recall.Limit > 50 {
		c.Recall.Limit = 5
	}
	if c.Recall.MinScore < 0 || c.Recall.MinScore >= 1 {
		c.Recall.MinScore = 0.2
	}
	return c, nil
}

// Path trả đường dẫn file config đang dùng.
func (c *Config) Path() string { return c.path }

// HasLegacyAPIKey: file config còn [gateway].api_key (bị bỏ qua).
func (c *Config) HasLegacyAPIKey() bool { return c.legacyAPIKey }

// LegacyAPIKey: giá trị [gateway].api_key cũ — chỉ để setup chuyển key sang
// setting MCP khi nâng cấp, không bao giờ dùng để gọi API.
func (c *Config) LegacyAPIKey() string { return c.legacyKeyVal }

// ApplyEnv áp env override MIND_RUNNER_* lên c.
func ApplyEnv(c *Config) {
	if v := os.Getenv("MIND_RUNNER_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("MIND_RUNNER_LOG_LEVEL"); v != "" {
		c.LogLevel = v
	}
	if v := envVal("MIND_RUNNER_GATEWAY_BASE_URL"); v != "" {
		c.Gateway.BaseURL = v
	}
	if v := envVal("MIND_RUNNER_GATEWAY_API_KEY"); v != "" {
		c.Gateway.APIKey = v
	}
	if v := envVal("MIND_RUNNER_MODEL_EXTRACT"); v != "" {
		c.Gateway.Models.Extract = v
	}
	if v := envVal("MIND_RUNNER_MODEL_EMBED"); v != "" {
		c.Gateway.Models.Embed = v
	}
	if v := envVal("MIND_RUNNER_MODEL_RERANK"); v != "" {
		c.Gateway.Models.Rerank = v
	}
	if v := envVal("MIND_RUNNER_MODEL_OMNI"); v != "" {
		c.Gateway.Models.Omni = v
	}
}

// Warnings trả cảnh báo về file config: thiếu file, perms lỏng.
func (c *Config) Warnings() []string {
	var w []string
	fi, err := os.Stat(c.path)
	if err != nil {
		return append(w, fmt.Sprintf("config chưa tồn tại (%s) — chạy setup", c.path))
	}
	if fi.Mode().Perm()&0o077 != 0 {
		w = append(w, fmt.Sprintf("config perms lỏng (%04o): nên 0600", fi.Mode().Perm()))
	}
	if c.legacyAPIKey {
		w = append(w, "config.toml còn [gateway].api_key — giá trị này bị BỎ QUA; đặt key trong cấu hình MCP client "+
			"(env MIND_RUNNER_GATEWAY_API_KEY) rồi xoá dòng api_key (chạy lại `mind-runner setup` để tự dọn)")
	}
	return w
}

// DayStart trả mốc bắt đầu "ngày làm việc" chứa t (giờ địa phương của t):
// trước DayStartHour thì vẫn thuộc ngày hôm trước.
func (c *Config) DayStart(t time.Time) time.Time {
	h := c.Briefing.DayStartHour
	d := time.Date(t.Year(), t.Month(), t.Day(), h, 0, 0, 0, t.Location())
	if t.Before(d) {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

// SpacePolicy trả policy của space; không có trong map → "cloud".
func (c *Config) SpacePolicy(name string) string {
	if p, ok := c.Spaces.Policy[name]; ok {
		return p
	}
	return "cloud"
}

// MatchSpace: first match của [spaces.match] (glob sau ExpandHome; "**" khớp
// 0..n thư mục, các đoạn khác theo path.Match); không khớp → spaces.default
// (rỗng → personal). Dùng chung hook (cwd) và watch dirs (đường dẫn file).
func (c *Config) MatchSpace(p string) string {
	p = filepath.Clean(ExpandHome(p))
	for _, r := range c.Spaces.Match.Rules {
		if GlobMatch(filepath.Clean(ExpandHome(r.Glob)), p) {
			return r.Space
		}
	}
	if c.Spaces.Default != "" {
		return c.Spaces.Default
	}
	return "personal"
}

// GlobMatch khớp đường dẫn tuyệt đối theo từng đoạn '/': "**" khớp 0..n đoạn
// (vd "~/work/**" khớp ~/work, ~/work/a, ~/work/a/b/c); đoạn khác dùng
// path.Match (*, ?, [..] không vượt '/'). Pattern lỗi → false.
func GlobMatch(pattern, name string) bool {
	return globSegs(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func globSegs(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			rest := pat[1:]
			for i := 0; i <= len(name); i++ {
				if globSegs(rest, name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], name[0]); err != nil || !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

// envVal đọc biến môi trường cấu hình gateway, bỏ khoảng trắng và bỏ qua
// placeholder chưa được thay (vd "${user_config.base_url}" khi người dùng để
// trống ô cấu hình extension Claude Desktop).
func envVal(k string) string {
	v := strings.TrimSpace(os.Getenv(k))
	if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") {
		return ""
	}
	return v
}
