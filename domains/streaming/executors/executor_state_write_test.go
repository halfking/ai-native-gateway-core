package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func TestShouldWriteCredentialState(t *testing.T) {
	tests := []struct {
		name string
		kind errorsx.ErrorKind
		want bool
	}{
		{name: "model not found writes model state", kind: errorsx.KindModelNotFound, want: true},
		{name: "model deprecated writes model state", kind: errorsx.KindModelDeprecated, want: true},
		// 2026-11-02（第八轮 · F-3）：裸 404 = 「上游不提供这一对」= 绑定级
		// 证据 ⇒ 与上面两个兄弟**同判**（writer 里有专用分支写这一对的冷却：
		// domains/credential/writer.go 的 writeModelUnavailableTiered）。
		// ★ 此前它**不在**列表里 ⇒ 两个通往 credential.Writer.WriteOnError
		//   的门对同一个 kind 意见不一致。生产上这条同步路径不可达
		//   （dispatch_v2 是唯一 Execute 路径，且写凭据状态的分支对
		//   DispatchAttempt 早退）⇒ 本次补齐**不改变任何在网行为**。
		{name: "model unavailable writes model state", kind: errorsx.KindModelUnavailable, want: true},
		{name: "auth writes credential state", kind: errorsx.KindAuth, want: true},
		// 2026-11-02（第八轮 · F-3 的阴性对照）：请求侧证据**不写**任何
		// 共享状态 —— 这是第一轮（4fe043dc3）的不变量：上游拒收这次请求
		// ⇒ 不进「节点不健康」阶梯 ⇒ 不写凭据/绑定状态。
		{name: "request rejected writes nothing", kind: errorsx.KindRequestRejected, want: false},
		{name: "client cancellation is ignored", kind: errorsx.KindCanceled, want: false},
		{name: "tool mismatch is ignored", kind: errorsx.KindToolCallIdMismatch, want: false},
		{name: "transient waits for confirmation", kind: errorsx.KindTransient, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldWriteCredentialState(tt.kind); got != tt.want {
				t.Fatalf("shouldWriteCredentialState(%q) = %v, want %v", tt.kind, got, tt.want)
			}
		})
	}
}

// TestShouldWriteCredentialState_ModelLevelBindingKindsAgree 是**类门**
// （2026-11-02 第八轮 · F-3）：上面的表是逐格钉死，这一格钉的是**类**——
//
//	「模型级绑定证据」三兄弟（model_not_found / model_deprecated /
//	model_unavailable）在写入门上必须**同判**。
//
// ★ 为什么需要这一格：F-3 就是「三兄弟里唯独它没登记」⇒ 只在表里补一行
// 只能挡住**同一个** kind 再漏一次；这一格挡住的是**这一族**里任何
// 一个新 kind 再漏一次。
//
// ★ 口径依据：这三个 kind 的归责都是 `errorsx.PenaltyBinding`（写这一对
// (凭据,模型) 的冷却，不动账号），且 credential.Writer 里都有专用分支
// ⇒ 三者对写入门必须有同一个答案。
func TestShouldWriteCredentialState_ModelLevelBindingKindsAgree(t *testing.T) {
	siblings := []errorsx.ErrorKind{
		errorsx.KindModelNotFound,
		errorsx.KindModelDeprecated,
		errorsx.KindModelUnavailable,
	}
	if len(siblings) == 0 {
		t.Fatal("被断言的 kind 集合是空的 ⇒ 门会恒绿失去意义")
	}
	for _, kind := range siblings {
		if errorsx.PenaltyOf(kind).Target != errorsx.PenaltyBinding {
			t.Fatalf("%s 的归责不是 PenaltyBinding ⇒ 本用例的前提变了（它不再属于「模型级绑定证据」这一族）", kind)
		}
		if !shouldWriteCredentialState(kind) {
			t.Errorf("%s 是模型级绑定证据（writer 里有专用分支写这一对的冷却），但 shouldWriteCredentialState=false ⇒ "+
				"它与两个兄弟待遇不一致 ⇒ 两个写入门意见不合", kind)
		}
	}
}

func TestLegacyWritersEnabled(t *testing.T) {
	var nilExecutor *Executor
	if got := nilExecutor.legacyWritersEnabled(); !got {
		t.Fatal("nil executor should keep legacy writers enabled")
	}

	if got := (&Executor{}).legacyWritersEnabled(); !got {
		t.Fatal("executor without URSM v2 should keep legacy writers enabled")
	}
}
