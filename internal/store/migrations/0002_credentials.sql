-- 0002_credentials.sql — M1：SSH 凭证、servers 扩展列、主机信息快照。
-- 对应 docs/ARCHITECTURE.md 第 11 节，字段与类型不得改动。

CREATE TABLE IF NOT EXISTS credentials (
    id          TEXT PRIMARY KEY,
    name        TEXT UNIQUE NOT NULL,
    kind        TEXT NOT NULL,
    username    TEXT NOT NULL,
    secret_enc  BLOB NOT NULL,
    fingerprint TEXT,
    created_at  INTEGER NOT NULL
);

-- servers 扩展列：凭证绑定、分组、主机密钥策略、最近一次采集错误。
ALTER TABLE servers ADD COLUMN credential_id TEXT;
ALTER TABLE servers ADD COLUMN "group" TEXT;
ALTER TABLE servers ADD COLUMN host_key_policy TEXT NOT NULL DEFAULT 'accept-new';
ALTER TABLE servers ADD COLUMN last_error TEXT;

CREATE TABLE IF NOT EXISTS server_host_info (
    server_id    TEXT PRIMARY KEY,
    info_json    TEXT NOT NULL,
    collected_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_servers_credential ON servers (credential_id);
CREATE INDEX IF NOT EXISTS idx_servers_group ON servers ("group");
CREATE INDEX IF NOT EXISTS idx_servers_mode ON servers (mode);
