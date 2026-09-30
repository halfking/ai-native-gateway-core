package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestProjectTasksSkipsNullTaskID (R34-P1，真库)：session_dim.task_id 大面积
// NULL，NULL 组进入 GROUP BY 后 Scan 进 string 失败 → project-costs 端点
// 500（R32-P-1 同类）。修复为查询侧过滤 sd.task_id IS NOT NULL。本测试用
// 762 触发器的真实写链：无 task_id 行（application_code='r34p1'）会被回填
// 进同一 project ref，但任务列表必须只含带 task 的行且不报错。
// 门控：TEST_DATABASE_URL；-short 跳过。
func TestProjectTasksSkipsNullTaskID(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	defer pool.Close()

	const (
		tenant = "default" // fk_session_tenant 外键要求已存在租户
		app    = "r34p1"
		taskID = "r34-p1-task"
		sessA  = "r34-p1-sess-a" // 无 task_id
		sessB  = "r34-p1-sess-b" // 有 task_id
	)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	base := time.Now().UTC().Truncate(time.Second)

	seed := func(sess, task, appCode string) {
		t.Helper()
		// 先 ss 后 dim：762 触发器在 session_dim 写入时同步 session_summaries，
		// dim 先种会让同步扑空（ss 行尚不存在）。
		if _, err := pool.Exec(ctx,
			`INSERT INTO session_summaries (session_key, tenant_id, first_request_at, last_request_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5)
			 ON CONFLICT (session_key) DO UPDATE SET first_request_at=EXCLUDED.first_request_at, last_request_at=EXCLUDED.last_request_at`,
			sess, tenant, base.Add(-time.Hour), base, base,
		); err != nil {
			t.Fatalf("seed session_summaries %s: %v", sess, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO session_dim (gw_session_id, session_key, tenant_id, owner_user, task_id, application_code, created_at)
			 VALUES ($1,$1,$2,'r34-owner',NULLIF($3,''),$4,$5)
			 ON CONFLICT (gw_session_id) DO UPDATE SET task_id=NULLIF(EXCLUDED.task_id,''), application_code=EXCLUDED.application_code, tenant_id=EXCLUDED.tenant_id`,
			sess, tenant, task, appCode, base,
		); err != nil {
			t.Fatalf("seed session_dim %s: %v", sess, err)
		}
	}
	seed(sessA, "", app)  // NULL task → 762 触发器回填 gw_project_id='app:r34p1'
	seed(sessB, taskID, app)
	defer func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer dcancel()
		_, _ = pool.Exec(dctx, `DELETE FROM session_summaries WHERE session_key LIKE 'r34-p1-%'`)
		_, _ = pool.Exec(dctx, `DELETE FROM session_dim WHERE gw_session_id LIKE 'r34-p1-%'`)
	}()

	// 触发器回填是同步的（AFTER INSERT），无需等 worker。
	var projSet int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM session_summaries WHERE session_key LIKE 'r34-p1-%' AND gw_project_id = 'app:r34p1'`,
	).Scan(&projSet); err != nil {
		t.Fatalf("probe backfill: %v", err)
	}
	if projSet != 2 {
		t.Fatalf("762 trigger should backfill both fixture rows, got %d", projSet)
	}

	h := &Handler{db: pool}
	r := httptest.NewRequest(http.MethodGet, "/api/sessions/project-costs/app:r34p1", nil)
	r = SetAuthContext(r, &AuthContext{Role: "super_admin", TenantID: tenant})
	tasks, err := h.queryProjectTasks(ctx, r, "app:r34p1")
	if err != nil {
		t.Fatalf("queryProjectTasks must not fail on NULL task_id rows: %v", err)
	}
	if len(tasks) != 1 || tasks[0].TaskID != taskID {
		t.Fatalf("expected exactly 1 task item %q, got %#v", taskID, tasks)
	}
}
