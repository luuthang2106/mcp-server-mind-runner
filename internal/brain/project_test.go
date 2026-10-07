package brain

import (
	"context"
	"strings"
	"testing"
	"time"

	"mind-runner/internal/egress/egressfake"
	"mind-runner/internal/store"
)

// TestRecallPrefersCurrentProject: hai note ngang điểm rerank — note của
// project đang mở lên trước; note lạc đề (điểm thấp hơn nhiều) không bị kéo lên;
// lọc project chỉ trả note của project đó; điểm báo ra là điểm gốc.
func TestRecallPrefersCurrentProject(t *testing.T) {
	st := newStore(t)
	scores := map[string]float64{
		"deploy dùng helm chart": 0.70, // project khác
		"deploy dùng kustomize":  0.66, // project hiện tại, ngang điểm
		"deploy ghi chú lạc đề":  0.30, // project hiện tại nhưng kém hẳn
	}
	fake := egressfake.New(t, egressfake.Options{
		Rerank: func(_ string, docs []string) []float64 {
			out := make([]float64, len(docs))
			for i, d := range docs {
				out[i] = scores[d]
			}
			return out
		},
	})
	b := New(st, fake.Egress, testCfg())
	ctx := context.Background()
	sp, _ := st.SpaceByName(ctx, "personal")
	write := func(text, proj string) int64 {
		t.Helper()
		res, err := b.WriteNote(ctx, WriteParams{SpaceID: sp, Kind: "note", Text: text, Source: "tool:remember", Project: proj})
		if err != nil {
			t.Fatal(err)
		}
		return res.NoteID
	}
	other := write("deploy dùng helm chart", "billing")
	mine := write("deploy dùng kustomize", "api")
	weak := write("deploy ghi chú lạc đề", "api")

	zero := 0.0
	b.SetProject("api")
	res, err := b.Recall(ctx, RecallParams{Query: "deploy", MinScore: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 3 || res.Hits[0].NoteID != mine || res.Hits[1].NoteID != other || res.Hits[2].NoteID != weak {
		t.Fatalf("thứ tự sai: %+v", res.Hits)
	}
	if res.Hits[0].Score != 0.66 || res.Hits[0].Project != "api" {
		t.Fatalf("điểm/nhãn phải là gốc: %+v", res.Hits[0])
	}

	// không ở project nào → thứ tự thuần điểm
	b.SetProject("")
	res, _ = b.Recall(ctx, RecallParams{Query: "deploy", MinScore: &zero})
	if res.Hits[0].NoteID != other {
		t.Fatalf("không project phải theo điểm: %+v", res.Hits)
	}

	// lọc cứng
	res, _ = b.Recall(ctx, RecallParams{Query: "deploy", MinScore: &zero, Project: "billing"})
	if len(res.Hits) != 1 || res.Hits[0].NoteID != other {
		t.Fatalf("lọc project: %+v", res.Hits)
	}
}

// TestWriteNoteProjectDefault: không truyền → project tiến trình; "-" → chung.
func TestWriteNoteProjectDefault(t *testing.T) {
	st := newStore(t)
	b := New(st, nil, testCfg())
	ctx := context.Background()
	sp, _ := st.SpaceByName(ctx, "personal")
	b.SetProject("api")
	r1, _ := b.WriteNote(ctx, WriteParams{SpaceID: sp, Kind: "note", Text: "một", Source: "tool:remember"})
	r2, _ := b.WriteNote(ctx, WriteParams{SpaceID: sp, Kind: "note", Text: "hai", Source: "tool:remember", Project: "-"})
	n1, _ := st.FetchNote(ctx, r1.NoteID)
	n2, _ := st.FetchNote(ctx, r2.NoteID)
	if n1.Project != "api" || n2.Project != "" {
		t.Fatalf("project=%q/%q", n1.Project, n2.Project)
	}
}

// TestBriefingProjectFirst: việc của project đang mở lên đầu, việc project
// khác có nhãn [project], việc chung không nhãn.
func TestBriefingProjectFirst(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	b := New(st, nil, testCfg())
	sp, _ := st.SpaceByName(ctx, "personal")
	now := time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	add := func(title, proj string, at time.Time) {
		t.Helper()
		if _, _, err := st.InsertTaskWith(ctx, sp, title, store.TaskFields{Project: proj}, at); err != nil {
			t.Fatal(err)
		}
	}
	add("Sửa lỗi hoá đơn", "billing", now.Add(-1*time.Hour)) // mới nhất nhưng project khác
	add("Viết API đăng nhập", "api", now.Add(-48*time.Hour))
	add("Mua quà sinh nhật", "", now.Add(-2*time.Hour))
	b.SetProject("api")
	br, err := b.Briefing(ctx, BriefingParams{SpaceID: sp, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	txt := br.Text
	if !strings.Contains(txt, "# Briefing — 2026-10-06 — project api") {
		t.Fatalf("header:\n%s", txt)
	}
	iAPI := strings.Index(txt, "Viết API đăng nhập\n")
	iBill := strings.Index(txt, "Sửa lỗi hoá đơn [billing]")
	iGift := strings.Index(txt, "Mua quà sinh nhật\n")
	if iAPI < 0 || iBill < 0 || iGift < 0 || iAPI > iBill || iAPI > iGift {
		t.Fatalf("thứ tự/nhãn sai:\n%s", txt)
	}
}
