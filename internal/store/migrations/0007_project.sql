-- project: nhãn tự động theo thư mục làm việc (tên gốc git) — thay cho việc
-- người dùng phải chia space. Recall ưu tiên (không lọc) cùng project; recap
-- đưa việc của project đang mở lên đầu. NULL = chung (Desktop, nạp tay…).
ALTER TABLE sessions ADD COLUMN cwd TEXT;
ALTER TABLE sessions ADD COLUMN project TEXT;
ALTER TABLE notes ADD COLUMN project TEXT;
ALTER TABLE tasks ADD COLUMN project TEXT;
CREATE INDEX idx_notes_project ON notes(project);
CREATE INDEX idx_tasks_project ON tasks(project);
