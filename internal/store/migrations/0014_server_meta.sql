-- 0014_server_meta.sql — 服务器档案：运维备注与自定义字段。
--
-- notes         运维备注，默认空串（最多 2000 个字符，允许换行）。
-- custom_fields 自定义字段的 JSON 对象文本（键值均为字符串），默认 '{}'；
--               约束（<=32 键、键 <=40 字符、值 <=200 字符、序列化 <=8KB、键禁控制字符）
--               由 PATCH /servers/{id} 校验，库内只保证「非空 + 合法 JSON 对象」。
-- 两者都不是机密：仅 GET /servers/{id} 返回，列表接口不返回；审计 detail 只记键名不记值。
ALTER TABLE servers ADD COLUMN notes TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN custom_fields TEXT NOT NULL DEFAULT '{}';
