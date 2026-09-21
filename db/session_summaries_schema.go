package db

import (
	"context"
	"fmt"
	"log/slog"
)

// ensureSessionSummariesCanonical mirrors
// sql/migrations/startup/655_session_summaries_schema_reconcile.sql.
//
// 2026-09-05 session_summaries 表结构对账修复：
//
// 共享库上的 memora/kxmemory 部署用自身最小结构（session_id PK +
// summary_json）覆盖了 public.session_summaries，丢掉 canonical 全部统计列。
// 后果（本地 2026-09-04 事故，生产同构环境同样暴露）：
//   - request_logs_hot 的 AFTER INSERT 触发器 trg_update_session_summary 按
//     canonical 列 INSERT session_summaries (session_key, ...) 报 42703，
//     整条 INSERT 回滚 —— 带 gw_session_id 的业务请求日志全部落库失败
//     （探测请求无 gw_session_id 不触发，仍能落库），前端"实时请求流 →
//     点击色块"对业务请求全部 404（/api/logs/:id request log not found）。
//   - admin 读路径（serveSessionSnapshot / turns_sessions /
//     session_management_api / session_task_project_api / 会话健康 worker /
//     auto-route affinity & settle）引用 ss.session_key 等列同样 42703
//     （/api/admin/sessions/:id/snapshot HTTP 500）。
//
// 修复策略（与 migration 442 补列先例一致，全部幂等）：
//   - 只 ADD COLUMN IF NOT EXISTS，不改 PK、不删除 memora 列
//     （session_id/summary_json 等保留，memora 侧继续可用）。
//   - session_key 保持可空：非空语义由触发器/写入方保证；唯一索引默认
//     NULLS DISTINCT，存量 memora 行（session_key 为 NULL）互不冲突。
//   - ON CONFLICT (session_key) 需要非部分唯一索引；若存量数据已有重复
//     key，守卫块降级为 WARNING，不阻塞网关启动。
//
// 编号 SQL 文件供 DBA 同步流程使用；本函数保证二进制启动即生效。
func (d *DB) ensureSessionSummariesCanonical(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	// 2026-09-05 audit D-#5: the bare ALTER TABLE below assumes the table
	// exists. A deployment that DROPped public.session_summaries outright
	// (rather than overwriting its shape) would fail gateway startup here,
	// defeating the self-heal intent. Skip with a pointer to the reconcile
	// SQL instead — the same degraded posture as before this ensure existed.
	var tableExists bool
	if err := d.pool.QueryRow(ctx,
		`SELECT to_regclass('public.session_summaries') IS NOT NULL`,
	).Scan(&tableExists); err != nil {
		return fmt.Errorf("probe session_summaries existence: %w", err)
	}
	if !tableExists {
		slog.Warn("session_summaries table missing; skipping canonical schema reconcile " +
			"(run sql/migrations/startup/655_session_summaries_schema_reconcile.sql to restore)")
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		-- 先确保 tenant_id 存在（memora 最小 shape 可能缺失）
		ALTER TABLE public.session_summaries
		    ADD COLUMN IF NOT EXISTS tenant_id character varying(255);

		ALTER TABLE public.session_summaries
		    ADD COLUMN IF NOT EXISTS session_key character varying(255),
		    ADD COLUMN IF NOT EXISTS first_request_at timestamp with time zone,
		    ADD COLUMN IF NOT EXISTS last_request_at timestamp with time zone,
		    ADD COLUMN IF NOT EXISTS duration_seconds integer
		        GENERATED ALWAYS AS ((EXTRACT(epoch FROM (last_request_at - first_request_at)))::integer) STORED,
		    ADD COLUMN IF NOT EXISTS request_count integer NOT NULL DEFAULT 0,
		    ADD COLUMN IF NOT EXISTS success_count integer NOT NULL DEFAULT 0,
		    ADD COLUMN IF NOT EXISTS error_count integer NOT NULL DEFAULT 0,
		    ADD COLUMN IF NOT EXISTS total_cost_usd numeric(12,6) NOT NULL DEFAULT 0,
		    ADD COLUMN IF NOT EXISTS input_cost_usd numeric(12,6) NOT NULL DEFAULT 0,
		    ADD COLUMN IF NOT EXISTS output_cost_usd numeric(12,6) NOT NULL DEFAULT 0,
		    ADD COLUMN IF NOT EXISTS total_prompt_tokens bigint NOT NULL DEFAULT 0,
		    ADD COLUMN IF NOT EXISTS total_completion_tokens bigint NOT NULL DEFAULT 0,
		    ADD COLUMN IF NOT EXISTS total_tokens bigint
		        GENERATED ALWAYS AS (total_prompt_tokens + total_completion_tokens) STORED,
		    ADD COLUMN IF NOT EXISTS avg_latency_ms integer NOT NULL DEFAULT 0,
		    ADD COLUMN IF NOT EXISTS min_latency_ms integer,
		    ADD COLUMN IF NOT EXISTS max_latency_ms integer,
		    ADD COLUMN IF NOT EXISTS models_used text[] NOT NULL DEFAULT '{}'::text[],
		    ADD COLUMN IF NOT EXISTS primary_model character varying(100),
		    ADD COLUMN IF NOT EXISTS model_switch_count integer NOT NULL DEFAULT 0,
		    ADD COLUMN IF NOT EXISTS title character varying(200),
		    ADD COLUMN IF NOT EXISTS summary text,
		    ADD COLUMN IF NOT EXISTS key_topics text[],
		    ADD COLUMN IF NOT EXISTS user_intent character varying(50),
		    ADD COLUMN IF NOT EXISTS quality_score integer,
		    ADD COLUMN IF NOT EXISTS compliance_status character varying(20) NOT NULL DEFAULT 'compliant'::character varying,
		    ADD COLUMN IF NOT EXISTS compliance_issues_count integer NOT NULL DEFAULT 0,
		    ADD COLUMN IF NOT EXISTS prompt_injection_detected boolean NOT NULL DEFAULT false,
		    ADD COLUMN IF NOT EXISTS pii_detected boolean NOT NULL DEFAULT false,
		    ADD COLUMN IF NOT EXISTS toxic_output_detected boolean NOT NULL DEFAULT false,
		    ADD COLUMN IF NOT EXISTS work_types text[] DEFAULT '{}'::text[],
		    ADD COLUMN IF NOT EXISTS providers text[] DEFAULT '{}'::text[],
		    ADD COLUMN IF NOT EXISTS client_models text[] DEFAULT '{}'::text[],
		    ADD COLUMN IF NOT EXISTS last_summarized_at timestamp with time zone,
		    ADD COLUMN IF NOT EXISTS summary_version integer DEFAULT 1,
		    ADD COLUMN IF NOT EXISTS handoff_count integer DEFAULT 0,
		    ADD COLUMN IF NOT EXISTS last_handoff_at timestamp,
		    ADD COLUMN IF NOT EXISTS health_score integer,
		    ADD COLUMN IF NOT EXISTS health_grade character varying(1),
		    ADD COLUMN IF NOT EXISTS range character varying(20),
		    ADD COLUMN IF NOT EXISTS last_health_at timestamp,
		    ADD COLUMN IF NOT EXISTS tokens_at_trigger bigint NOT NULL DEFAULT 0,
		    ADD COLUMN IF NOT EXISTS messages_at_trigger integer NOT NULL DEFAULT 0,
		    ADD COLUMN IF NOT EXISTS last_trigger_reason character varying(64),
		    ADD COLUMN IF NOT EXISTS last_trigger_at timestamp with time zone;

		ALTER TABLE public.session_summaries
		    ADD COLUMN IF NOT EXISTS gw_project_id text,
		    ADD COLUMN IF NOT EXISTS gw_task_id text,
		    ADD COLUMN IF NOT EXISTS user_tags text[] NOT NULL DEFAULT '{}'::text[],
		    ADD COLUMN IF NOT EXISTS session_status character varying(20) NOT NULL DEFAULT 'active'::character varying,
		    ADD COLUMN IF NOT EXISTS outcome TEXT,
		    ADD COLUMN IF NOT EXISTS agent_type TEXT NOT NULL DEFAULT '',
		    ADD COLUMN IF NOT EXISTS expert_type TEXT NOT NULL DEFAULT '',
		    ADD COLUMN IF NOT EXISTS tags TEXT[] NOT NULL DEFAULT '{}'::text[];

		-- search_vector 生成列依赖 title/summary，两类列都就位后再添加。
		ALTER TABLE public.session_summaries
		    ADD COLUMN IF NOT EXISTS search_vector tsvector
		        GENERATED ALWAYS AS (
		            to_tsvector('simple', COALESCE(title, '') || ' ' || COALESCE(summary, ''))
		        ) STORED;

		DO $$
		BEGIN
		    IF NOT EXISTS (
		        SELECT 1 FROM pg_constraint
		        WHERE conname = 'chk_session_status'
		          AND conrelid = 'public.session_summaries'::regclass
		    ) THEN
		        ALTER TABLE public.session_summaries
		            ADD CONSTRAINT chk_session_status
		            CHECK (session_status IN ('active', 'completed', 'abandoned'));
		    END IF;

		    IF NOT EXISTS (
		        SELECT 1 FROM pg_constraint
		        WHERE conname = 'session_summaries_quality_score_check'
		          AND conrelid = 'public.session_summaries'::regclass
		    ) THEN
		        ALTER TABLE public.session_summaries
		            ADD CONSTRAINT session_summaries_quality_score_check
		            CHECK (quality_score >= 0 AND quality_score <= 10);
		    END IF;
		END $$;

		DO $$
		BEGIN
		    IF NOT EXISTS (
		        SELECT 1 FROM pg_indexes
		        WHERE schemaname = 'public'
		          AND tablename = 'session_summaries'
		          AND indexname = 'session_summaries_session_key_uidx'
		    ) THEN
		        CREATE UNIQUE INDEX session_summaries_session_key_uidx
		            ON public.session_summaries (session_key);
		    END IF;
		EXCEPTION WHEN others THEN
		    RAISE WARNING 'session_summaries: create unique index on session_key failed (%); ON CONFLICT (session_key) in trg_update_session_summary will keep failing until duplicates are cleaned', SQLERRM;
		END $$;

		DO $$
		BEGIN
		    IF NOT EXISTS (
		        SELECT 1 FROM pg_constraint
		        WHERE conname = 'session_summaries_session_key_per_tenant'
		          AND conrelid = 'public.session_summaries'::regclass
		    ) THEN
		        ALTER TABLE public.session_summaries
		            ADD CONSTRAINT session_summaries_session_key_per_tenant
		            UNIQUE (tenant_id, session_key);
		    END IF;
		EXCEPTION WHEN others THEN
		    RAISE WARNING 'session_summaries: add UNIQUE(tenant_id, session_key) failed (%); cross-tenant session_key collision defense inactive', SQLERRM;
		END $$;

		CREATE INDEX IF NOT EXISTS idx_session_summaries_project
		    ON public.session_summaries (tenant_id, gw_project_id)
		    WHERE gw_project_id IS NOT NULL;

		CREATE INDEX IF NOT EXISTS idx_session_summaries_task
		    ON public.session_summaries (tenant_id, gw_task_id)
		    WHERE gw_task_id IS NOT NULL;

		CREATE INDEX IF NOT EXISTS idx_session_summaries_user_tags
		    ON public.session_summaries USING gin (user_tags);

		CREATE INDEX IF NOT EXISTS idx_session_summaries_status_time
		    ON public.session_summaries (tenant_id, session_status, last_request_at DESC);

		CREATE INDEX IF NOT EXISTS idx_session_summaries_search
		    ON public.session_summaries USING gin (search_vector);

		-- 审计 R8 (2026-09-10)：692 将 user_intent widen 至 varchar(200)
		-- （50 上限令 P95≈110 字符的自动摘要在每次 upsert 上 22001 失败）。
		-- 数字迁移到不了纯 ensure 链环境，这里按 information_schema 条件
		-- 收敛；列被 v_session_flow 依赖，widen 前后同义 DROP/CREATE
		-- （视图定义与 692 逐字一致）。
		DO $$
		DECLARE
		    cur_len integer;
		BEGIN
		    SELECT character_maximum_length INTO cur_len
		      FROM information_schema.columns
		     WHERE table_schema = 'public'
		       AND table_name = 'session_summaries'
		       AND column_name = 'user_intent';
		    IF cur_len IS NOT NULL AND cur_len < 200 THEN
		        DROP VIEW IF EXISTS public.v_session_flow;
		        ALTER TABLE public.session_summaries
		            ALTER COLUMN user_intent TYPE varchar(200);
		        CREATE VIEW public.v_session_flow AS
		        SELECT
		          s.session_key,
		          s.tenant_id,
		          s.gw_project_id,
		          s.gw_task_id,
		          s.title,
		          s.summary,
		          s.user_intent,
		          s.first_request_at,
		          s.last_request_at,
		          s.duration_seconds,
		          s.request_count,
		          s.success_count,
		          s.error_count,
		          s.total_cost_usd,
		          s.total_tokens,
		          s.total_prompt_tokens,
		          s.total_completion_tokens,
		          s.user_tags,
		          s.session_status,
		          s.models_used,
		          s.primary_model,
		          LAG(s.session_key) OVER (PARTITION BY s.tenant_id, s.gw_task_id ORDER BY s.first_request_at) as prev_session_key,
		          LAG(s.title) OVER (PARTITION BY s.tenant_id, s.gw_task_id ORDER BY s.first_request_at) as prev_session_title,
		          LEAD(s.session_key) OVER (PARTITION BY s.tenant_id, s.gw_task_id ORDER BY s.first_request_at) as next_session_key,
		          LEAD(s.title) OVER (PARTITION BY s.tenant_id, s.gw_task_id ORDER BY s.first_request_at) as next_session_title,
		          ROW_NUMBER() OVER (PARTITION BY s.tenant_id, s.gw_task_id ORDER BY s.first_request_at) as session_order_in_task,
		          LAG(s.session_key) OVER (PARTITION BY s.tenant_id, s.gw_project_id ORDER BY s.first_request_at) as prev_session_in_project,
		          LEAD(s.session_key) OVER (PARTITION BY s.tenant_id, s.gw_project_id ORDER BY s.first_request_at) as next_session_in_project
		        FROM public.session_summaries s
		        WHERE s.gw_task_id IS NOT NULL OR s.gw_project_id IS NOT NULL;
		        COMMENT ON VIEW public.v_session_flow IS '会话流程视图：显示会话在任务/项目中的前后关系';
		    END IF;
		END $$;
	`)
	if err != nil {
		return err
	}
	slog.Info("session_summaries canonical columns ensured (migration 655)")
	return nil
}
