-- 0006_deploy.sql — M4：应用、模板、制品、发布与执行日志。
-- 对应 docs/ARCHITECTURE.md 第 28 节，字段与类型不得改动。

CREATE TABLE IF NOT EXISTS apps(
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  slug TEXT UNIQUE NOT NULL,
  root TEXT,
  description TEXT,
  created_at INTEGER NOT NULL);

CREATE TABLE IF NOT EXISTS deploy_templates(
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,
  content TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL);

CREATE TABLE IF NOT EXISTS artifacts(
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  location TEXT NOT NULL,
  sha256 TEXT,
  size_bytes INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL);

CREATE TABLE IF NOT EXISTS releases(
  id TEXT PRIMARY KEY,
  app_id TEXT NOT NULL,
  template_id TEXT NOT NULL,
  version TEXT NOT NULL,
  artifact_id TEXT,
  template_snapshot TEXT NOT NULL,
  status TEXT NOT NULL,
  created_by TEXT,
  created_at INTEGER NOT NULL,
  started_at INTEGER,
  finished_at INTEGER,
  concurrency INTEGER NOT NULL DEFAULT 4);

CREATE TABLE IF NOT EXISTS release_runs(
  id TEXT PRIMARY KEY,
  release_id TEXT NOT NULL,
  server_id TEXT NOT NULL,
  status TEXT NOT NULL,
  exit_code INTEGER,
  message TEXT,
  failed_step TEXT,
  prev_release_id TEXT,
  started_at INTEGER,
  finished_at INTEGER,
  UNIQUE(release_id, server_id));

CREATE INDEX IF NOT EXISTS idx_release_runs_lookup ON release_runs(release_id, status);

CREATE TABLE IF NOT EXISTS release_logs(
  release_run_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  ts INTEGER NOT NULL,
  stream TEXT NOT NULL,
  chunk TEXT NOT NULL,
  PRIMARY KEY(release_run_id, seq)) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_releases_app ON releases(app_id, created_at);
CREATE INDEX IF NOT EXISTS idx_releases_status ON releases(status, created_at);
