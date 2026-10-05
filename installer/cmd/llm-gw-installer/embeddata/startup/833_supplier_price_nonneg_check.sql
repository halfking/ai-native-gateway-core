-- ===========================================================================
-- File:          sql/migrations/startup/833_supplier_price_nonneg_check.sql
-- Migration:     833
-- Database:      llm_gateway
-- Purpose:       给供应商侧的**价格列**加非负约束（对新写入立即强制）。
--
-- ⚠ 订正（2026-10-05）：本行初版还写着「并给『in>out』这种方向可疑但未必
-- 非法的价加一条健康面告警」。**那条告警从未实现** —— 复核 `bg/
-- routing_health_checks.go` 的 15 条 check_id（canonical_id_null … stats_
-- ground_truth_gap）逐条看过，没有任何一条检查价格方向。这与本项目里已撤回
-- 过的两处同类假承诺同形（`internal/ir/response.go` 的 "for accurate
-- multimodal billing"、`scripts/apply-db-revision-sequence.sh` 里把 821
-- 标成「820」的注释）：**文件头写了不等于代码里有**。
-- ⇒ 「in>out 方向可疑」目前是**未实现的待办**，不是本迁移的既有能力。
--   要补的话，落点是在 bg 的检查表里加第 16 条（数据面已就绪：
--   v_supplier_price_vs_baseline 里同一行两个方向自相矛盾的形态就是
--   `100.00/1.00` 那一档，倍率 20.0 / 0.04），而不是在本迁移里。
--
-- 立项依据（2026-10-04 真库实测，非推断）
--
-- 目标里「根据供应商的实际计费方式与价格进行设置，准确控制模型的实际成本」
-- 有两个前提，而**第二个前提当时完全没有**：
--
--   1. 基准价（原厂标准价）—— 826 建了列，**带 CHECK（非负）**。
--   2. 供应商价（实付价）  —— 826 的偏差视图读它算倍率，而**它在全 schema 上
--      没有任何 CHECK、NOT NULL 或枚举约束**。
--
-- 实测：CREATE TABLE 后直接 INSERT 下列三条，rc=0，全部落库：
--
--     INSERT INTO credential_model_bindings
--       (unit_price_in_per_1m, unit_price_out_per_1m, billing_mode) VALUES
--       (-5.00, -25.00, 'asdf_not_a_mode'),   -- 负价 + 乱枚举
--       (100.00, 1.00, NULL);                -- in 比 out 贵 99 倍
--
-- 危害不是「多了一个负数」，而是**偏差报表被它污染**。同一批数据灌进 826 的
-- v_supplier_price_vs_baseline（基准价 5.00/25.00），实测读数：
--
--   供应商 in/out          | 倍率 in   | 倍率 out  | 问题
--   ----------------------|-----------|-----------|--------------------------
--   -5.00 / -25.00         | -1.0000   | -1.0000   | 负倍率 = 「比原厂便宜 100%」
--   100.00 / 1.00          | 20.0000   | 0.0400    | 同一条绑定两个方向自相矛盾
--
-- 第二行是最坏的形态：out 报「比原厂便宜 96%」而 in 报「贵 1900%」，**同一行
-- 里两个数字互相打架，且没有任何字段标记它不可信**。SSOT 注释里写的「宁可少提，
-- 不可提错」是在**提取侧**说的；错价在这里换了个入口 —— 从「录入」进来，而
-- 录入侧当时一个约束都没有。
--
-- 为什么只约束「非负」，不给 out >= in、也不给 billing_mode 枚举
--
--   · 非负：**任何**计价方式下价格都不可能为负。没有例外、没有口径分歧。
--   · out >= in：绝大多数模型成立，但仓里 billing_mode 取值散落 `free` /
--     token / per_token / keyless / token_plan / code_plan / agent_plan …
--     **没有 SSOT 枚举**（grep 全仓得出一串硬编码字面量），说明「哪些模式
--     允许 out < in」这件事当前**无法从仓内确定**。拿一条无法证伪的规则去
--     挡生产写入，失败时是整轮迁移失败 ⇒ 改做健康面告警（见下），让人裁决。
--   · billing_mode 枚举：同上，加 CHECK 会误伤合法值。要收口得先给
--     billing_mode 建 SSOT 枚举，那是另一次改造。
--
-- 存量数据与「为什么是 NOT VALID」（2026-10-05 改写，此前是已验证约束）
--
-- 初版写的是不带 NOT VALID 的 ADD CONSTRAINT，它的实测行为是：只要库里有
-- 任何一行负价，ADD CONSTRAINT **整条失败**。而本迁移的投递通道是
-- `scripts/apply-db-revision-sequence.sh`，它第 4 行 `set -euo pipefail`，
-- 且逐文件用 `psql -X -v ON_ERROR_STOP=1` 执行 —— 任何一条迁移失败，
-- **整轮升级当场中止**（不是跳过后继续）。
--
-- 也就是说：初版把「某个环境恰好有存量负价」这件事，从一条数据质量问题
-- 升级成了**该环境任何一次部署都做不完**。这个代价与本迁移要关的洞
-- （负价**新写入**）不成比例。
--
-- 文件头初版给的盘点只在**一个**库上跑过：
--     SELECT count(*) AS negative_price_rows
--       FROM public.credential_model_bindings
--      WHERE unit_price_in_per_1m < 0 OR unit_price_out_per_1m < 0
--         OR cache_read_price_per_1m < 0 OR cache_write_price_per_1m < 0;
--   → 本机 127.0.0.1:5432 读数 0。但 252/245/154 三个共享库**未验**，
--   而通道是往那些库上跑的。用「一个库的读数」去担保「所有库都不会失败」，
--   正是本仓反复记的取证边界错误。
--
-- NOT VALID 把两件事分开（测试库 55432 逐条实测，见下）：
--   · **立即生效的部分**（本迁移要的东西）：对所有**新写入**立即强制。
--     实测：新 INSERT 负价 → `violates check constraint`；把既有行
--     UPDATE 成负价 → 同样被拒。负价入口就此关掉，与是否扫存量无关。
--   · **不做的部分**（本迁移刻意不做）：不扫描既有行，因此**在结构上
--     不可能因存量数据失败**。
--   · 阳性对照（防恒真）：合法价 `3.00` 的新行仍插入成功 1 行。
--
-- 两步收口（人裁决，不自动）：
--   1. 把那几行负价导出来给人看，由人决定改成 NULL（未知）还是改正 ——
--      **刻意不自动 UPDATE**：价格是钱，自动改价等于机器替运营决定了
--      「我们按这个价付」。与 826「只记账、不自动改价」同一条纪律。
--   2. 存量清理干净后跑 `ALTER TABLE public.credential_model_bindings
--      VALIDATE CONSTRAINT cmb_price_nonneg_in;`（四个各一次），convalidated
--      会由 f 变 t，约束从此被规划器纳入检查。
--
-- 幂等：DROP ... IF EXISTS + ADD，可安全重放。注意重放会重新置回 NOT VALID，
--   VALIDATE 过的环境若重放本迁移需要再跑一次 VALIDATE（与既有内容指纹
--   重放通道 `legacy_content_replays` 的语义一致：内容未变则不重放）。
-- ===========================================================================
BEGIN;

