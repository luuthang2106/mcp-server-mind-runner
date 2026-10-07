package extract

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func jl(v any) string { b, _ := json.Marshal(v); return string(b) + "\n" }

// TestCondenseKeepsConversationOnly: chỉ giữ lời user/assistant + tóm tắt tool;
// bỏ thinking, tool_result, attachment (kể cả recap hook chèn vào), meta,
// sidechain và system-reminder trong lượt user.
func TestCondenseKeepsConversationOnly(t *testing.T) {
	in := jl(map[string]any{"type": "attachment", "attachment": map[string]any{"type": "hook_success", "content": "# Briefing — task #1 RECAP"}}) +
		jl(map[string]any{"type": "user", "message": map[string]any{"content": "<system-reminder>SECRET-SYS</system-reminder>chốt dùng sqlite"}}) +
		jl(map[string]any{"type": "user", "isMeta": true, "message": map[string]any{"content": "META-NOISE"}}) +
		jl(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
			map[string]any{"type": "thinking", "thinking": "THINK-NOISE"},
			map[string]any{"type": "text", "text": "Ok, dùng SQLite."},
			map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "go test ./...", "description": "Run tests"}},
		}}}) +
		jl(map[string]any{"type": "user", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "content": "TOOL-OUTPUT-NOISE"},
		}}}) +
		jl(map[string]any{"type": "assistant", "isSidechain": true, "message": map[string]any{"content": "SIDECHAIN-NOISE"}}) +
		jl(map[string]any{"type": "attachment", "attachment": map[string]any{"type": "queued_command", "prompt": "thêm index nữa", "origin": map[string]any{"kind": "human"}}}) +
		jl(map[string]any{"type": "last-prompt", "lastPrompt": "LAST-NOISE"})

	got := condense([]byte(in))
	want := "User: chốt dùng sqlite\nAssistant: Ok, dùng SQLite.\nTool: Bash Run tests\nUser: thêm index nữa\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestCondensePlainTextAndClip: dòng không phải JSON giữ nguyên; lượt quá dài
// bị cắt giữa, giữ đầu và cuối.
func TestCondensePlainTextAndClip(t *testing.T) {
	if got := condense([]byte("ghi chú thường\n")); got != "ghi chú thường\n" {
		t.Fatalf("got=%q", got)
	}
	// message dạng chuỗi trơn
	if got := condense([]byte(`{"type":"user","message":"chốt A"}` + "\n")); got != "User: chốt A\n" {
		t.Fatalf("got=%q", got)
	}
	long := "ĐẦU" + strings.Repeat("ố", 10_000) + "CUỐI"
	got := condense([]byte(jl(map[string]any{"type": "user", "message": map[string]any{"content": long}})))
	if n := utf8.RuneCountInString(got); n > msgMaxRunes+40 {
		t.Fatalf("len=%d", n)
	}
	if !strings.HasPrefix(got, "User: ĐẦU") || !strings.HasSuffix(got, "CUỐI\n") || !strings.Contains(got, "đã cắt") {
		t.Fatalf("got=%q…", got[:40])
	}
}

func TestWindows(t *testing.T) {
	line := strings.Repeat("a", 99) + "\n" // 100 rune
	s := strings.Repeat(line, 25)          // 2500 rune
	ws, dropped := windows(s, 1000, 10)
	if len(ws) != 3 || dropped != 0 {
		t.Fatalf("ws=%d dropped=%d", len(ws), dropped)
	}
	for _, w := range ws {
		if utf8.RuneCountInString(w) > 1000 || !strings.HasSuffix(w, "\n") {
			t.Fatalf("window lỗi: %d rune", utf8.RuneCountInString(w))
		}
	}
	if strings.Join(ws, "") != s {
		t.Fatal("ghép lại không khớp")
	}
	ws, dropped = windows(s+"CUỐI\n", 1000, 2)
	if len(ws) != 2 || dropped != 1 || !strings.HasSuffix(ws[1], "CUỐI\n") {
		t.Fatalf("ws=%d dropped=%d", len(ws), dropped)
	}
}

// TestCondenseRoleOnlyRecords: file tạm của ZCode chỉ có message.role.
func TestCondenseRoleOnlyRecords(t *testing.T) {
	in := `{"message":{"content":[{"text":"nhớ giúp tôi deploy thứ sáu","type":"text"}],"role":"user"}}
{"message":{"content":[{"text":"Đã ghi.","type":"text"}],"role":"assistant"}}
`
	got := condense([]byte(in))
	want := "User: nhớ giúp tôi deploy thứ sáu\nAssistant: Đã ghi.\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
