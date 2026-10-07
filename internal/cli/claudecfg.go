package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ClaudeCfg thao tác trên settings.json của Claude Code.
type ClaudeCfg struct {
	Path string
}

// hookSub: event Claude Code → subcommand của mind-runner.
// UserPromptSubmit = recap đầu ngày cho session sống qua đêm (gập máy/mở lại).
var hookSub = map[string]string{
	"SessionStart":     "session-start",
	"UserPromptSubmit": "prompt",
	"Stop":             "stop",
	"SessionEnd":       "session-end",
}

// MergeHooks thêm/hợp nhất hooks trỏ về binaryPath, giữ nguyên mọi key khác.
// Idempotent: hook mind-runner cũ cho cùng subcommand (kể cả đường dẫn binary
// cũ) được thay thế thay vì nhân đôi. Backup có timestamp trước khi ghi; ghi
// atomic (temp + rename) để Claude Code không bao giờ đọc file dở.
func (c *ClaudeCfg) MergeHooks(binaryPath string) error {
	m := map[string]any{}
	orig, err := os.ReadFile(c.Path)
	if err == nil {
		if err := json.Unmarshal(orig, &m); err != nil {
			return fmt.Errorf("settings.json không phải JSON: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	hooks, _ := m["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for ev, sub := range hookSub {
		cmd := fmt.Sprintf("%q hook %s", binaryPath, sub)
		groups, _ := hooks[ev].([]any)
		if hasHookCommand(groups, cmd) {
			continue
		}
		groups = removeMindRunnerHooks(groups, sub)
		hooks[ev] = append(groups, map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": cmd}},
		})
	}
	m["hooks"] = hooks

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if orig != nil && string(orig) == string(data) {
		return nil // không đổi gì → không ghi, không tạo backup
	}
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o700); err != nil {
		return err
	}
	if orig != nil {
		bak := c.Path + ".bak-" + time.Now().Format("20060102-150405")
		if err := os.WriteFile(bak, orig, 0o600); err != nil {
			return fmt.Errorf("backup settings.json: %w", err)
		}
	}
	return writeFileAtomic(c.Path, data, 0o600)
}

// writeFileAtomic ghi temp cùng thư mục rồi rename (atomic trên cùng FS).
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op sau rename thành công
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// isMindRunnerHook: command dạng `"<.../mind-runner>" hook <sub>`.
func isMindRunnerHook(cmd, sub string) bool {
	if !strings.HasSuffix(cmd, " hook "+sub) {
		return false
	}
	bin := strings.Trim(strings.TrimSuffix(cmd, " hook "+sub), `"' `)
	return filepath.Base(bin) == "mind-runner"
}

// removeMindRunnerHooks bỏ các hook mind-runner cũ của sub; group rỗng bị bỏ.
func removeMindRunnerHooks(groups []any, sub string) []any {
	out := groups[:0:0]
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			out = append(out, g)
			continue
		}
		hs, ok := gm["hooks"].([]any)
		if !ok {
			out = append(out, g)
			continue
		}
		kept := hs[:0:0]
		for _, h := range hs {
			if hm, ok := h.(map[string]any); ok {
				if c, _ := hm["command"].(string); isMindRunnerHook(c, sub) {
					continue
				}
			}
			kept = append(kept, h)
		}
		if len(kept) == 0 {
			continue
		}
		gm["hooks"] = kept
		out = append(out, gm)
	}
	return out
}

func hasHookCommand(groups []any, cmd string) bool {
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			continue
		}
		hs, ok := gm["hooks"].([]any)
		if !ok {
			continue
		}
		for _, h := range hs {
			if hm, ok := h.(map[string]any); ok && hm["command"] == cmd {
				return true
			}
		}
	}
	return false
}
