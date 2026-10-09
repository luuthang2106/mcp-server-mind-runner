package brain

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mind-runner/internal/store"
)

var update = flag.Bool("update", false, "ghi lại golden files")

// seedBriefingFixture: dữ liệu tất định cho briefing — 2 task open (1 stale,
// 1 tươi) + 1 task done, 2 decision (1 ngoài cửa sổ 30 ngày), 2 preference,
// 2 episode (1 phiên space khác), 2 note (1 ngoài cửa sổ 7 ngày, 1 space khác).
func seedBriefingFixture(t *testing.T, st *store.Store, now time.Time) {
	t.Helper()
	ctx := context.Background()
	tx := func(y int, mo time.Month, d, h int) time.Time {
		return time.Date(y, mo, d, h, 0, 0, 0, time.UTC)
	}
	iso := func(tm time.Time) string { return tm.Format(time.RFC3339Nano) }
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.DB().ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}

	// sessions + episodes (raw SQL — InsertEpisode thuộc Task 3.5)
	exec(`INSERT INTO sessions(id, client, space_id, started_at, last_seen_at, transcript_offset)
	      VALUES('sess-brief','claude-code',1,?,?,0)`, iso(now.AddDate(0, 0, -3)), iso(now))
	exec(`INSERT INTO sessions(id, client, space_id, started_at, last_seen_at, transcript_offset)
	      VALUES('sess-work','claude-code',2,?,?,0)`, iso(now.AddDate(0, 0, -2)), iso(now))
	exec(`INSERT INTO episodes(session_id, seq, summary, at) VALUES('sess-brief',1,?,?)`,
		"Bàn thiết kế chunker", iso(tx(2026, 10, 3, 15)))
	exec(`INSERT INTO episodes(session_id, seq, summary, at) VALUES('sess-brief',2,?,?)`,
		"Đã bàn thiết kế mind-runner, chốt D12/D13", iso(tx(2026, 10, 5, 17)))
	exec(`INSERT INTO episodes(session_id, seq, summary, at) VALUES('sess-work',1,?,?)`,
		"Họp work — không thuộc space này", iso(tx(2026, 10, 4, 9)))

	// tasks
	exec(`INSERT INTO tasks(space_id, title, status, next_step, updated_at) VALUES(1,?,'open',NULL,?)`,
		"Chuẩn bị demo mind-runner", iso(tx(2026, 10, 5, 9)))
	exec(`INSERT INTO tasks(space_id, title, status, next_step, updated_at) VALUES(1,?,'open',?,?)`,
		"Nộp báo cáo quý", "viết phần 2", iso(tx(2026, 9, 20, 10)))
	exec(`INSERT INTO tasks(space_id, title, status, next_step, updated_at) VALUES(1,?,'done',NULL,?)`,
		"Việc đã xong", iso(tx(2026, 10, 4, 9)))

	// notes
	note := func(spaceID int64, kind, text string, at time.Time) {
		t.Helper()
		if _, _, err := st.UpsertNote(ctx, &store.Note{
			SpaceID: spaceID, Kind: kind, Text: text, Source: "test", CreatedAt: at, UpdatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	note(1, "decision", "Dùng SQLite thay Postgres", tx(2026, 10, 3, 11))
	note(1, "decision", "Phương án embedded Postgres", tx(2026, 8, 1, 11)) // ngoài 30 ngày
	note(1, "preference", "Thích trả lời ngắn, đi thẳng kết luận", tx(2026, 9, 20, 11))
	note(1, "preference", "Ưu tiên tiếng Việt trong ghi chú", tx(2026, 9, 1, 11))
	note(1, "note", "Ví dụ workflow release", tx(2026, 10, 5, 11))
	note(1, "note", "Ghi chú cũ ngoài 7 ngày", tx(2026, 9, 1, 11))
	note(2, "note", "Ghi chú space khác", tx(2026, 10, 5, 11))

	// relations: 1 cặp trùng bộ ba (from,to,type — briefing chỉ hiện 1 dòng),
	// 1 ngoài cửa sổ 30 ngày, 1 space khác.
	rel := func(spaceID int64, from, to, rtype string, at time.Time) {
		t.Helper()
		if _, err := st.InsertRelation(ctx, spaceID, from, to, rtype, nil, at); err != nil {
			t.Fatal(err)
		}
	}
	rel(1, "Chuẩn bị demo mind-runner", "mind-runner", "mentions", tx(2026, 10, 5, 10))
	rel(1, "Chuẩn bị demo mind-runner", "mind-runner", "mentions", tx(2026, 10, 4, 10))
	rel(1, "Nộp báo cáo quý", "kế toán quý 4", "related", tx(2026, 9, 25, 10))
	rel(1, "Dự án bị bỏ", "hồ sơ X", "related", tx(2026, 8, 27, 10)) // ngoài 30 ngày
	rel(2, "Space hai", "không lọt", "related", tx(2026, 10, 5, 10))
}

func briefingFor(t *testing.T, st *store.Store, now time.Time, budget int) Briefing {
	t.Helper()
	ctx := context.Background()
	b := New(st, nil, testCfg())
	b.SetBriefingBudget(budget)
	spaceID, err := st.SpaceByName(ctx, "personal")
	if err != nil {
		t.Fatal(err)
	}
	res, err := b.Briefing(ctx, BriefingParams{SpaceID: spaceID, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestBriefingGolden: fixture cố định + Now cố định → byte-exact với golden.
func TestBriefingGolden(t *testing.T) {
	st := newStore(t)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	seedBriefingFixture(t, st, now)

	res := briefingFor(t, st, now, 0)
	if !res.Delivered {
		t.Fatalf("delivered=false reason=%q", res.Reason)
	}
	if res.Reason != "" {
		t.Fatalf("reason=%q", res.Reason)
	}

	golden := filepath.Join("testdata", "briefing_golden.md")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(res.Text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("golden chưa có — chạy: go test ./internal/brain/ -run TestBriefingGolden -update: %v", err)
	}
	if res.Text != string(want) {
		t.Fatalf("briefing khác golden:\n--- got ---\n%s\n--- want ---\n%s", res.Text, want)
	}
}

// TestBriefingRelations: mục "## Liên quan" đúng vị trí (sau Sở thích, trước
// Phiên gần đây), dedup bộ ba (from,to,type), relation ngoài 30 ngày / space
// khác không lọt.
func TestBriefingRelations(t *testing.T) {
	st := newStore(t)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	seedBriefingFixture(t, st, now)

	text := briefingFor(t, st, now, 0).Text
	if !strings.Contains(text, "## Liên quan") {
		t.Fatalf("thiếu mục Liên quan:\n%s", text)
	}
	if got := strings.Count(text, "Chuẩn bị demo mind-runner → mind-runner (mentions)"); got != 1 {
		t.Fatalf("dedup bộ ba: %d dòng, muốn 1:\n%s", got, text)
	}
	if got := strings.Count(text, "Nộp báo cáo quý → kế toán quý 4 (related)"); got != 1 {
		t.Fatalf("relation thường: %d dòng, muốn 1:\n%s", got, text)
	}
	if strings.Contains(text, "Dự án bị bỏ") {
		t.Fatalf("relation ngoài 30 ngày không được lọt:\n%s", text)
	}
	if strings.Contains(text, "Space hai") {
		t.Fatalf("relation space khác không được lọt:\n%s", text)
	}
	iPref := strings.Index(text, "## Sở thích")
	iRel := strings.Index(text, "## Liên quan")
	iEp := strings.Index(text, "## Phiên gần đây")
	if iPref >= iRel || iRel >= iEp {
		t.Fatalf("vị trí mục sai (%d, %d, %d):\n%s", iPref, iRel, iEp, text)
	}
}

// TestBriefingBudgetCutsTail: ngân sách nhỏ → cắt đuôi, đếm đúng số mục bị cắt.
func TestBriefingBudgetCutsTail(t *testing.T) {
	st := newStore(t)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	seedBriefingFixture(t, st, now)

	res := briefingFor(t, st, now, 60)
	if !res.Delivered {
		t.Fatalf("delivered=false reason=%q", res.Reason)
	}
	const total = 10 // 2 task + 1 decision + 2 sở thích + 2 liên quan + 2 episode + 1 note
	idx := strings.Index(res.Text, "… (còn ")
	if idx < 0 {
		t.Fatalf("thiếu dòng cắt đuôi:\n%s", res.Text)
	}
	var cut int
	if _, err := fmt.Sscanf(res.Text[idx:], "… (còn %d mục bị cắt do ngân sách)", &cut); err != nil {
		t.Fatalf("parse dòng cắt: %v\n%s", err, res.Text)
	}
	shown := strings.Count(res.Text, "\n- ")
	if cut == 0 {
		t.Fatalf("budget 60 phải cắt bớt:\n%s", res.Text)
	}
	if shown+cut != total {
		t.Fatalf("shown=%d cut=%d, tổng phải %d:\n%s", shown, cut, total, res.Text)
	}
	if !strings.Contains(res.Text, "## Việc đang mở") {
		t.Fatalf("mục đầu phải còn:\n%s", res.Text)
	}
}

// TestBriefingGate: đã brief hôm nay → không assemble; force → assemble lại.
func TestBriefingGate(t *testing.T) {
	st := newStore(t)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	b := New(st, nil, testCfg())
	spaceID, _ := st.SpaceByName(ctx, "personal")

	first, err := b.Briefing(ctx, BriefingParams{SpaceID: spaceID, Now: now})
	if err != nil || !first.Delivered {
		t.Fatalf("lần 1: %+v err=%v", first, err)
	}
	second, err := b.Briefing(ctx, BriefingParams{SpaceID: spaceID, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if second.Delivered || second.Reason != "already_briefed_today" || second.Text != "" {
		t.Fatalf("lần 2: %+v", second)
	}
	forced, err := b.Briefing(ctx, BriefingParams{SpaceID: spaceID, Now: now, Force: true})
	if err != nil || !forced.Delivered {
		t.Fatalf("force: %+v err=%v", forced, err)
	}
	// ngày khác → brief lại bình thường
	next, err := b.Briefing(ctx, BriefingParams{SpaceID: spaceID, Now: now.AddDate(0, 0, 1)})
	if err != nil || !next.Delivered {
		t.Fatalf("ngày khác: %+v err=%v", next, err)
	}
	v, ok, err := st.GetMeta(ctx, briefingKey(spaceID))
	if err != nil || !ok || v != "2026-10-07" {
		t.Fatalf("meta=%q ok=%v err=%v", v, ok, err)
	}
}

// TestBriefingCapturedSince: mục "Đã ghi kể từ recap trước" (cuối briefing)
// liệt kê note tự ghi (hook/tool, không gồm ingest) + task mới còn mở trong
// cửa sổ, kèm #id; việc đã đóng trong cửa sổ không lọt.
func TestBriefingCapturedSince(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	b := New(st, nil, testCfg())
	spaceID, _ := st.SpaceByName(ctx, "personal")
	day1 := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	if _, err := b.Briefing(ctx, BriefingParams{SpaceID: spaceID, Now: day1}); err != nil {
		t.Fatal(err)
	}
	note := func(src, kind, text string, at time.Time) int64 {
		t.Helper()
		n := &store.Note{SpaceID: spaceID, Kind: kind, Text: text, Source: src, CreatedAt: at, UpdatedAt: at}
		id, _, err := st.UpsertNote(ctx, n)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	at := day1.Add(3 * time.Hour)
	auto := note("hook:stop", "decision", "Chốt dùng ngưỡng 0.92 cho chống trùng", at)
	note("ingest:/tmp/a.md", "note", "Tài liệu nạp tay — không liệt kê", at)
	oldTask, _, err := st.InsertTaskWith(ctx, spaceID, "Việc cũ", store.TaskFields{}, day1.AddDate(0, 0, -3))
	if err != nil {
		t.Fatal(err)
	}
	newTask, _, err := st.InsertTaskWith(ctx, spaceID, "Tạo MR MCLBO-1697", store.TaskFields{}, at)
	if err != nil {
		t.Fatal(err)
	}
	done := "done"
	if _, err := st.UpdateTask(ctx, oldTask, &done, nil, at); err != nil {
		t.Fatal(err)
	}
	// ngày sau, sau giờ đầu ngày → cửa sổ = [đầu ngày 5, đầu ngày 6)
	res, err := b.Briefing(ctx, BriefingParams{SpaceID: spaceID, Now: day1.AddDate(0, 0, 1)})
	if err != nil || !res.Delivered {
		t.Fatalf("%+v err=%v", res, err)
	}
	for _, want := range []string{
		"Đã ghi kể từ recap trước",
		fmt.Sprintf("note #%d [decision]: Chốt dùng ngưỡng 0.92", auto),
		fmt.Sprintf("task #%d (việc mới): Tạo MR MCLBO-1697", newTask),
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("thiếu %q trong:\n%s", want, res.Text)
		}
	}
	if strings.Contains(res.Text, "(đã xong): Việc cũ") {
		t.Errorf("việc đã đóng không được liệt kê:\n%s", res.Text)
	}
	if i, j := strings.Index(res.Text, "## Đã ghi"), strings.Index(res.Text, "## Notes mới"); i < j {
		t.Errorf("mục 'Đã ghi' phải nằm cuối (i=%d, notes=%d):\n%s", i, j, res.Text)
	}
	capSec := res.Text[strings.Index(res.Text, "## Đã ghi"):]
	if i := strings.Index(capSec[3:], "\n## "); i >= 0 {
		capSec = capSec[:i+3]
	}
	if strings.Contains(capSec, "Tài liệu nạp tay") {
		t.Errorf("ingest không được liệt kê:\n%s", res.Text)
	}
	// force cùng ngày → vẫn chỉ ngày hôm qua, không rỗng
	again, _ := b.Briefing(ctx, BriefingParams{SpaceID: spaceID, Now: day1.AddDate(0, 0, 1), Force: true})
	if !strings.Contains(again.Text, "Tạo MR MCLBO-1697") {
		t.Errorf("force:\n%s", again.Text)
	}
}

// TestBriefingProjectFamily: mục "Việc đang mở" lọc theo project family —
// "macallan" thấy cả "macallan-*" + đếm việc repo khác; project lẻ chỉ thấy
// mình nó; không có project (Desktop) thấy tất cả, không đếm.
func TestBriefingProjectFamily(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	b := New(st, nil, testCfg())
	spaceID, _ := st.SpaceByName(ctx, "personal")
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	seed := func(title, proj string) int64 {
		t.Helper()
		id, _, err := st.InsertTaskWith(ctx, spaceID, title, store.TaskFields{Project: proj}, now)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	seed("Giữ mức phí", "macallan")
	seed("Sửa form KYC", "macallan-0002-be-user-service")
	seed("Dịch trang chủ", "translator-platform")

	brief := func(proj string) string {
		t.Helper()
		b.SetProject(proj)
		res, err := b.Briefing(ctx, BriefingParams{SpaceID: spaceID, Now: now, Force: true})
		if err != nil || !res.Delivered {
			t.Fatalf("project %q: %+v err=%v", proj, res, err)
		}
		return res.Text
	}

	// Umbrella: thấy task macallan + macallan-*; còn 1 việc ngoài family.
	um := brief("macallan")
	for _, want := range []string{"Giữ mức phí", "Sửa form KYC", "còn 1 việc ngoài project hiện tại"} {
		if !strings.Contains(um, want) {
			t.Errorf("macallan: thiếu %q trong:\n%s", want, um)
		}
	}
	if strings.Contains(um, "Dịch trang chủ") {
		t.Errorf("macallan: repo khác không được hiện:\n%s", um)
	}

	// Repo lẻ: chỉ thấy chính nó; còn 2 việc ngoài family.
	one := brief("macallan-0002-be-user-service")
	if !strings.Contains(one, "Sửa form KYC") || !strings.Contains(one, "còn 2 việc ngoài project hiện tại") {
		t.Errorf("user-service: thiếu nội dung/đếm:\n%s", one)
	}
	for _, no := range []string{"Giữ mức phí", "Dịch trang chủ"} {
		if strings.Contains(one, no) {
			t.Errorf("user-service: không được hiện %q:\n%s", no, one)
		}
	}

	// Không project (Desktop): thấy tất cả, không đếm phần còn lại.
	all := brief("")
	for _, want := range []string{"Giữ mức phí", "Sửa form KYC", "Dịch trang chủ"} {
		if !strings.Contains(all, want) {
			t.Errorf("Desktop: thiếu %q trong:\n%s", want, all)
		}
	}
	if strings.Contains(all, "việc ngoài project") {
		t.Errorf("Desktop: không được có dòng đếm:\n%s", all)
	}
}
