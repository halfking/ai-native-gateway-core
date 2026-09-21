package promptoptimization

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

// MetaKeyPromptOptimization 优化对比信息在 env.Metadata 中的键名。
//
// 写：PromptOptimizationHook.Execute（Transform 阶段）
// 读：审计日志（audit stage）与运维排查。
// 值为 map[string]any，字段见 recordMetadata。
const MetaKeyPromptOptimization = "prompt_optimization"

// PromptOptimizationHook 把提示词优化服务接入 Pipeline。
//
// 行为：
//   - Enabled: config.Enabled（PROMPT_OPTIMIZATION_ENABLED）且请求体非空
//   - Execute: 解析 OpenAI 格式请求体 → 提取 system/user prompt →
//     查缓存（SHA256）→ 未命中则调用 prompt-optimizer-service →
//     把优化后的内容写回请求体
//   - OnError: 吞掉 error（优化失败回退原始 prompt，不影响主流程）
//
// 适用阶段：PhaseTransform，priority 90 —— 在 compression（priority 100）
// 之前执行，先优化再压缩。
//
// 请求体以 map[string]any（json.Decoder.UseNumber）解析后整体写回，
// 保证 temperature/tools/stream 等未知字段与数字字面量不丢失。
type PromptOptimizationHook struct {
	client  *OptimizerClient
	cache   *OptimizationCache
	config  *Config
	metrics *Metrics
	logger  *slog.Logger
}

// NewHook 构造 Hook。config/logger 为 nil 时使用默认值。
func NewHook(config *Config, logger *slog.Logger) *PromptOptimizationHook {
	if config == nil {
		config = DefaultConfig()
	}
	if logger == nil {
		logger = slog.Default()
	}
	metrics := NewMetrics()
	return &PromptOptimizationHook{
		client:  NewOptimizerClient(config.OptimizerURL, config.Timeout),
		cache:   NewOptimizationCache(config.CacheTTL, 0, metrics),
		config:  config,
		metrics: metrics,
		logger:  logger,
	}
}

// NewHookFromEnv 从环境变量构造 Hook（网关启动路径使用）。
func NewHookFromEnv(logger *slog.Logger) *PromptOptimizationHook {
	return NewHook(FromEnv(), logger)
}

// Name 返回 Hook 名称。
func (h *PromptOptimizationHook) Name() string { return "prompt_optimization" }

// Priority 返回优先级（Transform 阶段，先于 compression 的 100）。
func (h *PromptOptimizationHook) Priority() int { return 90 }

// Enabled 报告 Hook 是否启用。
//
// 默认关闭；仅在配置开启且存在待处理请求体时激活。
// 模型白名单/租户开关在 Execute 内判断（需要解析请求体后才知道模型）。
func (h *PromptOptimizationHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	if !h.config.Enabled {
		return false
	}
	if env == nil || len(env.TransformedRequest) == 0 {
		return false
	}
	return true
}

