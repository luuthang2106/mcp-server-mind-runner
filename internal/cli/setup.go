// Package cli chứa các subcommand của mind-runner.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"

	"mind-runner/internal/config"
	"mind-runner/internal/execx"
	"mind-runner/internal/launchd"
)

// stdin tách riêng để test bơm script tương tác.
var stdin io.Reader = os.Stdin

// runner cho các bước shell-out của setup (claude mcp add); test bơm fake.
var runner execx.Runner = execx.OS{}

// RunSetup: config + dirs + DB + migrate. Non-interactive hoặc wizard.
func RunSetup(args []string, stdout, stderr io.Writer, env func(string) string) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		nonInteractive = fs.Bool("non-interactive", false, "")
		claudeCode     = fs.Bool("claude-code", false, "")
		withHooks      = fs.Bool("hooks", false, "")
		dataDir        = fs.String("data-dir", "", "")
		gatewayURL     = fs.String("gateway-url", "", "")
		gatewayKey     = fs.String("gateway-key", "", "")
		extractModel   = fs.String("extract-model", "", "")
		omniModel      = fs.String("omni-model", "", "")
		embedModel     = fs.String("embed-model", "", "")
		rerankModel    = fs.String("rerank-model", "", "")
		localURL       = fs.String("local-url", "", "")
		localEmbed     = fs.String("local-embed-model", "", "")
		localChat      = fs.String("local-chat-model", "", "")
		skipLaunchd    = fs.Bool("skip-launchd", false, "")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfgPath := env("MIND_RUNNER_CONFIG")
	if cfgPath == "" {
		cfgPath = config.DefaultConfigPath()
	}
	_, statErr := os.Stat(cfgPath)
	firstRun := os.IsNotExist(statErr)

	// Đọc file KHÔNG áp env: env override (kể cả API key) chỉ có hiệu lực lúc
	// chạy, không bao giờ bị ghi ngược vào config.toml.
	cfg, err := config.LoadFile(cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "setup:", err)
		return 1
	}
	legacyKey := cfg.HasLegacyAPIKey()

	// API key KHÔNG lưu trong config.toml — chỉ dùng để đăng ký vào setting MCP
	// của client (env MIND_RUNNER_GATEWAY_API_KEY). Nguồn: flag > env > key cũ
	// trong config.toml > key đang đăng ký trong ~/.claude.json — chạy lại setup
	// khi nâng cấp không được làm mất key (setup gỡ rồi đăng ký lại MCP).
	apiKey := *gatewayKey
	if apiKey == "" {
		apiKey = env("MIND_RUNNER_GATEWAY_API_KEY")
	}
	if apiKey == "" {
		apiKey = cfg.LegacyAPIKey()
	}
	home := config.ExpandHome("~")
	if apiKey == "" && *claudeCode {
		apiKey = existingClaudeMCPKey(filepath.Join(home, ".claude.json"))
	}

	// Giá trị flag đè lên giá trị trong file.
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}
	if *gatewayURL != "" {
		cfg.Gateway.BaseURL = *gatewayURL
	}
	if *extractModel != "" {
		cfg.Gateway.Models.Extract = *extractModel
	}
	if *omniModel != "" {
		cfg.Gateway.Models.Omni = *omniModel
	}
	if *embedModel != "" {
		cfg.Gateway.Models.Embed = *embedModel
	}
	if *rerankModel != "" {
		cfg.Gateway.Models.Rerank = *rerankModel
	}
	if *localURL != "" {
		cfg.Spaces.Local.BaseURL = *localURL
	}
	if *localEmbed != "" {
		cfg.Spaces.Local.Models.Embed = *localEmbed
	}
	if *localChat != "" {
		cfg.Spaces.Local.Models.Extract = *localChat
	}

	if !*nonInteractive {
		r := bufio.NewReader(stdin)
		ask := func(prompt, def string) string {
			if def != "" {
				fmt.Fprintf(stdout, "%s [%s]: ", prompt, def)
			} else {
				fmt.Fprintf(stdout, "%s: ", prompt)
			}
			line, _ := r.ReadString('\n')
			if line = strings.TrimSpace(line); line == "" {
				return def
			}
			return line
		}
		cfg.Gateway.BaseURL = ask("Gateway base URL (OpenAI-compatible, ví dụ https://gateway.example.com/v1; enter trống để điền sau)", cfg.Gateway.BaseURL)
		if apiKey == "" && *claudeCode {
			// chỉ hỏi khi có client để đăng ký; không echo lại key ra màn hình.
			apiKey = ask("Gateway API key (lưu vào setting MCP của Claude Code, không lưu config.toml)", "")
		}
		cfg.Gateway.Models.Extract = ask("Model extract (enter trống để điền sau)", cfg.Gateway.Models.Extract)
		cfg.Gateway.Models.Omni = ask("Model omni (enter trống để điền sau)", cfg.Gateway.Models.Omni)
		if cfg.Spaces.Default == "" {
			cfg.Spaces.Default = "personal"
		}
		cfg.Spaces.Default = ask("Space mặc định", cfg.Spaces.Default)
		workPolicy := "cloud"
		if p, ok := cfg.Spaces.Policy["work"]; ok {
			workPolicy = p
		}
		workPolicy = ask(`Policy cho space "work" (cloud/local)`, workPolicy)
		if cfg.Spaces.Policy == nil {
			cfg.Spaces.Policy = map[string]string{}
		}
		cfg.Spaces.Policy["work"] = workPolicy
		if workPolicy == "local" {
			if cfg.Spaces.Local.BaseURL == "" {
				cfg.Spaces.Local.BaseURL = "http://127.0.0.1:11434/v1"
			}
			cfg.Spaces.Local.BaseURL = ask("Local base URL (Ollama)", cfg.Spaces.Local.BaseURL)
			if cfg.Spaces.Local.Models.Embed == "" {
				cfg.Spaces.Local.Models.Embed = "nomic-embed-text"
			}
			cfg.Spaces.Local.Models.Embed = ask("Local embed model", cfg.Spaces.Local.Models.Embed)
			if cfg.Spaces.Local.Models.Extract == "" {
				cfg.Spaces.Local.Models.Extract = "qwen3:8b"
			}
			cfg.Spaces.Local.Models.Extract = ask("Local chat model", cfg.Spaces.Local.Models.Extract)
		}
	}

	if *claudeCode && apiKey == "" {
		fmt.Fprintln(stdout, "lưu ý: chưa có gateway key — MCP server vẫn chạy (recall FTS, briefing, ghi nhớ),"+
			" job cloud sẽ chờ tới khi có key. Thêm sau bằng --gateway-key hoặc sửa env trong setting MCP.")
	}
	if cfg.Gateway.BaseURL == "" {
		fmt.Fprintln(stdout, "lưu ý: chưa có gateway base URL — điền sau vào config")
	}

	base := config.ExpandHome(cfg.DataDir)
	for _, d := range []string{base, filepath.Join(base, "media"), filepath.Join(base, "backups"), filepath.Join(base, "logs")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			fmt.Fprintln(stderr, "setup:", err)
			return 1
		}
	}

	if err := writeConfig(cfgPath, cfg); err != nil {
		fmt.Fprintln(stderr, "setup:", err)
		return 1
	}
	if firstRun {
		fmt.Fprintf(stdout, "đã ghi config %s (0600)\n", cfgPath)
	} else {
		fmt.Fprintf(stdout, "đã cập nhật config %s\n", cfgPath)
	}
	if legacyKey {
		fmt.Fprintln(stdout, "đã gỡ [gateway].api_key khỏi config.toml — key giờ chỉ nằm trong setting MCP của client")
	}

	st, err := openDB(context.Background(), cfg)
	if err != nil {
		fmt.Fprintln(stderr, "setup:", err)
		return 1
	}
	defer st.Close()

	if *claudeCode {
		bin, err := os.Executable()
		if err != nil {
			fmt.Fprintln(stderr, "setup: claude-code:", err)
			return 1
		}
		// Mặc định KHÔNG cài hook: agent tự quyết gọi briefing/remember/recall
		// theo MCP instructions; hook cũ của mind-runner bị gỡ. --hooks = bật
		// capture tự động (recap chèn sẵn + extract transcript).
		cc := &ClaudeCfg{Path: filepath.Join(home, ".claude", "settings.json")}
		if *withHooks {
			if err := cc.MergeHooks(bin); err != nil {
				fmt.Fprintln(stdout, "warn: claude-code: ghi hooks vào settings.json lỗi:", err)
			} else {
				fmt.Fprintln(stdout, "claude-code: đã ghi hooks SessionStart/UserPromptSubmit/Stop/SessionEnd")
			}
		} else if n, err := cc.RemoveHooks(bin); err != nil {
			fmt.Fprintln(stdout, "warn: claude-code: gỡ hooks cũ lỗi:", err)
		} else if n > 0 {
			fmt.Fprintf(stdout, "claude-code: đã gỡ %d hook mind-runner cũ (agent tự gọi tool theo instructions)\n", n)
		}
		if err := registerClaudeMCP(bin, apiKey); err != nil {
			fmt.Fprintf(stdout, "warn: đăng ký MCP lỗi (%v) — chạy tay:\n  claude mcp add-json -s user mind-runner '%s'\n",
				err, mcpServerJSON(bin, "<API_KEY>"))
		} else {
			fmt.Fprintln(stdout, "claude-code: đã đăng ký MCP server (user scope, key nằm trong env của server)")
		}
	}

	switch {
	case *skipLaunchd:
		fmt.Fprintln(stdout, "launchd: bỏ qua (--skip-launchd)")
	case runtime.GOOS != "darwin":
		fmt.Fprintln(stdout, "launchd: bỏ qua (không phải macOS)")
	default:
		bin, err := os.Executable()
		if err != nil {
			fmt.Fprintln(stderr, "setup: launchd:", err)
			return 1
		}
		if err := launchd.Install(context.Background(), runner, bin, home, filepath.Join(base, "logs")); err != nil {
			fmt.Fprintln(stderr, "setup: launchd:", err)
			return 1
		}
		fmt.Fprintf(stdout, "launchd: đã cài maintenance 3:30 hằng ngày (%s)\n", launchd.PlistPath(home))
	}

	fmt.Fprintf(stdout, "xong. Data dir: %s\n", base)
	fmt.Fprintln(stdout, "Tiếp theo: Claude Desktop → kéo file .mcpb (nhập API key trong hộp cấu hình);"+
		" Claude Code → setup --claude-code.")
	fmt.Fprintln(stdout, "Client khác (Qoder, ZCode, Cursor…): thêm server stdio `mind-runner mcp` với env MIND_RUNNER_GATEWAY_API_KEY — xem INSTALL.md.")
	fmt.Fprintln(stdout, "Hướng dẫn dùng tool được server gửi tự động (MCP instructions) — không cần dán custom instructions.")
	fmt.Fprintln(stdout, "lưu ý: khi ghi âm có người khác, chỉ ingest khi họ đã đồng ý (consent).")
	return 0
}

