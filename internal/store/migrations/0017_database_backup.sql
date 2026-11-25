CREATE TABLE IF NOT EXISTS database_backup_configs (
    id             TEXT PRIMARY KEY,
    server_id      TEXT NOT NULL,
    databases      TEXT NOT NULL,
    host           TEXT NOT NULL,
    port           INTEGER NOT NULL,
    username       TEXT NOT NULL,
    password_enc   BLOB NOT NULL,
    interval_hours INTEGER NOT NULL,
    retention      INTEGER NOT NULL,
    enabled        INTEGER NOT NULL DEFAULT 1,
    last_run_at    INTEGER,
    next_run_at    INTEGER NOT NULL,
    last_error     TEXT,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_database_backup_due ON database_backup_configs(enabled, next_run_at);
CREATE INDEX IF NOT EXISTS idx_database_backup_server ON database_backup_configs(server_id);
