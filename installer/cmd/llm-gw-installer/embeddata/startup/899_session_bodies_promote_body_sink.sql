-- Migration 899: 把 promote_session_bodies_hot_to_partition 的**生产现行体**
-- 下沉成迁移（★ session_bodies 族的 copy-gap 修复）。
--
-- ═══════════════════════════════════════════════════════════════════════
-- 为什么需要它（★ 2026-10-23 生产只读实测 + 仓内文件级取证）
-- ═══════════════════════════════════════════════════════════════════════
--
-- 【问题：这个函数的"活体形态"只活在 Go 源码里，不在任何迁移里】
--   生产 252 上 public.promote_session_bodies_hot_to_partition 的实装形态
--   （2026-10-23 实测 prosrc 指纹）：
--     identity_args          = retention_window interval, batch_size integer
--     result_type            = TABLE(moved_count bigint)
--     has_bare_on_conflict   = t     ← 裸 ON CONFLICT DO NOTHING（无仲裁目标）
--     has_targeted_on_conflict = f   ← **没有** ON CONFLICT (id, partition_date)
--     has_reconciled         = t     ← 有 reconciled CTE
--     has_shape_guard        = t     ← 有父表/hot 列形状守卫
--     has_try_lock           = t     ← 用 pg_try_advisory_xact_lock（非阻塞）
--     src_len                = 3486
--   ⇒ 这个形态**逐字等于** db/db.go 的 ensureSessionBodiesPromoteDrain
--   （db/db.go:3984-4095，启动 ensure 链 db/db.go:527 的一个调用点 ⇒
--   **每次网关启动都重装一次函数体**）。
--
--   ⇒ 而仓内的四版迁移全部是另一种形态：
--     615 / 626 / 638 / 708 —— 全是 `ON CONFLICT (id, partition_date)
--     DO NOTHING`（**带仲裁目标**）。
--   ⇒ ⇒ **「台账说已应用」≠「实际对象是那一版」**：
--   schema_migrations 里 708 有记账行（2026-09-14 14:35:27）⇒ 但那条记账
--   描述的是**带仲裁目标**的形态 ⇒ 而活库上装的是**裸 ON CONFLICT** 的形态
--   （由 Go ensure 覆盖上去的）⇒ ⇒ **两版都不对**：
--     · 只跑迁移不启 Go ⇒ 装回 708 的带仲裁目标形态；
--     · 启 Go ⇒ 覆盖成裸 ON CONFLICT 形态。
--
-- 【为什么带仲裁目标的形态是缺陷（2026-09-17 PG 日志审计，65 轮 23505 实测，
--   逐字记录在 db/db.go:3965-3972 的注释里）】
--   708 的仲裁目标是 (id, partition_date)，但父表上还有
--     session_bodies_tenant_request_partition_key UNIQUE
--       (tenant_id, request_id, partition_date)
--   ⇒ 两者**键集不同** ⇒ 23505 不会被仲裁目标兜住 ⇒ 整批回滚 ⇒
--   ⇒ 那行永远晋升不出去 ⇒ **hot 窗口被毒丸行卡死**。
--   ★ 裸 ON CONFLICT DO NOTHING（**无仲裁目标**）兜住**所有**唯一冲突 ⇒
--    reconciled CTE 把父表已有的同 (tenant_id, request_id,
--   partition_date) 异 id 行按写端 upsert 语义刷新内容 ⇒ ⇒ hot 窗口必排干。
--
-- 【★ 本迁移不改语义：函数体逐字等于生产现行体】
--   纪律同 897（§143.96）：★ **基线必须是生产现行体** ⇒ 本迁移安装的
--   函数体 = db/db.go 的 ensureSessionBodiesPromoteDrain 逐字 ⇒
--   ⇒ 语义零变更；本迁移只做一件事：**把这个形态从「只活在 Go 源码里」
--   变成「也活在迁移里」** ⇒ ⇒ 消除 copy-gap。
--
-- 【★ 为什么这不只是形式问题】
--   「只跑迁移、不启动 Go」是一次**真实可发生的操作**（迁移通道
--   scripts/apply-db-revision-sequence.sh 就是这条路径）⇒ 在这条路径上，
--   708 的带仲裁目标形态会被装回生产 ⇒ ⇒ 23505 复发 + 毒丸行卡死
--   ⇒ ⇒ 而这正是 Go ensure 当初要修的病灶 ⇒ ⇒ **修法只活在 Go 侧 ⇒
--   修法本身在通道路径上无效**。
--
-- 【★ 为什么 899 放在通道链的【最后一项】】
--   615 / 626 / 638 / 708 都重定义这个函数 ⇒ 通道的 clobber guard 要求把它们
--   登记进 intentional_function_chains，且**链序 = 投递序、最后一项胜出**
--   （897 同款：链序 707 → 640 → 869 → 897）。⇒ 899 必须登记为
--   615 → 626 → 638 → 708 → 899 的最后一项。
--
-- 【★ 为什么 899 不改 708】
--   708 已是历史交付件（schema_migrations 有记账行），改它会让
--   「已应用」的哈希与文件内容不一致 ⇒ 本迁移以「追加新链尾」的方式
--   收敛（897 同款）。
--
-- 【★ 函数签名逐字不变】
--   retention_window interval / batch_size integer / RETURNS TABLE(
--   moved_count bigint) ⇒ CREATE OR REPLACE 是**替换**、不是新增 ⇒
--   无重载残留、无调用窗口 ⇒ Go 侧 runArchive/promote 调用零改动。
--
-- 【★ 幂等】纯 CREATE OR REPLACE FUNCTION + 后置条件断言；重复应用无副作用。

