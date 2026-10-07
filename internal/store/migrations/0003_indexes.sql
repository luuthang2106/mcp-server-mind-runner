-- 0003: index cho các truy vấn nóng (claim job, dedupe enqueue, recall theo space,
-- briefing theo thời gian, purge theo TTL). Không đổi dữ liệu.
CREATE INDEX IF NOT EXISTS idx_jobs_state_run ON jobs(state, run_after);
CREATE INDEX IF NOT EXISTS idx_jobs_type_payload ON jobs(type, payload) WHERE state IN ('queued','failed','running');
CREATE INDEX IF NOT EXISTS idx_notes_space_kind ON notes(space_id, kind, updated_at) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_notes_deleted ON notes(deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_episodes_at ON episodes(at);
CREATE INDEX IF NOT EXISTS idx_tasks_space_status ON tasks(space_id, status, updated_at);
CREATE INDEX IF NOT EXISTS idx_relations_space_created ON relations(space_id, created_at);
CREATE INDEX IF NOT EXISTS idx_relations_source ON relations(source_note_id);
CREATE INDEX IF NOT EXISTS idx_chunks_note ON chunks(note_id);
CREATE INDEX IF NOT EXISTS idx_media_updated ON media(updated_at);
CREATE INDEX IF NOT EXISTS idx_session_raw_retained ON session_raw(retained_until);
