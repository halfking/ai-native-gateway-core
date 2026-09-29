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

// TestTaskSummaryAggregatesModelsTagsAndWallClockDuration (R33 P-5，真库)：
// 1. models_used / all_user_tags 必须是任务范围内 text[] 列的 DISTINCT 聚合
//    （此前 SELECT 根本不查这两列，恒 null）；
// 2. 任务级 duration_seconds 必须是 wall-clock 跨度（首会话 first_request_at
//    → 末会话 last_request_at；此前为 MAX(单会话时长)，语义错位）。
// 门控：TEST_DATABASE_URL；-short 跳过。种子行带 r33-p5- 前缀并自清理。
func TestTaskSummaryAggregatesModelsTagsAndWallClockDuration(t *testing.T) {
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
		// tenant 必须已存在（fk_session_tenant 外键）；行隔离靠 r33-p5-
		// 前缀的唯一键与清理逻辑，super_admin 视角不按租户过滤。
		tenant = "default"
		taskID = "r33-p5-task"
		sessA  = "r33-p5-sess-a"
		sessB  = "r33-p5-sess-b"
	)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 种子：s1 models {glm-5.3, gpt-5.6} tags {urgent}，跨度 [T-2h, T-90m]；
	// s2 models {glm-5.3} tags {urgent, beta}，跨度 [T-30m, T]。
	// 任务 wall-clock = T-2h → T = 7200s；任一单会话时长都 < 7200s，
	// 旧实现 MAX(duration_seconds) 会返回 5400 而非 7200。
	base := time.Now().UTC().Truncate(time.Second)
	seed := func(sess string, models, tags []string, first, last time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`INSERT INTO session_dim (gw_session_id, session_key, tenant_id, owner_user, task_id, created_at)
			 VALUES ($1,$1,$2,'r33-owner',$3,$4)
			 ON CONFLICT (gw_session_id) DO UPDATE SET task_id=EXCLUDED.task_id, tenant_id=EXCLUDED.tenant_id`,
			sess, tenant, taskID, base,
		); err != nil {
			t.Fatalf("seed session_dim %s: %v", sess, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO session_summaries (session_key, tenant_id, first_request_at, last_request_at, updated_at, models_used, user_tags)
			 VALUES ($1,$2,$3,$4,$5,$6,$7)
			 ON CONFLICT (session_key) DO UPDATE SET first_request_at=EXCLUDED.first_request_at,
			   last_request_at=EXCLUDED.last_request_at, models_used=EXCLUDED.models_used, user_tags=EXCLUDED.user_tags`,
			sess, tenant, first, last, base, models, tags,
		); err != nil {
			t.Fatalf("seed session_summaries %s: %v", sess, err)
		}
	}
	seed(sessA, []string{"glm-5.3", "gpt-5.6"}, []string{"urgent"}, base.Add(-2*time.Hour), base.Add(-90*time.Minute))
	seed(sessB, []string{"glm-5.3"}, []string{"urgent", "beta"}, base.Add(-30*time.Minute), base)
	defer func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer dcancel()
		_, _ = pool.Exec(dctx, `DELETE FROM session_summaries WHERE session_key LIKE 'r33-p5-%'`)
		_, _ = pool.Exec(dctx, `DELETE FROM session_dim WHERE gw_session_id LIKE 'r33-p5-%'`)
	}()

	h := &Handler{db: pool}
	r := httptest.NewRequest(http.MethodGet, "/api/sessions/task-flow/"+taskID, nil)
	r = SetAuthContext(r, &AuthContext{Role: "super_admin", TenantID: tenant})

	result, err := h.queryTaskSummary(ctx, r, taskID)
	if err != nil {
		t.Fatalf("queryTaskSummary: %v", err)
	}
	if result.Summary.SessionCount != 2 {
		t.Fatalf("expected 2 sessions, got %d", result.Summary.SessionCount)
	}
	if got := sortedCopy(result.Summary.ModelsUsed); !equalSlices(got, []string{"glm-5.3", "gpt-5.6"}) {
		t.Fatalf("ModelsUsed distinct aggregate wrong: %#v", result.Summary.ModelsUsed)
	}
	if got := sortedCopy(result.Summary.AllUserTags); !equalSlices(got, []string{"beta", "urgent"}) {
		t.Fatalf("AllUserTags distinct aggregate wrong: %#v", result.Summary.AllUserTags)
	}
	if got := result.Summary.DurationSeconds; got != 7200 {
		t.Fatalf("wall-clock duration should be 7200s (first session start → last session activity), got %d", got)
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
