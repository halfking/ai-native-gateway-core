// 覆盖 executor → ursmv2api 这一层的映射。
//
// 2026-10-03 之前这段没有测试，原因是结构性的：e.URSMv2 是具体类型
// *ursmv2.Manager 而非接口，EffectUpdateURSM 分支无法在不构造真实 Manager
// 的前提下写单测。正因如此，**HealthStatus 被漏掉两年都没被发现** ——
// store 层与 writer 层的用例都直接给下游赋值 / 直接 HSET 种子数据，
// 两端都绿，中间这一段无人覆盖。
//
// 现在把映射抽成纯函数 ursmOutcomeFromDecision，于是这段可测。
package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/nodehealth"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

func dec(status requestjourney.NodeHealthStatus) nodehealth.Decision {
	return nodehealth.Decision{
		Node:        nodehealth.NodeKey{CredentialID: 7, Model: "m", TenantID: "t"},
		AttemptID:   "att-1",
		Outcome:     requestjourney.OutcomeSuccess,
		BillingMode: "paid",
		LatencyMs:   123,
		RequestID:   "req-1",
		Status:      status,
	}
}

// TestURSMOutcomeCarriesHealthStatus 是这条链路的核心回归守卫。
//
// 曾经的缺陷：映射里**没有** HealthStatus，于是它恒空 → lua 的
// valid_health[""] 恒假 → `HSET node_key "health"` 永不执行 →
// health_status typed 列在生产上 100% 为空（实测 1275 行 0 非空）。
// 而 api/types.go 的字段契约明写 "Supplied by the executor"。
func TestURSMOutcomeCarriesHealthStatus(t *testing.T) {
	inVocab := []requestjourney.NodeHealthStatus{
		requestjourney.NodeHealthHealthy,
		requestjourney.NodeHealthSuspect,
		requestjourney.NodeHealthDegraded,
		requestjourney.NodeHealthRecovering,
		requestjourney.NodeHealthQuarantined,
	}
	for _, s := range inVocab {
		got := ursmOutcomeFromDecision(dec(s), "timeout")
		if got.HealthStatus != string(s) {
			t.Errorf("status %q → HealthStatus %q, want %q", s, got.HealthStatus, string(s))
		}
		if !api.IsValidHealthStatus(got.HealthStatus) {
			t.Errorf("status %q produced %q which the URSM bridge would reject", s, got.HealthStatus)
		}
	}
}

// 词表外的状态不是「健康信号」。它们照原样传出，交给 lua 的 valid_health
// 表拒掉并保持原值 —— 这里不预过滤，语义由下游负责。
// 但必须钉住：它们**不能**被误当成合法健康值。
func TestURSMOutcomeOutOfVocabularyStatusIsNotTreatedAsHealth(t *testing.T) {
	outOfVocab := []requestjourney.NodeHealthStatus{
		requestjourney.NodeHealthUnknown,
		requestjourney.NodeHealthCooling,
		requestjourney.NodeHealthProbing,
		requestjourney.NodeHealthDisabled,
		requestjourney.NodeHealthUnhealthy,
	}
	for _, s := range outOfVocab {
		got := ursmOutcomeFromDecision(dec(s), "timeout")
		if api.IsValidHealthStatus(got.HealthStatus) {
			t.Errorf("status %q must NOT be accepted as a URSM health value, got %q",
				s, got.HealthStatus)
		}
	}
}

// 空 status 保持空 —— 契约是「empty keeps the previously persisted value」。
func TestURSMOutcomeEmptyStatusStaysEmpty(t *testing.T) {
	got := ursmOutcomeFromDecision(dec(""), "timeout")
	if got.HealthStatus != "" {
		t.Fatalf("empty status must stay empty (empty = keep existing), got %q", got.HealthStatus)
	}
}

// 其余字段不能因为这次重构而错位 —— 抽成纯函数最容易出的事故。
func TestURSMOutcomePreservesOtherFields(t *testing.T) {
	got := ursmOutcomeFromDecision(dec(requestjourney.NodeHealthDegraded), "timeout")
	switch {
	case got.CredentialID != 7:
		t.Errorf("CredentialID=%d, want 7", got.CredentialID)
	case got.RawModel != "m":
		t.Errorf("RawModel=%q, want m", got.RawModel)
	case got.TenantID != "t":
		t.Errorf("TenantID=%q, want t", got.TenantID)
	case !got.Success:
		t.Error("Success=false, want true (Outcome==OutcomeSuccess)")
	case got.LatencyMs != 123:
		t.Errorf("LatencyMs=%d, want 123", got.LatencyMs)
	case got.ErrorKind != "timeout":
		t.Errorf("ErrorKind=%q, want timeout", got.ErrorKind)
	case got.RequestID != "req-1":
		t.Errorf("RequestID=%q, want req-1", got.RequestID)
	case got.DedupKey != "att-1":
		t.Errorf("DedupKey=%q, want att-1", got.DedupKey)
	case got.BillingMode != "paid":
		t.Errorf("BillingMode=%q, want paid", got.BillingMode)
	}
}
