package credentialstate

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// 本文件钉住 2026-11-02「4xx 归因族收口轮」新增的**绑定级**档位
// （errorsx.KindModelUnavailable）在 credentialstate 上的闸门。
//
// 判据本体在 errorsx（errorsx/model_unavailable_test.go）；本文件钉调用点。
//
// ★ 与请求侧族（request_rejected）的处置**相反**，这是本轮最容易搞错的一格：
//
//	请求侧（request_rejected）：早退 ⇒ 什么都不记（证据指向这次请求）
//	绑定级（model_unavailable）：要记 ⇒ 标记这一对 broken（证据指向这一对）
//
// ★ 为什么不能早退：早退意味着这一对**永远**留在候选池里，直到探针阶梯
//   慢慢发现它（生产实测：那 6 对的 consecutive_failures 已到 9~21，仍在
//   被反复重探）。绑定级证据的价值恰恰是「立刻把这一对摘掉」，
//   而它的作用域**天然是 (凭据, 模型)**（key := m.cacheKey(credID, model)）
//   ⇒ 兄弟模型不受影响。
//
// ★ 为什么不能立刻扇出主动探针：上游已经说了「我不提供它」，再探一次
//   只是再打一次必然失败的请求（且探针拿到的 404 会被判成 protocol ⇒
//   退避 6 小时 ⇒ 探针进入盲区，这正是事故的放大环节之一）。

// TestUpdateOnFailureModelUnavailableIsRecorded 钉「要记」：
// 绑定级失败必须落到 (凭据,模型) 这一对的状态上。
func TestUpdateOnFailureModelUnavailableIsRecorded(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, nil)

	probeTriggered := false
	m.SetActiveProbeSubmitter(func(credID int, model, tenantID, parentReqID string) {
		probeTriggered = true
	}, 2)

	m.UpdateOnFailure(ctx, 1, "some-model", errorsx.KindModelUnavailable, "req-1", "default", "")

	state, _ := m.getFromMemCache(m.cacheKey(1, "some-model"))
	if state == nil {
		t.Fatal("model_unavailable 没有落下任何状态 —— 绑定级证据必须让这一对" +
			"离开候选池，否则它会一直留在路由里被反复打（生产实测 6 对的 " +
			"consecutive_failures 已到 9~21）")
	}
	if state.ConsecutiveFails == 0 {
		t.Fatal("model_unavailable 没有计入 consecutive_fails")
	}
	if state.LastError != string(errorsx.KindModelUnavailable) {
		t.Fatalf("state.LastError = %q, want %q", state.LastError, errorsx.KindModelUnavailable)
	}
	// 第一次失败还不该立刻扇出探针（阈值是 2 次），但也不能是「立即」集合。
	if probeTriggered {
		t.Error("model_unavailable 立刻扇出了主动探针 —— 上游已经说了「我不提供它」，" +
			"再探一次只是再打一次必然失败的请求")
	}
}

// TestUpdateOnFailureModelUnavailableMarksPairBrokenAfterTwo 钉「第二次即
// 标记 broken」：与 model_not_found 同档（永久性证据的最低档）。
func TestUpdateOnFailureModelUnavailableMarksPairBrokenAfterTwo(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, nil)

	m.UpdateOnFailure(ctx, 1, "some-model", errorsx.KindModelUnavailable, "req-1", "default", "")
	m.UpdateOnFailure(ctx, 1, "some-model", errorsx.KindModelUnavailable, "req-2", "default", "")

	state, _ := m.getFromMemCache(m.cacheKey(1, "some-model"))
	if state == nil {
		t.Fatal("model_unavailable 没有落下任何状态")
	}
	if state.Available {
		t.Fatal("连续两次绑定级失败后这一对仍然 Available —— " +
			"绑定级证据应当把这一对标记为不可用")
	}
	if state.RecoverAt == nil {
		t.Fatal("标记 broken 后没有设置 RecoverAt —— 恢复只能靠探针阶梯时，" +
			"这一对会永远留在不可用状态")
	}
}

// TestUpdateOnFailureModelUnavailableIsNotRequestSide 钉判据本身的归属：
// model_unavailable **不在**请求侧族里。
//
// ★ 这是一条回归护栏：若有人把 model_unavailable 摘进
//
//	errorsx.IsRequestSideError（只改了 errorsx 没改调用点，或反之），
//	本门会红 —— 而且红的方式正是事故的形态：这一对永远摘不掉。
func TestUpdateOnFailureModelUnavailableIsNotRequestSide(t *testing.T) {
	if errorsx.IsRequestSideError(errorsx.KindModelUnavailable) {
		t.Fatal("IsRequestSideError(KindModelUnavailable) = true —— " +
			"绑定级证据被当成请求侧 ⇒ 这一对永远留在候选池里被反复打")
	}
	if !errorsx.IsRequestSideError(errorsx.KindRequestRejected) {
		t.Fatal("IsRequestSideError(KindRequestRejected) = false —— " +
			"请求侧族的早退口径依赖这个判据")
	}
}