// writeConfig ghi config TOML (atomic, 0600, dir 0700). APIKey có tag
// toml:"-" nên không bao giờ xuất hiện trong file.
func writeConfig(path string, c config.Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	header := []byte("# mind-runner config — API key KHÔNG đặt ở đây: khai báo env\n" +
		"# MIND_RUNNER_GATEWAY_API_KEY trong setting MCP của client.\n\n")
	return writeFileAtomic(path, append(header, data...), 0o600)
}

// mcpServerJSON: cấu hình server stdio cho `claude mcp add-json`.
func mcpServerJSON(bin, apiKey string) string {
	m := map[string]any{"type": "stdio", "command": bin, "args": []string{"mcp"}}
	if apiKey != "" {
		m["env"] = map[string]string{"MIND_RUNNER_GATEWAY_API_KEY": apiKey}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// registerClaudeMCP đăng ký (hoặc đăng ký lại) server ở user scope. Dùng
// add-json thay `mcp add -e` vì -e là variadic, dễ nuốt nhầm tên server.
// Gỡ bản cũ trước (lỗi "không tồn tại" bị bỏ qua) để chạy lại setup idempotent.
func registerClaudeMCP(bin, apiKey string) error {
	ctx := context.Background()
	_, _ = runner.Run(ctx, "claude", "mcp", "remove", "-s", "user", "mind-runner")
	_, err := runner.Run(ctx, "claude", "mcp", "add-json", "-s", "user", "mind-runner", mcpServerJSON(bin, apiKey))
	return err
}

// existingClaudeMCPKey đọc key của server mind-runner đã đăng ký (user scope)
// trong ~/.claude.json; không có/không đọc được → "".
func existingClaudeMCPKey(path string) string {
	return strings.TrimSpace(readClaudeMCP(path).Env["MIND_RUNNER_GATEWAY_API_KEY"])
}

// existingClaudeMCPCommand: đường dẫn binary mà setup đã đăng ký cho Claude
// Code (hooks + launchd dùng cùng đường dẫn này).
func existingClaudeMCPCommand(path string) string {
	return strings.TrimSpace(readClaudeMCP(path).Command)
}

type claudeMCPEntry struct {
	Command string            `json:"command"`
	Env     map[string]string `json:"env"`
}

func readClaudeMCP(path string) claudeMCPEntry {
	data, err := os.ReadFile(path)
	if err != nil {
		return claudeMCPEntry{}
	}
	var doc struct {
		MCPServers map[string]claudeMCPEntry `json:"mcpServers"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return claudeMCPEntry{}
	}
	return doc.MCPServers["mind-runner"]
}
