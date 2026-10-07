package store

import (
	"context"
	"testing"
	"time"
)

func TestSanitizeFTSQuery(t *testing.T) {
	cases := []struct{ in, want string }{
		{`quyet dinh`, `"quyet" OR "dinh"`},
		{`bộ nhớ`, `"bộ" OR "nhớ"`},  // giữ dấu Việt
		{`he"llo `, `"he" OR "llo"`}, // ký tự FTS5 đặc biệt bị loại → không lỗi cú pháp
		{`  ***  `, ``},              // chỉ ký tự đặc biệt → rỗng
		{``, ``},
		{`a-b c_d`, `"a" OR "b" OR "c" OR "d"`},
	}
	for _, c := range cases {
		if got := SanitizeFTSQuery(c.in); got != c.want {
			t.Errorf("SanitizeFTSQuery(%q)=%q, muốn %q", c.in, got, c.want)
		}
	}
}

func TestSearchFTSSemantics(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	seed := func(spaceID int64, text string, tags []string) int64 {
		t.Helper()
		n := noteAt(spaceID, "note", text, t0)
		n.Tags = tags
		id, _, err := s.UpsertNote(ctx, n)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.InsertChunks(ctx, id, []Chunk{{Ordinal: 0, Text: text, TokenCount: (len(text) + 3) / 4}}); err != nil {
			t.Fatal(err)
		}
		return id
	}

	idNhớ := seed(1, "Quyết định dùng SQLite cho bộ nhớ", []string{"decision"})
	idDeploy := seed(1, "họp nhóm về deploy", []string{"work"})
	idSpace2 := seed(2, "Quyết định dùng Postgres", nil)
	idDel := seed(1, "Quyết định cũ đã xoá", nil)
	if _, err := s.DB().ExecContext(ctx, `UPDATE notes SET deleted_at=? WHERE id=?`, ts(t0), idDel); err != nil {
		t.Fatal(err)
	}
	// ranking: note chứa cả 2 token phải trên note chỉ chứa 1
	idBoth := seed(1, "ghi nhớ tìm kiếm nhanh", nil)
	idOne := seed(1, "ghi nhớ điều cần ghi nhớ nhiều lần", nil)

	// không dấu vẫn khớp nhờ remove_diacritics 2; lọc space 1
	hits, err := s.SearchFTS(ctx, "quyet dinh", NoteFilter{SpaceIDs: []int64{1}}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].NoteID != idNhớ {
		t.Fatalf("hits=%+v, muốn note %d", hits, idNhớ)
	}

	// không lọc space → thêm note space 2, không có note đã xoá
	hits, err = s.SearchFTS(ctx, "quyet dinh", NoteFilter{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]bool{}
	for _, h := range hits {
		got[h.NoteID] = true
	}
	if !got[idNhớ] || !got[idSpace2] || got[idDel] {
		t.Fatalf("hits=%+v, muốn có %d + %d, không có %d", hits, idNhớ, idSpace2, idDel)
	}

	// lọc tag
	hits, err = s.SearchFTS(ctx, "hop nhom", NoteFilter{Tags: []string{"work"}}, 10)
	if err != nil || len(hits) != 1 || hits[0].NoteID != idDeploy {
		t.Fatalf("tag hits=%+v err=%v", hits, err)
	}
	hits, err = s.SearchFTS(ctx, "hop nhom", NoteFilter{Tags: []string{"decision"}}, 10)
	if err != nil || len(hits) != 0 {
		t.Fatalf("tag sai khớp: hits=%+v err=%v", hits, err)
	}

	// ranking: nhiều token khớp > ít hơn; "nhớ" đơn lẻ khớp idNhớ nên có 3 hit
	hits, err = s.SearchFTS(ctx, "ghi nhớ tìm kiếm", NoteFilter{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 || hits[0].NoteID != idBoth || hits[1].NoteID != idOne || hits[2].NoteID != idNhớ {
		t.Fatalf("ranking hits=%+v, muốn [%d %d %d]", hits, idBoth, idOne, idNhớ)
	}

	// query rác → rỗng, không lỗi
	hits, err = s.SearchFTS(ctx, `***`, NoteFilter{}, 10)
	if err != nil || len(hits) != 0 {
		t.Fatalf("rác: hits=%+v err=%v", hits, err)
	}
}
