package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMergeHooksKeepsExistingKeys(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := `{"permissions":{"allow":["Bash(ls:*)"]},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo cũ"}]}]}}`
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}

	cc := &ClaudeCfg{Path: path}
	if err := cc.MergeHooks("/opt/mind-runner"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	// key cũ còn nguyên
	perms := m["permissions"].(map[string]any)
	if perms["allow"].([]any)[0] != "Bash(ls:*)" {
		t.Fatalf("permissions=%v", perms)
	}
	hooks := m["hooks"].(map[string]any)
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd"} {
		if hooks[ev] == nil {
			t.Fatalf("thiếu hooks.%s: %v", ev, hooks)
		}
	}
	// Stop: entry cũ giữ + entry mới thêm
	if g := hooks["Stop"].([]any); len(g) != 2 {
		t.Fatalf("Stop=%v", g)
	}
	if !hasHookCommand(hooks["SessionStart"].([]any), `"/opt/mind-runner" hook session-start`) {
		t.Fatalf("thiếu command session-start: %v", hooks["SessionStart"])
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm: %v %v", fi.Mode().Perm(), err)
	}

	// merge lần 2 → idempotent, không nhân bản
	if err := cc.MergeHooks("/opt/mind-runner"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	m = map[string]any{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	hooks = m["hooks"].(map[string]any)
	if g := hooks["Stop"].([]any); len(g) != 2 {
		t.Fatalf("nhân bản: Stop=%v", g)
	}
	if g := hooks["SessionStart"].([]any); len(g) != 1 {
		t.Fatalf("SessionStart=%v", g)
	}
}

func TestMergeHooksMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude", "settings.json")
	cc := &ClaudeCfg{Path: path}
	if err := cc.MergeHooks("/bin/mr"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	hooks := m["hooks"].(map[string]any)
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd"} {
		if hooks[ev] == nil {
			t.Fatalf("thiếu hooks.%s", ev)
		}
	}
}

func TestMergeHooksReplacesOldBinaryPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	cc := &ClaudeCfg{Path: path}
	if err := cc.MergeHooks("/old/bin/mind-runner"); err != nil {
		t.Fatal(err)
	}
	if err := cc.MergeHooks("/new/bin/mind-runner"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	hooks := m["hooks"].(map[string]any)
	for ev, sub := range hookSub {
		g := hooks[ev].([]any)
		if len(g) != 1 || !hasHookCommand(g, `"/new/bin/mind-runner" hook `+sub) {
			t.Fatalf("%s: %v", ev, g)
		}
	}
	baks, _ := filepath.Glob(path + ".bak-*")
	if len(baks) == 0 {
		t.Fatal("thiếu backup")
	}
}
