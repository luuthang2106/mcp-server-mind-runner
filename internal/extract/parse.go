package extract

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ExtractedNote một note model trích được.
// Trường cấu trúc đều tùy chọn — model chỉ điền khi transcript nói rõ.
type ExtractedNote struct {
	Kind         string   `json:"kind"`
	Text         string   `json:"text"`
	Tags         []string `json:"tags"`
	Why          string   `json:"why"`
	Who          []string `json:"who"`
	When         string   `json:"when"`
	AsOf         string   `json:"as_of"`
	Ref          string   `json:"ref"`
	Alternatives []string `json:"alternatives"`
	Scope        string   `json:"scope"`
}

// ExtractedTask một việc còn dang dở.
type ExtractedTask struct {
	Title       string `json:"title"`
	NextStep    string `json:"next_step"`
	Why         string `json:"why"`
	Owner       string `json:"owner"`
	WaitingOn   string `json:"waiting_on"`
	Due         string `json:"due"`
	Constraints string `json:"constraints"`
}

// ExtractedRelation quan hệ giữa hai thực thể; NoteIndex trỏ vào Notes (nil = không gắn note nguồn).
type ExtractedRelation struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Type      string `json:"type"`
	NoteIndex *int   `json:"note_index"`
}

// ExtractedTaskUpdate thay đổi cho một việc ĐANG MỞ (id lấy từ danh sách gửi
// kèm transcript). Status rỗng = chỉ cập nhật trường.
type ExtractedTaskUpdate struct {
	ID        int64  `json:"id"`
	Status    string `json:"status"` // done|dropped|""
	NextStep  string `json:"next_step"`
	WaitingOn string `json:"waiting_on"`
	Due       string `json:"due"`
}

// Extraction toàn bộ output của model cho một delta phiên.
type Extraction struct {
	Notes       []ExtractedNote       `json:"notes"`
	Tasks       []ExtractedTask       `json:"tasks"`
	TaskUpdates []ExtractedTaskUpdate `json:"task_updates"`
	Relations   []ExtractedRelation   `json:"relations"`
}

// extractKinds: whitelist kind cho extract — hẹp hơn validKinds của brain
// (transcript/caption chỉ do media ghi).
var extractKinds = map[string]bool{
	"fact": true, "preference": true, "decision": true, "note": true, "task_hint": true,
}

// ParseExtraction đọc output model: lấy đoạn từ '{' đầu tới '}' cuối (bỏ fence
// ```json hay lời dẫn quanh JSON), unmarshal rồi validate từng item — item vi
// phạm → error nêu rõ vị trí (job fail ồn ào, không bỏ qua lặng lẽ).
func ParseExtraction(s string) (Extraction, error) {
	s = strings.TrimSpace(s)
	if i, j := strings.Index(s, "{"), strings.LastIndex(s, "}"); i >= 0 && j > i {
		s = s[i : j+1]
	}
	var ex Extraction
	if err := json.Unmarshal([]byte(s), &ex); err != nil {
		return Extraction{}, fmt.Errorf("JSON không hợp lệ: %w", err)
	}
	for i, n := range ex.Notes {
		kind := strings.ToLower(strings.TrimSpace(n.Kind))
		if !extractKinds[kind] {
			return Extraction{}, fmt.Errorf("notes[%d]: kind không hợp lệ %q", i, n.Kind)
		}
		ex.Notes[i].Kind = kind
		if strings.TrimSpace(n.Text) == "" {
			return Extraction{}, fmt.Errorf("notes[%d]: text rỗng", i)
		}
	}
	for i, tk := range ex.Tasks {
		if strings.TrimSpace(tk.Title) == "" {
			return Extraction{}, fmt.Errorf("tasks[%d]: title rỗng", i)
		}
		// Hạn sai định dạng → bỏ hạn, giữ task (không fail cả job vì một trường phụ).
		due := strings.TrimSpace(tk.Due)
		if _, err := time.Parse("2006-01-02", due); err != nil {
			due = ""
		}
		ex.Tasks[i].Due = due
	}
	// task_updates: status lạ / hạn sai → bỏ phần đó (không fail cả job).
	for i, u := range ex.TaskUpdates {
		st := strings.ToLower(strings.TrimSpace(u.Status))
		if st != "done" && st != "dropped" {
			st = ""
		}
		ex.TaskUpdates[i].Status = st
		due := strings.TrimSpace(u.Due)
		if _, err := time.Parse("2006-01-02", due); err != nil {
			due = ""
		}
		ex.TaskUpdates[i].Due = due
	}
	return ex, nil
}
