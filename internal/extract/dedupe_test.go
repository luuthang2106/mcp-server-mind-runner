package extract

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"mind-runner/internal/brain"
	"mind-runner/internal/egress"
	"mind-runner/internal/egress/egressfake"
	"mind-runner/internal/store"
)

// TestPlanDedupeSameOpening: fact "kể lại" cùng một chuyện nhưng câu chữ khác
// (cosine dưới ngưỡng 0.92) vẫn bị nhận diện qua phần mở đầu trùng → thay thế
// (note cũ sẽ bị xoá cứng); fact khác chủ đề và note kind "note" thì không đụng gì.
func TestPlanDedupeSameOpening(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Migrate(ctx, filepath.Join(t.TempDir(), "backups")); err != nil {
		t.Fatal(err)
	}
	spaceID, err := st.SpaceByName(ctx, "personal")
	if err != nil {
		t.Fatal(err)
	}

	const oldText = "Repo macallan-0002-be-user-service là backend Go quản lý ticket"
	sameFact := oldText + " trên Jira."                    // kể lại: prefix chuẩn hoá trùng ≥ 40 rune
	otherFact := "Keycloak là identity provider của dự án" // chủ đề khác hẳn

	fake := egressfake.New(t, egressfake.Options{
		EmbedVec: func(text string) []float32 {
			switch {
			case strings.Contains(strings.ToLower(text), "keycloak"):
				return []float32{0, 0, 1, 0}
			case text == oldText:
				return []float32{1, 0, 0, 0}
			case text == sameFact:
				return []float32{0.6, 0.8, 0, 0} // cosine 0.6 với note cũ — dưới ngưỡng
			default:
				return []float32{0, 0, 0, 1}
			}
		},
	})
	cfg := egressfake.Config(fake, nil)
	b := brain.New(st, fake.Egress, &cfg)
	x := New(st, b, fake.Egress, &cfg)

	old, err := b.WriteNote(ctx, brain.WriteParams{SpaceID: spaceID, Kind: "fact", Text: oldText, Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.EmbedNote(ctx, old.NoteID); err != nil {
		t.Fatal(err)
	}

	plan, err := x.planDedupe(ctx, egress.PolicyCloud, spaceID, []ExtractedNote{
		{Kind: "fact", Text: sameFact},
		{Kind: "fact", Text: otherFact},
		{Kind: "note", Text: sameFact},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.skip[0] || plan.skip[1] || plan.skip[2] {
		t.Fatalf("không note nào là trùng-trong-batch: skip=%v", plan.skip)
	}
	if plan.replaces[0] != old.NoteID {
		t.Fatalf("fact kể lại phải thay thế note #%d: %v", old.NoteID, plan.replaces)
	}
	if plan.replaces[1] != 0 || plan.replaces[2] != 0 {
		t.Fatalf("chủ đề khác / kind note không được thay thế: %v", plan.replaces)
	}
}
