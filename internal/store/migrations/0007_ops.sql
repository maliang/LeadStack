-- 0007_ops.sql — M5/M6：审计日志、API Token、定时任务，users 增列。
-- 对应 docs/ARCHITECTURE.md 第 36/37/38/42 节，字段与类型不得改动。

CREATE TABLE IF NOT EXISTS audit_logs(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts INTEGER NOT NULL,
  actor TEXT NOT NULL,
  actor_role TEXT,
  action TEXT NOT NULL,
  resource_type TEXT,
  resource_id TEXT,
  method TEXT,
  path TEXT,
  status_code INTEGER,
  ip TEXT,
  user_agent TEXT,
  detail TEXT);

CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_logs(ts);
CREATE INDEX IF NOT EXISTS idx_audit_actor ON audit_logs(actor, ts);
CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_logs(action, ts);

ALTER TABLE users ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN updated_at INTEGER;

CREATE TABLE IF NOT EXISTS api_tokens(
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  token_hash TEXT UNIQUE NOT NULL,
  role TEXT NOT NULL,
  created_by TEXT,
  created_at INTEGER NOT NULL,
  last_used_at INTEGER,
  expires_at INTEGER,
  revoked_at INTEGER);

CREATE INDEX IF NOT EXISTS idx_api_tokens_hash ON api_tokens(token_hash);

CREATE TABLE IF NOT EXISTS scheduled_tasks(
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  cron TEXT NOT NULL,
  command TEXT NOT NULL,
  timeout_s INTEGER NOT NULL DEFAULT 60,
  enabled INTEGER NOT NULL DEFAULT 1,
  concurrency INTEGER NOT NULL DEFAULT 4,
  created_at INTEGER NOT NULL,
  last_run_at INTEGER,
  next_run_at INTEGER);

CREATE TABLE IF NOT EXISTS scheduled_task_targets(
  task_id TEXT NOT NULL,
  server_id TEXT NOT NULL,
  PRIMARY KEY(task_id, server_id)) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS scheduled_task_runs(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id TEXT NOT NULL,
  server_id TEXT NOT NULL,
  ts INTEGER NOT NULL,
  status TEXT NOT NULL,
  exit_code INTEGER,
  duration_ms INTEGER,
  stdout TEXT,
  stderr TEXT,
  message TEXT);

CREATE INDEX IF NOT EXISTS idx_str_task ON scheduled_task_runs(task_id, ts);
CREATE INDEX IF NOT EXISTS idx_str_ts ON scheduled_task_runs(ts);
