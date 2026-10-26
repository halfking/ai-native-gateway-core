package admin

// R79 轮（2026-10-24）：`/api/admin/data-lifecycle/degradation/status`
// 与 `/api/admin/db-status` 的**有界预算 + 显式降级**守卫。
//
// ★ 背景（生产实测，154 build 2825）：
//
//	degradation/status → **≥120s**（curl 超时打满）
//	db-status         → 30.6~33.7s
//
//	根因：两个端点都把**无 deadline** 的 `r.Context()` 交给
//	「Redis 全量 SCAN」+「gzip 逐文件解压」⇒ 慢 Redis / 大备份时可以无限跑。
//
// ★ 三条不变量：
//
//	① **预算必须有界**（`degradationStatusBudget`）；
//	② **`degraded` 必须恒发**（前端要能区分「真的是 0」与「没查出来」）；
//	③ **查不出来必须显式记账**（`degraded_reason`）——
//	   不得让「部分计数」看起来像「完整计数」。
//
// ★ 判据断言**真实产物**（真跑一次 handler，看真实响应体），
// 而非源码字面量（R76/R77/R78 轮同款做法）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/domains/dbdegradation"
	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/redis/go-redis/v9"
)

// TestR79DegradationStatusBudgetMustBeBounded（★ 不变量①）
func TestR79DegradationStatusBudgetMustBeBounded(t *testing.T) {
	if degradationStatusBudget <= 0 {
		t.Error("★ degradation 状态类端点必须有**有界**预算（0 = 无界 ⇒ 慢 Redis 时能跑到分钟级）")
	}
	if degradationStatusBudget > 10*time.Second {
		t.Errorf("★ 预算上限 %v 太大（>10s）⇒ 端点仍会长时间占住连接", degradationStatusBudget)
	}
}

// TestR79DegradationStatusAlwaysReportsDegraded（★ 不变量②）
//
// ★ `degraded` **恒发**：前端要能区分「真的是 0」与「没查出来」。
func TestR79DegradationStatusAlwaysReportsDegraded(t *testing.T) {
	h := &Handler{}
	h.degradation = &degradationControl{} // monitor / ttl / reader 全 nil ⇒ 最简路径

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/data-lifecycle/degradation/status", nil)
	h.handleDegradationStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("★ 期望 200，实测 %d（body=%s）", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("★ 响应不是 JSON：%v（body=%s）", err, rec.Body.String())
	}
	if _, ok := body["degraded"]; !ok {
		t.Errorf("★ 响应里必须有 `degraded` 字段（恒发）—— 前端要靠它区分「真的是 0」与「没查出来」；body=%v", body)
	}
	if body["degraded"] != false {
		t.Errorf("★ 全部子查询都成功时 degraded 应为 false，实测 %v", body["degraded"])
	}
}

// TestR79DegradationStatusMustReportInterruptedScan（★ 不变量③）
//
// ★ 扫描被打断（ctx 已取消）⇒ 必须 **200 + degraded=true + degraded_reason**
//
//	⇒ 既不挂死，也不把「部分计数」当成「完整计数」。
func TestR79DegradationStatusMustReportInterruptedScan(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("★ 起 miniredis 失败：%v", err)
	}
	defer mr.Close()
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rc.Close()
	mr.Set("session:a", "1")

	h := &Handler{}
	h.degradation = &degradationControl{
		ttl: dbdegradation.NewTTLManager(session.NewRedisClientFromClient(rc), time.Hour, time.Hour),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消 ⇒ Redis 扫描必然被打断

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/data-lifecycle/degradation/status", nil).WithContext(ctx)
	h.handleDegradationStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("★ 期望 200（不得因为查不到就整块失败），实测 %d（body=%s）", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("★ 响应不是 JSON：%v（body=%s）", err, rec.Body.String())
	}
	if body["degraded"] != true {
		t.Errorf("★ 扫描被打断 ⇒ degraded 必须为 true（部分计数不得被当成完整计数）；body=%v", body)
	}
	reason, _ := body["degraded_reason"].(string)
	if reason == "" {
		t.Errorf("★ 降级原因不得为空（必须点名哪一段不可得）；body=%v", body)
	}
	if !strings.Contains(reason, "ttl") {
		t.Errorf("★ 降级原因应点名不可得的部分（ttl stats）；实际 %q", reason)
	}
}
