package store

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestSessionLifecycle(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	tp := "/tmp/transcript.jsonl"

	if err := s.UpsertSessionStart(ctx, "sess-1", "claude-code", 1, &tp, t0); err != nil {
		t.Fatal(err)
	}
	sess, err := s.GetSession(ctx, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Client != "claude-code" || sess.SpaceID != 1 || sess.TranscriptPath == nil || *sess.TranscriptPath != tp {
		t.Fatalf("sess=%+v", sess)
	}
	if sess.TranscriptOffset != 0 || !sess.LastSeenAt.Equal(t0) {
		t.Fatalf("sess=%+v", sess)
	}

	// resume: upsert lại không lỗi, last_seen mới
	t1 := t0.Add(time.Minute)
	if err := s.UpsertSessionStart(ctx, "sess-1", "claude-code", 1, &tp, t1); err != nil {
		t.Fatal(err)
	}
	// touch
	t2 := t1.Add(time.Minute)
	if err := s.TouchSession(ctx, "sess-1", t2); err != nil {
		t.Fatal(err)
	}
	// offset
	if err := s.UpdateTranscriptOffset(ctx, "sess-1", 1234); err != nil {
		t.Fatal(err)
	}
	sess, err = s.GetSession(ctx, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if !sess.LastSeenAt.Equal(t2) || sess.TranscriptOffset != 1234 {
		t.Fatalf("sess=%+v", sess)
	}

	if _, err := s.GetSession(ctx, "không-có"); err == nil {
		t.Fatal("session lạ phải lỗi")
	}
}

func TestInsertRawNextSeq(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	tp := "/tmp/t.jsonl"
	if err := s.UpsertSessionStart(ctx, "sess-1", "claude-code", 1, &tp, t0); err != nil {
		t.Fatal(err)
	}

	seq, err := s.NextRawSeq(ctx, "sess-1")
	if err != nil || seq != 1 {
		t.Fatalf("seq=%d err=%v", seq, err)
	}
	blob := []byte("gzip-giả")
	if _, err := s.InsertRaw(ctx, "sess-1", seq, blob, 90, t0); err != nil {
		t.Fatal(err)
	}
	var got []byte
	var retained string
	if err := s.DB().QueryRowContext(ctx,
		`SELECT content, retained_until FROM session_raw WHERE session_id=? AND seq=1`, "sess-1").
		Scan(&got, &retained); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, blob) {
		t.Fatalf("content=%q", got)
	}
	if want := ts(t0.AddDate(0, 0, 90)); retained != want {
		t.Fatalf("retained_until=%s, muốn %s", retained, want)
	}

	seq2, err := s.NextRawSeq(ctx, "sess-1")
	if err != nil || seq2 != 2 {
		t.Fatalf("seq2=%d err=%v", seq2, err)
	}
}