-- ---------------------------------------------------------------------------
-- (a) 非负约束
--
-- 用四个独立约束而不是一个大 CHECK：出问题时 PG 会直接报出是哪一列越界，
-- 而一个大 CHECK 只能报「违反 models_canonical_..._check」让人自己猜。
--
-- 四个都带 NOT VALID —— 理由见文件头「存量数据」节：对新写入立即强制，
-- 但不扫描既有行，因此在任何环境上都不会因存量数据而让整轮部署中止。
-- ---------------------------------------------------------------------------
ALTER TABLE public.credential_model_bindings
    DROP CONSTRAINT IF EXISTS cmb_price_nonneg_in;
ALTER TABLE public.credential_model_bindings
    ADD CONSTRAINT cmb_price_nonneg_in
    CHECK (unit_price_in_per_1m IS NULL OR unit_price_in_per_1m >= 0) NOT VALID;

ALTER TABLE public.credential_model_bindings
    DROP CONSTRAINT IF EXISTS cmb_price_nonneg_out;
ALTER TABLE public.credential_model_bindings
    ADD CONSTRAINT cmb_price_nonneg_out
    CHECK (unit_price_out_per_1m IS NULL OR unit_price_out_per_1m >= 0) NOT VALID;

ALTER TABLE public.credential_model_bindings
    DROP CONSTRAINT IF EXISTS cmb_price_nonneg_cache_read;
ALTER TABLE public.credential_model_bindings
    ADD CONSTRAINT cmb_price_nonneg_cache_read
    CHECK (cache_read_price_per_1m IS NULL OR cache_read_price_per_1m >= 0) NOT VALID;

ALTER TABLE public.credential_model_bindings
    DROP CONSTRAINT IF EXISTS cmb_price_nonneg_cache_write;
ALTER TABLE public.credential_model_bindings
    ADD CONSTRAINT cmb_price_nonneg_cache_write
    CHECK (cache_write_price_per_1m IS NULL OR cache_write_price_per_1m >= 0) NOT VALID;

-- COMMENT 里写明「未验证」：运维在库里 pg_constraint 看 convalidated 时，
-- 看到 f 必须知道它是**设计**而不是漏跑 VALIDATE，否则会去「修」它而
-- 不知道先决条件是存量负价已清干净。
COMMENT ON CONSTRAINT cmb_price_nonneg_in ON public.credential_model_bindings IS
    'A negative supplier price is always a data error. Measured 2026-10-04: without it, -5.00/1M against a 5.00 baseline yields ratio -1.0, which a cost report reads as "100% cheaper than list". Added NOT VALID on purpose: enforced on every new INSERT/UPDATE, existing rows are not scanned, so this can never abort a deploy run. See the migration header for the two-step closeout (clean existing rows, then VALIDATE CONSTRAINT). convalidated=false is expected until then.';
COMMENT ON CONSTRAINT cmb_price_nonneg_out ON public.credential_model_bindings IS
    'Same as cmb_price_nonneg_in, for the output side. NOT VALID by design.';
COMMENT ON CONSTRAINT cmb_price_nonneg_cache_read ON public.credential_model_bindings IS
    'Same as cmb_price_nonneg_in, for cache-read pricing. NOT VALID by design.';
COMMENT ON CONSTRAINT cmb_price_nonneg_cache_write ON public.credential_model_bindings IS
    'Same as cmb_price_nonneg_in, for cache-write pricing. NOT VALID by design.';

COMMIT;
