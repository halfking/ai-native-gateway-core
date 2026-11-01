package credentialstate

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// 本文件钉住 2026-11-02「上游 4xx 被记成节点不健康」一轮在
// **credentialstate 旧凭据状态表**上的闸门。
//
// 这一处的危害在两处：
//
//	① UpdateOnFailure 的早退集合：不在集合里的 kind 会累计 consecutive_fails，
//	   3 连败后把凭据置 cooling（凭据级 ⇒ 该凭据下**所有**模型、**所有**
//	   租户一起不可路由）。
//	② probeImmediately 集合：它含 KindTransient ⇒ 一次失败就**立刻**扇出一次
//	   主动探针。而探针问上游的是「你健康吗」，上游的答复却是「你这条请求
//	   我不收」⇒ 探针拿到的 4xx 被判成 protocol ⇒ 退避 6 小时 ⇒ 探针进入盲区。
//
// 事故当天两者都发生了（211 生产，credential 99005）。
//
// 判据本体在 errorsx（IsRequestSideError）；本文件钉调用点。

// TestUpdateOnFailureRequestRejectedIsIgnored 钉早退：请求侧拒绝既不记
// 失败、也不触发主动探针、也不落任何状态。
//
// 用 nil DB 建 Manager：本用例要验的是「早退」——早退发生在任何持久化之前，
// 因此不需要真库（仓内依赖真库的用例由 setupTestDB 直接 skip）。
func TestUpdateOnFailureRequestRejectedIsIgnored(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, nil)

	probeTriggered := false
	m.SetActiveProbeSubmitter(func(credID int, model, tenantID, parentReqID string) {
		probeTriggered = true
	}, 2)

	m.UpdateOnFailure(ctx, 1, "some-model", errorsx.KindRequestRejected, "req-1", "default", "")

	if probeTriggered {
		t.Error("request_rejected 触发了主动探针 —— 探针查不出客户端的错误，" +
			"且探针拿到的 4xx 会被判成 protocol 退避 6 小时（事故的放大环节之一）")
	}
	// 直接查内存缓存（不能用 GetState：它回落查库，而本用例刻意用 nil DB
	// ——早退发生在任何持久化之前，所以「缓存里根本没有这一条」就是证据）。
	key := m.cacheKey(1, "some-model")
	if state, _ := m.getFromMemCache(key); state != nil {
		t.Fatalf("request_rejected 落下了状态 %+v —— 请求侧拒绝不得计入凭据失败统计", state)
	}
}

// TestUpdateOnFailureRequestRejectedIsRequestSide 钉判据本身的归属：
// request_rejected 与 client_bug 同属「请求侧错误」，早退口径一致。
//
// 这一条同时是**回归护栏**：若有人把 request_rejected 从
// errorsx.IsRequestSideError 里摘掉（只改了 errorsx 没改调用点，或反之），
// 本门会红。
func TestUpdateOnFailureRequestRejectedIsRequestSide(t *testing.T) {
	if !errorsx.IsRequestSideError(errorsx.KindRequestRejected) {
		t.Fatal("IsRequestSideError(KindRequestRejected) = false —— " +
			"调用点（UpdateOnFailure 早退）依赖这个判据")
	}
	if errorsx.IsRequestSideError(errorsx.KindTransient) {
		t.Fatal("IsRequestSideError(KindTransient) = true —— 真故障必须仍被计入")
	}
}
