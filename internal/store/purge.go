package store

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// Retention TTL theo tầng, map từ config.Retention.
type Retention struct {
	EventsDays, RawDays, JobsDays int
}

// PurgeReport đếm từng loại đã xoá (chạy lại lần 2 → toàn 0).
type PurgeReport struct {
	Events, Episodes, Relations, Tasks, Raws, Jobs, MediaRows, MediaFiles int
}

// purgeConn phần conn cần cho purge (test/stub không cần).
type purgeConn interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Purge xoá theo TTL phân tầng trong MỘT transaction (BEGIN IMMEDIATE):
// kiến thức (fact/preference/decision) vĩnh viễn; sự kiện theo events_days;
// note quên quá ân hạn jobs_days → xoá cứng cả chuỗi. Thứ tự tường minh:
// relations → embeddings → chunks → notes → episodes → tasks → raws → jobs →
// media (FK ON vẫn bảo vệ). Xoá không bao giờ im lặng — caller in report.
func (s *Store) Purge(ctx context.Context, now time.Time, ret Retention, mediaDir string) (PurgeReport, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return PurgeReport{}, err
	}
	defer conn.Close() // rollback defer đăng ký sau → chạy trước
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return PurgeReport{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	rep, paths, err := purgeTx(ctx, conn, now, ret)
	if err != nil {
		return PurgeReport{}, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return PurgeReport{}, err
	}
	committed = true
	s.BumpGen()

	// Chỉ xoá file media SAU khi COMMIT thành công — rollback không thể làm
	// mất file của row vẫn còn. File thiếu/bị chặn → warn, không gãy purge.
	for _, p := range paths {
		full := filepath.Join(mediaDir, p)
		if err := os.Remove(full); err != nil {
			if !os.IsNotExist(err) {
				slog.Default().Warn("purge: media file không xoá được", "path", full, "err", err.Error())
			}
			continue
		}
		rep.MediaFiles++
	}
	return rep, nil
}

// cutoff: days <= 0 nghĩa là "không giới hạn / tắt tầng này" — trả mốc rất
// xa trong quá khứ để không row nào khớp (an toàn: cấu hình 0 hoặc thiếu
// không bao giờ xoá sạch dữ liệu).
func cutoff(now time.Time, days int) string {
	if days <= 0 {
		return "0000"
	}
	return ts(now.AddDate(0, 0, -days))
}

// purgeTx xoá row trong tx và trả danh sách file media (tương đối) cần xoá
// sau khi commit.
func purgeTx(ctx context.Context, c purgeConn, now time.Time, ret Retention) (PurgeReport, []string, error) {
	rep := PurgeReport{}
	evCut := cutoff(now, ret.EventsDays) // sự kiện (notes/episodes/media/tasks kết thúc)
	jobsCut := cutoff(now, ret.JobsDays) // jobs done/failed/dead + ân hạn hồi sinh sau forget
	nowS := ts(now)

	exec := func(q string, args ...any) (int, error) {
		res, err := c.ExecContext(ctx, q, args...)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		return int(n), err
	}

	// (1) Tập note sẽ xoá: sự kiện quá hạn hoặc đã quên quá ân hạn.
	rows, err := c.QueryContext(ctx,
		`SELECT id FROM notes
		 WHERE (deleted_at IS NULL AND kind IN ('note','task_hint','transcript','caption') AND updated_at < ?)
		    OR (deleted_at IS NOT NULL AND deleted_at < ?)
		 ORDER BY id`, evCut, jobsCut)
	if err != nil {
		return PurgeReport{}, nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return PurgeReport{}, nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return PurgeReport{}, nil, err
	}
	rep.Events = len(ids)

	// (2) Relations trước notes (FK): orphan không note nguồn + theo note sắp xoá.
	n, err := exec(`DELETE FROM relations WHERE source_note_id IS NULL AND created_at < ?`, evCut)
	if err != nil {
		return PurgeReport{}, nil, err
	}
	rep.Relations += n
	if len(ids) > 0 {
		n, err := exec(`DELETE FROM relations WHERE source_note_id IN (`+placeholders(len(ids))+`)`, int64Args(ids)...)
		if err != nil {
			return PurgeReport{}, nil, err
		}
		rep.Relations += n
	}

	// (3) Chuỗi note → embeddings → chunks (trigger chunks_ad dọn chunks_fts) → notes.
	if len(ids) > 0 {
		args := int64Args(ids)
		ph := placeholders(len(ids))
		if _, err := exec(`DELETE FROM embeddings WHERE chunk_id IN (SELECT id FROM chunks WHERE note_id IN (`+ph+`))`, args...); err != nil {
			return PurgeReport{}, nil, err
		}
		if _, err := exec(`DELETE FROM chunks WHERE note_id IN (`+ph+`)`, args...); err != nil {
			return PurgeReport{}, nil, err
		}
		if _, err := exec(`DELETE FROM notes WHERE id IN (`+ph+`)`, args...); err != nil {
			return PurgeReport{}, nil, err
		}
	}

	// (4) Episodes, tasks đã kết thúc, session_raw quá hạn giữ.
	if rep.Episodes, err = exec(`DELETE FROM episodes WHERE at < ?`, evCut); err != nil {
		return PurgeReport{}, nil, err
	}
	if rep.Tasks, err = exec(`DELETE FROM tasks WHERE status != 'open' AND updated_at < ?`, evCut); err != nil {
		return PurgeReport{}, nil, err
	}
	if rep.Raws, err = exec(`DELETE FROM session_raw WHERE retained_until < ?`, nowS); err != nil {
		return PurgeReport{}, nil, err
	}

	// (5) Jobs done cũ + dead quá ân hạn (đủ lâu để điều tra qua status/doctor).
	// failed là job đang chờ retry — không xoá.
	if rep.Jobs, err = exec(`DELETE FROM jobs WHERE state IN ('done','dead') AND updated_at < ?`, jobsCut); err != nil {
		return PurgeReport{}, nil, err
	}

	// (6) Media: row cũ; file trong mediaDir được xoá sau COMMIT (Purge).
	mrows, err := c.QueryContext(ctx, `SELECT path FROM media WHERE updated_at < ? ORDER BY id`, evCut)
	if err != nil {
		return PurgeReport{}, nil, err
	}
	var paths []string
	for mrows.Next() {
		var p string
		if err := mrows.Scan(&p); err != nil {
			mrows.Close()
			return PurgeReport{}, nil, err
		}
		paths = append(paths, p)
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		return PurgeReport{}, nil, err
	}
	if rep.MediaRows, err = exec(`DELETE FROM media WHERE updated_at < ?`, evCut); err != nil {
		return PurgeReport{}, nil, err
	}
	files := paths[:0]
	for _, p := range paths {
		if p != "" { // "" = file gốc đã xoá chủ đích (keep_originals=false)
			files = append(files, p)
		}
	}
	return rep, files, nil
}

func int64Args(ids []int64) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}
