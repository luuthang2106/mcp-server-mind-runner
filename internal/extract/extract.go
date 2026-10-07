// Package extract: worker xử lý delta thô của phiên — model trích note/task/
// relation (extract_session) và tóm tắt phiên thành episode (summarize_session).
package extract

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"mind-runner/internal/brain"
	"mind-runner/internal/config"
	"mind-runner/internal/egress"
	"mind-runner/internal/store"
)

//go:embed prompts/extract.md
var extractPrompt string

//go:embed prompts/summarize.md
var summarizePrompt string

// summarizeMaxRunes: trần input tóm tắt — phiên dài vượt context model → 4xx
// mọi lần retry; giữ phần cuối (mới nhất).
const summarizeMaxRunes = 48_000

// Extractor xử lý extract_session/summarize_session.
type Extractor struct {
	st  *store.Store
	b   *brain.Brain
	eg  *egress.Egress
	cfg *config.Config // nguồn chân lý policy của space ([spaces.policy])
}

func New(st *store.Store, b *brain.Brain, eg *egress.Egress, cfg *config.Config) *Extractor {
	return &Extractor{st: st, b: b, eg: eg, cfg: cfg}
}

// policyForSpace resolve policy của phiên theo space: space_id → tên → config.
func (x *Extractor) policyForSpace(ctx context.Context, spaceID int64) (egress.Policy, error) {
	sp, err := x.st.SpaceByID(ctx, spaceID)
	if err != nil {
		return "", err
	}
	return egress.Policy(x.cfg.SpacePolicy(sp.Name)), nil
}

// RunRaw: job extract_session dạng cũ ({"raw_id":…}, tạo trước schema 5) —
// gom luôn cả phiên chứa raw đó.
func (x *Extractor) RunRaw(ctx context.Context, rawID int64) error {
	raw, err := x.st.GetRaw(ctx, rawID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // raw đã bị purge
	}
	if err != nil {
		return err
	}
	return x.RunSession(ctx, raw.SessionID)
}

