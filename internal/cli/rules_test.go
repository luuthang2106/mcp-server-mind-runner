package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRulesInstallIdempotentUninstall(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	agents := filepath.Join(home, ".codex", "AGENTS.md")
	os.WriteFile(agents, []byte("# my rules\n"), 0o644)
	os.MkdirAll(filepath.Join(home, ".config", "opencode"), 0o755) // agent có thư mục, chưa có file
	env := func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	}
	run := func(args ...string) string {
		if RunRules(args, io.Discard, io.Discard, env) != 0 {
			t.Fatal("RunRules failed")
		}
		b, _ := os.ReadFile(agents)
		return string(b)
	}
	once := run()
	if !strings.HasPrefix(once, "# my rules\n\n"+rulesStart) || !strings.Contains(once, "recall") {
		t.Fatalf("install: %q", once)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".config", "opencode", "AGENTS.md")); string(b) != rulesBlock() {
		t.Fatalf("new file: %q", b)
	}
	if twice := run(); twice != once {
		t.Fatalf("not idempotent:\n%s", twice)
	}
	if got := run("--uninstall"); got != "# my rules\n" {
		t.Fatalf("uninstall: %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini")); !os.IsNotExist(err) {
		t.Fatal("must not create dirs of agents that are not installed")
	}
}
