package ir

import (
	"encoding/json"
	"os"
	"strings"
	"sync"

	"github.com/kaixuan/llm-gateway-go/internal/paramreg"
)

// paramregEnabled 报告是否启用注册表驱动的 Extensions 还原。
//
// PARAMREG_ENABLED=false 退回旧行为（仅同协议还原），用于紧急回退。
// 默认启用。
var paramregEnabled = sync.OnceValue(func() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv("PARAMREG_ENABLED")), "false")
})

// restoreExtensions 把 req.Extensions 按参数注册表策略还原进 out。
//
// 这个函数替换了此前散落在 serialize_openai.go:179 / serialize_anthropic.go:196 /
// serialize_responses.go:166 的三段重复代码，并补上了 serialize_gemini.go 完全
// 缺失的还原逻辑。
//
// 旧实现的问题（docs/参数全量兼容/01-审计基线与研究结论.md 1.1-1.3）：
//
//	if req.SourceProtocol == "" || req.SourceProtocol == ProtocolOpenAIChat {
//	    // 还原
//	}
//
// 该门禁把"来源协议 == 目标协议"当成"字段安全"的充分条件。但网关的核心场景
// 恰恰是跨协议转发（Claude Code anthropic → DeepSeek openai-chat），此时门禁
// 永假，客户端的厂商私有参数 100% 丢失。
//
// 新实现按**字段**判定而非按整包判定：
//   - 未登记字段（未知/前向兼容）→ 无条件还原，含跨协议
//   - 方言私有字段 → 仅目标方言认识时还原，否则裁剪并上报 loss
//   - 目标方言硬拒绝的字段 → 裁剪（优先级最高，防 400）
//
// 这样既让 Claude Code 的 CLAUDE_CODE_EXTRA_BODY 任意字段能穿过网关，
// 又不会把 Anthropic 私有的 context_management 泄漏进 Mistral 的
// additionalProperties:false body。
//
// targetProtocol 是目标线格式协议常量（Protocol* 之一）。
// 已存在于 out 的键不会被覆盖 —— IR 序列化器的输出优先于 Extensions。
func restoreExtensions(out map[string]any, req *InternalRequest, targetProtocol string) {
	if req == nil || len(req.Extensions) == 0 {
		return
	}

	if !paramregEnabled() {
		// 回退路径：旧的同协议门禁行为。
		if req.SourceProtocol != "" && req.SourceProtocol != targetProtocol {
			return
		}
		for key, val := range req.Extensions {
			if _, exists := out[key]; exists {
				continue
			}
			var v any
			if err := json.Unmarshal(val, &v); err == nil {
				out[key] = v
			}
		}
		return
	}

	src := paramreg.DialectForProtocol(req.SourceProtocol)
	dst := resolveTargetDialect(req, targetProtocol)

	for key, val := range req.Extensions {
		if _, exists := out[key]; exists {
			// IR 序列化器已经输出了这个键，不覆盖。
			continue
		}

		outKey, outVal, action, spec := paramreg.Apply(key, val, src, dst)

		switch action {
		case paramreg.ActionRestore, paramreg.ActionTranslate:
			if outKey == "" {
				continue
			}
			if _, exists := out[outKey]; exists {
				continue
			}
			var v any
			if err := json.Unmarshal(outVal, &v); err == nil {
				out[outKey] = v
			}

		case paramreg.ActionDrop:
			reason := "dialect_scoped_field"
			note := ""
			if spec != nil {
				note = spec.Note
				if spec.RejectedBy != nil {
					for _, bad := range spec.RejectedBy {
						if bad == dst {
							reason = "rejected_by_target_dialect"
							break
						}
					}
				}
			}
			ReportProtocolLoss("", key, req.SourceProtocol, targetProtocol, reason,
				"extension field dropped by param registry policy",
				map[string]any{
					"source_dialect": string(src),
					"target_dialect": string(dst),
					"note":           note,
				})

		case paramreg.ActionSkip:
			// IR 已处理该字段，序列化器负责输出。不是丢失，不上报。
		}
	}
}

// resolveTargetDialect 推导目标方言。
//
// TargetProvider（来自 provider.Candidate.CatalogCode）比协议粒度更细 ——
// DeepSeek 和 GLM 都说 openai-chat，但私有参数互不相认。优先用它；
// 无法识别时回退到目标协议。
func resolveTargetDialect(req *InternalRequest, targetProtocol string) paramreg.Dialect {
	if req.TargetProvider != "" {
		if d := paramreg.DialectForCatalogCode(req.TargetProvider); d != paramreg.DialectUnknown {
			return d
		}
	}
	return paramreg.DialectForProtocol(targetProtocol)
}
