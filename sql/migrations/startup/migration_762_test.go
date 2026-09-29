package startup

// migration_762_test.go — R32-P-3 项目维度回填链（审计三十三轮 Track C）的
// 迁移契约测试。真库 up/down/触发器/回填纵切见 bg 包
// project_backfill_worker_realdb_test.go（TEST_DATABASE_URL 门控）；本文件
// 只钉 SQL 文本契约。
//
// 编号：762 为 2026-09-30 复核时的首个空闲号（759 跳空，760/761 已占用，
// 800 为 supplier-protocol 线保留）。

import (
	"os"
	"strings"
	"testing"
)

func TestMigration762ProjectBackfillChainContract(t *testing.T) {
	upBytes, err := os.ReadFile("762_session_project_backfill_chain.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	upCompact := normalizeSQL(up)

	// ── C1: installer 通道安全 —— 禁止显式 BEGIN/COMMIT（755 先例：
	// 显式事务会打破 installer 的 --single-transaction 包装）。──────────
	if strings.Contains(up, "BEGIN;") || strings.Contains(up, "COMMIT;") {
		t.Error("762 must not carry explicit BEGIN/COMMIT (installer single-transaction channel)")
	}

	// ── C2: 两级解析口径唯一且明确 ─────────────────────────────────────
	if !strings.Contains(upCompact,
		"CREATE OR REPLACE FUNCTION PUBLIC.GW_RESOLVE_PROJECT_REF(") {
		t.Error("762 must define gw_resolve_project_ref (两级口径的唯一实现)")
	}
	// 权威值优先、无信号返回 NULL（547：宁可留空，不用兜底值）。
	if !strings.Contains(up, "'app:' || btrim(p_application_code)") {
		t.Error("app-derived ref must be namespaced as 'app:<code>'")
	}

	// ── C3: 同步函数三段式 + 防覆盖语义 ────────────────────────────────
	if !strings.Contains(upCompact,
		"CREATE OR REPLACE FUNCTION PUBLIC.SYNC_SESSION_PROJECT_ATTR(") {
		t.Error("762 must define sync_session_project_attr (触发链与 bg worker 共用口径)")
	}
	if !strings.Contains(up,
		"(gw_project_id LIKE 'app:%' AND v_ref NOT LIKE 'app:%')") {
		t.Error("only authoritative refs may upgrade app:* refs (never the reverse)")
	}
	if !strings.Contains(up,
		"WHERE project_dim.synced_from_acc_at IS NULL") {
		t.Error("project_dim upsert must never clobber ACC-synced rows")
	}
	if !strings.Contains(up,
		"ON CONFLICT (tenant_id, gw_session_id) DO NOTHING") {
		t.Error("attribution insert must be DO NOTHING (rejected/manual rows never re-inferred)")
	}

	// ── C4: 写链挂钩挂在 session_dim（非 request_logs_hot 热路径）──────
	// PG 的 WHEN 只能引用 NEW/OLD（无 TG_OP），INSERT/UPDATE 必须拆两个触发器。
	if !strings.Contains(up, "CREATE TRIGGER trg_session_dim_project_attr_ins") {
		t.Error("762 must hook the write chain on session_dim (insert trigger)")
	}
	if !strings.Contains(up, "CREATE TRIGGER trg_session_dim_project_attr_upd") {
		t.Error("762 must hook the write chain on session_dim (update trigger)")
	}
	// TG_OP 禁令只看代码（注释允许解释这一限制本身）。
	if strings.Contains(stripSQLComments(up), "TG_OP") {
		t.Error("trigger WHEN cannot reference TG_OP (only NEW/OLD) — keep INSERT/UPDATE split")
	}
	if !strings.Contains(up, "AFTER UPDATE OF project_id, application_code ON public.session_dim") {
		t.Error("update trigger must be scoped to project dimension columns")
	}
	// 去抖守卫：仅项目维度字段变化才执行触发体。
	if !strings.Contains(up, "NEW.project_id IS DISTINCT FROM OLD.project_id") {
		t.Error("trigger WHEN must debounce on project_id distinctness")
	}
	// 热路径触发器（563）不得被重写：只禁止函数定义级操作，注释里
	// 提及 563 链路不算（本迁移头注需要交代根因）。
	if strings.Contains(upCompact, "CREATE OR REPLACE FUNCTION PUBLIC.UPDATE_SESSION_SUMMARY(") ||
		strings.Contains(upCompact, "DROP FUNCTION IF EXISTS PUBLIC.UPDATE_SESSION_SUMMARY(") ||
		strings.Contains(upCompact, "TRIGGER TRG_UPDATE_SESSION_SUMMARY") {
		t.Error("762 must not rewrite the hot-path trigger update_session_summary (563 owns it)")
	}

	// ── C5: 存量回填扫描支撑索引 ──────────────────────────────────────
	if !strings.Contains(up, "idx_session_summaries_project_null") {
		t.Error("762 must add the IS NULL partial index for backfill scans")
	}
}

func TestMigration762DownContract(t *testing.T) {
	downBytes, err := os.ReadFile("762_session_project_backfill_chain.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	for _, marker := range []string{
		"DROP TRIGGER IF EXISTS trg_session_dim_project_attr_ins",
		"DROP TRIGGER IF EXISTS trg_session_dim_project_attr_upd",
		"DROP FUNCTION IF EXISTS public.session_dim_project_attr_trg()",
		"DROP FUNCTION IF EXISTS public.sync_session_project_attr(",
		"DROP FUNCTION IF EXISTS public.gw_resolve_project_ref(",
		"DROP INDEX IF EXISTS idx_session_summaries_project_null",
	} {
		if !strings.Contains(down, marker) {
			t.Errorf("762 down missing %q", marker)
		}
	}
}
