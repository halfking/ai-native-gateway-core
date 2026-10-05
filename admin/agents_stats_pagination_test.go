package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/apihub"
)

// ── §10.27：/api/agents/stats 的分页接线门 ──────────────────────────────
//
// 为什么单独写文件而不加进 agents_test.go：那边共享的 stubService.List
// **无视 f.Offset**，直接返回 s.listOut。拿它测分页等于什么都没测 ——
// 探针侧已经踩过同一个坑（假 store 忽略 Offset ⇒ 翻页失效永远测不出来）。
// 所以这里另起一个**忠实实现 LIMIT/OFFSET** 的 stub。

type pagedAgentService struct {
	all   []apihub.Asset
	calls []apihub.Filter
}

func (s *pagedAgentService) List(_ context.Context, f apihub.Filter) ([]apihub.Asset, error) {
	s.calls = append(s.calls, f)
	// 照抄 pgStore.List 的语义：limit 截 500，OFFSET 真正跳行。
	limit := f.Limit
	if limit == 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if f.Offset >= len(s.all) {
		return nil, nil
	}
	out := s.all[f.Offset:]
	if len(out) > limit {
		out = out[:limit]
	}
	res := make([]apihub.Asset, len(out))
	copy(res, out)
	return res, nil
}

func (s *pagedAgentService) Get(context.Context, apihub.Kind, int64) (apihub.Asset, error) {
	return apihub.Asset{}, nil
}
func (s *pagedAgentService) Link(context.Context, apihub.Relationship) error { return nil }
func (s *pagedAgentService) Neighbors(context.Context, apihub.Kind, int64, int) ([]apihub.Asset, []apihub.Relationship, error) {
	return nil, nil, nil
}
func (s *pagedAgentService) ListStale(context.Context, time.Duration) ([]apihub.Asset, error) {
	return nil, nil
}
func (s *pagedAgentService) ListTenants(context.Context) ([]string, error) { return nil, nil }

func statsBody(t *testing.T, h *AgentsHandler) map[string]interface{} {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Stats(rec, httptest.NewRequest(http.MethodGet, "/api/agents/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200。body=%s", rec.Code, rec.Body.String())
	}
	var m map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("解析响应失败: %v。body=%s", err, rec.Body.String())
	}
	return m
}

// ── 门 1：超过一页时必须翻页，且 total 是全量 ─────────────────────────
//
// 旧实现写死 `Limit: 1000`，而 apihub.List 会静默截到 500 ⇒ 生产上
// default 租户 2021 行只聚合了前 500 行，total 报 500。
// 这里用 1200 行复现同一形状。

func TestStatsPagesThroughAllAssets(t *testing.T) {
	const n = 1200
	all := make([]apihub.Asset, 0, n)
	for i := 0; i < n; i++ {
		st := apihub.HealthUnknown
		if i%4 == 0 {
			st = apihub.HealthDown
		}
		all = append(all, apihub.Asset{
			Kind:        apihub.KindLLMEndpoint,
			RefID:       int64(i + 1),
			TenantID:    "default",
			Name:        "a",
			HealthState: st,
			Owner:       "o",
		})
	}
	svc := &pagedAgentService{all: all}
	h := newAgentsHandlerWithSvc(svc)

	m := statsBody(t, h)

	if got := int(m["total"].(float64)); got != n {
		t.Errorf("total = %d，期望 %d。\n"+
			"  说明 Stats 没有翻页 —— 它仍只聚合了第一页。\n"+
			"  实测调用游标: %+v", got, n, svc.calls)
	}

	byHealth := m["by_health"].(map[string]interface{})
	if got := int(byHealth["down"].(float64)); got != n/4 {
		t.Errorf("by_health.down = %d，期望 %d", got, n/4)
	}
	byKind := m["by_kind"].(map[string]interface{})
	if got := int(byKind["llm_endpoint"].(float64)); got != n {
		t.Errorf("by_kind.llm_endpoint = %d，期望 %d", got, n)
	}

	// 游标必须真的递进，且步长等于页长。
	if len(svc.calls) != 3 {
		t.Fatalf("List 被调用 %d 次，期望 3（1200 / 500）。游标: %+v", len(svc.calls), svc.calls)
	}
	for i, want := range []apihub.Filter{
		{Limit: 500, Offset: 0},
		{Limit: 500, Offset: 500},
		{Limit: 500, Offset: 1000},
	} {
		if svc.calls[i].Offset != want.Offset {
			t.Errorf("第 %d 页 Offset = %d，期望 %d", i+1, svc.calls[i].Offset, want.Offset)
		}
	}
}

// ── 门 2：单页以内不许多打一次请求 ─────────────────────────────────────
//
// 60 行 < 一页 ⇒ 必须只调 1 次。老实现也是 1 次，所以这条防的是
// 「有人把翻页写成无条件多循环一次」的反向回归。

func TestStatsSinglePageMakesOneCall(t *testing.T) {
	all := make([]apihub.Asset, 0, 60)
	for i := 0; i < 60; i++ {
		all = append(all, apihub.Asset{
			Kind: apihub.KindLLMEndpoint, RefID: int64(i + 1),
			TenantID: "default", HealthState: apihub.HealthHealthy,
		})
	}
	svc := &pagedAgentService{all: all}
	m := statsBody(t, newAgentsHandlerWithSvc(svc))

	if len(svc.calls) != 1 {
		t.Errorf("List 被调用 %d 次，期望 1。游标: %+v", len(svc.calls), svc.calls)
	}
	if got := int(m["total"].(float64)); got != 60 {
		t.Errorf("total = %d，期望 60", got)
	}
}

// ── 门 3：空表返回 0，且不出现 truncated ──────────────────────────────

func TestStatsEmptyTableHasNoTruncatedFlag(t *testing.T) {
	svc := &pagedAgentService{}
	m := statsBody(t, newAgentsHandlerWithSvc(svc))

	if got := int(m["total"].(float64)); got != 0 {
		t.Errorf("total = %d，期望 0", got)
	}
	if _, ok := m["truncated"]; ok {
		t.Errorf("空表不该带 truncated 标记，实际响应: %+v", m)
	}
}
