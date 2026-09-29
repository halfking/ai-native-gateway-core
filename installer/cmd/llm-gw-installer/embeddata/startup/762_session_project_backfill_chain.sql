-- 762: session_summaries 项目维度回填链（R32-P-3 / 审计三十三轮 Track C）
--
-- 背景（2026-09-30 三十二轮实测，本地真库）：
--   session_summaries 330,423 行 gw_project_id 100% NULL；project_dim 0 行
--   （ACC 同步 worker 默认关）；session_project_attribution 0 行。按项目聚合的
--   读面（/api/sessions/project-costs/*、TaskAnalyticsView）在生产数据形态下
--   恒为空。根因：唯一写入方 trg_update_session_summary（563）从不写
--   gw_project_id，而 X-Gw-Project-Id 在本部署几乎从不出现
--   （request_context_attrs.project_id 558,526 行中 0 行非空）。
--
-- 真实数据形态下可用的项目标识（按优先级）：
--   P1 权威：session_dim.project_id（X-Gw-Project-Id 经 request_context_attrs
--      投影，407/547 链）。现网 1.2M 行仅 1 行非空，但语义是计费权威值，
--      出现即最优先，且允许覆盖低优先级值。
--   P2 规则推断：session_dim.application_code（join 命中行 99.9% 非空、
--      10 个稳定取值：system/brandmind/hermes/ide/...）。ref 格式 'app:<code>'，
--      与真实 project_ref 命名空间隔离（真实 ref 如 's1a-e2e-project' 不带
--      'app:' 前缀）。按 547 的分离原则：推断值只进 session_summaries.
--      gw_project_id（观测聚合列）与 session_project_attribution（method=
--      'rule'），绝不写 session_dim.project_id（计费列）。
--   不采用：request_logs_hot 无 project 列；work_type 98% NULL；目录名启发式
--      需反序列化请求体（547 已论证不入 PG 路径）。
--
-- 本迁移只建链（函数/触发器/索引），不搬存量数据：
--   新会话：session_dim 每次 UPSERT（sessionv2mirror，请求终态）触发
--      trg_session_dim_project_attr → sync_session_project_attr()。
--   存量 33 万行：bg/project_backfill_worker.go 分批回填（每批 ≤2000），
--      调用同一 sync 函数，口径单一。
--
-- 幂等：是（OR REPLACE / IF NOT EXISTS / DROP-then-CREATE trigger）。
-- 无显式 BEGIN/COMMIT：installer 通道 --single-transaction 包装（755 先例）。

-- ---------------------------------------------------------------
-- 1. 解析函数：两级优先级的唯一口径
-- ---------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.gw_resolve_project_ref(
    p_project_id       TEXT,
    p_application_code TEXT
) RETURNS TEXT
LANGUAGE sql IMMUTABLE PARALLEL SAFE
AS $$
    SELECT COALESCE(
        NULLIF(btrim(p_project_id), ''),
        CASE WHEN COALESCE(btrim(p_application_code), '') <> ''
             THEN 'app:' || btrim(p_application_code)
        END
    );
$$;

COMMENT ON FUNCTION public.gw_resolve_project_ref(TEXT, TEXT) IS
    '会话项目 ref 两级解析：X-Gw-Project-Id 权威值优先，否则 app:<application_code>；两者皆空返回 NULL（宁可留空，不用兜底值——547 原则）';

-- ---------------------------------------------------------------
-- 2. 同步函数：session_summaries.gw_project_id + 维度 upsert + 归属留痕
--    触发链与 bg 回填 worker 共用本函数（口径单一）。
-- ---------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.sync_session_project_attr(
    p_tenant_id         TEXT,
    p_session_key       TEXT,
    p_project_id        TEXT,
    p_application_code  TEXT
) RETURNS INTEGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_ref   TEXT;
    v_app   TEXT := NULLIF(btrim(p_application_code), '');
    v_rows  INTEGER := 0;
BEGIN
    IF p_session_key IS NULL OR p_session_key = '' THEN
        RETURN 0;
    END IF;
    v_ref := public.gw_resolve_project_ref(p_project_id, p_application_code);
    IF v_ref IS NULL THEN
        RETURN 0;
    END IF;

    -- 2a. 回填聚合列：NULL 填充；仅允许 权威值 覆盖 推断值（app:*），
    --     反向绝不覆盖（首值优先，与 sessionv2mirror 冲突子句同语义）。
    UPDATE session_summaries
       SET gw_project_id = v_ref
     WHERE session_key = p_session_key
       AND tenant_id IS NOT DISTINCT FROM p_tenant_id
       AND (gw_project_id IS NULL
            OR (gw_project_id LIKE 'app:%' AND v_ref NOT LIKE 'app:%'));
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows = 0 THEN
        RETURN 0;  -- 会话行不存在或已是更强值：无需维度留痕
    END IF;

    -- 2b. 维度 upsert：只为本部署推断出的 app:* ref 造本地行；
    --     绝不触碰 ACC 同步行（synced_from_acc_at IS NOT NULL）。
    IF v_ref LIKE 'app:%' THEN
        INSERT INTO project_dim (project_ref, tenant_id, name, enabled)
        VALUES (v_ref, NULLIF(btrim(p_tenant_id), ''), substring(v_ref FROM 5), TRUE)
        ON CONFLICT (project_ref) DO UPDATE
            SET name      = EXCLUDED.name,
                enabled   = TRUE,
                updated_at = NOW()
          WHERE project_dim.synced_from_acc_at IS NULL;
    END IF;

    -- 2c. 归属留痕（仅推断路径；权威路径 547 规定不经 attribution 表）。
    --     DO NOTHING：rejected 行永不重推，人工 manual 行不被覆盖。
    IF v_ref LIKE 'app:%' THEN
        INSERT INTO session_project_attribution (
            gw_session_id, tenant_id, project_ref, project_label,
            method, confidence, status, evidence
        ) VALUES (
            p_session_key,
            COALESCE(NULLIF(btrim(p_tenant_id), ''), ''),
            v_ref, v_app,
            'rule', 0.9, 'confirmed',
            jsonb_build_object(
                'source', 'session_dim.application_code',
                'application_code', v_app,
                'chain', 'startup-762'
            )
        )
        ON CONFLICT (tenant_id, gw_session_id) DO NOTHING;
    END IF;

    RETURN v_rows;
END;
$$;

COMMENT ON FUNCTION public.sync_session_project_attr(TEXT, TEXT, TEXT, TEXT) IS
    '会话→项目同步：回填 session_summaries.gw_project_id + app:* 维度 upsert（不覆盖 ACC 行）+ attribution 留痕（DO NOTHING）。762 触发链与 bg 回填 worker 共用';

-- ---------------------------------------------------------------
-- 3. 写链挂钩：session_dim UPSERT 后自动同步（新会话链路）
-- ---------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.session_dim_project_attr_trg()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM public.sync_session_project_attr(
        NEW.tenant_id, NEW.gw_session_id, NEW.project_id, NEW.application_code);
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_session_dim_project_attr_ins ON public.session_dim;
CREATE TRIGGER trg_session_dim_project_attr_ins
    AFTER INSERT ON public.session_dim
    FOR EACH ROW
    WHEN (NEW.gw_session_id IS NOT NULL AND NEW.gw_session_id <> '')
    EXECUTE FUNCTION public.session_dim_project_attr_trg();

-- UPDATE 触发器带去抖守卫：仅当项目维度字段实际变化才执行触发体
-- （sessionv2mirror 每请求终态都 UPSERT 本表，无守卫会空转每个请求）。
-- 注：PG 的 CREATE TRIGGER WHEN 只能引用 NEW/OLD，不能引用 TG_OP，
-- 因此 INSERT/UPDATE 分成两个触发器（scratch 演练实证）。
DROP TRIGGER IF EXISTS trg_session_dim_project_attr_upd ON public.session_dim;
CREATE TRIGGER trg_session_dim_project_attr_upd
    AFTER UPDATE OF project_id, application_code ON public.session_dim
    FOR EACH ROW
    WHEN (
        NEW.gw_session_id IS NOT NULL AND NEW.gw_session_id <> ''
        AND (
            NEW.project_id IS DISTINCT FROM OLD.project_id
            OR NEW.application_code IS DISTINCT FROM OLD.application_code
        )
    )
    EXECUTE FUNCTION public.session_dim_project_attr_trg();

COMMENT ON TRIGGER trg_session_dim_project_attr_ins ON public.session_dim IS
    '762: session_dim 首次落库即按两级口径同步 session_summaries.gw_project_id 与项目维度';
COMMENT ON TRIGGER trg_session_dim_project_attr_upd ON public.session_dim IS
    '762: session_dim 项目维度变化时同步 session_summaries.gw_project_id（去抖：字段无变化不执行）';

-- ---------------------------------------------------------------
-- 4. 回填扫描支撑索引（存量 33 万行 IS NULL 集合的下探索引）
-- ---------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_session_summaries_project_null
    ON public.session_summaries (session_key)
    WHERE gw_project_id IS NULL;