BEGIN;

-- ★★★ 函数体逐字等于生产现行体（= db/db.go ensureSessionBodiesPromoteDrain，
-- ★★★ db/db.go:3984-4095，2026-10-23 于生产 252 实测指纹核对一致）。
CREATE OR REPLACE FUNCTION public.promote_session_bodies_hot_to_partition(
    retention_window interval DEFAULT '8 hours',
    batch_size integer DEFAULT 5000
)
RETURNS TABLE(moved_count bigint)
LANGUAGE plpgsql
AS $function$
		DECLARE
		    cutoff_ts timestamptz;
		    v_parent_shape TEXT;
		    v_hot_shape TEXT;
		    v_cols TEXT;
		BEGIN
		    IF retention_window IS NULL OR retention_window <= interval '0 seconds' THEN
		        RAISE EXCEPTION 'retention_window must be positive';
		    END IF;
		    IF batch_size IS NULL OR batch_size < 1 THEN
		        RAISE EXCEPTION 'batch_size must be >= 1';
		    END IF;

		    IF to_regclass('public.session_bodies_hot') IS NULL
		       OR to_regclass('public.session_bodies') IS NULL THEN
		        RAISE EXCEPTION 'session_bodies hot and parent tables must both exist';
		    END IF;

		    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
		                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
		      INTO v_parent_shape
		      FROM pg_attribute
		     WHERE attrelid = 'public.session_bodies'::regclass
		       AND attnum > 0 AND NOT attisdropped;
		    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
		                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
		      INTO v_hot_shape
		      FROM pg_attribute
		     WHERE attrelid = 'public.session_bodies_hot'::regclass
		       AND attnum > 0 AND NOT attisdropped;
		    IF v_parent_shape IS DISTINCT FROM v_hot_shape THEN
		        RAISE EXCEPTION 'session_bodies hot/parent column contract has drifted (column set mismatch)';
		    END IF;

		    SELECT string_agg(quote_ident(attname), ',' ORDER BY attnum)
		      INTO v_cols
		      FROM pg_attribute
		     WHERE attrelid = 'public.session_bodies_hot'::regclass
		       AND attnum > 0 AND NOT attisdropped;

		    cutoff_ts := now() - retention_window;

		    IF NOT pg_try_advisory_xact_lock(hashtext('public.promote_session_bodies_hot_to_partition')) THEN
		        RETURN QUERY SELECT 0::bigint;
		        RETURN;
		    END IF;

		    EXECUTE format(
		        'WITH to_move AS (
		            SELECT %1$s
		            FROM public.session_bodies_hot
		            WHERE ts < %3$L::timestamptz
		            ORDER BY ts
		            LIMIT %2$s
		            FOR UPDATE SKIP LOCKED
		        ),
		        inserted AS (
		            INSERT INTO public.session_bodies (%1$s)
		            SELECT %1$s FROM to_move
		            ON CONFLICT DO NOTHING
		            RETURNING id, partition_date
		        ),
		        reconciled AS (
		            UPDATE public.session_bodies s
		            SET request_delta = m.request_delta,
		                response_delta = m.response_delta,
		                outbound_body = m.outbound_body,
		                request_attachments = m.request_attachments,
		                response_attachments = m.response_attachments,
		                ts = m.ts
		            FROM to_move m
		            WHERE s.tenant_id = m.tenant_id
		              AND s.request_id = m.request_id
		              AND s.partition_date = m.partition_date
		              AND s.id <> m.id
		            RETURNING m.id AS hot_id
		        ),
		        deleted AS (
		            DELETE FROM public.session_bodies_hot h
		            WHERE EXISTS (SELECT 1 FROM inserted i
		                          WHERE i.id = h.id AND i.partition_date = h.partition_date)
		               OR EXISTS (SELECT 1 FROM reconciled r WHERE r.hot_id = h.id)
		            RETURNING 1
		        )
		        SELECT count(*) FROM deleted', v_cols, batch_size::text, cutoff_ts::text)
		    INTO moved_count;

		    RETURN QUERY SELECT moved_count;
		END;
		
