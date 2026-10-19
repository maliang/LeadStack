-- 0005_alerts.sql — M3：告警规则、通知渠道、告警与事件轨迹。
-- 对应 docs/ARCHITECTURE.md 第 21 节，字段与类型不得改动。

CREATE TABLE IF NOT EXISTS alert_rules (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    enabled        INTEGER NOT NULL DEFAULT 1,
    source         TEXT NOT NULL,
    check_ids      TEXT NOT NULL DEFAULT '[]',
    server_ids     TEXT NOT NULL DEFAULT '[]',
    groups         TEXT NOT NULL DEFAULT '[]',
    tags           TEXT NOT NULL DEFAULT '[]',
    metric         TEXT,
    comparator     TEXT,
    threshold      REAL,
    for_duration_s INTEGER NOT NULL DEFAULT 0,
    severity       TEXT NOT NULL DEFAULT 'warning',
    dedup_window_s INTEGER NOT NULL DEFAULT 300,
    channel_ids    TEXT NOT NULL DEFAULT '[]',
    silence_until  INTEGER,
    created_at     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS alert_channels (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    kind        TEXT NOT NULL,
    config_enc  BLOB NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1,
    last_error  TEXT,
    last_ok_at  INTEGER,
    created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS alerts (
    id             TEXT PRIMARY KEY,
    rule_id        TEXT NOT NULL,
    dedup_key      TEXT NOT NULL,
    server_id      TEXT,
    check_id       TEXT,
    status         TEXT NOT NULL,
    severity       TEXT NOT NULL,
    title          TEXT NOT NULL,
    message        TEXT,
    value          REAL,
    started_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    resolved_at    INTEGER,
    acked_at       INTEGER,
    acked_by       TEXT,
    notify_count   INTEGER NOT NULL DEFAULT 0,
    last_notify_at INTEGER,
    silence_until  INTEGER
);

-- 同一 dedup_key 同时只允许一条未解决告警，靠这两个索引加速查找。
CREATE INDEX IF NOT EXISTS idx_alerts_dedup ON alerts (dedup_key, status);
CREATE INDEX IF NOT EXISTS idx_alerts_started ON alerts (started_at);

CREATE TABLE IF NOT EXISTS alert_events (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    alert_id TEXT NOT NULL,
    kind     TEXT NOT NULL,
    ts       INTEGER NOT NULL,
    detail   TEXT
);

CREATE INDEX IF NOT EXISTS idx_alert_events_alert ON alert_events (alert_id, ts);
CREATE INDEX IF NOT EXISTS idx_alerts_rule ON alerts (rule_id, started_at);
