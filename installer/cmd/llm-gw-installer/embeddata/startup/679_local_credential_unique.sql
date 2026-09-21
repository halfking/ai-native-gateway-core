-- Migration 679: 本地供应商占位凭据唯一性（check-then-act 根治）
--
-- 背景（2026-09-07 24h 审计 P2）：admin.ensureLocalCredential 先 SELECT
-- 是否已有占位凭据、无则 INSERT。两个并发调用（双运维操作 / 部署脚本与
-- 手工操作重叠 / repair 路径重入）都查到"无凭据"后各插一条 label='local'
-- 占位凭据——路由层看到双份并发额度（各 4 并发），本地单进程推理服务
-- 压力翻倍，且账面凭据数失真。
--
-- 修复：局部唯一索引表达"每个 provider 至多一条活着的 local 占位凭据"，
-- Go 侧 INSERT 改 ON CONFLICT DO NOTHING + 冲突后复读（见
-- admin/local_provider.go ensureLocalCredential）。
--
-- 已有重复数据的环境：先软删多余行（保留最小 id，与复用语义一致），
-- 否则唯一索引创建失败。幂等，可重放。
--
-- 2026-09-07

\set ON_ERROR_STOP on

-- 1. 折叠既有重复占位凭据（保留每个 provider 最小 id）
UPDATE credentials c
SET status = 'deleted', updated_at = NOW()
WHERE label = 'local'
  AND status <> 'deleted'
  AND id <> (
    SELECT MIN(c2.id) FROM credentials c2
    WHERE c2.provider_id = c.provider_id
      AND c2.label = 'local'
      AND c2.status <> 'deleted'
  );

-- 2. 唯一性收口
CREATE UNIQUE INDEX IF NOT EXISTS uq_credentials_local_placeholder_per_provider
  ON credentials (provider_id)
  WHERE label = 'local' AND status <> 'deleted';

COMMENT ON INDEX uq_credentials_local_placeholder_per_provider IS
  '679: at most one live local placeholder credential per provider (ensureLocalCredential race)';