// RunSession gom MỌI raw chưa extract của phiên (theo seq) → condense → model
// trích xuất (chia cửa sổ) → ghi notes/tasks/relations → đánh dấu raw đã
// extract. Writes chỉ xảy ra sau khi parse thành công toàn bộ; job retry an
// toàn (raw chỉ đánh dấu khi xong; notes idempotent, task chống trùng
// open-title); relations có thể lặp khi retry giữa chừng — hiếm, chấp nhận.
func (x *Extractor) RunSession(ctx context.Context, sessionID string) error {
	raws, err := x.st.PendingRaws(ctx, sessionID)
	if err != nil || len(raws) == 0 {
		return err
	}
	sess, err := x.st.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	pol, err := x.policyForSpace(ctx, sess.SpaceID)
	if err != nil {
		return err
	}
	// Chỉ gửi hội thoại thật (condense), chia cửa sổ để phiên dài không thành
	// một request khổng lồ.
	var b strings.Builder
	ids := make([]int64, 0, len(raws))
	rawBytes := 0
	for _, r := range raws {
		text, err := gunzip(r.Content)
		if err != nil {
			return fmt.Errorf("raw %d: %w", r.ID, err)
		}
		rawBytes += len(text)
		b.WriteString(condense(text))
		ids = append(ids, r.ID)
	}
	convo := b.String()
	now := time.Now()
	if strings.TrimSpace(convo) == "" {
		return x.st.MarkRawsExtracted(ctx, ids, now)
	}
	wins, dropped := windows(convo, extractWindowRunes, extractMaxWindows)
	if lg := x.eg.UsageLog(); lg != nil && (dropped > 0 || len(wins) > 2) {
		// chỉ số đo, không nội dung — lượng hội thoại bất thường
		lg.Warn("extract: delta dài", "raws", len(raws), "raw_bytes", rawBytes,
			"condensed_runes", utf8.RuneCountInString(convo), "windows", len(wins), "dropped_windows", dropped)
	}
	// Việc đang mở của space đi kèm mỗi cửa sổ: model đóng/cập nhật đúng việc
	// thay vì tạo việc trùng. Chỉ id trong danh sách này được phép cập nhật.
	open, err := x.st.OpenTasks(ctx, sess.SpaceID, openTasksForExtract)
	if err != nil {
		return err
	}
	openIDs := map[int64]bool{}
	for _, t := range open {
		openIDs[t.ID] = true
	}
	prefix := openTasksBlock(open)
	ex := &Extraction{}
	for _, w := range wins {
		out, err := x.eg.Chat(ctx, pol, extractPrompt, prefix+w)
		if err != nil {
			return err
		}
		part, err := ParseExtraction(out)
		if err != nil {
			return err
		}
		base := len(ex.Notes)
		for _, r := range part.Relations {
			if r.NoteIndex != nil {
				if *r.NoteIndex >= 0 && *r.NoteIndex < len(part.Notes) {
					i := *r.NoteIndex + base
					r.NoteIndex = &i
				} else {
					r.NoteIndex = nil
				}
			}
			ex.Relations = append(ex.Relations, r)
		}
		ex.Notes = append(ex.Notes, part.Notes...)
		ex.Tasks = append(ex.Tasks, part.Tasks...)
		ex.TaskUpdates = append(ex.TaskUpdates, part.TaskUpdates...)
	}

	plan, derr := x.planDedupe(ctx, pol, sess.SpaceID, ex.Notes)
	lg := x.eg.UsageLog()
	if derr != nil && lg != nil {
		lg.Warn("extract: bỏ qua dò trùng", "err", shortErr(derr))
	}
	var stat struct{ skipped, superseded, updated, badIDs int }

	noteIDs := make([]int64, len(ex.Notes))
	for i, n := range ex.Notes {
		if plan.skip[i] {
			stat.skipped++
			noteIDs[i] = -1
			continue
		}
		sid := sessionID
		res, err := x.b.WriteNote(ctx, brain.WriteParams{
			SpaceID: sess.SpaceID, Kind: n.Kind, Text: n.Text, Tags: n.Tags,
			Source: "hook:stop", SessionID: &sid,
			Meta: store.NoteMeta{
				Why: strings.TrimSpace(n.Why), Who: n.Who, When: strings.TrimSpace(n.When),
				AsOf: strings.TrimSpace(n.AsOf), Ref: strings.TrimSpace(n.Ref),
				Alternatives: n.Alternatives, Scope: strings.TrimSpace(n.Scope),
			},
		})
		if err != nil {
			return fmt.Errorf("notes[%d]: %w", i, err)
		}
		noteIDs[i] = res.NoteID
		if old := plan.supersede[i]; old != 0 && old != res.NoteID {
			if err := x.st.SupersedeNote(ctx, old, res.NoteID, now); err != nil {
				return fmt.Errorf("notes[%d] supersede %d: %w", i, old, err)
			}
			stat.superseded++
		}
	}
	for i, tk := range ex.Tasks {
		f := store.TaskFields{
			NextStep: nonEmpty(tk.NextStep), Why: nonEmpty(tk.Why), Owner: nonEmpty(tk.Owner),
			WaitingOn: nonEmpty(tk.WaitingOn), DueAt: nonEmpty(tk.Due), Constraints: nonEmpty(tk.Constraints),
		}
		if _, _, err := x.st.InsertTaskWith(ctx, sess.SpaceID, tk.Title, f, now); err != nil {
			return fmt.Errorf("tasks[%d]: %w", i, err)
		}
	}
	for _, u := range ex.TaskUpdates {
		if !openIDs[u.ID] {
			stat.badIDs++ // model bịa id / id không thuộc space → bỏ
			continue
		}
		f := store.TaskFields{NextStep: nonEmpty(u.NextStep), WaitingOn: nonEmpty(u.WaitingOn), DueAt: nonEmpty(u.Due)}
		var status *string
		if u.Status != "" {
			status = &u.Status
		}
		if _, err := x.st.UpdateTaskWith(ctx, u.ID, status, f, now, true); err != nil {
			return fmt.Errorf("task_updates #%d: %w", u.ID, err)
		}
		stat.updated++
	}
	if lg != nil && (stat.skipped+stat.superseded+stat.badIDs > 0) {
		// chỉ số đo (không nội dung) — để chỉnh ngưỡng dò trùng
		lg.Info("extract: dò trùng", "notes", len(ex.Notes), "skipped", stat.skipped,
			"superseded", stat.superseded, "task_updates", stat.updated, "bad_task_ids", stat.badIDs)
	}
	for i, r := range ex.Relations {
		var src *int64
		if r.NoteIndex != nil && *r.NoteIndex >= 0 && *r.NoteIndex < len(noteIDs) && noteIDs[*r.NoteIndex] > 0 {
			src = &noteIDs[*r.NoteIndex]
		}
		if _, err := x.st.InsertRelation(ctx, sess.SpaceID, r.From, r.To, r.Type, src, now); err != nil {
			return fmt.Errorf("relations[%d]: %w", i, err)
		}
	}
	return x.st.MarkRawsExtracted(ctx, ids, now)
}

// SummarizeSession gom delta thô của session theo seq → model tóm tắt →
// episode (seq tăng dần qua InsertEpisode).
func (x *Extractor) SummarizeSession(ctx context.Context, sessionID string) error {
	raws, err := x.st.RawsForSession(ctx, sessionID)
	if err != nil {
		return err
	}
	sess, err := x.st.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	pol, err := x.policyForSpace(ctx, sess.SpaceID)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, r := range raws {
		text, err := gunzip(r.Content)
		if err != nil {
			return fmt.Errorf("raw %d: %w", r.ID, err)
		}
		buf.WriteString(condense(text))
	}
	input, cut := tailRunes(buf.String(), summarizeMaxRunes)
	if cut {
		input = "[Đã cắt bớt phần đầu phiên — chỉ còn phần gần nhất]\n" + input
	}
	summary, err := x.eg.Chat(ctx, pol, summarizePrompt, input)
	if err != nil {
		return err
	}
	_, err = x.st.InsertEpisode(ctx, sessionID, strings.TrimSpace(summary), time.Now())
	return err
}

// tailRunes giữ n rune cuối của s (cắt đúng biên rune); cut=true nếu đã cắt.
func tailRunes(s string, n int) (string, bool) {
	if utf8.RuneCountInString(s) <= n {
		return s, false
	}
	i := len(s)
	for k := 0; k < n; k++ {
		_, size := utf8.DecodeLastRuneInString(s[:i])
		i -= size
	}
	return s[i:], true
}

func gunzip(b []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("gunzip: %w", err)
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// nonEmpty: chuỗi rỗng → nil (trường không được nói tới, không ghi đè).
func nonEmpty(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

func shortErr(err error) string {
	s := err.Error()
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
