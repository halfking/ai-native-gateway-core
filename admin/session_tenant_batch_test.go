package admin

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

// 2026-10-07 审计回归门：POST /api/system/session-context/titles/batch 的
// task_id 由客户端直接提供，而 session_titles 无 tenant_id 列——修复前
// tenant_admin 可用任意 task_id 跨租户探测会话标题（同族
// extraction-status 有 assertTaskInTenant 门，本端点漏配）。
// 钉桩两层：taskIDsInTenant 的 SQL 放行集与 filterTaskPairsInTenant 的
// fail-closed 过滤。
func TestTaskIDsInTenant(t *testing.T) {
	t.Run("只放行本租户的 task_id", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()

		mock.ExpectQuery(`FROM session_summaries`).
			WithArgs("acme", []string{"t1", "t2", "t3"}).
			WillReturnRows(pgxmock.NewRows([]string{"gw_task_id"}).
				AddRow("t1").AddRow("t3"))

		allowed, err := taskIDsInTenant(context.Background(), mock,
			[]string{"t1", "t2", "t3"}, "acme")
		if err != nil {
			t.Fatal(err)
		}
		if !allowed["t1"] || !allowed["t3"] {
			t.Fatalf("in-tenant ids missing: %v", allowed)
		}
		if allowed["t2"] {
			t.Fatalf("cross-tenant id t2 must not be allowed: %v", allowed)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("空租户或空集合不触库", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()

		allowed, err := taskIDsInTenant(context.Background(), mock, []string{"t1"}, "")
		if err != nil || len(allowed) != 0 {
			t.Fatalf("empty tenant must short-circuit: %v %v", allowed, err)
		}
		allowed, err = taskIDsInTenant(context.Background(), mock, nil, "acme")
		if err != nil || len(allowed) != 0 {
			t.Fatalf("empty task set must short-circuit: %v %v", allowed, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("查询失败 fail-closed", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()

		mock.ExpectQuery(`FROM session_summaries`).
			WillReturnError(context.DeadlineExceeded)

		allowed, err := taskIDsInTenant(context.Background(), mock, []string{"t1"}, "acme")
		if err == nil {
			t.Fatal("expected error to propagate so the caller can fail closed")
		}
		if len(allowed) != 0 {
			t.Fatalf("error path must return nil set, got %v", allowed)
		}
	})
}

func TestFilterTaskPairsInTenant(t *testing.T) {
	pairs := [][2]string{{"t1", "s1"}, {"t2", "s2"}, {"t3", "s3"}}
	got := filterTaskPairsInTenant(pairs, map[string]bool{"t1": true, "t3": true})
	if len(got) != 2 || got[0][0] != "t1" || got[1][0] != "t3" {
		t.Fatalf("unexpected filter result: %v", got)
	}
	// fail-closed：空放行集 ⇒ 全部丢弃（租户解析失败时的降级路径）。
	if got := filterTaskPairsInTenant(pairs, map[string]bool{}); len(got) != 0 {
		t.Fatalf("empty allow-set must drop everything, got %v", got)
	}
}

// 钉桩端点侧的租户判定条件与 requireSessionTaskAccess / extraction-status
// 对齐：tenant_admin 且租户非空且非 default 才过滤；super_admin/admin_key/
// default 租户不过滤。文本锚定 handler 源码中的判定序列，防未来重构悄悄
// 丢掉整段过滤。
func TestTitlesBatchTenantGateWired(t *testing.T) {
	raw, err := os.ReadFile("session_title.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	for _, want := range []string{
		`IsTenantAdmin(r) && GetTenantID(r) != "" && GetTenantID(r) != "default"`,
		`taskIDsInTenant(ctx, h.db, taskIDs, tenantID)`,
		`filterTaskPairsInTenant(pairs, allowed)`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("handleSessionTitlesBatch 缺少租户收口要素 %q", want)
		}
	}
}
