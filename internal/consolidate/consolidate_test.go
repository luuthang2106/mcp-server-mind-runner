package consolidate

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mind-runner/internal/brain"
	"mind-runner/internal/egress/egressfake"
	"mind-runner/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background(), filepath.Join(t.TempDir(), "backups")); err != nil {
		t.Fatal(err)
	}
	return st
}

func seed(t *testing.T, st *store.Store, kind, text string) int64 {
	t.Helper()
	now := time.Now()
	id, _, err := st.UpsertNote(context.Background(), &store.Note{
		SpaceID: 1, Kind: kind, Text: text, Source: "tool:remember", CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestRunMerges: model gộp 2 fact trùng → note mới (source consolidate, tag
// hợp nhất), note cũ bị xoá cứng; note không trùng không đụng; lần 2 (còn 1
// fact) không gọi model.
func TestRunMerges(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a := seed(t, st, "fact", "Server chạy ở cổng 8080")
	b := seed(t, st, "fact", "Cổng của server là 8080")
	keep := seed(t, st, "preference", "Thích cà phê đen")
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE notes SET tags='["infra"]' WHERE id=?`, a); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE notes SET tags='["server"]' WHERE id=?`, b); err != nil {
		t.Fatal(err)
	}

	fake := egressfake.New(t, egressfake.Options{
		ChatResp: func(_, _ string) string {
			return fmt.Sprintf(`{"merges":[{"kind":"fact","text":"Server của dịch vụ chạy ở cổng 8080.","notes":[%d,%d]}]}`, a, b)
		},
	})
	cfg := egressfake.Config(fake, nil)
	r := New(st, brain.New(st, fake.Egress, &cfg), fake.Egress, &cfg)
	if err := r.Run(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := st.FetchNote(ctx, a); err == nil {
		t.Fatal("note cũ a chưa bị xoá")
	}
	if _, err := st.FetchNote(ctx, b); err == nil {
		t.Fatal("note cũ b chưa bị xoá")
	}
	facts, err := st.ActiveNotesByKinds(ctx, 1, []string{"fact"})
	if err != nil || len(facts) != 1 {
		t.Fatalf("facts=%+v err=%v", facts, err)
	}
	m := facts[0]
	if m.Source != "consolidate" || m.Text != "Server của dịch vụ chạy ở cổng 8080." ||
		len(m.Tags) != 2 {
		t.Fatalf("note gộp=%+v", m)
	}
	if _, err := st.FetchNote(ctx, keep); err != nil {
		t.Fatalf("preference bị đụng: %v", err)
	}

	// lần 2: chỉ còn 1 fact → không có batch ≥2 → không thêm lượt gọi model
	calls := fake.ChatCalls
	if err := r.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if fake.ChatCalls != calls {
		t.Fatalf("lần 2 không được gọi model: %d → %d", calls, fake.ChatCalls)
	}
}

// TestRunGuardSkipsMassDelete: batch 10 note, model đòi gộp 7 → xoá 6 = 60% >
// 50% → bỏ nguyên batch, không note nào mất.
func TestRunGuardSkipsMassDelete(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	ids := make([]int64, 0, 10)
	for i := 0; i < 10; i++ {
		ids = append(ids, seed(t, st, "fact", fmt.Sprintf("sự thật số %d", i)))
	}
	fake := egressfake.New(t, egressfake.Options{
		ChatResp: func(_, _ string) string {
			parts := make([]string, 7)
			for i := 0; i < 7; i++ {
				parts[i] = fmt.Sprint(ids[i])
			}
			return `{"merges":[{"kind":"fact","text":"sự thật gộp","notes":[` + strings.Join(parts, ",") + `]}]}`
		},
	})
	cfg := egressfake.Config(fake, nil)
	r := New(st, brain.New(st, fake.Egress, &cfg), fake.Egress, &cfg)
	if err := r.Run(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.ActiveNotesByKinds(ctx, 1, []string{"fact"})
	if err != nil || len(got) != 10 {
		t.Fatalf("guard không chặn: còn %d note err=%v", len(got), err)
	}
}

// TestRunRejectsInvalidMerges: merge sai (kind ngoài tầng kiến thức, id ngoài
// batch, <2 note, JSON hỏng) → lỗi, không note nào bị xoá.
func TestRunRejectsInvalidMerges(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a := seed(t, st, "fact", "sự thật một")
	b := seed(t, st, "fact", "sự thật hai")
	c := seed(t, st, "fact", "sự thật ba")

	var resp string
	fake := egressfake.New(t, egressfake.Options{ChatResp: func(_, _ string) string { return resp }})
	cfg := egressfake.Config(fake, nil)
	r := New(st, brain.New(st, fake.Egress, &cfg), fake.Egress, &cfg)

	cases := []struct {
		name, in string
	}{
		{"kind ngoài kiến thức", `{"merges":[{"kind":"note","text":"x","notes":[` + fmt.Sprint(a) + `,` + fmt.Sprint(b) + `]}]}`},
		{"id ngoài batch", `{"merges":[{"kind":"fact","text":"x","notes":[` + fmt.Sprint(a) + `,99999]}]}`},
		{"thiếu note", `{"merges":[{"kind":"fact","text":"x","notes":[` + fmt.Sprint(a) + `]}]}`},
		{"JSON hỏng", `{"merges":[`},
	}
	for _, tc := range cases {
		resp = tc.in
		if err := r.Run(ctx); err == nil {
			t.Fatalf("%s: muốn lỗi, nhận nil", tc.name)
		}
		for _, id := range []int64{a, b, c} {
			if _, err := st.FetchNote(ctx, id); err != nil {
				t.Fatalf("%s: note %d bị xoá dù lỗi: %v", tc.name, id, err)
			}
		}
	}
}
