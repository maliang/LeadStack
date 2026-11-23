-- 0015_server_accounts.sql — 服务器访问账号：一台服务器可保存多条登录信息
-- （名称 / 地址 / 用户名 / 密码 / 备注 / 自定义字段 / 一键登录脚本）。
--
-- 密码只存 AES-256-GCM 密文（与 credentials.secret_enc 同一把主密钥）；
-- 自定义字段与服务器档案一样用 JSON 对象文本存储，默认 '{}'。
-- 删除服务器时账号一并删除（DeleteServer 显式清理，迁移里也保留级联声明）。

CREATE TABLE IF NOT EXISTS server_accounts (
    id                 TEXT PRIMARY KEY,
    server_id          TEXT NOT NULL,
    name               TEXT NOT NULL,
    address            TEXT NOT NULL DEFAULT '',
    username           TEXT NOT NULL DEFAULT '',
    password_enc       BLOB,
    note               TEXT NOT NULL DEFAULT '',
    custom_fields      TEXT NOT NULL DEFAULT '{}',
    login_script       TEXT NOT NULL DEFAULT '',
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_server_accounts_server
    ON server_accounts (server_id, created_at, id);
