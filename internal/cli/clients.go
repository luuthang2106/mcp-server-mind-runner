package cli

import (
	"encoding/json"
	"os"
	"strings"
)

// Client ngoài Claude Code mà setup tự cài được MCP + hooks.
//
// Qoder (fork Claude Code): ~/.qoder/settings.json — mcpServers + hooks cùng
// định dạng Claude Code; transcript JSONL thật nên capture theo offset như
// Claude Code, chỉ khác nhãn client.
//
// ZCode: ~/.zcode/cli/config.json — MCP ở mcp.servers, hooks ở
// hooks.{enabled, events}; không có SessionEnd, và transcript_path gửi cho hook
// là file tạm chỉ chứa lượt hiện tại → hook chạy chế độ --snapshot.

const gatewayKeyEnv = "MIND_RUNNER_GATEWAY_API_KEY"

// zcodeHookSub: ZCode chỉ có 7 event, không có SessionEnd — extract chạy sau
// debounce như bình thường.
var zcodeHookSub = map[string]string{
	"SessionStart":     "session-start",
	"UserPromptSubmit": "prompt",
	"Stop":             "stop",
}

// mcpEntry dựng entry server stdio; key rỗng → giữ env cũ (nếu có) để chạy lại
// setup không làm mất key.
func mcpEntry(old map[string]any, bin, apiKey string, withType bool) map[string]any {
	e := map[string]any{}
	for k, v := range old {
		e[k] = v
	}
	if withType {
		e["type"] = "stdio"
	}
	e["command"] = bin
	e["args"] = []any{"mcp"}
	env, _ := e["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	if apiKey != "" {
		env[gatewayKeyEnv] = apiKey
	}
	if len(env) > 0 {
		e["env"] = env
	} else {
		delete(e, "env")
	}
	return e
}

// SetupQoder ghi MCP server + hooks vào settings.json của Qoder.
func SetupQoder(path, bin, apiKey string) error {
	return updateJSONFile(path, func(m map[string]any) error {
		servers, _ := m["mcpServers"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
		}
		old, _ := servers["mind-runner"].(map[string]any)
		servers["mind-runner"] = mcpEntry(old, bin, apiKey, false)
		m["mcpServers"] = servers

		hooks, _ := m["hooks"].(map[string]any)
		if hooks == nil {
			hooks = map[string]any{}
		}
		mergeHookGroups(hooks, bin, hookSub, " --client qoder")
		m["hooks"] = hooks
		return nil
	})
}

// SetupZCode ghi MCP server + hooks (bật hooks.enabled) vào config ZCode.
func SetupZCode(path, bin, apiKey string) error {
	return updateJSONFile(path, func(m map[string]any) error {
		mcp, _ := m["mcp"].(map[string]any)
		if mcp == nil {
			mcp = map[string]any{}
		}
		servers, _ := mcp["servers"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
		}
		old, _ := servers["mind-runner"].(map[string]any)
		servers["mind-runner"] = mcpEntry(old, bin, apiKey, true)
		mcp["servers"] = servers
		m["mcp"] = mcp

		hooks, _ := m["hooks"].(map[string]any)
		if hooks == nil {
			hooks = map[string]any{}
		}
		hooks["enabled"] = true
		events, _ := hooks["events"].(map[string]any)
		if events == nil {
			events = map[string]any{}
		}
		mergeHookGroups(events, bin, zcodeHookSub, " --snapshot --client zcode")
		hooks["events"] = events
		m["hooks"] = hooks
		return nil
	})
}

// existingKeyIn đọc key mind-runner đã cấu hình trong file JSON của client,
// theo đường dẫn tới map servers (vd ["mcpServers"] hoặc ["mcp","servers"]).
func existingKeyIn(path string, serversPath ...string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var cur any
	if json.Unmarshal(data, &cur) != nil {
		return ""
	}
	for _, k := range append(serversPath, "mind-runner", "env", gatewayKeyEnv) {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[k]
	}
	s, _ := cur.(string)
	return strings.TrimSpace(s)
}