// Execute 执行提示词优化。任何失败路径都保持原始请求不变并返回 nil。
func (h *PromptOptimizationHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	if !h.Enabled(ctx, env) {
		return nil
	}
	start := time.Now()

	// 1. 解析请求体（UseNumber 保留数字字面量，避免写回时精度/格式漂移）
	decoder := json.NewDecoder(bytes.NewReader(env.TransformedRequest))
	decoder.UseNumber()
	var body map[string]any
	if err := decoder.Decode(&body); err != nil {
		h.logger.Debug("prompt_optimization: request body is not a JSON object, skip",
			"request_id", requestID(env), "error", err)
		return nil
	}

	// 2. 模型/租户开关判断
	model, _ := body["model"].(string)
	if !h.config.modelAllowed(model) {
		h.logger.Debug("prompt_optimization: model not allowed, skip", "model", model)
		return nil
	}
	if !h.config.tenantAllowed(env.TenantID) {
		h.logger.Debug("prompt_optimization: tenant not allowed, skip", "tenant_id", env.TenantID)
		return nil
	}

	// 3. 提取可优化的 prompt（按 mode 过滤；multimodal content 跳过）
	rawMsgs, _ := body["messages"].([]any)
	targets := h.extractTargets(rawMsgs)
	if len(targets) == 0 {
		return nil
	}
	prompts := make([]PromptItem, len(targets))
	originalText := make([]string, len(targets))
	for i, t := range targets {
		prompts[i] = PromptItem{Role: t.role, Content: t.content}
		originalText[i] = t.content
	}

	// 4. 查缓存 → 5. 未命中调用优化服务（失败回退）
	key := CacheKey(model, h.config.Mode, prompts)
	var (
		result   *OptimizeResult
		cacheHit bool
	)
	if cached, ok := h.cache.Get(key); ok {
		result, cacheHit = cached, true
	} else {
		optCtx, cancel := context.WithTimeout(ctx, h.config.Timeout)
		res, err := h.client.Optimize(optCtx, &OptimizeRequest{
			TenantID: env.TenantID,
			Model:    model,
			Prompts:  prompts,
		})
		cancel()
		if err != nil {
			h.metrics.OptimizationErrors.Inc()
			h.metrics.Fallbacks.Inc()
			h.logger.Warn("prompt_optimization: optimizer call failed, fallback to original",
				"error", err,
				"model", model,
				"tenant_id", env.TenantID,
				"prompt_count", len(prompts))
			h.recordMetadata(env, model, originalText, nil, false, time.Since(start), err)
			return nil // 回退：请求原样通过
		}
		h.cache.Set(key, res)
		result = res
	}
	h.metrics.Optimizations.Inc()

	// 6. 防御：返回的 prompts 数量必须与提取数量一致，否则回退
	if len(result.Prompts) != len(targets) {
		err := fmt.Errorf("prompt count mismatch: expected %d got %d", len(targets), len(result.Prompts))
		h.logger.Warn("prompt_optimization: fallback to original", "error", err)
		h.metrics.Fallbacks.Inc()
		h.recordMetadata(env, model, originalText, nil, cacheHit, time.Since(start), err)
		return nil
	}

	// 7. 应用优化结果（仅写回 changed=true 的 content；msg 是引用，原地生效）
	optimizedText := make([]string, len(targets))
	changedRoles := make([]string, 0, len(targets))
	for i, p := range result.Prompts {
		optimizedText[i] = targets[i].content // 默认保持原文
		if !p.Changed {
			continue
		}
		msg, ok := rawMsgs[targets[i].idx].(map[string]any)
		if !ok {
			continue // 双保险：只写回 object 形式的消息
		}
		if _, ok := msg["content"].(string); !ok {
			continue // 只写回 string content
		}
		msg["content"] = p.Content
		optimizedText[i] = p.Content
		changedRoles = append(changedRoles, targets[i].role)
	}
	if len(changedRoles) == 0 {
		h.recordMetadata(env, model, originalText, optimizedText, cacheHit, time.Since(start), nil)
		return nil
	}

	// 8. 写回请求体
	newBody, err := json.Marshal(body)
	if err != nil {
		h.logger.Warn("prompt_optimization: re-marshal failed, fallback to original", "error", err)
		h.metrics.Fallbacks.Inc()
		h.recordMetadata(env, model, originalText, optimizedText, cacheHit, time.Since(start), err)
		return nil
	}
	env.TransformedRequest = newBody

	// 9. 审计：记录优化前后对比
	h.recordMetadata(env, model, originalText, optimizedText, cacheHit, time.Since(start), nil)
	h.logger.Info("prompt_optimization: applied",
		"request_id", requestID(env),
		"model", model,
		"changed_roles", changedRoles,
		"cache_hit", cacheHit,
		"latency_ms", time.Since(start).Milliseconds(),
		"original_sha256", sha256Hex(joinTexts(originalText)),
		"optimized_sha256", sha256Hex(joinTexts(optimizedText)),
		"original_tokens", result.OriginalTokens,
		"optimized_tokens", result.OptimizedTokens,
		"optimization_id", result.OptimizationID)
	return nil
}

// OnError 优化失败：吞掉 error（降级到原始 prompt）。
func (h *PromptOptimizationHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	h.logger.Warn("prompt_optimization: hook error suppressed",
		"request_id", requestID(env), "error", err)
	if env != nil {
		if env.Metadata == nil {
			env.Metadata = make(map[string]any)
		}
		env.Metadata["prompt_optimization_error"] = err.Error()
	}
	return nil
}

// optimizationTarget 一条待优化的消息（idx 指向 rawMsgs 下标）。
type optimizationTarget struct {
	idx     int
	role    string
	content string
}

// extractTargets 按 config.Mode 提取可优化的消息。
// 跳过：mode 未覆盖的角色、content 非字符串（multimodal parts）、空白内容。
func (h *PromptOptimizationHook) extractTargets(rawMsgs []any) []optimizationTarget {
	var targets []optimizationTarget
	for i, raw := range rawMsgs {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if !h.config.Mode.Covers(role) {
			continue
		}
		content, ok := msg["content"].(string)
		if !ok || strings.TrimSpace(content) == "" {
			continue
		}
		targets = append(targets, optimizationTarget{idx: i, role: role, content: content})
	}
	return targets
}

// recordMetadata 把优化对比写入 env.Metadata（审计日志读取）。
func (h *PromptOptimizationHook) recordMetadata(env *domain.PipelineRequest, model string,
	original, optimized []string, cacheHit bool, took time.Duration, optErr error) {
	if env == nil {
		return
	}
	if env.Metadata == nil {
		env.Metadata = make(map[string]any)
	}
	originalJoined := joinTexts(original)
	entry := map[string]any{
		"hook":            h.Name(),
		"model":           model,
		"mode":            string(h.config.Mode),
		"cache_hit":       cacheHit,
		"latency_ms":      took.Milliseconds(),
		"original_sha256": sha256Hex(originalJoined),
		"original_chars":  len(originalJoined),
	}
	if len(optimized) > 0 {
		optimizedJoined := joinTexts(optimized)
		entry["optimized_sha256"] = sha256Hex(optimizedJoined)
		entry["optimized_chars"] = len(optimizedJoined)
	}
	if optErr != nil {
		entry["error"] = optErr.Error()
		entry["fallback"] = true
	}
	env.Metadata[MetaKeyPromptOptimization] = entry
}

// requestID 从 env 中尽力取请求 ID（日志用）。
func requestID(env *domain.PipelineRequest) string {
	if env == nil {
		return ""
	}
	if env.Envelope != nil {
		return env.Envelope.RequestID
	}
	return ""
}

// joinTexts 拼接多段文本（hash/长度统计用）。
func joinTexts(texts []string) string {
	return strings.Join(texts, "\n")
}

// sha256Hex 计算 SHA256 十六进制摘要。
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// 编译期接口断言
var _ pipeline.Hook = (*PromptOptimizationHook)(nil)
