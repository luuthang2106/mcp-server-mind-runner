-- 0004: dữ liệu có cấu trúc cho second brain.
-- notes.meta: JSON tuỳ loại (why, who, when, as_of, ref, alternatives, scope, title, summary)
--   — chỉ chứa trường người dùng nói ra, không bịa.
-- notes.status: active|superseded — decision/fact bị thay thế vẫn giữ để tra lịch sử
--   nhưng recall/briefing mặc định ẩn.
-- tasks: đủ 5W — why, owner, waiting_on, due_at (YYYY-MM-DD), constraints.
ALTER TABLE notes ADD COLUMN meta TEXT NOT NULL DEFAULT '{}';
ALTER TABLE notes ADD COLUMN status TEXT NOT NULL DEFAULT 'active';
ALTER TABLE notes ADD COLUMN superseded_by INTEGER REFERENCES notes(id);
ALTER TABLE tasks ADD COLUMN why TEXT;
ALTER TABLE tasks ADD COLUMN owner TEXT;
ALTER TABLE tasks ADD COLUMN waiting_on TEXT;
ALTER TABLE tasks ADD COLUMN due_at TEXT;
ALTER TABLE tasks ADD COLUMN constraints TEXT;
CREATE INDEX IF NOT EXISTS idx_tasks_due ON tasks(space_id, due_at) WHERE status = 'open' AND due_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_notes_status ON notes(status) WHERE status != 'active';
