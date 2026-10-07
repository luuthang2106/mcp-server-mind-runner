package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mind-runner/internal/brain"
	"mind-runner/internal/config"
	"mind-runner/internal/egress"
	"mind-runner/internal/execx"
	"mind-runner/internal/logging"
	"mind-runner/internal/media"
	"mind-runner/internal/project"
	"mind-runner/internal/selfsync"
	"mind-runner/internal/server"
	"mind-runner/internal/store"
	"mind-runner/internal/version"
	"mind-runner/internal/worker"
)

// sweepOnce chạy tối đa 5 job tới hạn (registry dùng chung với maintenance).
func sweepOnce(ctx context.Context, st *store.Store, d worker.Deps) (int, error) {
	reg := worker.NewRegistry()
	worker.RegisterAll(reg, d)
	return worker.Run(ctx, st, reg, 5, time.Now)
}

// RunMCP chạy MCP server qua stdio. stdout là kênh JSON-RPC — tuyệt đối
// không ghi gì khác ra stdout; log JSON đi logs/mcp.log.
func RunMCP(args []string, stdout, stderr io.Writer, env func(string) string) int {
	cfgPath := env("MIND_RUNNER_CONFIG")
	if cfgPath == "" {
		cfgPath = config.DefaultConfigPath()
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "mcp:", err)
		return 1
	}
	base := config.ExpandHome(cfg.DataDir)

	st, err := openDB(context.Background(), cfg)
	if err != nil {
		fmt.Fprintln(stderr, "mcp:", err)
		return 1
	}
	defer st.Close()

	lg, err := logging.New(base, "mcp", cfg.LogLevel)
	if err != nil {
		fmt.Fprintln(stderr, "mcp:", err)
		return 1
	}
	lg.Info("mcp: bắt đầu", "version", version.String())
	if env("MIND_RUNNER_BUNDLED") == "1" {
		go syncInstalledBinary(lg, env)
	}

	eg := egress.New(cfg, nil)
	attachUsageLog(eg, base)
	defer eg.FlushUsage()
	br := brain.New(st, eg, &cfg)
	br.SetBriefingBudget(cfg.Briefing.TokenBudget)
	// Claude Code chạy MCP server với cwd = thư mục dự án → nhãn project tự
	// động (tên gốc git). Claude Desktop cwd "/" → không project.
	if wd, err := os.Getwd(); err == nil {
		br.SetProject(project.Of(wd))
		lg.Info("mcp: project", "project", br.Project())
	}
	md := media.New(st, br, eg, &cfg, execx.OS{}, base)

	// Sweep cơ hội: chạy ngay 1 lượt rồi lặp 5′ (embedding có trong ngày,
	// không đợi maintenance 3:30); ctx hủy khi server kết thúc.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Process không có key (vd Claude Desktop bật extension trước khi người
	// dùng nhập key) không quét job: nếu quét nó claim job cloud rồi hoãn,
	// làm chậm queue của process có key. Policy local thì vẫn quét.
	if !canRunJobs(cfg) {
		lg.Warn("mcp: không có API key gateway — bỏ qua chạy job nền (job chờ process có key)")
	} else {
		go sweepLoop(ctx, lg, st, eg, worker.Deps{Store: st, Brain: br, Egress: eg, Config: &cfg,
			Execx: execx.OS{}, DataDir: base})
	}

	if err := server.New(br, st, md).Run(ctx, &mcp.StdioTransport{}); err != nil {
		lg.Error("mcp: run", "err", err.Error())
		return 1
	}
	return 0
}

// canRunJobs: có key cloud, hoặc có space dùng policy local (không cần key).
func canRunJobs(cfg config.Config) bool {
	if cfg.Gateway.APIKey != "" {
		return true
	}
	for _, p := range cfg.Spaces.Policy {
		if p == "local" {
			return true
		}
	}
	return false
}

func sweepLoop(ctx context.Context, lg *slog.Logger, st *store.Store, eg *egress.Egress, deps worker.Deps) {
	for {
		if _, err := sweepOnce(ctx, st, deps); err != nil {
			lg.Error("sweep", "err", err.Error())
		}
		eg.FlushUsageIfDue()
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Minute):
		}
	}
}

// syncInstalledBinary: chạy từ .mcpb (Claude Desktop) → cập nhật bản cài
// ~/.local/bin/mind-runner (Claude Code + hook) nếu bản đó cũ hơn. Chạy nền,
// lỗi chỉ ghi log — không bao giờ làm hỏng MCP server.
func syncInstalledBinary(lg *slog.Logger, env func(string) string) {
	self, err := os.Executable()
	if err != nil {
		lg.Warn("selfsync: không xác định được binary", "err", err.Error())
		return
	}
	target := installedBinary(env)
	if target == "" {
		lg.Debug("selfsync: bỏ qua", "reason", "chưa cài cho Claude Code")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := selfsync.Sync(ctx, self, target, version.Version, func(ctx context.Context, p string) (string, error) {
		out, err := exec.CommandContext(ctx, p, "version").Output()
		return string(out), err
	})
	switch {
	case err != nil:
		lg.Warn("selfsync: cập nhật bản cài thất bại", "target", target, "err", err.Error())
	case r.Copied:
		lg.Info("selfsync: đã cập nhật bản cài", "target", target, "from", r.Installed, "to", version.Version)
	default:
		lg.Debug("selfsync: bỏ qua", "reason", r.Reason, "installed", r.Installed)
	}
}

// installedBinary: bản cài cho Claude Code = command đã đăng ký trong
// ~/.claude.json (setup ghi os.Executable() vào đó, hooks/launchd dùng chung);
// không có thì thử ~/.local/bin/mind-runner. MIND_RUNNER_INSTALLED_BIN để test.
func installedBinary(env func(string) string) string {
	if p := env("MIND_RUNNER_INSTALLED_BIN"); p != "" {
		return p
	}
	if p := existingClaudeMCPCommand(config.ExpandHome("~/.claude.json")); p != "" && filepath.IsAbs(p) {
		return p
	}
	p := config.ExpandHome("~/.local/bin/mind-runner")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}