$function$;

COMMENT ON FUNCTION public.promote_session_bodies_hot_to_partition(interval, integer) IS
    'Atomically move old rows from session_bodies_hot to monthly partitions (708 contract checks + tenant/request-key conflict reconcile so hot window always drains). Rejects NULL or non-positive retention_window/batch_size (638 semantics kept).';

-- ── 后置条件：装完的必须是【生产现行体】的那几个性质 ───────────────────
-- ★ 判据逐条对应 2026-10-23 于生产 252 实测的 prosrc 指纹（Y4 探针）。
DO $post_899$
DECLARE
    v_prosrc TEXT;
BEGIN
    IF to_regclass('public.session_bodies_hot') IS NULL
       OR to_regclass('public.session_bodies') IS NULL THEN
        RAISE EXCEPTION '899 post-condition failed: session_bodies family missing';
    END IF;

    SELECT p.prosrc INTO v_prosrc
    FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
    WHERE n.nspname = 'public'
      AND p.proname = 'promote_session_bodies_hot_to_partition'
      AND pg_get_function_identity_arguments(p.oid) = 'retention_window interval, batch_size integer';

    IF v_prosrc IS NULL THEN
        RAISE EXCEPTION '899 post-condition failed: promote_session_bodies_hot_to_partition missing or has a different identity signature';
    END IF;

    -- ★ 必须是**裸** ON CONFLICT DO NOTHING（无仲裁目标）⇒ 兜住所有唯一冲突。
    --   带仲裁目标（ON CONFLICT (id, partition_date)）不兜父表的
    --   session_bodies_tenant_request_partition_key ⇒ 23505 复发 + 毒丸行卡死。
    IF v_prosrc NOT LIKE '%ON CONFLICT DO NOTHING%' THEN
        RAISE EXCEPTION '899 post-condition failed: installed body does not use bare ON CONFLICT DO NOTHING (a targeted arbiter would let 23505 roll the whole batch back)';
    END IF;
    IF v_prosrc LIKE '%ON CONFLICT (id, partition_date)%' THEN
        RAISE EXCEPTION '899 post-condition failed: installed body still carries the targeted arbiter ON CONFLICT (id, partition_date) (708 shape; the parent unique key is (tenant_id, request_id, partition_date))';
    END IF;

    -- ★ reconciled CTE 必须在（它负责把父表已存在的同键行刷新成 hot 载荷）。
    IF v_prosrc NOT LIKE '%reconciled AS%' THEN
        RAISE EXCEPTION '899 post-condition failed: reconciled CTE missing (parent-held rows would never be refreshed, and their hot rows would never be drained)';
    END IF;

    -- ★ 列形状守卫必须在（父表/hot 列集不一致时响亮失败，而不是静默丢列）。
    IF v_prosrc NOT LIKE '%v_parent_shape%' OR v_prosrc NOT LIKE '%v_hot_shape%' THEN
        RAISE EXCEPTION '899 post-condition failed: column shape guard missing (v_parent_shape / v_hot_shape)';
    END IF;

    -- ★ 必须用 try 锁（拿不到就跳过本轮，而不是阻塞另一个网关实例的同表排水）。
    IF v_prosrc NOT LIKE '%pg_try_advisory_xact_lock%' THEN
        RAISE EXCEPTION '899 post-condition failed: pg_try_advisory_xact_lock missing (a blocking lock would stall the peer instance)';
    END IF;

    -- ★ 目录派生的列清单必须在（不得硬编码列名清单）。
    IF v_prosrc NOT LIKE '%v_cols%' THEN
        RAISE EXCEPTION '899 post-condition failed: derived column list (v_cols) missing';
    END IF;

    RAISE NOTICE '899: promote_session_bodies_hot_to_partition installed with the production-current body (bare ON CONFLICT DO NOTHING + reconciled CTE + shape guard + try-lock)';
END
$post_899$;

-- 双账本自登记（695-705 定式）。
INSERT INTO public.schema_migrations (version, description)
VALUES ('899', 'promote_session_bodies_hot_to_partition: sink the production-current body (bare ON CONFLICT DO NOTHING + reconciled CTE) into a migration so the delivery channel no longer re-installs the 708 targeted-arbiter shape (23505 batch rollback + poison-row hot-window stall)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
