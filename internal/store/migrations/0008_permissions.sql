-- 0008_permissions.sql — 权限体系：自定义角色、服务器分组、资源范围、登录失败锁定。
-- 对应 docs/ARCHITECTURE.md 第 52–56 节，字段与类型不得改动。

CREATE TABLE IF NOT EXISTS roles (
    key         TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    permissions TEXT NOT NULL DEFAULT '[]',
    builtin     INTEGER NOT NULL DEFAULT 0,
    scope_mode  TEXT NOT NULL DEFAULT 'all',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS role_scopes (
    role_key  TEXT NOT NULL,
    kind      TEXT NOT NULL,
    target_id TEXT NOT NULL,
    PRIMARY KEY (role_key, kind, target_id)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_role_scopes_kind ON role_scopes (kind, target_id);

CREATE TABLE IF NOT EXISTS server_groups (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS server_group_members (
    group_id  TEXT NOT NULL,
    server_id TEXT NOT NULL,
    PRIMARY KEY (group_id, server_id)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_server_group_members_server ON server_group_members (server_id);

ALTER TABLE users ADD COLUMN failed_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN locked_until    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN last_failed_at  INTEGER NOT NULL DEFAULT 0;

-- 历史兼容：把既有的自由文本 servers."group" 迁移成正式分组与成员关系，
-- 升级后老用户看到的分组与原来一致（重名/空值自动跳过）。
INSERT OR IGNORE INTO server_groups (id, name, description, created_at, updated_at)
SELECT lower(hex(randomblob(16))),
       TRIM("group"),
       '由旧版 servers.group 迁移',
       CAST(strftime('%s','now') AS INTEGER) * 1000,
       CAST(strftime('%s','now') AS INTEGER) * 1000
  FROM servers
 WHERE "group" IS NOT NULL AND TRIM("group") <> ''
 GROUP BY TRIM("group");

INSERT OR IGNORE INTO server_group_members (group_id, server_id)
SELECT g.id, s.id
  FROM servers s
  JOIN server_groups g ON g.name = TRIM(s."group")
 WHERE s."group" IS NOT NULL AND TRIM(s."group") <> '';
