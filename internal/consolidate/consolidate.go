// Package consolidate: job định kỳ quét note kiến thức (decision/fact/
// preference/procedure) từng space, gom theo project rồi gửi từng batch cho
// model tìm note trùng/lặp và gộp thành một note mới; note cũ bị XOÁ CỨNG.
// Kết quả chỉ đi qua WriteNote + SoftDeleteNote (không đụng bảng khác).
package consolidate

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"mind-runner/internal/brain"
	"mind-runner/internal/config"
	"mind-runner/internal/egress"
	"mind-runner/internal/store"
)

//go:embed prompts/consolidate.md
var consolidatePrompt string

// knowledgeKinds: tầng kiến thức (vĩnh viễn) — cùng tập với dedupe của extract.
var knowledgeKinds = []string{"decision", "fact", "preference", "procedure"}

// maxBatchRunes: trần rune một batch gửi model (cắt theo dòng note); 20k rune
// ≈ 6-8k token, an toàn cho cả model local.
const maxBatchRunes = 20_000

// minGuardBatch: guard "xoá > 50% batch" chỉ tin được từ 8 note trở lên —
// batch 2-7 note gộp cả batch là chuyện thường (kho thưa). ponytail: heuristic,
// nới khi thấy chặn nhầm.
const minGuardBatch = 8

// Runner chạy job consolidate.
type Runner struct {
	st  *store.Store
	b   *brain.Brain
	eg  *egress.Egress
	cfg *config.Config
}

func New(st *store.Store, b *brain.Brain, eg *egress.Egress, cfg *config.Config) *Runner {
	return &Runner{st: st, b: b, eg: eg, cfg: cfg}
}

// merge một nhóm note model trả về.
type merge struct {
	Kind  string  `json:"kind"`
	Text  string  `json:"text"`
	Notes []int64 `json:"notes"`
}

// Run quét toàn kho theo space → project → kind → batch; gộp note trùng, xoá
// cứng note cũ. Lỗi model/parse/validate → trả lỗi để job retry (nhất quán với
// extract: ồn ào, không bỏ qua lặng lẽ); batch đã áp dụng giữ nguyên — retry
// tính lại từ trạng thái hiện tại, ghi trùng là idempotent.
// ponytail: chạy trên một job, không heartbeat — vượt StaleRunning (30') có
// thể bị process khác chạy lại; hậu quả tự lành (gộp lần 2 thành no-op).
func (r *Runner) Run(ctx context.Context) error {
	sps, err := r.st.Spaces(ctx)
	if err != nil {
		return err
	}
	var stat struct{ batches, merges, deleted, guardSkipped int }
	for _, sp := range sps {
		notes, err := r.st.ActiveNotesByKinds(ctx, sp.ID, knowledgeKinds)
		if err != nil {
			return err
		}
		pol := egress.Policy(r.cfg.SpacePolicy(sp.Name))
		for _, proj := range projectsOf(notes) {
			for _, kind := range knowledgeKinds {
				for _, batch := range packBatches(filterProjectKind(notes, proj, kind), maxBatchRunes) {
					if len(batch) < 2 {
						continue
					}
					stat.batches++
					merges, err := r.mergeBatch(ctx, pol, batch)
					if err != nil {
						return err
					}
					if len(merges) == 0 {
						continue
					}
					if guardSkip(batch, merges) {
						stat.guardSkipped++
						continue
					}
					for _, m := range merges {
						del, err := r.applyMerge(ctx, sp.ID, proj, m, batch)
						if err != nil {
							return err
						}
						stat.merges++
						stat.deleted += del
					}
				}
			}
		}
	}
	if lg := r.eg.UsageLog(); lg != nil && (stat.merges > 0 || stat.guardSkipped > 0) {
		// chỉ số đo, không nội dung
		lg.Info("consolidate: xong", "batches", stat.batches, "merges", stat.merges,
			"deleted", stat.deleted, "guard_skipped", stat.guardSkipped)
	}
	return nil
}

// mergeBatch gửi một batch cho model, parse + validate kết quả.
func (r *Runner) mergeBatch(ctx context.Context, pol egress.Policy, batch []store.Note) ([]merge, error) {
	var bldr strings.Builder
	for _, n := range batch {
		bldr.WriteString(noteLine(n))
		bldr.WriteByte('\n')
	}
	out, err := r.eg.Chat(ctx, pol, consolidatePrompt, bldr.String())
	if err != nil {
		return nil, err
	}
	ms, err := parseMerges(out)
	if err != nil {
		return nil, err
	}
	if err := validateMerges(ms, batch); err != nil {
		return nil, err
	}
	return ms, nil
}

// noteLine: một dòng cho model — "#id [kind] (YYYY-MM-DD) tags: text"; ngày là
// updated_at (bản kể mới nhất thường mới nhất).
func noteLine(n store.Note) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#%d [%s] (%s)", n.ID, n.Kind, n.UpdatedAt.Format("2006-01-02"))
	if len(n.Tags) > 0 {
		b.WriteString(" " + strings.Join(n.Tags, ","))
	}
	b.WriteString(": " + n.Text)
	return b.String()
}

// parseMerges đọc output model: lấy đoạn '{'…'}' (bỏ fence/lời dẫn), unmarshal.
func parseMerges(s string) ([]merge, error) {
	s = strings.TrimSpace(s)
	if i, j := strings.Index(s, "{"), strings.LastIndex(s, "}"); i >= 0 && j > i {
		s = s[i : j+1]
	}
	var out struct {
		Merges []merge `json:"merges"`
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("JSON không hợp lệ: %w", err)
	}
	return out.Merges, nil
}

