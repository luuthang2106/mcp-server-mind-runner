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

	// Việc của project đang mở lên đầu; hạn/đang chờ lấy từ mọi project.
	tasks, err := b.st.QueryTasks(ctx, p.SpaceID, store.TaskQuery{Status: "open", Prefer: b.project, Limit: 20})
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
			lines = append(lines, taskLine(t, today, p.Now, b.project))
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
			line += projectTag(n.Project, b.project)
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
	capLines, err := b.capturedLines(ctx, p.SpaceID, p.Now)
	if err != nil {
		return "", err
	}
	sections := []section{
		{"Đã ghi kể từ recap trước (sai thì sửa/xoá theo #id)", capLines},
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
	title := p.Now.Format("2006-01-02")
	if b.project != "" {
		title += " — project " + b.project
	}
	if spaceName != "" && spaceName != b.DefaultSpace() {
		title += " — space " + spaceName
	}
	text := "# Briefing — " + title + "\n\n"
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
func taskLine(t store.Task, today string, now time.Time, cur string) string {
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
	line := fmt.Sprintf("- #%d (%s): %s", t.ID, strings.Join(tagsB, ", "), t.Title) + projectTag(t.Project, cur)
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

// capturedSources: note do agent/hook tự ghi (không gồm ingest/media mà người
// dùng chủ động nạp) — đây là thứ cần người dùng liếc lại.
var capturedSources = []string{"hook:stop", "tool:remember"}

// capturedMax: số dòng tối đa của mục "Đã ghi kể từ recap trước".
const capturedMax = 12

// capturedWindow: [đầu ngày làm việc của lần briefing trước, đầu hôm nay).
// Chưa từng briefing / briefing quá 7 ngày trước / đã briefing hôm nay (force)
// → chỉ lấy ngày làm việc hôm qua.
func (b *Brain) capturedWindow(ctx context.Context, spaceID int64, now time.Time) (time.Time, time.Time, error) {
	dayStart := func(t time.Time) time.Time {
		if b.cfg == nil {
			return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
		}
		return b.cfg.DayStart(t)
	}
	to := dayStart(now)
	from := to.AddDate(0, 0, -1)
	last, ok, err := b.st.GetMeta(ctx, briefingKey(spaceID))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if ok {
		if d, err := time.ParseInLocation("2006-01-02", last, now.Location()); err == nil {
			ls := dayStart(d.Add(12 * time.Hour))
			if ls.Before(to) && !ls.Before(to.AddDate(0, 0, -7)) {
				from = ls
			}
		}
	}
	return from, to, nil
}

// capturedLines: note/việc được ghi tự động trong cửa sổ kể từ recap trước,
// kèm #id để người dùng sửa nhanh ("mind fix: #12 …", "mind forget: #34").
func (b *Brain) capturedLines(ctx context.Context, spaceID int64, now time.Time) ([]string, error) {
	from, to, err := b.capturedWindow(ctx, spaceID, now)
	if err != nil {
		return nil, err
	}
	notes, err := b.st.NotesCreatedBetween(ctx, spaceID, from, to, capturedSources, 100)
	if err != nil {
		return nil, err
	}
	created, err := b.st.TasksCreatedBetween(ctx, spaceID, from, to, 100)
	if err != nil {
		return nil, err
	}
	closed, err := b.st.TasksClosedBetween(ctx, spaceID, from, to, 100)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, t := range created {
		st := "việc mới"
		switch t.Status {
		case "done":
			st = "việc mới, đã xong"
		case "dropped":
			st = "việc mới, đã bỏ"
		}
		lines = append(lines, fmt.Sprintf("- task #%d (%s): %s", t.ID, st, clipText(t.Title, 120))+projectTag(t.Project, b.project))
	}
	for _, t := range closed {
		st := "đã xong"
		if t.Status == "dropped" {
			st = "đã bỏ"
		}
		lines = append(lines, fmt.Sprintf("- task #%d (%s): %s", t.ID, st, clipText(t.Title, 120))+projectTag(t.Project, b.project))
	}
	for _, n := range notes {
		line := fmt.Sprintf("- note #%d [%s]: %s", n.ID, n.Kind, clipText(n.Text, 120)) + projectTag(n.Project, b.project)
		if n.Status == "superseded" && n.SupersededBy != nil {
			line += fmt.Sprintf(" (đã bị #%d thay)", *n.SupersededBy)
		}
		lines = append(lines, line)
	}
	if len(lines) > capturedMax {
		more := len(lines) - capturedMax
		lines = append(lines[:capturedMax], fmt.Sprintf("- … và %d mục nữa (task_list / recall để xem)", more))
	}
	return lines, nil
}

// clipText cắt theo rune, thêm "…" khi dài hơn n.
func clipText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// SetProject đặt project của tiến trình (MCP server chạy với cwd = thư mục dự
// án): note/task ghi qua tool được gắn nhãn này, recall ưu tiên nó, briefing
// đưa việc của nó lên đầu. Rỗng = chung (Claude Desktop).
func (b *Brain) SetProject(name string) { b.project = name }

// Project: project của tiến trình ("" = chung).
func (b *Brain) Project() string { return b.project }

// projectFor: "" → project tiến trình; "-" → không gắn project.
func (b *Brain) projectFor(p string) string {
	switch p {
	case "":
		return b.project
	case "-":
		return ""
	}
	return p
}

// projectTag: " [project]" khi mục thuộc project khác project đang mở (mục của
// project hiện tại hoặc mục chung không cần nhãn).
func projectTag(p, cur string) string {
	if p == "" || p == cur {
		return ""
	}
	return " [" + p + "]"
}
