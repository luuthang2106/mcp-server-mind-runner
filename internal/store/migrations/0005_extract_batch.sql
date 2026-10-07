-- 0005: gộp extract theo phiên. Mỗi phiên chỉ có MỘT job extract_session
-- đang chờ ({"session_id":…}); job xử lý mọi raw chưa extract (extracted_at
-- NULL) theo seq rồi đánh dấu. Raw cũ: coi như đã extract, trừ raw còn job
-- dạng cũ ({"raw_id":…}) đang chờ — job cũ vẫn chạy được (gom cả phiên).
ALTER TABLE session_raw ADD COLUMN extracted_at TEXT;
UPDATE session_raw SET extracted_at = at
 WHERE id NOT IN (
   SELECT CAST(json_extract(payload, '$.raw_id') AS INTEGER) FROM jobs
    WHERE type = 'extract_session' AND state IN ('queued', 'failed', 'running')
      AND json_extract(payload, '$.raw_id') IS NOT NULL);
CREATE INDEX IF NOT EXISTS idx_session_raw_pending ON session_raw(session_id, seq) WHERE extracted_at IS NULL;
