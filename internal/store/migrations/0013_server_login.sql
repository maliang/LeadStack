-- 0013_server_login.sql — 服务器登录脚本：每台服务器可配置一段在目标机上执行的脚本，
-- 脚本应输出一个 JSON 对象（url/terminal/output），由 POST /servers/{id}/login 解析返回。
-- 默认空串表示未配置；脚本内容不是机密（可见于服务器详情），不得写入凭据。
ALTER TABLE servers ADD COLUMN login_script TEXT NOT NULL DEFAULT '';
