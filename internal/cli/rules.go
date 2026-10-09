package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mind-runner/internal/server"
)

const (
	rulesStart = "<!-- mind-runner:start -->"
	rulesEnd   = "<!-- mind-runner:end -->"
)

// rulesTargets: file hướng dẫn chung (cấp user) của các agent. Chỉ ghi khi
// thư mục agent đã tồn tại (agent đã cài). Claude Code không có ở đây: nó nhận
// đủ MCP instructions (< 2048 ký tự), ghi thêm CLAUDE.md chỉ tốn token gấp đôi.
var rulesTargets = []struct{ name, agentDir, file string }{
	{"Codex", ".codex", ".codex/AGENTS.md"},
	{"ZCode", ".zcode", ".zcode/AGENTS.md"},
	{"Gemini CLI", ".gemini", ".gemini/GEMINI.md"},
	{"opencode", ".config/opencode", ".config/opencode/AGENTS.md"},
	{"Windsurf", ".codeium/windsurf", ".codeium/windsurf/memories/global_rules.md"},
}

func rulesBlock() string {
	return rulesStart + "\n## mind-runner (MCP memory tools)\n" + server.Instructions + "\n" + rulesEnd + "\n"
}

// upsertRules thay khối mind-runner trong s (hoặc nối vào cuối); remove=true gỡ khối.
func upsertRules(s string, remove bool) string {
	i := strings.Index(s, rulesStart)
	j := strings.Index(s, rulesEnd)
	if i >= 0 && j > i {
		rest := strings.TrimPrefix(s[j+len(rulesEnd):], "\n")
		if remove {
			return strings.TrimRight(s[:i], "\n") + trailNL(s[:i]) + rest
		}
		return s[:i] + rulesBlock() + rest
	}
	if remove {
		return s
	}
	if s != "" && !strings.HasSuffix(s, "\n\n") {
		s = strings.TrimRight(s, "\n") + "\n\n"
	}
	return s + rulesBlock()
}

func trailNL(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return "\n"
}

// RunRules: ghi/gỡ khối quy tắc mind-runner trong file hướng dẫn chung của agent.
func RunRules(args []string, stdout, stderr io.Writer, env func(string) string) int {
	fs := flag.NewFlagSet("rules", flag.ContinueOnError)
	fs.SetOutput(stderr)
	uninstall := fs.Bool("uninstall", false, "gỡ khối mind-runner")
	printOnly := fs.Bool("print", false, "chỉ in khối (để dán vào Cursor/Qoder…)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *printOnly {
		fmt.Fprint(stdout, rulesBlock())
		return 0
	}
	home := env("HOME")
	code := 0
	for _, t := range rulesTargets {
		if st, err := os.Stat(filepath.Join(home, t.agentDir)); err != nil || !st.IsDir() {
			continue
		}
		path := filepath.Join(home, t.file)
		old, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(stderr, "lỗi: %s: %v\n", path, err)
			code = 1
			continue
		}
		if os.IsNotExist(err) && *uninstall {
			continue
		}
		next := upsertRules(string(old), *uninstall)
		if next == string(old) {
			fmt.Fprintf(stdout, "ok: %s (%s) không đổi\n", t.name, path)
			continue
		}
		err = os.MkdirAll(filepath.Dir(path), 0o755)
		if err == nil {
			err = os.WriteFile(path, []byte(next), 0o644)
		}
		if err != nil {
			fmt.Fprintf(stderr, "lỗi: %s: %v\n", path, err)
			code = 1
			continue
		}
		fmt.Fprintf(stdout, "ok: %s → %s\n", t.name, path)
	}
	if !*uninstall {
		fmt.Fprintln(stdout, "Cursor/Qoder/agent khác: `mind-runner rules --print` rồi dán vào phần rules chung của app.")
	}
	return code
}
