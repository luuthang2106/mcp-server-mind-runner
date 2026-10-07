package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstalledBinary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	noEnv := func(string) string { return "" }

	if got := installedBinary(noEnv); got != "" {
		t.Fatalf("chưa cài mà trả %q", got)
	}
	local := filepath.Join(home, ".local", "bin", "mind-runner")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := installedBinary(noEnv); got != local {
		t.Fatalf("fallback = %q", got)
	}
	reg := filepath.Join(home, "mind-runner", "mind-runner")
	cj := `{"mcpServers":{"mind-runner":{"type":"stdio","command":"` + reg + `","args":["mcp"]}}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(cj), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := installedBinary(noEnv); got != reg {
		t.Fatalf("claude.json = %q", got)
	}
	over := func(k string) string {
		if k == "MIND_RUNNER_INSTALLED_BIN" {
			return "/x/y"
		}
		return ""
	}
	if got := installedBinary(over); got != "/x/y" {
		t.Fatalf("override = %q", got)
	}
}
