package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"mind-runner/internal/brain"
	"mind-runner/internal/capture"
	"mind-runner/internal/config"
	"mind-runner/internal/egress"
	"mind-runner/internal/execx"
	"mind-runner/internal/media"
	"mind-runner/internal/store"
	"mind-runner/internal/worker"
)

// freeSpaceFn trả dung lượng còn trống (bytes) — injectable cho test.
var freeSpaceFn = func(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

// RunMaintenance xử lý queue + backup theo thứ tự chuẩn (Task 1.6):
//
//	backfill embed → merge spool → [--retry-dead] retry-dead → queue → watch dirs
//	→ purge → wal_checkpoint(TRUNCATE) → VACUUM INTO + rotate → quick_check
func RunMaintenance(args []string, stdout, stderr io.Writer, env func(string) string) int {
	fs := flag.NewFlagSet("maintenance", flag.ContinueOnError)
	fs.SetOutput(stderr)
	daily := fs.Bool("daily", false, "")
	_ = daily // launchd chạy với --daily; hành vi hiện tại giống nhau
	retryDead := fs.Bool("retry-dead", false, "đưa job dead về queue và chạy lại trong lần này")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfgPath := env("MIND_RUNNER_CONFIG")
	if cfgPath == "" {
		cfgPath = config.DefaultConfigPath()
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "maintenance:", err)
		return 1
	}
	base := config.ExpandHome(cfg.DataDir)
	st, err := openDB(context.Background(), cfg)
	if err != nil {
		fmt.Fprintln(stderr, "maintenance:", err)
		return 1
	}
	defer st.Close()
	ctx := context.Background()

	eg := egress.New(cfg, nil)
	attachUsageLog(eg, base)
	defer eg.FlushUsage()

	// (0) backfill embed (3.1): quét theo từng policy — model cloud và model
	// local khác nhau; chỉ quét model cloud thì note space local không bao giờ
	// được backfill. Local chưa cấu hình → bỏ qua (doctor sẽ nhắc — 4.3).
	sps, err := st.Spaces(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "maintenance: backfill:", err)
		return 1
	}
	missTotal := 0
	for _, pol := range []egress.Policy{egress.PolicyCloud, egress.PolicyLocal} {
		model := eg.EmbedModel(pol)
		if model == "" {
			continue
		}
		var ids []int64
		for _, sp := range sps {
			if egress.Policy(cfg.SpacePolicy(sp.Name)) == pol {
				ids = append(ids, sp.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		miss, err := st.MissingEmbedNoteIDs(ctx, model, ids, 1000)
		if err != nil {
			fmt.Fprintln(stderr, "maintenance: backfill:", err)
			return 1
		}
		for _, noteID := range miss {
			if _, err := st.Enqueue(ctx, "embed_chunk", map[string]int64{"note_id": noteID}, time.Now()); err != nil {
				fmt.Fprintln(stderr, "maintenance: backfill:", err)
				return 1
			}
		}
		missTotal += len(miss)
	}
	fmt.Fprintf(stdout, "backfill: %d note thiếu embedding\n", missTotal)

	// (1) merge spool (2.4) — trước checkpoint/backup
	merged, err := capture.MergeSpool(ctx, st, filepath.Join(base, "spool"), cfg.Retention.RawDays, time.Now())
	if err != nil {
		fmt.Fprintln(stderr, "maintenance: spool:", err)
		return 1
	}
	fmt.Fprintf(stdout, "spool: merged=%d\n", merged)

	// (2) retry-dead (tùy chọn, 5.5): đưa job dead về queue — chạy ngay trong
	// lần maintenance này vì raw còn thì làm lại được.
	if *retryDead {
		n, err := st.RetryDead(ctx, time.Now())
		if err != nil {
			fmt.Fprintln(stderr, "maintenance: retry-dead:", err)
			return 1
		}
		if n > 0 {
			fmt.Fprintf(stdout, "retry-dead: %d job\n", n)
		}
	}

	// (3) queue (3.1): chạy job tới hạn (extract_session chưa có handler tới 3.5)
	br := brain.New(st, eg, &cfg)
	reg := worker.NewRegistry()
	worker.RegisterAll(reg, worker.Deps{Store: st, Brain: br, Egress: eg, Config: &cfg,
		Execx: execx.OS{}, DataDir: base})
	processed, err := worker.Run(ctx, st, reg, 0, time.Now)
	if err != nil {
		fmt.Fprintln(stderr, "maintenance: queue:", err)
		return 1
	}
	fmt.Fprintf(stdout, "queue: processed=%d\n", processed)

	// (3b) watch dirs (6.4): quét file mới trong [media].watch_dirs — sau queue
	// (job cũ chạy trước), trước purge. File mtime < 60s hoặc đuôi lạ → bỏ qua.
	if len(cfg.Media.WatchDirs) > 0 {
		md := media.New(st, br, eg, &cfg, execx.OS{}, base)
		rep, err := md.ScanWatchDirs(ctx, cfg.Media.WatchDirs, time.Now())
		if err != nil {
			fmt.Fprintln(stderr, "maintenance: watch:", err)
			return 1
		}
		fmt.Fprintf(stdout, "watch: seen=%d ingested=%d fresh=%d type=%d already=%d failed=%d\n",
			rep.Seen, rep.Ingested, rep.SkippedFresh, rep.SkippedType, rep.AlreadyHave, rep.Failed)
	}

	// (4) purge TTL phân tầng (5.5) — xoá không im lặng: in đủ mọi loại kể cả 0
	rep, err := st.Purge(ctx, time.Now(), store.Retention{
		EventsDays: cfg.Retention.EventsDays,
		RawDays:    cfg.Retention.RawDays,
		JobsDays:   cfg.Retention.JobsDays,
	}, filepath.Join(base, "media"))
	if err != nil {
		fmt.Fprintln(stderr, "maintenance: purge:", err)
		return 1
	}
	fmt.Fprintf(stdout, "purge: events=%d episodes=%d relations=%d tasks=%d raws=%d jobs=%d media_rows=%d media_files=%d\n",
		rep.Events, rep.Episodes, rep.Relations, rep.Tasks, rep.Raws, rep.Jobs, rep.MediaRows, rep.MediaFiles)

	// (5) WAL checkpoint
	// busy=1 khi có reader/writer khác (MCP server, hook) đang giữ WAL → chưa
	// truncate được; không phải lỗi, lần sau sẽ xong.
	var busy, logFrames, ckptFrames int
	if err := st.DB().QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &ckptFrames); err != nil {
		fmt.Fprintln(stderr, "maintenance: checkpoint:", err)
		return 1
	}
	if busy != 0 {
		fmt.Fprintf(stdout, "checkpoint: busy (log=%d checkpointed=%d) — WAL đang được process khác dùng, để lần sau\n",
			logFrames, ckptFrames)
	} else {
		fmt.Fprintln(stdout, "checkpoint: ok")
	}

	// (6) backup + rotate (warn trước nếu đĩa thấp)
	dbPath := filepath.Join(base, "mind-runner.db")
	var dbSize int64
	if fi, err := os.Stat(dbPath); err == nil {
		dbSize = fi.Size()
	}
	if free, err := freeSpaceFn(base); err == nil && free < uint64(2*dbSize) {
		fmt.Fprintf(stdout, "warn: đĩa còn %d MB < 2× DB (%d MB) — backup có thể fail\n", free>>20, dbSize>>20)
	}
	backupDir := filepath.Join(base, "backups")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		fmt.Fprintln(stderr, "maintenance:", err)
		return 1
	}
	dst := filepath.Join(backupDir, "mind-runner-"+time.Now().Format("20060102")+".db")
	tmp := dst + ".tmp"
	_ = os.Remove(tmp)
	if _, err := st.DB().ExecContext(ctx, `VACUUM INTO '`+strings.ReplaceAll(tmp, "'", "''")+`'`); err != nil {
		fmt.Fprintln(stderr, "maintenance: backup:", err)
		return 1
	}
	if err := os.Rename(tmp, dst); err != nil { // đè bản backup cùng ngày nếu có
		fmt.Fprintln(stderr, "maintenance: backup:", err)
		return 1
	}
	fmt.Fprintf(stdout, "backup: %s\n", dst)
	rotateBackups(stdout, backupDir, cfg.Backup.Keep)

	// (7) quick_check
	var qc string
	if err := st.DB().QueryRowContext(ctx, "PRAGMA quick_check").Scan(&qc); err != nil {
		fmt.Fprintln(stderr, "maintenance: quick_check:", err)
		return 1
	}
	if qc != "ok" {
		fmt.Fprintf(stderr, "maintenance: quick_check: %s\n", qc)
		return 1
	}
	fmt.Fprintln(stdout, "quick_check: ok")
	return 0
}

func rotateBackups(stdout io.Writer, dir string, keep int) {
	if keep <= 0 {
		keep = 7
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "mind-runner-*.db"))
	sort.Sort(sort.Reverse(sort.StringSlice(matches))) // YYYYMMDD sorts lexically
	removed := 0
	for i := keep; i < len(matches); i++ {
		if err := os.Remove(matches[i]); err == nil {
			removed++
		}
	}
	if removed > 0 {
		fmt.Fprintf(stdout, "backup rotate: xoá %d bản cũ\n", removed)
	}
}
