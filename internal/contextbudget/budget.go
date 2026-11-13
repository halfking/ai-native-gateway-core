// Package contextbudget enforces per-attempt input budgets on the final JSON
// sent to an LLM. Its default counter is deliberately conservative, not the
// character/token heuristic used to decide when compression is worth trying.
package contextbudget

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

const (
	MaxInputTokens      = 500000
	DefaultMaxJSONBytes = 8 << 20 // Gateway resource policy, not a vendor limit.
	TokensExceeded      = "context_budget_exceeded"
	BytesExceeded       = "upstream_body_too_large"
	UnknownContext      = "context_budget_unknown"
	Uncountable         = "context_budget_uncountable"
	InvalidRequest      = "context_budget_invalid_request"
	InvalidSpec         = "context_budget_invalid_spec"
	requestOverhead     = 64
	nodeOverhead        = 16
)

// Spec must come from trusted bucket capabilities, never client claims.
// ExternalInputTokens covers remote external state not represented by text in
// this body (URLs to fetch, file ids, provider-managed state). A known
// external bound must be positive when such input is used. Inline media
// (data: URLs, inline base64) does NOT need this bound: the calibrated
// estimator meters its bytes through the blob branch (2026-11-13).
type Spec struct {
	ContextWindow       int
	MaxInputTokens      int
	MaxJSONBytes        int
	OutputReserve       int
	SafetyMargin        int
	ExternalInputTokens int
	ExternalInputKnown  bool
}

type Budget struct {
	InputTokens int
	JSONBytes   int
}

// Error is safe to log: Detail contains fixed diagnostics, never source text.
type Error struct {
	Kind   string
	Limit  int
	Actual int
	Detail string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s (actual=%d limit=%d)", e.Kind, e.Detail, e.Actual, e.Limit)
}

// Resolve derives the per-attempt budget from trusted bucket capabilities.
// A missing/invalid context window is never treated as unlimited: it resolves
// to the package-global conservative caps (MaxInputTokens / DefaultMaxJSONBytes)
// instead of failing. 2026-11-10: the previous behavior failed closed with
// UnknownContext for any candidate whose window was unknown, and production
// data carries ~90% NULL models_canonical.context_window — every candidate of
// such a model was rejected pre-dispatch (claude-opus-5-5 422 outage, ZCode
// empty_model_response round 3). Unknown now means "cannot verify the exact
// window", not "over budget": enforcement falls back to the global caps, and
// buckets with KNOWN windows keep the exact arithmetic below unchanged.
func Resolve(spec Spec) (Budget, error) {
	if spec.ContextWindow <= 0 {
		// 2026-11-10 审计 A8：回落分支同样尊重显式 per-bucket 上限
		// （spec.MaxInputTokens / spec.MaxJSONBytes）。此前直接 return 全局
		// 上限 ⇒ 配了更紧桶限但窗口未知的桶拿到 500k 全局值。零/负值仍按
		// 「未设置」处理（与已知窗口臂的 `> 0` 守卫同口径）。
		input, jsonBytes := MaxInputTokens, DefaultMaxJSONBytes
		if spec.MaxInputTokens > 0 {
			input = min(input, spec.MaxInputTokens)
		}
		if spec.MaxJSONBytes > 0 {
			jsonBytes = min(jsonBytes, spec.MaxJSONBytes)
		}
		return Budget{InputTokens: input, JSONBytes: jsonBytes}, nil
	}
	if spec.OutputReserve < 0 || spec.SafetyMargin < 0 || spec.MaxInputTokens < 0 || spec.MaxJSONBytes < 0 || spec.ExternalInputTokens < 0 {
		return Budget{}, &Error{Kind: InvalidSpec, Detail: "negative bucket capacity"}
	}
	if spec.OutputReserve >= spec.ContextWindow || spec.SafetyMargin >= spec.ContextWindow-spec.OutputReserve {
		return Budget{}, &Error{Kind: TokensExceeded, Detail: "output and safety reserves exhaust context", Limit: spec.ContextWindow}
	}
	// r1051（2026-11-13，opus-4-8 反复 422 根修）：输入侧不再叠加「半窗帽」
	// （旧的 spec.ContextWindow/2 min 项）。半窗帽与 2026-11-11 的「body 侧
	// 预留按半窗封顶」修复意图相反：该修复的语义是「输入至少拿到半窗、
	// 输出至少保住 min(max_tokens, 半窗)」，但半窗帽把输入钉死在至多半窗
	// ——输出预留取小时（ZCode 对 opus 只 ask 32k），200k 窗的输入预算仍
	// 是 100k。ZCode 固定载荷（tools ~225KB + system ≈ 70k 估计 token）吃掉
	// 七成，会话历史只剩 ~30k token 余量；越过即 TokensExceeded → fit 字节
	// 目标 300KB 装不下「tools+system+受保护区」（Fit 只裁 messages，tools
	// 不可裁）⇒ ErrProtectedContext ⇒ 422 retryable=false。会话缓存重放同一
	// 出站体 ⇒ 重试同败（211 gw_f9fdc2de 10:23 失败 → 10:52 重试同形状再败，
	// 生产实证）。容量等式由 W−OutputReserve−SafetyMargin 独立保证：body
	// 侧预留仍按半窗封顶（CheckWithCounter），输出保底不变；真正超窗的
	// 请求照旧被拒/经 fit 压缩，估不准的残余归上游 context_length 错误 +
	// 网关既有恢复链（2026-11-10 分层执法哲学）。
	limit := min(MaxInputTokens, spec.ContextWindow-spec.OutputReserve-spec.SafetyMargin)
	if spec.MaxInputTokens > 0 {
		limit = min(limit, spec.MaxInputTokens)
	}
	if limit <= 0 {
		return Budget{}, &Error{Kind: TokensExceeded, Detail: "no input capacity remains"}
	}
	byteLimit := spec.MaxJSONBytes
	if byteLimit == 0 {
		byteLimit = DefaultMaxJSONBytes
	}
	return Budget{InputTokens: limit, JSONBytes: byteLimit}, nil
}