// validateMerges: chặt, vi phạm → lỗi (job retry, không bỏ qua lặng lẽ).
func validateMerges(ms []merge, batch []store.Note) error {
	inBatch := make(map[int64]store.Note, len(batch))
	for _, n := range batch {
		inBatch[n.ID] = n
	}
	used := map[int64]bool{}
	for i, m := range ms {
		ok := false
		for _, k := range knowledgeKinds {
			if k == m.Kind {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("merges[%d]: kind %q không thuộc tầng kiến thức", i, m.Kind)
		}
		if strings.TrimSpace(m.Text) == "" {
			return fmt.Errorf("merges[%d]: text rỗng", i)
		}
		if len(m.Notes) < 2 {
			return fmt.Errorf("merges[%d]: cần ≥2 note", i)
		}
		seen := map[int64]bool{}
		for _, id := range m.Notes {
			n, in := inBatch[id]
			if !in {
				return fmt.Errorf("merges[%d]: note #%d không thuộc batch", i, id)
			}
			if seen[id] {
				return fmt.Errorf("merges[%d]: note #%d lặp", i, id)
			}
			seen[id] = true
			if used[id] {
				return fmt.Errorf("merges[%d]: note #%d đã dùng ở merge khác", i, id)
			}
			used[id] = true
			if n.Kind != m.Kind {
				return fmt.Errorf("merges[%d]: kind %q khác kind note #%d (%s)", i, m.Kind, id, n.Kind)
			}
		}
	}
	return nil
}

// guardSkip: model đòi xoá > nửa batch (từ 8 note) → nghi ngờ, bỏ nguyên batch.
// deletions = tổng note mất đi = Σ(len(notes)-1) (mỗi merge giữ lại 1 note kết quả).
func guardSkip(batch []store.Note, ms []merge) bool {
	if len(batch) < minGuardBatch {
		return false
	}
	deletions := 0
	for _, m := range ms {
		deletions += len(m.Notes) - 1
	}
	return deletions*2 > len(batch)
}

// applyMerge ghi note gộp rồi xoá cứng note cũ (trừ chính note vừa ghi — nội
// dung trùng một note cũ thì UpsertNote trả về id note đó). Tag = hợp nhất tag
// các note cũ (giữ đường lọc quen thuộc). Trả số note đã xoá.
func (r *Runner) applyMerge(ctx context.Context, spaceID int64, proj string, m merge, batch []store.Note) (int, error) {
	byID := make(map[int64]store.Note, len(batch))
	for _, n := range batch {
		byID[n.ID] = n
	}
	var tags []string
	seen := map[string]bool{}
	for _, id := range m.Notes {
		for _, t := range byID[id].Tags {
			if !seen[t] {
				seen[t] = true
				tags = append(tags, t)
			}
		}
	}
	res, err := r.b.WriteNote(ctx, brain.WriteParams{
		SpaceID: spaceID, Kind: m.Kind, Text: m.Text, Tags: tags,
		Source: "consolidate", Project: orNone(proj),
	})
	if err != nil {
		return 0, err
	}
	var old []int64
	for _, id := range m.Notes {
		if id != res.NoteID {
			old = append(old, id)
		}
	}
	if len(old) == 0 {
		return 0, nil
	}
	// xoá mềm: model gộp sai vẫn cứu được trong retention.jobs_days, sau đó
	// purge xoá thật.
	n := 0
	for _, id := range old {
		ok, err := r.st.SoftDeleteNote(ctx, id, time.Now())
		if err != nil {
			return n, err
		}
		if ok {
			n++
		}
	}
	return n, nil
}

// projectsOf: danh sách project có note, sắp tất định. Batch không trộn hai
// project — note gộp phải thuộc một project.
func projectsOf(notes []store.Note) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range notes {
		if !seen[n.Project] {
			seen[n.Project] = true
			out = append(out, n.Project)
		}
	}
	sort.Strings(out)
	return out
}

// filterProjectKind: note của đúng (project, kind) — batch không trộn project
// hay kind: gộp chỉ có nghĩa trong cùng kind (validate bắt buộc), tách kind để
// model không phải lọc nhiễu và batch đúng phạm vi so với guard.
func filterProjectKind(notes []store.Note, proj, kind string) []store.Note {
	out := make([]store.Note, 0, len(notes))
	for _, n := range notes {
		if n.Project == proj && n.Kind == kind {
			out = append(out, n)
		}
	}
	return out
}

// packBatches cắt danh sách note thành các batch ≤ limit rune (theo dòng);
// một dòng dài hơn limit đứng riêng một batch.
func packBatches(notes []store.Note, limit int) [][]store.Note {
	var out [][]store.Note
	var cur []store.Note
	size := 0
	for _, n := range notes {
		ln := utf8.RuneCountInString(noteLine(n)) + 1
		if len(cur) > 0 && size+ln > limit {
			out = append(out, cur)
			cur, size = nil, 0
		}
		cur = append(cur, n)
		size += ln
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// orNone: project rỗng → "-" (Brain không gắn project của tiến trình khi ghi).
func orNone(p string) string {
	if p == "" {
		return "-"
	}
	return p
}
