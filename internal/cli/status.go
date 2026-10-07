package cli

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"mind-runner/internal/config"
	"mind-runner/internal/store"
)

// RunStatus in trạng thái: counts, jobs theo state (kèm dead ngắn),
// dung lượng media/ + backups/, last_briefing_date.
func RunStatus(args []string, stdout, stderr io.Writer, env func(string) string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfgPath := env("MIND_RUNNER_CONFIG")
	if cfgPath == "" {
		cfgPath = config.DefaultConfigPath()
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "status:", err)
		return 1
	}
	base := config.ExpandHome(cfg.DataDir)
	st, err := store.Open(filepath.Join(base, "mind-runner.db"))
	if err != nil {
		fmt.Fprintln(stderr, "status:", err)
		return 1
	}
	defer st.Close()
	ctx := context.Background()

	for _, c := range []struct {
		label string
		table string
	}{{"sessions", "sessions"}, {"notes", "notes"}, {"media", "media"}} {
		var n int
		if err := st.DB().QueryRowContext(ctx, `SELECT count(*) FROM `+c.table).Scan(&n); err != nil {
			fmt.Fprintln(stderr, "status:", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s: %d\n", c.label, n)
	}

	// usage counters (stats.* trong meta) — key hiện không có prefix
	statRows, err := st.DB().QueryContext(ctx, `SELECT key, value FROM meta WHERE key LIKE 'stats.%' ORDER BY key`)
	if err != nil {
		fmt.Fprintln(stderr, "status:", err)
		return 1
	}
	for statRows.Next() {
		var key, value string
		if err := statRows.Scan(&key, &value); err != nil {
			statRows.Close()
			fmt.Fprintln(stderr, "status:", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s=%s\n", strings.TrimPrefix(key, "stats."), value)
	}
	statRows.Close()

	// chỉ số vận hành: phiên 7 ngày, note gắn phiên, tồn đọng embed
	weekAgo := store.TS(time.Now().AddDate(0, 0, -7))
	var sessions7, notesWithSession, embedLag int
	if err := st.DB().QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE last_seen_at >= ?`, weekAgo).Scan(&sessions7); err != nil {
		fmt.Fprintln(stderr, "status:", err)
		return 1
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT count(*) FROM notes WHERE session_id IS NOT NULL`).Scan(&notesWithSession); err != nil {
		fmt.Fprintln(stderr, "status:", err)
		return 1
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE type='embed_chunk' AND state IN ('queued','failed')`).Scan(&embedLag); err != nil {
		fmt.Fprintln(stderr, "status:", err)
		return 1
	}
	fmt.Fprintf(stdout, "sessions 7 ngày: %d\n", sessions7)
	fmt.Fprintf(stdout, "notes có session: %d\n", notesWithSession)
	fmt.Fprintf(stdout, "embed lag: %d\n", embedLag)

	rows, err := st.DB().QueryContext(ctx, `SELECT state, count(*) FROM jobs GROUP BY state ORDER BY state`)
	if err != nil {
		fmt.Fprintln(stderr, "status:", err)
		return 1
	}
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			rows.Close()
			fmt.Fprintln(stderr, "status:", err)
			return 1
		}
		fmt.Fprintf(stdout, "jobs %s: %d\n", state, n)
	}
	rows.Close()

	dead, err := st.DB().QueryContext(ctx, `SELECT id, type, COALESCE(last_error,'') FROM jobs WHERE state='dead' ORDER BY id LIMIT 5`)
	if err != nil {
		fmt.Fprintln(stderr, "status:", err)
		return 1
	}
	for dead.Next() {
		var id int64
		var typ, lastErr string
		if err := dead.Scan(&id, &typ, &lastErr); err != nil {
			dead.Close()
			fmt.Fprintln(stderr, "status:", err)
			return 1
		}
		fmt.Fprintf(stdout, "  dead #%d %s: %s\n", id, typ, lastErr)
	}
	dead.Close()

	for _, d := range []struct{ label, path string }{
		{"media/", filepath.Join(base, "media")},
		{"backups/", filepath.Join(base, "backups")},
	} {
		n, bytes := dirStats(d.path)
		fmt.Fprintf(stdout, "%s: %d file, %d bytes\n", d.label, n, bytes)
	}

	var last sql.NullString
	err = st.DB().QueryRowContext(ctx,
		`SELECT MAX(value) FROM meta WHERE key LIKE 'last_briefing_date%'`).Scan(&last)
	if err != nil || !last.Valid {
		last = sql.NullString{String: "(chưa)"}
	}
	fmt.Fprintf(stdout, "last_briefing_date: %s\n", last.String)
	return 0
}

func dirStats(dir string) (files int, bytes int64) {
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			files++
			bytes += fi.Size()
		}
		return nil
	})
	return files, bytes
}
