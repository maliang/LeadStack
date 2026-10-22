-- 0010_deploy_v2.sql — 部署 v2：既有目录接管、重启命令、首版初始化、环境变量、健康检查端口。
-- 对应 docs/ARCHITECTURE.md 第 58 节，字段与类型不得改动。

-- 部署模式：release（默认，releases/<id> + current 符号链接，原子切换）
--           inplace（接管既有目录：备份 → 同步 → 重启）
ALTER TABLE apps ADD COLUMN deploy_mode TEXT NOT NULL DEFAULT 'release';
-- inplace 模式的目标目录（为空时取 root；再为空则报错）
ALTER TABLE apps ADD COLUMN target_dir TEXT;
-- 每次发布后执行的重启命令（如 systemctl restart nengtan-hub / docker compose up -d）
ALTER TABLE apps ADD COLUMN restart_cmd TEXT;
-- 首版初始化脚本：只在「该服务器第一次成功部署」时执行一次
ALTER TABLE apps ADD COLUMN init_script TEXT;
-- 应用级环境变量（JSON 对象），发布时写成目标目录下的 .leadstack.env（0600）
ALTER TABLE apps ADD COLUMN env_json TEXT;
-- inplace 同步时保留的路径（逗号分隔，支持 * 通配；数据目录/上传目录等）
ALTER TABLE apps ADD COLUMN keep_paths TEXT;
-- 健康检查端口（为空时：baota 通道用 site_port，builtin 通道跳过）
ALTER TABLE apps ADD COLUMN healthcheck_port INTEGER;
