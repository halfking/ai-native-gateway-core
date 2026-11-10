-- 956_minimax_m31_flash_featured.sql
-- 标记 minimax-m3.1-flash 为 featured（免费/入门级模型的探测优先）
--
-- 背景：2026-11-10 上游恢复检测及时性轮（211 真库 acc-pg17 取证）。
--
-- ★ 本迁移的选名口径与 953 不同，且必须如此：
--
-- 1) 953（2026-11-09）用一份**手写名单**标注 featured。该名单 26 项里有
--    **11 项在 models_canonical 里根本不存在**（幻觉名，取证核对）：
--      baichuan2-turbo / deepseek-v3-flash / gemini-2-flash / glm-5-flash /
--      glm-5.1-flash / minimax-abab7-flash / minimax-abab7-lite /
--      qwen-plus-flash / qwen-turbo-flash / yi-lightning / yi-spark
--    ⇒ 953 实际只命中 15 个 canonical。名单「看起来生效」是因为
--    UPDATE 不报错、命中数不校验 —— 缺的那 11 项是静默失效的。
--    ⇒ 本迁移**只写经真库核对存在的 canonical_name**，且下面有命中数断言。
--
-- 2) 2026-11-10 取证给出的硬判据（不是「看起来常用」而是**实测**）：
--    免费凭据在 24h 窗口内**有真实业务流量**的 canonical 只有两个：
--      - glm-5.3-flash      585 次（raw 名 z-ai/glm-5.3-flash）—— 953 已标记
--      - minimax-m3.1-flash 110 次（raw 名 minimaxai/minimax-m3.1-flash）—— 未标记
--    ⇒ 本迁移只补 minimax-m3.1-flash 这一个。
--
-- ★ 为什么「有流量」是唯一入选判据：
--   featured 的作用面是**探针分层**（scope 门放行 / backoff 乘数 / 队列优先），
--   只影响**探测量**，不改路由。给零流量的冷门模型加 featured 只会白白抬高
--   探测量（实测：free 面 999 个候选 canonical 里 788 个只有 1 个绑定对、
--   24h 流量为 0）。⇒ 判据 = 「免费 + 有真实业务流量 + 尚未 featured」。
--
-- ★ 与 953 的分工：
--   953 负责**通用入门级模型**（按名标注，覆盖面广）。
--   956 负责**实测有流量的免费模型**（按流量标注，精确）。
--   两者并存不冲突：956 的 UPDATE 带 featured=FALSE 守卫，幂等。
--
-- ★ 另一条平行的修法（同轮实施，见 bg/model_tier.go）：
--   即使 canonical 被标 featured，探针侧的 scope 门 / backoff 乘数 /
--   队列优先级都用 globalIsFeaturedModel(raw, "") —— **只比 raw 名**。
--   而 provider_models.raw_model_name 多数带供应商命名空间前缀
--   （z-ai/glm-5.3-flash、minimaxai/minimax-m3.1-flash）。
--   ⇒ 只标裸 canonical 名对 namespaced 免费对**零效果**
--     （受控对照：namespaced 的 featured 与非 featured 同为 4 次/24h）。
--   ⇒ 同轮在 model_tier.go 的装载处展开 raw 变体，两者叠加才闭环。
--   本迁移因此只负责「标 canonical」这一半。

BEGIN;
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '10min';

DO $$
DECLARE
    v_hit INTEGER;
BEGIN
    -- 前置守卫：目标 canonical 必须存在（防再次写入幻觉名）。
    IF NOT EXISTS (
        SELECT 1 FROM models_canonical
        WHERE canonical_name = 'minimax-m3.1-flash' AND status = 'active'
    ) THEN
        RAISE EXCEPTION
            'migration 956 aborted: canonical_name=minimax-m3.1-flash does not exist or is not active';
    END IF;

    -- 标记 featured（幂等：featured=FALSE 守卫）
    UPDATE models_canonical
    SET featured = TRUE
    WHERE featured = FALSE
      AND status = 'active'
      AND canonical_name = 'minimax-m3.1-flash';

    GET DIAGNOSTICS v_hit = ROW_COUNT;

    -- 命中数断言：目标已被别人（并发会话/前序迁移）标记过时 v_hit=0，
    -- 属正常幂等；但**目标必须处于 featured=TRUE 终态**。
    IF NOT EXISTS (
        SELECT 1 FROM models_canonical
        WHERE canonical_name = 'minimax-m3.1-flash'
          AND status = 'active'
          AND featured = TRUE
    ) THEN
        RAISE EXCEPTION
            'migration 956 aborted: minimax-m3.1-flash is not featured=TRUE after update';
    END IF;

    RAISE NOTICE 'migration 956: minimax-m3.1-flash featured=TRUE (rows updated=%)', v_hit;
END $$;

INSERT INTO public.schema_migrations (version, description)
VALUES ('956', 'mark minimax-m3.1-flash featured: free model with measured 24h business traffic (110 calls)')
ON CONFLICT (version) DO NOTHING;
COMMIT;
