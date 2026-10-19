-- 0001_init.sql — 控制端 schema v1，对应 docs/ARCHITECTURE.md 第 5 节。
-- 约束：ID 使用 UUID v4 字符串；时间戳统一为 Unix 毫秒整数。

CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
    id            TEXT PRIMARY KEY,
    username      TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'admin',
    created_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS servers (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    host          TEXT NOT NULL,
    port          INTEGER NOT NULL DEFAULT 22,
    mode          TEXT NOT NULL DEFAULT 'agent',
    tags          TEXT NOT NULL DEFAULT '[]',
    status        TEXT NOT NULL DEFAULT 'unknown',
    last_seen_at  INTEGER,
    agent_version TEXT,
    os            TEXT,
    arch          TEXT,
    kernel        TEXT,
    created_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS enroll_tokens (
    id         TEXT PRIMARY KEY,
    token_hash TEXT UNIQUE NOT NULL,
    note       TEXT,
    expires_at INTEGER NOT NULL,
    used_at    INTEGER,
    server_id  TEXT
);

CREATE TABLE IF NOT EXISTS metrics_samples (
    server_id TEXT NOT NULL,
    metric    TEXT NOT NULL,
    ts        INTEGER NOT NULL,
    value     REAL NOT NULL,
    PRIMARY KEY (server_id, metric, ts)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS events (
    id        TEXT PRIMARY KEY,
    server_id TEXT,
    kind      TEXT NOT NULL,
    message   TEXT,
    ts        INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- 常用查询的辅助索引。
CREATE INDEX IF NOT EXISTS idx_servers_last_seen ON servers (last_seen_at);
CREATE INDEX IF NOT EXISTS idx_metrics_server_ts ON metrics_samples (server_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_events_server_ts ON events (server_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_enroll_tokens_expires ON enroll_tokens (expires_at);
