package extract

import (
	"bufio"
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Transcript JSONL của Claude Code chủ yếu là thứ không cần nhớ: thinking,
// tool_result (file, log), attachment (system prompt, CLAUDE.md, danh sách
// tool/skill, recap do chính hook chèn vào) và vỏ JSON mỗi dòng. Gửi nguyên
// văn → một lượt dài lên tới ~1M token, tốn tiền, vượt context model và đẩy
// cả system prompt/email ra gateway. condense chỉ giữ hội thoại thật:
//
//	User: <lời người dùng>
//	Assistant: <câu trả lời>
//	Tool: <tên> <mô tả/lệnh ngắn>
//
// Dòng không phải JSON (input dạng text thường) giữ nguyên.

const (
	// msgMaxRunes: trần mỗi lượt (người dùng dán log dài) — giữ đầu + cuối.
	msgMaxRunes = 4_000
	// toolMaxRunes: trần mô tả một tool call.
	toolMaxRunes = 200
	// extractWindowRunes: trần input một lần gọi extract; dài hơn → chia cửa sổ.
	extractWindowRunes = 40_000
	// extractMaxWindows: một delta dài hơn ~8 cửa sổ → chỉ giữ phần cuối.
	extractMaxWindows = 8
)

var (
	reSystemReminder = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)
	reCommandTags    = regexp.MustCompile(`(?s)<(local-command-stdout|local-command-stderr|command-message|command-args)>.*?</(local-command-stdout|local-command-stderr|command-message|command-args)>`)
	reCommandName    = regexp.MustCompile(`<command-name>(.*?)</command-name>`)
)

type ccRecord struct {
	Type        string          `json:"type"`
	IsMeta      bool            `json:"isMeta"`
	IsSidechain bool            `json:"isSidechain"`
	Message     json.RawMessage `json:"message"`
	Attachment  *struct {
		Type   string `json:"type"`
		Prompt string `json:"prompt"`
		Origin *struct {
			Kind string `json:"kind"`
		} `json:"origin"`
	} `json:"attachment"`
}

type ccMessage struct {
	Content json.RawMessage `json:"content"`
}

type ccBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// condense rút transcript JSONL về hội thoại thật (xem đầu file).
func condense(raw []byte) string {
	var out strings.Builder
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if line[0] != '{' {
			writeLine(&out, "", string(line))
			continue
		}
		var rec ccRecord
		if json.Unmarshal(line, &rec) != nil {
			writeLine(&out, "", string(line))
			continue
		}
		if rec.IsSidechain {
			continue // subagent: kết luận đã nằm trong câu trả lời của agent chính
		}
		switch rec.Type {
		case "user":
			if !rec.IsMeta {
				condenseMessage(&out, "User", rec.Message)
			}
		case "assistant":
			condenseMessage(&out, "Assistant", rec.Message)
		case "attachment":
			// prompt người dùng gõ khi agent đang chạy (xếp hàng)
			if a := rec.Attachment; a != nil && a.Type == "queued_command" && a.Origin != nil && a.Origin.Kind == "human" {
				writeLine(&out, "User", cleanUserText(a.Prompt))
			}
		}
	}
	return out.String()
}

func condenseMessage(out *strings.Builder, role string, raw json.RawMessage) {
	var m ccMessage
	if len(raw) > 0 && raw[0] == '"' {
		// biến thể message là chuỗi trơn (transcript cũ / client khác)
		m.Content = raw
	} else if json.Unmarshal(raw, &m) != nil || len(m.Content) == 0 {
		return
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		if role == "User" {
			s = cleanUserText(s)
		}
		writeLine(out, role, s)
		return
	}
	var blocks []ccBlock
	if json.Unmarshal(m.Content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			t := b.Text
			if role == "User" {
				t = cleanUserText(t)
			}
			writeLine(out, role, t)
		case "tool_use":
			writeLine(out, "Tool", truncRunes(strings.TrimSpace(b.Name+" "+toolSummary(b.Input)), toolMaxRunes))
		}
		// thinking, tool_result, image, … → bỏ
	}
}

// toolSummary: trường mô tả ý định của tool call (không phải nội dung file).
func toolSummary(input json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(input, &m) != nil {
		return ""
	}
	for _, k := range []string{"description", "command", "file_path", "pattern", "query", "url", "prompt", "skill"} {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.Join(strings.Fields(v), " ")
		}
	}
	return ""
}

// cleanUserText bỏ khối do client chèn vào lượt người dùng (system-reminder,
// output lệnh local); slash command giữ lại tên lệnh.
func cleanUserText(s string) string {
	s = reSystemReminder.ReplaceAllString(s, "")
	s = reCommandTags.ReplaceAllString(s, "")
	s = reCommandName.ReplaceAllString(s, "$1")
	return strings.TrimSpace(s)
}

func writeLine(out *strings.Builder, role, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	text = clipMiddle(text, msgMaxRunes)
	if role != "" {
		out.WriteString(role)
		out.WriteString(": ")
	}
	out.WriteString(text)
	out.WriteString("\n")
}

// clipMiddle giữ đầu + cuối khi s dài hơn n rune.
func clipMiddle(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	h := n * 2 / 3
	return string(r[:h]) + " …[đã cắt]… " + string(r[len(r)-(n-h):])
}

func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// windows chia text đã condense thành các cửa sổ ≤ n rune theo biên dòng
// (một dòng dài hơn n đã bị clipMiddle nên luôn vừa). Quá max cửa sổ → giữ
// các cửa sổ cuối, dropped = số cửa sổ bị bỏ.
func windows(s string, n, max int) (ws []string, dropped int) {
	var cur strings.Builder
	curRunes := 0
	for _, line := range strings.SplitAfter(s, "\n") {
		if line == "" {
			continue
		}
		lr := utf8.RuneCountInString(line)
		if curRunes > 0 && curRunes+lr > n {
			ws = append(ws, cur.String())
			cur.Reset()
			curRunes = 0
		}
		cur.WriteString(line)
		curRunes += lr
	}
	if curRunes > 0 {
		ws = append(ws, cur.String())
	}
	if len(ws) > max {
		dropped = len(ws) - max
		ws = ws[dropped:]
	}
	return ws, dropped
}