// SafeBodyBytes is a worst-case byte target for the fallback counter: each
// syntax node consumes at least one wire byte. Realistic requests can use much
// larger bodies by calling Check and fitting iteratively. Output parameters in
// the body or known external input may tighten this target further.
func SafeBodyBytes(spec Spec) int {
	budget, err := Resolve(spec)
	if err != nil {
		return 0
	}
	return max(0, min(budget.JSONBytes, (budget.InputTokens-requestOverhead-spec.ExternalInputTokens)/(nodeOverhead+1)))
}

type Receipt struct {
	Tokens     int
	JSONBytes  int
	Budget     Budget
	BodySHA256 string
	Counter    string
}

// Counter counts the complete model-visible input, including media and remote
// state. An implementation must be appropriate for the actual bucket/model.
type Counter interface {
	Count(body []byte) (int, error)
}

type CounterFunc func([]byte) (int, error)

func (f CounterFunc) Count(body []byte) (int, error) { return f(body) }

func Check(body []byte, spec Spec) (Receipt, error) {
	return CheckWithCounter(body, spec, nil)
}

// CheckWithCounter validates the final body. Security invariants are
// unconditional: malformed/ambiguous JSON is rejected, unaccounted external
// references (remote state the gateway cannot see) fail closed, and the byte
// resource cap always applies.
//
// Capacity enforcement is tiered by window provenance (2026-11-10, user rule:
// capacity is the upstream's authority — its context_length errors are handled
// by the gateway's existing recovery chain):
//   - unknown window (global caps): the calibrated estimator is ADVISORY only.
//     The raw byte-as-token bound overestimated CJK/code payloads 2-3x and
//     rejected legitimate large sessions pre-dispatch, cascading into lossy
//     fit/protected-context failures (platform 422 outage).
//   - known window (catalog or upstream-discovered evidence): enforced with
//     the calibrated estimator (text chars/3.5, opaque blobs bytes/5, framing
//     overhead), not raw bytes. Upstream-evidenced discovered limits keep
//     governing recovery retries.
//   - buckets that configure an exact Counter keep full token enforcement.
func CheckWithCounter(body []byte, spec Spec, counter Counter) (Receipt, error) {
	budget, err := Resolve(spec)
	if err != nil {
		return Receipt{}, err
	}
	if len(body) > budget.JSONBytes {
		return Receipt{}, &Error{Kind: BytesExceeded, Limit: budget.JSONBytes, Actual: len(body), Detail: "final JSON exceeds byte limit"}
	}
	info, err := inspect(body)
	if err != nil {
		return Receipt{}, err
	}
	// 2026-11-11（211 实证，ZCode 会话连续 4×ErrProtectedContext + 1×422）：
	// body 侧的 max_tokens 是「本轮允许的输出上界」，不是「必然同时占满的
	// 输入预留」。此前把它全额并入 OutputReserve 再做 window−reserve 扣除
	// ⇒ claude-sonnet-5（200k 窗）× ZCode max_tokens=128000 ⇒ 输入预算只剩
	// 72000，76,792 的校准估计被拒 ⇒ fit 字节目标 216KB 装不下受保护区
	// （系统指令+工具链+最近轮）⇒ ErrProtectedContext ⇒ 422 retryable=false。
	// 厂商（Anthropic/OpenAI）请求期都不校验 input+max_tokens ≤ window——
	// 生成到窗口耗尽自然收尾，容量裁决归上游（2026-11-10 分层执法的既定
	// 哲学）。因此 body 侧预留按半窗封顶：输入至少拿到半窗、输出至少保住
	// min(max_tokens, 半窗)。桶侧显式配置的 spec.OutputReserve 仍全额扣除，
	// 配置语义不变。
	if info.outputReserve > 0 && spec.ContextWindow > 0 {
		capped := min(info.outputReserve, spec.ContextWindow/2)
		spec.OutputReserve = max(spec.OutputReserve, capped)
	} else if info.outputReserve > 0 {
		spec.OutputReserve = max(spec.OutputReserve, info.outputReserve)
	}
	budget, err = Resolve(spec)
	if err != nil {
		return Receipt{}, err
	}
	digest := sha256.Sum256(body)
	if counter == nil {
		// 2026-11-13（211 实证，gpt-6 图片 422 链）：inline 媒体（data: URL /
		// 内联 base64）的字节网关完全可见，校准估计器的 blob 分支（≥4KiB 无
		// 空白串 bytes/5）可以计量它——保守方向（对照片高估 ~200 倍），从不是
		// 低估面。只有 remote external（http(s) URL、file_id、provider 托管态）
		// 才真正需要可信 token 界——那是安全不变量「remote state the gateway
		// cannot see fails closed」的本意。旧实现把两者一起 fail-closed，而
		// ExternalInputTokens 全仓无生产填充点 ⇒ 直发带图请求必 422。
		if info.remote && (!spec.ExternalInputKnown || spec.ExternalInputTokens <= 0) {
			return Receipt{}, &Error{Kind: Uncountable, Detail: "media or remote context requires a trusted token bound"}
		}
		count := int(estimateJSONStringTokens(body)) + requestOverhead + nodeOverhead*info.nodes + spec.ExternalInputTokens
		if spec.ContextWindow > 0 && count > budget.InputTokens {
			return Receipt{}, &Error{Kind: TokensExceeded, Limit: budget.InputTokens, Actual: count, Detail: "final model input exceeds bucket budget"}
		}
		method := "calibrated_json_estimator"
		if spec.ContextWindow <= 0 {
			method = "calibrated_json_estimator_advisory"
		}
		return Receipt{
			Tokens:     count,
			JSONBytes:  len(body),
			Budget:     budget,
			BodySHA256: hex.EncodeToString(digest[:]),
			Counter:    method,
		}, nil
	}
	count, err := counter.Count(body)
	if err != nil {
		return Receipt{}, err
	}
	if count < 0 {
		return Receipt{}, &Error{Kind: Uncountable, Detail: "counter returned a negative count"}
	}
	if info.remote && (!spec.ExternalInputKnown || spec.ExternalInputTokens <= 0) {
		return Receipt{}, &Error{Kind: Uncountable, Detail: "media or remote context requires a trusted token bound"}
	}
	count += spec.ExternalInputTokens
	if count > budget.InputTokens {
		return Receipt{}, &Error{Kind: TokensExceeded, Limit: budget.InputTokens, Actual: count, Detail: "final model input exceeds bucket budget"}
	}
	return Receipt{Tokens: count, JSONBytes: len(body), Budget: budget, BodySHA256: hex.EncodeToString(digest[:]), Counter: "bucket_counter"}, nil
}

// UpperBound supports text JSON only. Use Check with a trusted external bound
// or a bucket-specific Counter for media and provider-managed state.
func UpperBound(body []byte) (int, error) {
	info, err := inspect(body)
	if err != nil {
		return 0, err
	}
	if info.external {
		return 0, &Error{Kind: Uncountable, Detail: "input contains media or remote state"}
	}
	return len(body) + requestOverhead + nodeOverhead*info.nodes, nil
}
