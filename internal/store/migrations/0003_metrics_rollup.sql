-- 0003_metrics_rollup.sql — M1：指标降采样汇总表。
-- 对应 docs/ARCHITECTURE.md 第 11 节，字段与类型不得改动。

CREATE TABLE IF NOT EXISTS metrics_rollup (
    server_id TEXT NOT NULL,
    metric    TEXT NOT NULL,
    bucket    TEXT NOT NULL,
    ts        INTEGER NOT NULL,
    avg       REAL NOT NULL,
    min       REAL NOT NULL,
    max       REAL NOT NULL,
    cnt       INTEGER NOT NULL,
    PRIMARY KEY (server_id, metric, bucket, ts)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_rollup_bucket_ts ON metrics_rollup (bucket, ts);
