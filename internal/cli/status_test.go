package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mind-runner/internal/store"
)

func TestStatusCountsAndSizes(t *testing.T) {
	_, dataDir := setupFresh(t)

	st, err := store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := "2026-10-06T10:00:00Z"
	for _, q := range []string{
		`INSERT INTO sessions(id, client, space_id, started_at, last_seen_at) VALUES ('s1','claude-code',1,'` + now + `','` + now + `')`,
		`INSERT INTO notes(space_id, kind, text, source, content_hash, created_at, updated_at) VALUES (1,'note','n1','test','h1','` + now + `','` + now + `')`,
		`INSERT INTO notes(space_id, kind, text, source, content_hash, created_at, updated_at) VALUES (1,'fact','n2','test','h2','` + now + `','` + now + `')`,
		`INSERT INTO media(space_id, sha256, kind, path, bytes, status, source, created_at, updated_at) VALUES (1,'sha1','audio','ab/x.m4a',100,'done','cli','` + now + `','` + now + `')`,
		`INSERT INTO jobs(type, payload, state, run_after, last_error, created_at, updated_at) VALUES ('transcribe_media','{}','dead','` + now + `','payload quá lớn','` + now + `','` + now + `')`,
	} {
		if _, err := st.DB().ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	// 2 file media giả (100 + 250 bytes) + 1 backup giả (50 bytes)
	mustWrite(t, filepath.Join(dataDir, "media", "ab", "x.m4a"), 100)
	mustWrite(t, filepath.Join(dataDir, "media", "loose.bin"), 250)
	mustWrite(t, filepath.Join(dataDir, "backups", "mind-runner-20260101.db"), 50)

	var out, errb bytes.Buffer
	code := RunStatus(nil, &out, &errb, os.Getenv)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	s := out.String()
	for _, want := range []string{
		"sessions: 1", "notes: 2", "media: 1",
		"dead #1 transcribe_media", "payload quá lớn",
		"media/: 2 file, 350 bytes", "backups/: 1 file, 50 bytes",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("out thiếu %q\nout=%s", want, s)
		}
	}
}

func mustWrite(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), size), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestStatusUsageStats: counters stats.*, sessions 7 ngày, notes có session,
// embed lag (queued+failed embed_chunk).
func TestStatusUsageStats(t *testing.T) {
	_, dataDir := setupFresh(t)
	st, err := store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if err := st.IncrMeta(ctx, "stats.recall_calls", 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.IncrMeta(ctx, "stats.remember_calls", 1); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	recent := now.Add(-24 * time.Hour).Format(time.RFC3339Nano)
	old := now.AddDate(0, 0, -30).Format(time.RFC3339Nano)
	for _, q := range []string{
		fmt.Sprintf(`INSERT INTO sessions(id, client, space_id, started_at, last_seen_at) VALUES ('s-recent','claude-code',1,'%s','%s')`, recent, recent),
		fmt.Sprintf(`INSERT INTO sessions(id, client, space_id, started_at, last_seen_at) VALUES ('s-old','claude-code',1,'%s','%s')`, old, old),
		fmt.Sprintf(`INSERT INTO notes(space_id, kind, text, source, content_hash, session_id, created_at, updated_at) VALUES (1,'note','từ phiên','hook:stop','h1','s-recent','%s','%s')`, recent, recent),
		fmt.Sprintf(`INSERT INTO notes(space_id, kind, text, source, content_hash, created_at, updated_at) VALUES (1,'note','ghi tay','tool:remember','h2','%s','%s')`, recent, recent),
		fmt.Sprintf(`INSERT INTO jobs(type, payload, state, run_after, created_at, updated_at) VALUES ('embed_chunk','{}','queued','%s','%s','%s')`, recent, recent, recent),
		fmt.Sprintf(`INSERT INTO jobs(type, payload, state, run_after, created_at, updated_at) VALUES ('embed_chunk','{}','failed','%s','%s','%s')`, recent, recent, recent),
		fmt.Sprintf(`INSERT INTO jobs(type, payload, state, run_after, created_at, updated_at) VALUES ('embed_chunk','{}','done','%s','%s','%s')`, recent, recent, recent),
		fmt.Sprintf(`INSERT INTO jobs(type, payload, state, run_after, created_at, updated_at) VALUES ('transcribe_media','{}','queued','%s','%s','%s')`, recent, recent, recent),
	} {
		if _, err := st.DB().ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	var out, errb bytes.Buffer
	code := RunStatus(nil, &out, &errb, os.Getenv)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	s := out.String()
	for _, want := range []string{
		"recall_calls=2", "remember_calls=1",
		"sessions 7 ngày: 1", "notes có session: 1", "embed lag: 2",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("out thiếu %q\nout=%s", want, s)
		}
	}
}
