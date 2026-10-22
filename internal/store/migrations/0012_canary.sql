-- 0012_canary.sql — 灰度/分批发布：先发第一批，观察一段时间再全量。
-- 对应 docs/ARCHITECTURE.md 第 61 节，字段与类型不得改动。

-- 发布策略：all（一次全量，默认）| canary（灰度：先 canary_count 台，观察 hold_seconds 秒）
ALTER TABLE releases ADD COLUMN strategy TEXT NOT NULL DEFAULT 'all';
-- 灰度批大小（第一批发几台）
ALTER TABLE releases ADD COLUMN canary_count INTEGER NOT NULL DEFAULT 0;
-- 灰度观察时长（秒）：第一批成功后等待这段时间，再决定是否继续
ALTER TABLE releases ADD COLUMN hold_seconds INTEGER NOT NULL DEFAULT 0;
