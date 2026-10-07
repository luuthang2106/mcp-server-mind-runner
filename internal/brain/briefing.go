package brain

import (
	"context"
	"fmt"
	"strings"
	"time"

	"mind-runner/internal/store"
)

// StaleTaskAfter: task quá 14 ngày không đụng = stale (chú thích trong
// briefing; M5.1 dùng lại cho task_list).
const StaleTaskAfter = 14 * 24 * time.Hour

const defaultBriefingBudget = 1500

// BriefingParams tham số dựng briefing.
type BriefingParams struct {
	SpaceID int64
	Force   bool
	Now     time.Time
}

// Briefing kết quả: Delivered=false kèm Reason khi gate chặn.
type Briefing struct {
	Delivered bool
	Reason    string
	Text      string
}

// SetBriefingBudget đặt ngân sách token briefing (0/âm → giữ mặc định 1500).
func (b *Brain) SetBriefingBudget(n int) { b.briefBudget = n }

// BriefingDay trả nhãn "ngày làm việc" (YYYY-MM-DD) chứa now, tính theo
// [briefing].day_start_hour (mặc định 4h sáng — thức khuya qua nửa đêm vẫn
// là hôm trước). Dùng chung cho gate briefing và hook recap đầu ngày.
func (b *Brain) BriefingDay(now time.Time) string {
	if b.cfg == nil {
		return now.Format("2006-01-02")
	}
	return b.cfg.DayStart(now).Format("2006-01-02")
}

func briefingKey(spaceID int64) string { return fmt.Sprintf("last_briefing_date:%d", spaceID) }

// BriefedToday: space đã nhận briefing trong ngày làm việc hiện tại chưa
// (kiểm nhanh, không dựng văn bản — hook gọi mỗi prompt).
func (b *Brain) BriefedToday(ctx context.Context, spaceID int64, now time.Time) (bool, error) {
	last, ok, err := b.st.GetMeta(ctx, briefingKey(spaceID))
	if err != nil {
		return false, err
	}
	return ok && last == b.BriefingDay(now), nil
}

// Briefing dựng văn bản tất định cho space (task mở → quyết định → sở thích →
// phiên gần đây → notes mới), gate 1 lần/ngày làm việc/space qua meta
// last_briefing_date:<space_id>. Gate là compare-and-set: hai hook chạy cùng
// lúc (SessionStart + UserPromptSubmit, hay 2 cửa sổ) chỉ một bên nhận.
func (b *Brain) Briefing(ctx context.Context, p BriefingParams) (Briefing, error) {
	day := b.BriefingDay(p.Now)
	if !p.Force {
		done, err := b.BriefedToday(ctx, p.SpaceID, p.Now)
		if err != nil {
			return Briefing{}, err
		}
		if done {
			return Briefing{Delivered: false, Reason: "already_briefed_today"}, nil
		}
	}
	text, err := b.assembleBriefing(ctx, p)
	if err != nil {
		return Briefing{}, err
	}
	// ghi meta SAU khi assemble xong (kể cả khi text rỗng) — lỗi assemble thì
	// không đốt lượt briefing của ngày.
	changed, err := b.st.SetMetaIfChanged(ctx, briefingKey(p.SpaceID), day)
	if err != nil {
		return Briefing{}, err
	}
	if !changed && !p.Force {
		return Briefing{Delivered: false, Reason: "already_briefed_today"}, nil
	}
	return Briefing{Delivered: true, Text: text}, nil
}

