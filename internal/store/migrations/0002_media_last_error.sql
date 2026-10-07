-- 0002: lý do lỗi/chết của media row (doctor/status đọc không cần join jobs).
ALTER TABLE media ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
