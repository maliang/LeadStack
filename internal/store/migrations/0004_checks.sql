-- 0004_checks.sql — M2：检测项、适用服务器、结果时间线与状态。
-- 对应 docs/ARCHITECTURE.md 第 16 节，字段与类型不得改动。

CREATE TABLE IF NOT EXISTS checks (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    kind        TEXT NOT NULL,
    target      TEXT NOT NULL,
    interval_s  INTEGER NOT NULL,
    timeout_s   INTEGER NOT NULL DEFAULT 10,
    params_json TEXT NOT NULL DEFAULT '{}',
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS check_targets (
    check_id  TEXT NOT NULL,
    server_id TEXT NOT NULL,
    PRIMARY KEY (check_id, server_id)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS check_results (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    check_id    TEXT NOT NULL,
    server_id   TEXT NOT NULL,
    ts          INTEGER NOT NULL,
    ok          INTEGER NOT NULL,
    latency_ms  INTEGER NOT NULL DEFAULT 0,
    status_code INTEGER,
    message     TEXT
);

CREATE INDEX IF NOT EXISTS idx_check_results_lookup ON check_results (check_id, server_id, ts);

CREATE TABLE IF NOT EXISTS check_state (
    check_id         TEXT NOT NULL,
    server_id        TEXT NOT NULL,
    ok               INTEGER NOT NULL,
    consecutive_fail INTEGER NOT NULL DEFAULT 0,
    since            INTEGER NOT NULL,
    last_ts          INTEGER NOT NULL,
    last_message     TEXT,
    PRIMARY KEY (check_id, server_id)
) WITHOUT ROWID;

-- 保留策略与列表聚合都按 ts 扫描，补两个辅助索引。
CREATE INDEX IF NOT EXISTS idx_check_results_ts ON check_results (ts);
CREATE INDEX IF NOT EXISTS idx_check_targets_server ON check_targets (server_id);
