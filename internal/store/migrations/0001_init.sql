-- 0001_init.sql — schema gốc (spec §4). Forward-only; migrate.go tạo schema_migrations.
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);

CREATE TABLE spaces (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,               -- personal | work | learning
  policy TEXT NOT NULL DEFAULT 'cloud',    -- cloud | local
  created_at TEXT NOT NULL
);

CREATE TABLE sessions (
  id TEXT PRIMARY KEY,                     -- hook session_id (Code) hoặc uuid (Desktop)
  client TEXT NOT NULL,                    -- claude-code | claude-desktop
  space_id INTEGER NOT NULL REFERENCES spaces(id),
  started_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,
  transcript_path TEXT,                    -- Code: file JSONL
  transcript_offset INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE notes (
  id INTEGER PRIMARY KEY,
  space_id INTEGER NOT NULL REFERENCES spaces(id),
  kind TEXT NOT NULL DEFAULT 'note',       -- note|fact|preference|decision|task_hint|transcript|caption — vòng đời: fact/preference/decision vĩnh viễn, còn lại là sự kiện (365 ngày)
  text TEXT NOT NULL,
  tags TEXT NOT NULL DEFAULT '[]',         -- JSON array
  source TEXT NOT NULL,                    -- hook:stop | tool:remember | ingest:<path> | media:<id>
  session_id TEXT REFERENCES sessions(id),
  content_hash TEXT NOT NULL,              -- sha256(space|kind|text) — ghi idempotent
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL, deleted_at TEXT,
  UNIQUE (space_id, content_hash)
);

CREATE TABLE episodes (                    -- checkpoint tóm tắt của phiên
  id INTEGER PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES sessions(id),
  seq INTEGER NOT NULL,
  summary TEXT NOT NULL, at TEXT NOT NULL,
  UNIQUE (session_id, seq)
);

CREATE TABLE session_raw (                 -- delta thô gzip — phục vụ re-derive
  id INTEGER PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES sessions(id),
  seq INTEGER NOT NULL,
  content BLOB NOT NULL,                   -- gzip(JSONL delta)
  at TEXT NOT NULL, retained_until TEXT NOT NULL,
  UNIQUE (session_id, seq)
);

CREATE TABLE tasks (
  id INTEGER PRIMARY KEY,
  space_id INTEGER NOT NULL REFERENCES spaces(id),
  title TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'open',     -- open|done|dropped
  next_step TEXT, updated_at TEXT NOT NULL
);

-- Deviation #1 so với spec §4: thêm created_at để purge relations không note nguồn (spec §10).
CREATE TABLE relations (
  id INTEGER PRIMARY KEY,
  space_id INTEGER NOT NULL REFERENCES spaces(id),
  from_ref TEXT NOT NULL, to_ref TEXT NOT NULL,
  rel_type TEXT NOT NULL,
  source_note_id INTEGER REFERENCES notes(id),
  created_at TEXT NOT NULL
);

CREATE TABLE media (                       -- subdomain Media/Omni
  id INTEGER PRIMARY KEY,
  space_id INTEGER NOT NULL REFERENCES spaces(id),
  sha256 TEXT NOT NULL,
  kind TEXT NOT NULL,                      -- audio | image | video
  path TEXT NOT NULL,                      -- tương đối trong media/ (video: file audio đã trích)
  bytes INTEGER NOT NULL,
  duration_s REAL,                         -- audio/video
  transcript TEXT,                         -- audio/video: transcript; image: caption
  model TEXT,                              -- model omni đã dùng
  status TEXT NOT NULL DEFAULT 'queued',   -- queued|processing|done|failed|dead
  source TEXT NOT NULL,                    -- tool:ingest | cli | watch:<dir>
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE (space_id, sha256)                -- ingest lặp không tạo trùng
);

CREATE TABLE chunks (
  id INTEGER PRIMARY KEY,
  note_id INTEGER NOT NULL REFERENCES notes(id) ON DELETE CASCADE,
  ordinal INTEGER NOT NULL,
  text TEXT NOT NULL, token_count INTEGER NOT NULL,
  UNIQUE (note_id, ordinal)
);

CREATE VIRTUAL TABLE chunks_fts USING fts5(
  text, content='chunks', content_rowid='id',
  tokenize = "unicode61 remove_diacritics 2"
);

CREATE TABLE embeddings (
  chunk_id INTEGER NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
  model TEXT NOT NULL, dim INTEGER NOT NULL,
  vec BLOB NOT NULL,                       -- float32 LE, dim*4 bytes
  created_at TEXT NOT NULL,
  PRIMARY KEY (chunk_id, model)
);

CREATE TABLE jobs (
  id INTEGER PRIMARY KEY,
  type TEXT NOT NULL,                      -- embed_chunk|extract_session|summarize_session|transcribe_media|purge|backup
  payload TEXT NOT NULL DEFAULT '{}',
  state TEXT NOT NULL DEFAULT 'queued',    -- queued|running|done|failed|dead
  attempts INTEGER NOT NULL DEFAULT 0,
  run_after TEXT NOT NULL, last_error TEXT,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);

-- FTS5 external-content không tự đồng bộ — 3 trigger giữ chunks_fts khớp chunks.
CREATE TRIGGER chunks_ai AFTER INSERT ON chunks BEGIN
  INSERT INTO chunks_fts(rowid, text) VALUES (new.id, new.text);
END;
CREATE TRIGGER chunks_ad AFTER DELETE ON chunks BEGIN
  INSERT INTO chunks_fts(chunks_fts, rowid, text) VALUES('delete', old.id, old.text);
END;
CREATE TRIGGER chunks_au AFTER UPDATE ON chunks BEGIN
  INSERT INTO chunks_fts(chunks_fts, rowid, text) VALUES('delete', old.id, old.text);
  INSERT INTO chunks_fts(rowid, text) VALUES (new.id, new.text);
END;

-- Seed spaces cơ bản (policy "work=local" là ví dụ config, không phải seed).
INSERT OR IGNORE INTO spaces(name, policy, created_at) VALUES
  ('personal','cloud', strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  ('work','cloud', strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  ('learning','cloud', strftime('%Y-%m-%dT%H:%M:%SZ','now'));
