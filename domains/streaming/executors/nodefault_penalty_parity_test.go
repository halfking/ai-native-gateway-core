package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/nodehealth"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// ── 第十三轮（2026-11-02 · F-E）的交叉门 ────────────────────────────────
//
// `errorsx.SeverityOf` / `errorsx.IsNodeFaultPenalty` 是 nodehealth 归责
// 在 errorsx 侧的**镜像**（errorsx 包无法 import executors / nodehealth
// —— 那是它们的反向依赖）。
//
// ★ 镜像最大的风险是**漂移**：生产归责改了，镜像没改 ⇒ 第七轮那道
//   「两分类器锁步」的门就会按**过期的**严重度判词放行分叉 ⇒ 门失去牙齿。
//   ⇒ 本文件把镜像**钉死在实现上**：逐 kind 比对，差一个就红。
//
// ★ 权威链（三个都要对上）：
//   ① errorsx 侧镜像：SeverityOf(kind)
//   ② nodehealth 侧档位：nodeHealthErrorKind(kind)
//   ③ nodehealth 侧归责层级：reducer 的 isBindingScoped 只认
//      ErrorKindModelBinding / ErrorKindModelUnavailable ⇒ 其余 ⇒ 凭据级

// TestSeverityOfParityWithNodeHealthAttribution 钉门 P：errorsx 侧的
// 严重度镜像必须与执行器/归责器的实际归责**逐 kind 一致**。
func TestSeverityOfParityWithNodeHealthAttribution(t *testing.T) {
	for _, kind := range errorsx.AllErrorKinds() {
		nhKind := nodeHealthErrorKind(kind)

		// ① 请求侧族 ⇒ nodehealth 落 ErrorKindRequest ⇒ effectsFor 返回 nil
		//    ⇒ 什么都不写 ⇒ 严重度 = 请求侧。
		if nhKind == nodehealth.ErrorKindRequest {
			if got := errorsx.SeverityOf(kind); got != errorsx.SeverityRequestSide {
				t.Errorf("%s：镜像判「%d」但 nodeHealthErrorKind 落 ErrorKindRequest（什么都不写）⇒ 镜像与实现不一致",
					kind, got)
			}
			continue
		}

		// ② 绑定级档位 ⇒ 只冷却这一对 ⇒ 严重度 = 绑定级。
		if nhKind == nodehealth.ErrorKindModelBinding || nhKind == nodehealth.ErrorKindModelUnavailable {
			if got := errorsx.SeverityOf(kind); got != errorsx.SeverityBinding {
				t.Errorf("%s：镜像判「%d」但 nodeHealthErrorKind 落 %s（绑定级）⇒ 镜像与实现不一致",
					kind, got, nhKind)
			}
			continue
		}

		// ③ 其余 ⇒ 凭据级（连败到阈值 ⇒ 整凭据 cooling + 熔断）。
		if got := errorsx.SeverityOf(kind); got != errorsx.SeverityCredential {
			t.Errorf("%s：镜像判「%d」但 nodeHealthErrorKind 落 %s（凭据级）⇒ 镜像与实现不一致",
				kind, got, nhKind)
		}
		if !errorsx.IsNodeFaultPenalty(kind) {
			t.Errorf("%s：IsNodeFaultPenalty=false 但它落凭据级档位 %s ⇒ 镜像与实现不一致",
				kind, nhKind)
		}
	}
}

// TestSeverityOfParityCoversEmptyKind 钉门 P 的边界：未登记的 kind
// （正文分类器「无命中」时的返回值）⇒ 不产生任何罚则。
//
// ★ 没有这条，镜像会把 "" 判成凭据级 ⇒ 「正文分类器没命中」会被误读成
//
//	「正文分类器判了凭据级」⇒ 门 1 的严重度序就失去意义。
func TestSeverityOfParityCoversEmptyKind(t *testing.T) {
	if got := errorsx.SeverityOf(""); got != errorsx.SeverityNone {
		t.Fatalf("SeverityOf(\"\") = %d, want SeverityNone", got)
	}
	if errorsx.IsNodeFaultPenalty("") {
		t.Fatal("IsNodeFaultPenalty(\"\") = true ⇒ 未命中的 kind 被误当成凭据级罚则")
	}
}
