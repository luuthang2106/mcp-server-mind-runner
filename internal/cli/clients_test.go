package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readJSONMap(t *testing.T, p string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestSetupQoderKeepsOthers: giữ hook/provider có sẵn, idempotent, giữ key cũ
// khi chạy lại không có key, đổi binary thì thay hook cũ.
func TestSetupQoderKeepsOthers(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	orig := `{"providers":{"x":{"apiKey":"secret"}},"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/h/karpathy.sh","timeout":5}]}]},"mcpServers":{"mind-runner":{"command":"/old/mind-runner","args":["mcp"]}}}`
	if err := os.WriteFile(p, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetupQoder(p, "/bin/a/mind-runner", "k1"); err != nil {
		t.Fatal(err)
	}
	if err := SetupQoder(p, "/bin/b/mind-runner", ""); err != nil {
		t.Fatal(err)
	}
	m := readJSONMap(t, p)
	if existingKeyIn(p, "mcpServers") != "k1" {
		t.Fatalf("key lost: %v", m["mcpServers"])
	}
	srv := m["mcpServers"].(map[string]any)["mind-runner"].(map[string]any)
	if srv["command"] != "/bin/b/mind-runner" {
		t.Fatalf("command=%v", srv["command"])
	}
	if m["providers"] == nil {
		t.Fatal("providers dropped")
	}
	raw, _ := json.Marshal(m["hooks"])
	h := string(raw)
	if !strings.Contains(h, "karpathy.sh") || strings.Contains(h, "/bin/a/") ||
		strings.Count(h, `hook stop --client qoder`) != 1 || strings.Count(h, "session-end --client qoder") != 1 {
		t.Fatalf("hooks=%s", h)
	}
}

func TestSetupZCode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cli", "config.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	orig := `{"plugins":{"a":1},"mcp":{"servers":{"open-design":{"type":"stdio","command":"/od","enabled":false}}}}`
	if err := os.WriteFile(p, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := SetupZCode(p, "/bin/mind-runner", "k2"); err != nil {
			t.Fatal(err)
		}
	}
	m := readJSONMap(t, p)
	servers := m["mcp"].(map[string]any)["servers"].(map[string]any)
	if servers["open-design"] == nil {
		t.Fatal("other server dropped")
	}
	srv := servers["mind-runner"].(map[string]any)
	if srv["type"] != "stdio" || existingKeyIn(p, "mcp", "servers") != "k2" {
		t.Fatalf("srv=%v", srv)
	}
	hooks := m["hooks"].(map[string]any)
	if hooks["enabled"] != true {
		t.Fatal("hooks not enabled")
	}
	ev := hooks["events"].(map[string]any)
	if _, ok := ev["SessionEnd"]; ok {
		t.Fatal("ZCode has no SessionEnd")
	}
	raw, _ := json.Marshal(ev)
	if strings.Count(string(raw), "hook stop --snapshot --client zcode") != 1 ||
		strings.Count(string(raw), "hook prompt --snapshot --client zcode") != 1 {
		t.Fatalf("events=%s", raw)
	}
}
