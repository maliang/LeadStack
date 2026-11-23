-- 0016_user_profile.sql — 用户资料：姓名 / 电话 / 备注。
-- 管理端与右上角「用户信息」共用；空串表示未填写。

ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN phone TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN notes TEXT NOT NULL DEFAULT '';
