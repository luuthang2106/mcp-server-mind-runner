-- created_at cho task: briefing liệt kê "đã ghi kể từ recap trước" kèm id để
-- người dùng sửa nhanh. Task cũ: lấy updated_at làm mốc tạo gần đúng.
ALTER TABLE tasks ADD COLUMN created_at TEXT;
UPDATE tasks SET created_at = updated_at WHERE created_at IS NULL;
CREATE INDEX idx_tasks_space_created ON tasks(space_id, created_at);
CREATE INDEX idx_notes_space_created ON notes(space_id, created_at);
