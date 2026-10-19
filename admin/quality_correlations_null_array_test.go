package admin

// ★ 这道门存在的理由（2026-10-20 复核轮，
//   docs/audit/2026-10-20-request-logs-session-turns-复核轮.md §5.8）：
//
//   逐页扫描在 `/quality-correlations` 上抓到**唯一一条 JS 运行时异常**：
//
//     [Vue Error] TypeError: Cannot read properties of null (reading 'reduce')
//       at ac.fn (http://127.0.0.1:8782/assets/QualityCorrelationsView-*.js:1:2961)
//
//   根因是**契约两端不一致**：
//     · 后端 `handleQualityCorrelations` 里 `var breakdown []QualityCorrelationRow`
//       是 Go 的 **nil slice** ⇒ JSON 序列化成 **`null`** 而不是 `[]`；
//       而同一个 handler 里下方的 `insights := []QualityCorrelationInsight{}`
//       被**显式**初始化了 ⇒ 同一个响应里两个数组字段的空值形态不一致，
//       显然是疏漏。
//     · 前端 `QualityCorrelationsView.vue` 的 TS 类型把 `breakdown` 声明成
//       **非空数组**（`breakdown: QualityCorrelationRow[]`）⇒ 类型是**假的**，
//       于是页面直接 `resp.value.breakdown.reduce(...)`，null 时当场抛异常。
//       同页另有三处 `resp.breakdown.length` / `resp.insights.length` 同样炸。
//
//   触发条件是**合法业务状态**：本地库 `session_turns` 里
//   `is_auto_request IS TRUE` 的行数为 0 ⇒ 聚合查询 0 行 ⇒ breakdown 为 null。
//   「无数据」不该以异常呈现。
//
//   这道门钉的**不变量**（不是节点名、不是具体行数）：
//     **数组型字段在响应里必须是 JSON 数组，永远不是 null。**
//   它对「有数据」与「无数据」两种状态都成立 ⇒ 不会被本机数据状态带偏。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// qualityCorrelationsRealDBPool 连真库；没有 TEST_PG_URL 就跳过。
func qualityCorrelationsRealDBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that this endpoint's contract holds")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to TEST_PG_URL: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("cannot ping TEST_PG_URL: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestQualityCorrelations_ArrayFieldsAreNeverNull 打真端点，断言响应里的
// `breakdown` / `insights` **永远是 JSON 数组**（`[]` 也算），绝不是 `null`。
//
// 走真 handler + 真库（不是抄一份 SQL）：handler 里的 nil-slice → null 这类
// 序列化问题只有在**真的序列化一次**时才看得见。
func TestQualityCorrelations_ArrayFieldsAreNeverNull(t *testing.T) {
	pool := qualityCorrelationsRealDBPool(t)
	h := &AutoRouteHandlers{db: pool}

	req := httptest.NewRequest(http.MethodGet,
		"/api/admin/auto-route/quality-correlations?days=7", nil)
	rec := httptest.NewRecorder()
	h.handleQualityCorrelations(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%.400s)", rec.Code, rec.Body.String())
	}

	// 用 map 解码才能看见「null」与「[]」的区别：解进结构体时两者都变成 nil 切片。
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v (body=%.400s)", err, rec.Body.String())
	}
	for _, field := range []string{"breakdown", "insights"} {
		v, ok := raw[field]
		if !ok {
			t.Errorf("响应缺少字段 %s（body=%.400s）", field, rec.Body.String())
			continue
		}
		if v == nil {
			t.Errorf("字段 %s 序列化成了 null —— 数组字段必须是 JSON 数组（[]），"+
				"否则前端 `resp.%s.reduce(...)` / `.length` 会抛 TypeError", field, field)
			continue
		}
		if _, isArr := v.([]any); !isArr {
			t.Errorf("字段 %s 不是 JSON 数组（实际类型 %T）", field, v)
		}
	}
}

// TestQualityCorrelations_BreakdownIsNotDeclaredAsNilSlice 静态门：源码里
// `breakdown` 必须**显式初始化为空切片**，不能是 `var breakdown []QualityCorrelationRow`。
//
// 为什么要有这道静态门：真库门断言的是**输出**；而把 nil slice 换回去这种改法
// 在**有数据**的环境里输出不变（只有无数据时才暴露）⇒ 光靠真库门会漏。
// 两道门分工：静态门钉**写法**，真库门钉**输出**。
func TestQualityCorrelations_BreakdownIsNotDeclaredAsNilSlice(t *testing.T) {
	raw, err := os.ReadFile("quality_correlations.go")
	if err != nil {
		t.Fatalf("read quality_correlations.go: %v", err)
	}
	src := string(raw)
	if strings.Contains(src, "var breakdown []QualityCorrelationRow") {
		t.Fatal("quality_correlations.go 又把 breakdown 声明成 nil slice 了 —— " +
			"Go 的 nil slice 序列化成 null（不是 []），前端会直接抛 TypeError")
	}
	if !strings.Contains(src, "breakdown := []QualityCorrelationRow{}") {
		t.Error("quality_correlations.go 没有把 breakdown 显式初始化为空切片 —— " +
			"空数组字段必须恒返 []（与同函数里的 insights 一致）")
	}
}
