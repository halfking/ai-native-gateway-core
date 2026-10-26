package admin

// 判据：/api/admin/auto-route/index 不能因为可空列里的 NULL 而整页 500。
//
// 线上症状（154，2725-f05058f7）：
//
//	WARN  admin aggregate row scan failed; row skipped
//	      op=autoRoute.indexSnapshot error=cannot scan into dest[4] (col: billing_mode):
//	      cannot scan NULL into *string
//	ERROR admin handler internal error caller=aggregate_read_guard.go:34
//
// 端点稳定 **500@1.1s** —— 快速失败，不是超时，和 session-analytics /
// data-lifecycle-stats 那两条超时型 500 是完全不同的性质。
//
// 为什么「跳过那一行」救不了：rows.Scan 失败会把 pgx 的 rows 置为出错状态，
// 循环结束后 rows.Err() 同样非空 ⇒ writeAggRowsErr 照样写 500。
// 所以**必须能接住 NULL**，光 continue 没用。
//
// 真值（252 实测）：credential_model_index_with_current_month 427,986 行里
// billing_mode IS NULL 有 2,153 行（per_token 324,935 / token_plan 50,867 /
// free 50,031）。这不是偶发脏数据，是常态。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestAutoRouteIndexSurvivesNullBillingMode —— 真库层端到端驱动 handler。
//
// 同层：调 `handleIndexSnapshot` 本身，而不是复刻那段扫描。
// 正向下限：既要 200，也要**真的有行**——
// 「什么都没返回」与「正常返回」在只断言状态码的判据眼里完全一样。
func TestAutoRouteIndexSurvivesNullBillingMode(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	h := &AutoRouteHandlers{db: pool}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/auto-route/index", nil)
	rec := httptest.NewRecorder()
	h.handleIndexSnapshot(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("端点返回 %d（body=%s）：billing_mode 里的 NULL 会让整页 500，"+
			"而线上该端点稳定 500@1.1s", rec.Code, rec.Body.String())
	}
	var out []map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if len(out) == 0 {
		t.Error("返回 0 行：状态码对了但内容是空的，这类判据在「干脆没做」面前永远是绿的")
	}

	// billing_mode 有值时必须是字符串；没有值时这个 key 不应出现（保持原语义）。
	var withMode, withoutMode int
	for _, e := range out {
		v, ok := e["billing_mode"]
		if !ok {
			withoutMode++
			continue
		}
		s, isStr := v.(string)
		if !isStr || s == "" {
			t.Errorf("billing_mode=%v 不是非空字符串：NULL 与空串都不该写进 entry", v)
		}
		withMode++
	}
	t.Logf("返回 %d 行：带 billing_mode %d，不带 %d", len(out), withMode, withoutMode)
}

// TestAutoRouteIndexBillingModeIsNullable —— 钉住「可空列必须扫进指针」。
//
// 真库层那条依赖线上真的有 NULL 行；本条钉住**类型契约**，与数据无关。
// 若有人把 billingMode 改回 `string`，本条立刻红。
func TestAutoRouteIndexBillingModeIsNullable(t *testing.T) {
	pool := &pgxpool.Pool{}
	_ = pool

	b, err := os.ReadFile("auto_route.go")
	if err != nil {
		t.Fatalf("read auto_route.go: %v", err)
	}
	src := string(b)
	i := strings.Index(src, "func (h *AutoRouteHandlers) handleIndexSnapshot")
	if i < 0 {
		t.Fatal("找不到 handleIndexSnapshot，判据锚点失效")
	}
	end := strings.Index(src[i:], "\n}\n")
	if end < 0 {
		t.Fatal("找不到 handleIndexSnapshot 的结尾，判据锚点失效")
	}
	body := src[i : i+end]

	if !strings.Contains(body, "var billingMode *string") {
		t.Error("billingMode 不是 *string：credential_model_index 里有 2,153 行 " +
			"billing_mode IS NULL，扫进 string 会报 cannot scan NULL into *string，" +
			"并经 rows.Err() 把整个端点变成 500")
	}
	// 该 SELECT 出来的 billing_mode 没有 COALESCE 保护，所以 Go 侧必须接住 NULL。
	if strings.Contains(body, "COALESCE(cmi.billing_mode") &&
		!strings.Contains(body, "var billingMode *string") {
		t.Error("billing_mode 已加 COALESCE 且 Go 侧仍是 string：确认 COALESCE 的" +
			"默认值与「NULL 不写进 entry」的既有语义一致后可简化本判据")
	}
	_ = time.Second
}
