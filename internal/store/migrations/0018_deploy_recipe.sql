-- 0018_deploy_recipe.sql — 应用内联部署方案（模板从必填对象变为可复用预设）。
-- deploy_script：自定义脚本模式的完整更新脚本。
-- deploy_template_kind/content：制品发布/Compose/Systemd 的默认方案内容，
-- 新建发布时未指定 template_id 就使用这里的快照。
ALTER TABLE apps ADD COLUMN deploy_script TEXT;
ALTER TABLE apps ADD COLUMN deploy_template_kind TEXT;
ALTER TABLE apps ADD COLUMN deploy_template_content TEXT;
