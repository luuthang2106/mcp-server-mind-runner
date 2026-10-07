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
// atomic (temp + rename) để client không bao giờ đọc file dở.
func (c *ClaudeCfg) MergeHooks(binaryPath string) error {
	return updateJSONFile(c.Path, func(m map[string]any) error {
		hooks, _ := m["hooks"].(map[string]any)
		if hooks == nil {
			hooks = map[string]any{}
		}
		mergeHookGroups(hooks, binaryPath, hookSub, "")
		m["hooks"] = hooks
		return nil
	})
}

// RemoveHooks gỡ mọi hook mind-runner (binary tên mind-runner hoặc đúng
// binaryPath) khỏi settings.json, giữ hook khác; event rỗng sau khi gỡ bị
// xoá. Trả số hook đã gỡ.
func (c *ClaudeCfg) RemoveHooks(binaryPath string) (int, error) {
	if _, err := os.Stat(c.Path); os.IsNotExist(err) {
		return 0, nil
	}
	n := 0
	err := updateJSONFile(c.Path, func(m map[string]any) error {
		hooks, _ := m["hooks"].(map[string]any)
		if hooks == nil {
			return nil
		}
		for ev, sub := range hookSub {
			groups, ok := hooks[ev].([]any)
			if !ok {
				continue
			}
			n += countMindRunnerHooks(groups, sub, binaryPath)
			if g := removeMindRunnerHooks(groups, sub, binaryPath); len(g) > 0 {
				hooks[ev] = g
			} else {
				delete(hooks, ev)
			}
		}
		if len(hooks) == 0 {
			delete(m, "hooks")
		}
		return nil
	})
	return n, err
}

// mergeHookGroups: hooks[event] = danh sách group kiểu Claude Code
// ([{matcher?, hooks:[{type,command}]}]); thay hook mind-runner cũ của từng sub.
func mergeHookGroups(hooks map[string]any, binaryPath string, events map[string]string, extra string) {
	for ev, sub := range events {
		cmd := fmt.Sprintf("%q hook %s%s", binaryPath, sub, extra)
		groups, _ := hooks[ev].([]any)
		if hasHookCommand(groups, cmd) && countMindRunnerHooks(groups, sub, binaryPath) == 1 {
			continue
		}
		groups = removeMindRunnerHooks(groups, sub, binaryPath)
		hooks[ev] = append(groups, map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": cmd}},
		})
	}
}

// updateJSONFile đọc file JSON (không có = {}), cho mutate sửa, rồi ghi lại
// nếu có thay đổi: backup có timestamp + ghi atomic, quyền 0600.
func updateJSONFile(path string, mutate func(m map[string]any) error) error {
	m := map[string]any{}
	orig, err := os.ReadFile(path)
	if err == nil {
		if len(strings.TrimSpace(string(orig))) > 0 {
			if err := json.Unmarshal(orig, &m); err != nil {
				return fmt.Errorf("%s không phải JSON: %w", filepath.Base(path), err)
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	} else {
		orig = nil
	}
	if err := mutate(m); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if orig != nil && string(orig) == string(data) {
		return nil // không đổi gì → không ghi, không tạo backup
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if orig != nil {
		bak := path + ".bak-" + time.Now().Format("20060102-150405")
		if err := os.WriteFile(bak, orig, 0o600); err != nil {
			return fmt.Errorf("backup %s: %w", filepath.Base(path), err)
		}
	}
	return writeFileAtomic(path, data, 0o600)
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

// isMindRunnerHook: command dạng `"<.../mind-runner>" hook <sub> [--cờ…]`.
func isMindRunnerHook(cmd, sub, bin string) bool {
	i := strings.LastIndex(cmd, " hook ")
	if i < 0 {
		return false
	}
	rest := strings.Fields(cmd[i+len(" hook "):])
	if len(rest) == 0 || rest[0] != sub {
		return false
	}
	b := strings.Trim(cmd[:i], `"' `)
	return filepath.Base(b) == "mind-runner" || (bin != "" && b == bin)
}

func countMindRunnerHooks(groups []any, sub, bin string) int {
	n := 0
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		hs, _ := gm["hooks"].([]any)
		for _, h := range hs {
			if hm, ok := h.(map[string]any); ok {
				if c, _ := hm["command"].(string); isMindRunnerHook(c, sub, bin) {
					n++
				}
			}
		}
	}
	return n
}

// removeMindRunnerHooks bỏ các hook mind-runner cũ của sub; group rỗng bị bỏ.
func removeMindRunnerHooks(groups []any, sub, bin string) []any {
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
				if c, _ := hm["command"].(string); isMindRunnerHook(c, sub, bin) {
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
