-- ===========================================================================
-- File:          sql/migrations/startup/918_models_canonical_minimax_m31_seed.sql
-- Migration:     918
-- Purpose:       把 minimax-m3.1-flash 镜像进【基线标准目录】（★ 让 956 的前提成立）
-- Database:      llm_gateway
-- Down:          918_models_canonical_minimax_m31_seed.down.sql
--
-- ★ 为什么需要它（★ 2026-11-23 实测挖出的既有 P1，★ 非本轮引入）：
--   956_minimax_m31_flash_featured.sql 的守卫要求 models_canonical 里已有
--     canonical_name = 'minimax-m3.1-flash' 且 status = 'active'
--   ⇒ 而**全新安装库的标准目录里没有这一行**（★ 基线 02-seed.sql 的
--     seed-standard-rollout 集合实测 19 行：grok-4.6 / kimi-k* / gemini-3.* …，
--     ★ **没有** minimax-m3.1-flash）⇒ ⇒ 956 的 RAISE EXCEPTION 必触发
--   ⇒ ⇒ **全新安装链在 956 中止** ⇒ ⇒ 957 之后的全部迁移（★ 含 982-985
--     团队四件套）在新装环境永远拿不到 ⇒ ⇒ 「登记进安装器清单」≠「新装能拿到」。
--   ★ 真实报错（★ 沙箱实跑）：ERROR: migration 956 aborted:
--     canonical_name=minimax-m3.1-flash does not exist or is not active
--
-- ★ ★ 为什么这是【种子的本职】而不是新增产品口径（★ 依据仓内自述）：
--   基线 sql/schema/02-seed.sql 的头注写着：
--     "The migration files remain the source of truth; this section only mirrors
--      the IDs into the seed payload."
--   ⇒ ⇒ ★ 种子**只镜像迁移链的前提**；★ 而 956（★ 已发布、★ 211/252 均已
--     applied 的迁移）就是「这个模型该在目录里」这条前提的**来源**
--   ⇒ ⇒ ★ 把它镜像进种子 = ★ **让种子追上链里已发布迁移的断言**，⇒ ★ 不是新增口径。
--   ★★ 另有一处上游佐证：953（★ 给 models_canonical 加 featured 列的那条）
--     自己的 UPDATE 清单里**已列了 MiniMax 家族的其它两个**
--     （minimax-abab7-lite / minimax-abab7-flash）⇒ ★ 「MiniMax 入免费/入门档」
--     这件事在上游已成立；★ 956 只是补 953 漏掉的那一个。
--
-- ★ ★ 与生产的等价性（★ 2026-11-23 211 只读实测）：
--   生产那行 = id=3301413 | canonical_name=minimax-m3.1-flash | family=minimax
--              | modality=multimodal | status=active | source=discovery
--              | context_window=NULL | display_name=NULL
--   ⇒ ★ 本迁移镜像的字段（canonical_name / family / modality / status）与之
--     **逐字同口径**；★ 唯 source 不同（★ 种子既有约定是 'seed-standard-rollout'
--     ⇒ ★ **不冒充** discovery 的来源）；★ context_window 生产为 NULL
--     ⇒ ★ 本迁移同样留 NULL（★ 不编造窗口大小）。
--   ⇒ ★★ 生产上本迁移是**纯 no-op**（★ ON CONFLICT (canonical_name) DO NOTHING
--     ⇒ 发现服务同步进来的那一行不会被覆盖）⇒ ⇒ **零生产风险**。
--
-- 幂等：ON CONFLICT (canonical_name) DO NOTHING
-- 顺序：必须排在 956 **之前**（★ 956 的守卫要求本行已在场）
-- ===========================================================================

BEGIN;

-- ★ 本件是纯 DML（无强锁 DDL）⇒ 不需要 lock_timeout 守卫
--   （★ §44 那条只约束「在常驻大表上拿强锁」的迁移）。
-- ★ 仍设语句上限（★ 防御性：目录大表的 INSERT 不该无限等待）。
SET statement_timeout = '10min';

INSERT INTO public.models_canonical
    (canonical_name, family, context_window, modality, status, source, created_at, updated_at)
VALUES
    ('minimax-m3.1-flash', 'minimax', NULL, 'multimodal', 'active', 'seed-standard-rollout', NOW(), NOW())
ON CONFLICT (canonical_name) DO NOTHING;

-- 台账自登记（≥952 纪律）
INSERT INTO public.schema_migrations (version, description)
VALUES ('918', 'seed minimax-m3.1-flash into the standard-rollout catalog (mirrors the precondition of 956; no-op where discovery already synced it)')
ON CONFLICT (version) DO NOTHING;

COMMIT;
