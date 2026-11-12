package main

// F-41 注入检测桥接（2026-11-13，docs/全面审计v3/2026-11-12/03 移交清单兑现）。
//
// 桥接前生产 v1 主链路「三重空转」（F-41 台账 §02）：
//  1. main.go SetV2DispatchAnalysisResources 传 nil（detector/checker/summarizer）；
//  2. pipeline 在 v2DispatchMux 内构建，SetV2DispatchAnalysisResources 在构建
//     之后才注入——buildV2DispatchPipeline 的条件注册在构建时刻恒 nil ⇒
//     governance_prompt_injection stage 永不出现；
//  3. hook 接口返回镜像 DetectionResult，核心 *promptinjection.Detector 的
//     Detect 返回核心侧结构——类型不匹配，**从未有过可直接装配的实现**
//     （hook.go 的「天然实现」注释与事实不符）。
//
// 本文件只补第 3 层的显式适配；第 1/2 层分别在 main.go 调用点（构建 detector）
// 与 main_pipeline.go（stage 无条件注册 + BindDetector 延迟绑定）。
//
// 行为边界：整条链在 LLM_GATEWAY_USE_V2_PIPELINE（默认关）之内；hook 仅在
// env.Metadata["user_content"] 非空时启用；检测器故障降级为 verdict=allow
// （hook.go Execute），不阻断主流程。

import (
	"context"

	promptinjectionhooks "github.com/kaixuan/llm-gateway-go/domains/hooks/promptinjection" //nolint:depguard // F-41 桥接：hook 最小接口
	promptinjection "github.com/kaixuan/llm-gateway-go/domains/promptinjection"            //nolint:depguard // F-41 桥接：核心检测器
)

// piDetectorAdapter 把核心 promptinjection.Detector 适配为 hook 的最小
// Detector 接口（两侧 DetectionResult 是镜像结构，字段逐项映射）。
type piDetectorAdapter struct {
	core *promptinjection.Detector
}

// Detect 实现 promptinjectionhooks.Detector。
func (a piDetectorAdapter) Detect(ctx context.Context, tenantID, input string) (*promptinjectionhooks.DetectionResult, error) {
	result, err := a.core.Detect(ctx, tenantID, input)
	if err != nil || result == nil {
		return nil, err
	}
	return &promptinjectionhooks.DetectionResult{
		Score:              result.Score,
		RiskLevel:          result.RiskLevel,
		Categories:         result.Categories,
		ActionTaken:        result.ActionTaken,
		Blocked:            result.Blocked,
		RequireApproval:    result.RequireApproval,
		ApprovalTimeoutMin: result.ApprovalTimeoutMin,
		Evidence:           result.Evidence,
		ReplacedContent:    result.ReplacedContent,
		CanaryTokenLeaked:  result.CanaryTokenLeaked,
		LLMConfidence:      result.LLMConfidence,
		LLMReason:          result.LLMReason,
		SessionHealthDelta: result.SessionHealthDelta,
		TerminateSession:   result.TerminateSession,
	}, nil
}
