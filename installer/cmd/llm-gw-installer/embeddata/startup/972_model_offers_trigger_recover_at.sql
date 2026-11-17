-- Migration: 972_model_offers_trigger_recover_at
-- Purpose: 修 model_offers_update_trigger 丢失 unavailable_recover_at —— 节点状态恢复
--          地平线（F3 残留）反复反弹的根因；并一次性清掉存量残留行。
-- Author: System
-- Date: 2026-11-17
--
-- ============================================================================
-- 0. 一句话根因
-- ============================================================================
--
-- `credential_model_bindings` 的健康状态是**四元组**，四列必须同时成立：
--
--   (available, unavailable_reason, unavailable_at, unavailable_recover_at)
--     可用 ⇒ reason IS NULL AND unavailable_at IS NULL AND recover_at IS NULL
--     不可用 ⇒ reason 非空 AND unavailable_at 非空 AND recover_at 非空（= 下次重试时刻）
--
-- 直写 cmb 的所有 Go 路径都同时维护这四列（见 domains/credential/writer.go、
-- credentialhealth/checker.go、bg/credential_recovery.go、bg/node_probe.go …）。
--
-- ★ **经 model_offers 视图的写入不是这样**：该视图是 INSTEAD OF UPDATE 触发器，
--   触发器把视图行翻译成 cmb 行的 UPDATE，而**触发器的 SET 清单是手写的一串列名**。
--   211 实测（2026-11-17，pg_get_functiondef）该清单里有 `available` /
--   `unavailable_reason` / `unavailable_at`，**唯独没有 `unavailable_recover_at`**
--   ⇒ 经视图写的恢复动作「清了 reason、清了 unavailable_at，却把 recover_at 留着」
--   ⇒ 该行落进矛盾态：available=TRUE 但 recover_at 仍指向未来（节点当初判不可用时
--     写入的「计划重试时刻」）。
--
-- ★ 仓内早已知道这件事，只是只绕了它没修它：
--   credentialhealth/checker.go:469-473 白纸黑字写着
--     「the view DOES expose unavailable_recover_at, but the INSTEAD OF UPDATE
--       trigger does NOT propagate that column to cmb, so writing it here would
--       be silently dropped.」
--   ⇒ 该处的处置是「cmb 侧先写好，视图侧只镜像触发器认得的列」。
--     这条约定**只覆盖了 cmb 侧先行写好的那一次**；凡「只有视图侧动作、
--     cmb 侧没先清」的场景（下面 3. 列出的三处），标记就被永久留下。
--
-- ============================================================================
-- 1. 症状：F3 残留地平线在 37~231 行之间反复反弹（2026-11-17 观测窗 19 轮）
-- ============================================================================
--
-- 监控脚本 scripts/audit/f3-auto-check.sh 的口径是：
--
--   SELECT COUNT(*) FROM credential_model_bindings
--   WHERE unavailable_recover_at IS NOT NULL
--     AND unavailable_at IS NULL
--     AND available = TRUE;
--
-- 即「**已恢复、但恢复标记没被清掉**的残留行」——它**不是**「当前不可用的节点数」。
-- 211 实测：残留行 100% 落在 availability_state='ready' + lifecycle_status='active'
-- 的健康凭据上，available 全为 TRUE ⇒ 这些绑定**现在都是可路由的**。
--
--   时刻    残留    99023
--   02:51   131      96
--   03:30    37       7     ← 谷值
--   04:00   205     146     ← 峰值
--   05:30   205     145     ← 峰值
--   07:00   203     147     ← 峰值
--   10:00   192     147     ← 峰值
--   11:00   218     138     ← 峰值
--   14:00   228     147     ← 峰值
--   16:00   231     147     ← 峰值（观测窗最高）
--
-- 形貌：**约 1 小时一轮的锯齿**，峰值稳定在 185~231，谷值稳定在 37~107，
-- 19 轮**从未归零**。驱动方是凭据 99023（provider 95302）的批量翻转，
-- 另有一个供应商（provider 18）的三个凭据 8/18/19 同步在 18~20 行。
--
-- ★ 为什么会锯齿（两条路径打同一块地平线）：
--   涨 = 经视图的镜像恢复写入（触发器丢字段 ⇒ 标记留下）
--   跌 = cmb 侧直写（cmbExpiredRecoverySQL 等，四列一起清）
--   ⇒ 谁的节奏快谁说了算 ⇒ 永久拉锯。
--
-- ★ 指纹核对（211 真库，2026-11-17 10:00 实测 192 行）：
--   unavailable_reason IS NULL       : 192/192  ← 触发器清 reason 的签名
--   unavailable_recover_at > NOW()    : 176/192  ← 标记仍指向未来（「计划重试时刻」）
--   admin_protected                  : 0/192
--   updated_at 集中区间              : 09:48:53~09:56:48（8 分钟一批）
--   ⇒ 与「一次批量镜像恢复写入」逐项吻合。
--
-- ============================================================================
-- 2. 修法：把 unavailable_recover_at 补进触发器的 SET 清单
-- ============================================================================
--
-- ★ 修法：把 unavailable_recover_at 补进触发器的 SET 清单（分支顺序见下）
--
--   unavailable_recover_at = CASE
--       WHEN NEW.available IS NOT NULL AND NEW.available = TRUE THEN NULL
--       WHEN NEW.unavailable_recover_at IS NOT NULL THEN NEW.unavailable_recover_at
--       ELSE credential_model_bindings.unavailable_recover_at
--   END,
--
--   分支① 绑定被置为可用 ⇒ 清空（「置可用清标记」= 本次根修）
--   分支② 未置可用但给了重试时刻 ⇒ 用它（「置不可用写重试时间」从此在视图上可用）
--   分支③ 其余 ⇒ 保持原值（对现有调用方**零行为变化**）
--
-- ★ 为什么分支顺序**不能**照抄它两个兄弟列的写法（「显式给值」在前）：
--   INSTEAD OF UPDATE 触发器的 NEW 行里，**未被 SET 触碰的列携带旧值**。
--   调用方通常只写 `SET available=TRUE, unavailable_reason=NULL,
--   unavailable_at=NULL`（**不写** recover_at）⇒ NEW.unavailable_recover_at
--   = OLD 的那个未来时间戳（非 NULL）⇒ 若「显式给值」在前，第一个分支就会命中
--   ⇒ 旧标记被原样保留 ⇒ **缺陷照旧**。
--   ★ 隔离 PG 真跑腿抓到的正是这一版：写法「看起来」与兄弟列同款，真跑却是
--     RESIDUE（见 scripts/audit/f3-trigger-recover-at-isolated-leg.sh 步骤 6）。
--   ⇒ 触发器**分辨不出**「显式写 NULL」与「没写这一列」⇒ 只能让**状态语义**
--     （available=TRUE ⇒ 无待恢复）优先于**来源语义**（调用方给没给值）。
--   ⇒ 顺带说明：兄弟列（reason / unavailable_at）的「显式给值」分支之所以没出事，
--     是因为**每个调用方都显式写了它们**（写 NULL 也算「给了值」—— NULL 落在
--     第二个分支）；recover_at 是唯一一个调用方常常不碰的列。
--
-- ★ 为什么「available=TRUE ⇒ 必须清 recover_at」是唯一正确的不变式：
--   矛盾态 available=TRUE ∧ recover_at 非空 没有合法语义 —— 它既不是「待恢复」
--   （unavailable_at 是 NULL，说明没人判它不可用），也不是「已恢复」
--   （recover_at 还在，说明重试时刻还挂着）。cmb 侧直写路径的既有约定也是
--   「available=TRUE ⇒ 四列全清」，本迁移只是让**经视图的那条路**也守同一条约定。
--
-- ★ 本次不改的两处（有意为之，登记在案）：
--   ① 触发器仍**不镜像** context_window_source / context_window_updated_at ——
--      那两列是 cmb 侧写入方自有的元数据，不是健康四元组；视图级写入不该改它们。
--   ② 分支③ 保持原值 ⇒ 「经视图置不可用但调用方没给重试时刻」的调用方
--      （discovery.go 的 staleExpiryOffersSQL 等）**行为不变**；它们的行由
--      cmbExpiredRecoverySQL 的 COALESCE(unavailable_recover_at,
--      unavailable_at + INTERVAL '30 seconds') 兜住（该 COALESCE 化已是
--      2026-11-17 节点状态漂移根修轮落的），不受本迁移影响。
--
-- ============================================================================
-- 3. 存量清理（必须在触发器修好之后，同一事务内、同一迁移内）
-- ============================================================================
--
-- 修好触发器只保证「**不再新增**」；已经躺在库里的残留行必须一次性清掉，
-- 否则它们会一直挂在监控口径里，直到各自的 recover_at 先后到期被 cmb 侧扫走
-- （未来时刻 ⇒ 最长的要等 7 天）。
--
-- ★ 清理谓词 = 监控口径本身（available=TRUE ∧ unavailable_at IS NULL ∧
--   recover_at 非空）⇒ 只动「已恢复但标记没清」的矛盾行，不碰任何在用状态位。
--   211 实测该集合内 admin_protected 全为 0（无管理员手工保护行被误伤），
--   且 available 全为 TRUE ⇒ 清理**不改变任何绑定的可路由性**。
--
-- ★ 顺序不可颠倒：若先清存量、后修触发器，下一次镜像恢复写入会立刻把它们
--   重新踩回（那正是本轮观测到的「清完又涨」）。
--
-- ============================================================================
-- 4. 实测（目标环境 = 211，acc-pg17 / PostgreSQL 17.10 / arm64）
-- ============================================================================
--
-- 迁移体成本（211 真库只读取证 + 隔离 PG 真跑，见本文件末尾 §6 验证记录）：
--   · CREATE OR REPLACE FUNCTION：不锁任何表（只替换函数定义）⇒ 毫秒级
--   · 存量清理 UPDATE：211 实测残留 ~231 行（谓词走 unavailable_recover_at
--     非空过滤，命中行数极少）⇒ 毫秒级
--   · 两者都在一个事务里，statement_timeout=10min / lock_timeout=5s 兜底
--
-- ============================================================================
-- 5. 迁移体
-- ============================================================================
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- ── 5.1 触发器修复 ────────────────────────────────────────────────────────
-- ★ 函数体 = 568_credential_priority_flag.sql 的现行体 + unavailable_recover_at
--   三分支 CASE（插在 unavailable_at 之后，与两个兄弟列同款）。
--   ★ 只加这一处，其余逐字保持不变 ⇒ 行为差异面 = 「标记的清空/保留」这一条。
CREATE OR REPLACE FUNCTION public.model_offers_update_trigger() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    v_pm_id BIGINT;
BEGIN
    SELECT provider_model_id INTO v_pm_id
    FROM credential_model_bindings WHERE id = OLD.id;

    IF v_pm_id IS NOT NULL THEN
        UPDATE provider_models SET
            canonical_id = COALESCE(NEW.canonical_id, provider_models.canonical_id),
            standardized_name = COALESCE(NEW.standardized_name, provider_models.standardized_name),
            outbound_model_name = COALESCE(NEW.outbound_model_name, provider_models.outbound_model_name),
            last_seen_at = COALESCE(NEW.last_seen_at, provider_models.last_seen_at),
            updated_at = now()
        WHERE id = v_pm_id;
    END IF;

    UPDATE credential_model_bindings SET
        available = COALESCE(NEW.available, credential_model_bindings.available),
        unavailable_reason = CASE
            WHEN NEW.unavailable_reason IS NOT NULL THEN NEW.unavailable_reason
            WHEN NEW.available IS NOT NULL AND NEW.available = TRUE THEN NULL
            ELSE credential_model_bindings.unavailable_reason
        END,
        unavailable_at = CASE
            WHEN NEW.unavailable_at IS NOT NULL THEN NEW.unavailable_at
            WHEN NEW.available IS NOT NULL AND NEW.available = TRUE THEN NULL
            ELSE credential_model_bindings.unavailable_at
        END,
        -- ★ 972（2026-11-17 F3 残留地平线根修轮）：补第四列。
        --   ★ 分支顺序**不能**照抄上面两列（「显式给值」在前）：
        --     INSTEAD OF UPDATE 触发器的 NEW 行里，**未被 SET 触碰的列携带旧值**
        --     ⇒ 调用方只写 `SET available=TRUE, unavailable_reason=NULL,
        --     unavailable_at=NULL`（不写 recover_at）时，NEW.unavailable_recover_at
        --     = OLD 的那个未来时间戳（非 NULL）⇒ 若把「显式给值」放前面，
        --     第一个分支就会命中 ⇒ **旧标记被原样保留 ⇒ 缺陷照旧**。
        --     ★ 这就是隔离 PG 真跑腿（scripts/audit/f3-trigger-recover-at-isolated-leg.sh）
        --       抓到的第一版修法的错：写法「看起来」与两个兄弟列同款，
        --       真跑却是 RESIDUE。⇒ 判据是「调用方有没有真的写这一列」，
        --       而触发器**分辨不出**「显式写 NULL」与「没写」——所以只能让
        --       状态语义（available=TRUE ⇒ 无待恢复）优先于来源语义。
        --   最终语义：
        --     ① 置为可用 ⇒ 清空（available=TRUE ∧ recover_at≠NULL 是矛盾态，
        --        既非「待恢复」（unavailable_at 为 NULL）也非「已恢复」
        --        （重试时刻还挂着）⇒ 必须清）
        --     ② 未置可用但调用方给了重试时刻 ⇒ 用它（「置不可用写重试时间」）
        --     ③ 其余 ⇒ 保持原值（对现有调用方零行为变化）
        unavailable_recover_at = CASE
            WHEN NEW.available IS NOT NULL AND NEW.available = TRUE THEN NULL
            WHEN NEW.unavailable_recover_at IS NOT NULL THEN NEW.unavailable_recover_at
            ELSE credential_model_bindings.unavailable_recover_at
        END,
        admin_protected = CASE
            WHEN NEW.admin_protected IS NOT NULL THEN NEW.admin_protected
            ELSE credential_model_bindings.admin_protected
        END,
        routing_tier = COALESCE(NEW.routing_tier, credential_model_bindings.routing_tier),
        weight = COALESCE(NEW.weight, credential_model_bindings.weight),
        manual_priority = COALESCE(NEW.manual_priority, credential_model_bindings.manual_priority),
        success_rate = COALESCE(NEW.success_rate, credential_model_bindings.success_rate),
        p95_latency_ms = COALESCE(NEW.p95_latency_ms, credential_model_bindings.p95_latency_ms),
        active_sessions = COALESCE(NEW.active_sessions, credential_model_bindings.active_sessions),
        consecutive_failures = COALESCE(NEW.consecutive_failures, credential_model_bindings.consecutive_failures),
        unit_price_in_per_1m = COALESCE(NEW.unit_price_in_per_1m, credential_model_bindings.unit_price_in_per_1m),
        unit_price_out_per_1m = COALESCE(NEW.unit_price_out_per_1m, credential_model_bindings.unit_price_out_per_1m),
        cache_read_price_per_1m = COALESCE(NEW.cache_read_price_per_1m, credential_model_bindings.cache_read_price_per_1m),
        cache_write_price_per_1m = COALESCE(NEW.cache_write_price_per_1m, credential_model_bindings.cache_write_price_per_1m),
        currency = COALESCE(NEW.currency, credential_model_bindings.currency),
        billing_mode = COALESCE(NEW.billing_mode, credential_model_bindings.billing_mode),
        pricing_source = COALESCE(NEW.pricing_source, credential_model_bindings.pricing_source),
        pricing_updated_at = COALESCE(NEW.pricing_updated_at, credential_model_bindings.pricing_updated_at),
        context_window_override = COALESCE(NEW.context_window_override, credential_model_bindings.context_window_override),
        priority = COALESCE(NEW.priority, credential_model_bindings.priority),
        updated_at = now()
    WHERE id = OLD.id;

    RETURN NEW;
END;
$$;

-- ── 5.2 存量清理（一次性）──────────────────────────────────────────────────
-- 谓词 = F3 监控口径本身；只清「已恢复但标记没清」的矛盾行。
-- 第二遍执行命中 0 行 ⇒ 幂等。
UPDATE credential_model_bindings cmb
SET unavailable_recover_at = NULL,
    updated_at = NOW()
WHERE cmb.available = TRUE
  AND cmb.unavailable_at IS NULL
  AND cmb.unavailable_recover_at IS NOT NULL;

-- ── 5.3 台账自登记（≥952 纪律）───────────────────────────────────────────
-- ★ 双账本纪律：211 的 public.schema_migrations 与 public.gateway_db_revision_sequences
--   都必须有 972 的行，否则「账本说没做」⇒ 后续按账本判断的人会踩空
--   （962 那次断层的同型，见 internal/sqlguard/migration_delivery_channel_coverage_test.go）。
INSERT INTO public.schema_migrations (version, description)
VALUES ('972', 'model_offers_update_trigger: 补 unavailable_recover_at（修 F3 残留地平线反弹的根因）+ 一次性清存量')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
