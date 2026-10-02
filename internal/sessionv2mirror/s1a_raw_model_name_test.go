package sessionv2mirror

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	v2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

// raw_model_name 的接线契约（2026-10-02，审计 §9.60.8）。
//
// 这道门存在的理由是**一次真实事故的形状**：本文件此前写着
// 「req.RawModelName：RequestLogEntry 无此字段，保持零值」——
// 那句话是**假的**。telemetry.RequestLogEntry 一直同时带 OutboundModel 与
// ClientModel（client.go:274-275）。真相是**接线漏了**，而一句「源结构体没有这个字段」
// 的注释把一个接线缺陷伪装成了一个结构性事实，连带让 §9.30.2 把
// 「补源字段」当成端口前置（成本高出一个量级），并让四张表的 0 覆盖率看起来
// 像是「这个事实不存在」。
//
// 所以这里用**行为断言**而不是源码扫描：构造 entry、跑映射、断言输出。
// 源码扫描型门在「文件里有没有这个词」这件事上假阳性率高，而本门断言的是
// 「给一个两值不同的 entry，落到 RawModelName 上的是哪一个」——
// 这正是这段接线的全部语义。
//
// 取值口径必须与 §9.30.2 的比对口径逐字一致：COALESCE(outbound_model, client_model)。
func TestApplyStorageS1AFieldsRawModelName(t *testing.T) {
	tests := []struct {
		name          string
		outboundModel *string
		clientModel   *string
		want          string
		why           string
	}{
		{
			name:          "两个都有且不同 → 取上游名（outbound_model）",
			outboundModel: ptr("MiniMax-M3"),
			clientModel:   ptr("minimax-m3"),
			want:          "MiniMax-M3",
			why: "252 生产库近 7 天有 3,227/29,201（11.0%）行两值不同，差异是真实映射" +
				"（大小写 + 别名）。取 client_model 会在这些行上让 credential_recovery " +
				"的比对系统性误判。",
		},
		{
			name:          "outbound_model 为 nil → 回落到 client_model",
			outboundModel: nil,
			clientModel:   ptr("minimax-m3"),
			want:          "minimax-m3",
			why: "252 上 outbound_model 有 4,903/29,201（16.8%）为 NULL。回落而不是留空，" +
				"才能让这一列在两族之间保持同义；留空会把「同义」变成「一半缺值」。",
		},
		{
			name:          "outbound_model 为空串 → 同样回落到 client_model",
			outboundModel: ptr(""),
			clientModel:   ptr("glm-5.3"),
			want:          "glm-5.3",
			why: "空串与 nil 是两种不同的「没有值」形态，只判 nil 会在空串上写出空列。",
		},
		{
			name:          "两个都空 → 空（由 turn_writer 的 nilIfEmpty 转 SQL NULL）",
			outboundModel: nil,
			clientModel:   nil,
			want:          "",
			why: "宁可这一格没有值，也不要拿 model / canonical_model 顶替——" +
				"近似实现比不修更危险（审计 §9.30.2）。",
		},
		{
			name:          "只有 outbound_model → 取它",
			outboundModel: ptr("glm-5.3-flash"),
			clientModel:   nil,
			want:          "glm-5.3-flash",
			why: "反向也要成立：不能因为 client_model 缺失就丢掉已解析出的上游名。",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry := &telemetry.RequestLogEntry{
				RequestID:     "req-1",
				TenantID:      "default",
				OutboundModel: tc.outboundModel,
				ClientModel:   tc.clientModel,
			}
			req := &v2.ProcessedRequest{SessionID: "s", TenantID: "default", RequestID: "req-1"}

			applyStorageS1AFields(req, entry)

			if req.RawModelName != tc.want {
				t.Errorf("RawModelName = %q, want %q。%s", req.RawModelName, tc.want, tc.why)
			}
		})
	}
}

// 钉住「不拿 model 顶替」这条纪律。session_turns.model 等于 client_model
// （§9.12.1 实测 1138/1138），拿它冒充 raw_model_name 会造出一个**持续产出
// 看似合理结论**的假信号，比现在明确不修更难拆。
func TestApplyStorageS1AFieldsRawModelNameNeverFallsBackToModel(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		RequestID:       "req-1",
		TenantID:        "default",
		ClientModel:     ptr("minimax-m3"),
		CanonicalModel:  ptr("minimax-m3"),
		OutboundModel:   nil,
	}
	req := &v2.ProcessedRequest{SessionID: "s", TenantID: "default", RequestID: "req-1"}

	applyStorageS1AFields(req, entry)

	if req.RawModelName != "minimax-m3" {
		t.Fatalf("setup 有误：ClientModel 存在时 RawModelName 应等于它，得到 %q", req.RawModelName)
	}
	// 关键否定式断言：拿掉 ClientModel 后必须为空，而不是被 model/canonical 填上。
	entry2 := &telemetry.RequestLogEntry{
		RequestID:      "req-1",
		TenantID:       "default",
		CanonicalModel: ptr("minimax-m3"),
	}
	req2 := &v2.ProcessedRequest{SessionID: "s", TenantID: "default", RequestID: "req-1"}

	applyStorageS1AFields(req2, entry2)

	if req2.RawModelName != "" {
		t.Errorf("两个模型列都没有时 RawModelName = %q，应为空。%s",
			req2.RawModelName,
			"canonical_model 是规范化后的名字，不是上游原始名；用它顶替会让 11% 的映射行"+
				"产生一个看起来合理的错误值")
	}
}

func ptr(s string) *string { return &s }
