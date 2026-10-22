-- 0009_baota.sql — 宝塔面板部署对接：应用级 provider 与站点配置、服务器面板凭据。
-- 对应 docs/ARCHITECTURE.md 第 57 节，字段与类型不得改动。

-- provider：builtin（SSH/agent 执行 shell 计划，默认）| baota（宝塔面板 API）
ALTER TABLE apps ADD COLUMN provider TEXT NOT NULL DEFAULT 'builtin';
-- 面板站点名（默认取 slug），站点根目录约定 /www/wwwroot/<site_name>
ALTER TABLE apps ADD COLUMN site_name TEXT;
-- 面板站点端口（默认 80）
ALTER TABLE apps ADD COLUMN site_port INTEGER;
-- 面板站点类型：static | php（node 站点的进程管理不在 v1 范围）
ALTER TABLE apps ADD COLUMN site_type TEXT;
-- 部署后健康检查路径（相对站点根，如 / 或 /healthz；为空表示跳过）
ALTER TABLE apps ADD COLUMN healthcheck_path TEXT;

-- 服务器绑定的宝塔面板凭据（kind=baota），与 SSH 凭据 credential_id 相互独立
ALTER TABLE servers ADD COLUMN panel_credential_id TEXT;

CREATE INDEX IF NOT EXISTS idx_servers_panel_credential ON servers (panel_credential_id);