// assembleBriefing gom nguồn + render greedy theo ngân sách token.
func (b *Brain) assembleBriefing(ctx context.Context, p BriefingParams) (string, error) {
	sps, err := b.st.Spaces(ctx)
	if err != nil {
		return "", err
	}
	spaceName := ""
	for _, sp := range sps {
		if sp.ID == p.SpaceID {
			spaceName = sp.Name
		}
	}

	tasks, err := b.st.OpenTasks(ctx, p.SpaceID, 20)
	if err != nil {
		return "", err
	}
	// Hạn tính theo ngày làm việc (day_start_hour) — 1h sáng vẫn là "hôm qua".
	today := b.BriefingDay(p.Now)
	soon := today
	if d, err := time.Parse("2006-01-02", today); err == nil {
		soon = d.AddDate(0, 0, dueSoonDays).Format("2006-01-02")
	}
	dueTasks, err := b.st.DueTasks(ctx, p.SpaceID, soon, 10)
	if err != nil {
		return "", err
	}
	waiting, err := b.st.WaitingTasks(ctx, p.SpaceID, 10)
	if err != nil {
		return "", err
	}
	decisions, err := b.st.NotesByKind(ctx, p.SpaceID, "decision", p.Now.AddDate(0, 0, -30), 12)
	if err != nil {
		return "", err
	}
	prefs, err := b.st.NotesByKind(ctx, p.SpaceID, "preference", time.Time{}, 12)
	if err != nil {
		return "", err
	}
	episodes, err := b.st.RecentEpisodes(ctx, p.SpaceID, 5)
	if err != nil {
		return "", err
	}
	notes, err := b.st.NotesByKind(ctx, p.SpaceID, "note", p.Now.AddDate(0, 0, -7), 10)
	if err != nil {
		return "", err
	}
	rels, err := b.st.RecentRelations(ctx, p.SpaceID, p.Now.AddDate(0, 0, -30), 10)
	if err != nil {
		return "", err
	}

	// Mục theo đúng thứ tự section; dedup theo note ID giữa các mục.
	type section struct {
		title string
		lines []string
	}
	// Mỗi task chỉ hiện một lần: quá hạn/đến hạn > đang chờ > còn lại.
	seenTask := map[int64]bool{}
	taskLines := func(ts []store.Task) []string {
		var lines []string
		for _, t := range ts {
			if seenTask[t.ID] {
				continue
			}
			seenTask[t.ID] = true
			lines = append(lines, taskLine(t, today, p.Now))
		}
		return lines
	}
	dueLines := taskLines(dueTasks)
	waitLines := taskLines(waiting)
	openLines := taskLines(tasks)
	seen := map[int64]bool{}
	noteLines := func(ns []store.Note) []string {
		var lines []string
		for _, n := range ns {
			if seen[n.ID] {
				continue
			}
			seen[n.ID] = true
			line := fmt.Sprintf("- %s: %s", n.UpdatedAt.UTC().Format("2006-01-02"), n.Text)
			if n.Meta.Why != "" && !strings.Contains(n.Text, n.Meta.Why) {
				line += " (vì: " + n.Meta.Why + ")"
			}
			if n.Meta.Scope != "" {
				line += " [" + n.Meta.Scope + "]"
			}
			lines = append(lines, line)
		}
		return lines
	}
	var epLines []string
	for _, e := range episodes {
		epLines = append(epLines, fmt.Sprintf("- %s: %s", e.At.UTC().Format("2006-01-02"), e.Summary))
	}
	seenRel := map[string]bool{}
	var relLines []string
	for _, r := range rels {
		key := r.FromRef + "\x00" + r.ToRef + "\x00" + r.RelType
		if seenRel[key] {
			continue
		}
		seenRel[key] = true
		relLines = append(relLines, fmt.Sprintf("- %s → %s (%s)", r.FromRef, r.ToRef, r.RelType))
	}
	sections := []section{
		{"Quá hạn / sắp đến hạn", dueLines},
		{"Đang chờ người khác", waitLines},
		{"Việc đang mở", openLines},
		{"Quyết định gần đây", noteLines(decisions)},
		{"Sở thích", noteLines(prefs)},
		{"Liên quan", relLines},
		{"Phiên gần đây", epLines},
		{"Notes mới", noteLines(notes)},
	}

	// Render greedy: item chỉ vào nếu text+header(nếu chưa)+item lọt ngân sách;
	// item không lọt bị đếm để báo cuối, KHÔNG dừng cứng — item sau vẫn thử.
	budget := b.briefBudget
	if budget <= 0 {
		budget = defaultBriefingBudget
	}
	text := fmt.Sprintf("# Briefing — %s — %s\n\n", spaceName, p.Now.Format("2006-01-02"))
	cut := 0
	for _, sec := range sections {
		header := "## " + sec.title + "\n"
		addedHeader := false
		for _, line := range sec.lines {
			next := text
			if !addedHeader {
				next += header
			}
			next += line + "\n"
			if EstTokens(next) <= budget {
				text = next
				addedHeader = true
			} else {
				cut++
			}
		}
	}
	if cut > 0 {
		text += fmt.Sprintf("… (còn %d mục bị cắt do ngân sách)\n", cut)
	}
	return text, nil
}

// DefaultSpace: [spaces].default trong config (mặc định "personal") — dùng khi
// tool không truyền space.
func (b *Brain) DefaultSpace() string {
	if b.cfg != nil && b.cfg.Spaces.Default != "" {
		return b.cfg.Spaces.Default
	}
	return "personal"
}

// dueSoonDays: task có hạn trong N ngày tới được đưa lên mục "sắp đến hạn".
const dueSoonDays = 3

// taskLine render một task cho briefing: id, hạn, người, bước kế.
func taskLine(t store.Task, today string, now time.Time) string {
	var tagsB []string
	switch {
	case t.DueAt != "" && t.DueAt < today:
		tagsB = append(tagsB, "QUÁ HẠN "+t.DueAt)
	case t.DueAt != "":
		tagsB = append(tagsB, "hạn "+t.DueAt)
	default:
		tagsB = append(tagsB, "cập nhật "+t.UpdatedAt.UTC().Format("2006-01-02"))
	}
	if now.Sub(t.UpdatedAt) > StaleTaskAfter {
		tagsB = append(tagsB, "stale")
	}
	line := fmt.Sprintf("- #%d (%s): %s", t.ID, strings.Join(tagsB, ", "), t.Title)
	if t.Owner != "" {
		line += " — người làm: " + t.Owner
	}
	if t.WaitingOn != "" {
		line += " — chờ: " + t.WaitingOn
	}
	if t.NextStep != nil && *t.NextStep != "" {
		line += " — bước kế: " + *t.NextStep
	}
	return line
}
