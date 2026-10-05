package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// TestHandleMonitorSummary_ServerDurationMeasuresTheQuery 钉住 `server_duration_ms`
// 到底量的是**哪一段时间**。
//
// 缺陷背景（2026-10-05，移动端 /nodes 排查时发现）：
//
//	原来写的是 `queryStart.Sub(startedAt)` —— 「查询开始时刻 − handler 开始时刻」。
//	两者都是**起点**，相减恒为 0（或因缓存等待而为负）。而这个字段的唯一用途
//	就是让人看见「这条 SQL 跑了多久」：web/src/components/credential-monitor/
//	helpers.ts:65 直接把它渲染成「服务端 {n}ms」。
//
//	实测线上冷查询 5.59–12.13s（完整模式 5 次里 2 次撞 15s 超时），
//	响应里的 server_duration_ms 却始终是 0 —— 正好在排查这个超时时，它是瞎的。
//
// 判据怎么做到能证伪：让 SQL 真的花掉时间（pgxmock 的 WillDelayFor），
// 然后要求报出来的毫秒数**至少**接近这个延迟。
//
//	· 修好后：`time.Since(queryStart)` ≈ 200ms → 通过
//	· 修前（两个起点相减）：≈ 0ms → 红
//
// 只断言「非负」是恒真判据，对这个缺陷完全无感。
func TestHandleMonitorSummary_ServerDurationMeasuresTheQuery(t *testing.T) {
	const delay = 200 * time.Millisecond

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	// pgxmock 的链式顺序：WithArgs/WillReturnRows 返回 *ExpectedQuery，
	// 而 WillDelayFor 只存在于 CallModifier 上，必须放在最后。
	mock.ExpectQuery(`SELECT[\s\S]*FROM credentials c`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(summaryMockRow()).
		WillDelayFor(delay)

	m := &CredentialMonitorHandlers{
		h:         &Handler{},
		summaryDB: mock, // ← 注入点；没有它整条 handler 不可测
	}

	// 清空全局缓存，保证测的是冷查询路径（否则可能直接命中、根本不跑 SQL，
	// 那样断言就成了恒真判据）。同包可直接操作缓存内部状态。
	monitorSummaryCache.mu.Lock()
	monitorSummaryCache.entries = nil
	monitorSummaryCache.flights = nil
	monitorSummaryCache.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/credentials/monitor-summary?credential_id=0&provider_id=0", nil)
	rec := httptest.NewRecorder()
	m.handleMonitorSummary(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d：%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Meta struct {
			CacheHit         bool  `json:"cache_hit"`
			ServerDurationMS int64 `json:"server_duration_ms"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v\n%s", err, rec.Body.String())
	}

	if resp.Meta.CacheHit {
		t.Fatalf("这条判据必须测冷查询，却拿到 cache_hit=true —— 缓存没清干净，" +
			"断言会对一个根本没跑 SQL 的请求生效")
	}

	// 延迟是 200ms，放宽到 150ms 吸收调度抖动；仍留足余量让 0 判定为失败。
	floor := int64(150)
	if resp.Meta.ServerDurationMS < floor {
		t.Errorf("server_duration_ms = %d，期望 ≥ %d —— 它没有量到 SQL 实际耗时。"+
			"多半是又写成了「两个起点相减」（queryStart.Sub(startedAt)），恒为 0。",
			resp.Meta.ServerDurationMS, floor)
	}
}
