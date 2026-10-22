-- 0011_script_deploy.sql — 部署模式 script：无制品、由脚本驱动的发布与回滚。
-- 对应 docs/ARCHITECTURE.md 第 59 节，字段与类型不得改动。

-- 回滚脚本（可选）：发布脚本失败或健康检查不通过时执行；为空表示不做自动回滚。
ALTER TABLE apps ADD COLUMN rollback_script TEXT;
