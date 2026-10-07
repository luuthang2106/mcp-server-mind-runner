package extract

import (
	"context"
	"fmt"
	"strings"

	"mind-runner/internal/brain"
	"mind-runner/internal/egress"
	"mind-runner/internal/store"
)

// Ngưỡng cosine (embedding) để coi hai note là cùng một ý.
//   - supersedeThreshold: kiến thức (decision/fact/preference) gần giống note
//     đang active cùng kind → note mới thay thế note cũ (cũ thành superseded,
//     vẫn giữ làm lịch sử). Thông tin mới nhất thắng — đúng cả khi người dùng
//     đổi ý ("dùng SQLite" → "dùng Postgres") vì hai câu đó cũng rất gần.
//   - batchDupThreshold: hai note trong cùng một lần trích gần như y hệt
//     (hay gặp khi nhiều cửa sổ) → bỏ note sau.
//
// Sự kiện (note/task_hint) không thay thế nhau: "deploy v0.1.2" và "deploy
// v0.1.3" rất gần nhưng là hai sự kiện khác nhau.
var (
	supersedeThreshold = 0.92
	batchDupThreshold  = 0.95
)

var knowledgeKinds = map[string]bool{"decision": true, "fact": true, "preference": true}

// dedupePlan: kết quả dò trùng cho từng note của một lần trích.
type dedupePlan struct {
	skip      []bool  // trùng note khác trong cùng lần trích → không ghi
	supersede []int64 // note cũ (đang active) sẽ bị note này thay thế; 0 = không
}

// planDedupe embed các note (một request) rồi so với vector đã có trong space
// và với nhau. Lỗi embed → không dò (ghi bình thường) và trả lỗi để log:
// dò trùng là tối ưu, không được chặn việc ghi nhớ.
func (x *Extractor) planDedupe(ctx context.Context, pol egress.Policy, spaceID int64, notes []ExtractedNote) (dedupePlan, error) {
	plan := dedupePlan{skip: make([]bool, len(notes)), supersede: make([]int64, len(notes))}
	if len(notes) == 0 || x.eg == nil {
		return plan, nil
	}
	texts := make([]string, len(notes))
	for i, n := range notes {
		texts[i] = brain.EmbedText(strings.TrimSpace(n.Text), store.NoteMeta{Why: strings.TrimSpace(n.Why)})
	}
	vecs, err := x.eg.Embed(ctx, pol, texts)
	if err != nil {
		return plan, err
	}
	if len(vecs) != len(notes) {
		return plan, fmt.Errorf("embed trả %d vector cho %d note", len(vecs), len(notes))
	}
	model := x.eg.EmbedModel(pol)
	for i, n := range notes {
		for j := 0; j < i; j++ {
			if !plan.skip[j] && notes[j].Kind == n.Kind && brain.Cosine(vecs[i], vecs[j]) >= batchDupThreshold {
				plan.skip[i] = true
				break
			}
		}
		if plan.skip[i] || !knowledgeKinds[n.Kind] {
			continue
		}
		hits, err := x.b.VectorTop(ctx, model, store.NoteFilter{SpaceIDs: []int64{spaceID}, Kinds: []string{n.Kind}}, vecs[i], 1)
		if err != nil {
			return plan, err
		}
		if len(hits) == 1 && hits[0].Score >= supersedeThreshold {
			plan.supersede[i] = hits[0].NoteID
		}
	}
	return plan, nil
}

// openTasksBlock: danh sách việc đang mở gửi kèm transcript để model cập nhật
// đúng việc (task_updates) thay vì tạo việc trùng.
func openTasksBlock(tasks []store.Task) string {
	if len(tasks) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Open tasks (id: title):\n")
	for _, t := range tasks {
		fmt.Fprintf(&b, "#%d: %s", t.ID, t.Title)
		if t.NextStep != nil && *t.NextStep != "" {
			fmt.Fprintf(&b, " — next: %s", *t.NextStep)
		}
		if t.WaitingOn != "" {
			fmt.Fprintf(&b, " — waiting on: %s", t.WaitingOn)
		}
		b.WriteByte('\n')
	}
	b.WriteString("\nTranscript:\n")
	return b.String()
}

// openTasksForExtract: số việc đang mở (mới cập nhật nhất) gửi kèm transcript.
const openTasksForExtract = 60
