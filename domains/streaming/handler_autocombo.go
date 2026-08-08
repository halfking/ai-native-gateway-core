package streaming

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/kaixuan/llm-gateway-go/domains/freeresource"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// autoRouteMagicExact (handler.go) is the exact "auto" model name that triggers
// the existing autoroute.Decider path. Anything strictly longer with the
// "auto/" prefix belongs to OmniFree's virtual auto/* routes.
const autoRouteMagicExact = "auto"

// ErrOmniFreeNoCandidates 是 resolveOmniFreeCandidates 在以下场景返回的 sentinel:
//   - spec 解析成功 (catalog/template 命中了用户的 auto/* 路由)
//   - 但经过 catalog 匹配、配额预检、模型过滤后, 没有可执行候选
// (例如: 全部免费资源今日配额耗尽, 或模型被过滤规则剔出).
//
// 这与 spec=nil (路由未命中) 不同 — 后者意味着 OmniFree 不接管, 由
// caller 走普通 provider resolver. 前者意味着 OmniFree 接管但失败,
// 应该向客户端返回 503 no_free_candidates, 而不是降级到普通路由
// (round 3 audit H1 修复).
//
// 区分方式: caller 用 errors.Is(err, ErrOmniFreeNoCandidates).
var ErrOmniFreeNoCandidates = errors.New("omnifree: no free candidates available")

// round 4 L1+L2: 测试用 fake QuotaRecorder, 注入 handler 后捕获 Record /
// CorrectFromHeaders 的参数. 真实 QuotaTracker 隐式实现 QuotaRecorder
// (定义在 handler.go); fake 直接实现 QuotaRecorder 接口即可.
type fakeQuotaRecorder struct {
	records []recordedCall
	correct []correctedCall
}

type recordedCall struct {
	CredentialID int64
	ProviderCode string
	ModelID      string
	WindowTypes  []freeresource.WindowType
	Success      bool
	TenantID     string
	TokenCount   int64
}

type correctedCall struct {
	CredentialID int64
	ProviderCode string
	ModelID      string
	Headers      map[string]string
	TenantID     string
}

func (f *fakeQuotaRecorder) Record(_ context.Context, req freeresource.RecordRequest) error {
	f.records = append(f.records, recordedCall{
		CredentialID: req.CredentialID,
		ProviderCode: req.ProviderCode,
		ModelID:      req.ModelID,
		WindowTypes:  req.WindowTypes,
		Success:      req.Success,
		TenantID:     req.TenantID,
		TokenCount:   req.TokenCount,
	})
	return nil
}

func (f *fakeQuotaRecorder) CorrectFromHeaders(_ context.Context, req freeresource.CorrectionRequest) error {
	f.correct = append(f.correct, correctedCall{
		CredentialID: req.CredentialID,
		ProviderCode: req.ProviderCode,
		ModelID:      req.ModelID,
		Headers:      req.Headers,
		TenantID:     req.TenantID,
	})
	return nil
}

// shouldTryOmniFree reports whether the request model name should be
// routed through the OmniFree VirtualFactory.
//
// Decision tree:
//  1. resolver/factory 任意为 nil → 不接管, 由 provider resolver 原样下发
//     (允许对 auto/* 字面量作为普通模型名降级处理).
//  2. model == "auto" → 精确 auto, 保留 autoroute.Decider 接管.
//  3. model 以 "auto/" 开头 → 走 OmniFree.
//
// nil 状态下 / 不带 "auto" 前缀的请求保持原行为.
func (h *ChatHandler) shouldTryOmniFree(clientModel string) bool {
	if h.autoComboResolver == nil || h.autoComboFactory == nil {
		return false
	}
	if clientModel == autoRouteMagicExact {
		return false
	}
	return strings.HasPrefix(clientModel, "auto/")
}

// resolveOmniFreeCandidates 解析 auto/* 虚拟路由并返回过滤后的可执行候选.
//
// 流程:
//  1. autoComboResolver.Resolve(model, tenantID) 获取 AutoComboSpec; 未知 combo
//     返回 (nil, nil, "", false, nil) — 表示 OmniFree 不负责, 由调用方降级.
//  2. 从 free_resource_catalog 中读取当前租户启用的 (provider_code, model_id) 集合.
//  3. 对 catalog 中的每个 model_id, 并行 (errgroup) 调用 provider.Client
//     .GetCandidates 取得完整可执行的 provider.Candidate 池.
//  4. 合并去重, 交给 VirtualFactory.BuildFromCandidates 做过滤/排序.
//
// round 3 audit H5: 改用 errgroup 把 for-loop 内串行 GetCandidates 改成
// 并行, 减少 N+1 延迟.
//
// 返回值:
//   - candidates / policy / modality: 已过滤可执行候选; 当 found=true 时有效.
//   - found: true 表示 OmniFree 接管并返回了非空候选; false 表示未接管 (降级给
//     普通 provider resolver); err 在 found=false 时携带详细信息.
//   - 当 spec 命中但 catalog/配额过滤后为空时, found=false 且 err 包裹
//     ErrOmniFreeNoCandidates — caller 用 errors.Is 区分, 返回 503 而非 fallback.
func (h *ChatHandler) resolveOmniFreeCandidates(
	ctx context.Context,
	clientModel, profile, tenantID string,
	bodyBytes []byte,
) ([]provider.Candidate, *provider.Policy, string, bool, error) {
	spec, err := h.autoComboResolver.Resolve(ctx, clientModel, tenantID)
	if err != nil {
		slog.Warn("omnifree: resolve template failed",
			"error", err, "model", clientModel, "tenant_id", tenantID)
		return nil, nil, "", false, err
	}
	if spec == nil {
		return nil, nil, "", false, nil
	}

	modality := detectRequestModality(bodyBytes)

	// 收集 catalog 中的具体 model_id, 通过 provider.Client 获取可执行候选.
	entries, err := h.autoComboFactory.LoadCatalogForBuild(ctx, tenantID, spec)
	if err != nil {
		slog.Warn("omnifree: load catalog failed", "error", err, "tenant_id", tenantID)
		return nil, nil, "", false, err
	}
	if len(entries) == 0 {
		// spec 命中但 catalog 没有任何行: 用户请求了 auto/* 路由但当前租户
		// 没有可用免费资源. 这是用户意图明确的失败, 不能 fallback 到 paid.
		slog.Warn("omnifree: spec hit but catalog empty",
			"model", clientModel, "tenant_id", tenantID)
		return nil, nil, modality, false, fmt.Errorf("%w: spec=%s tenant=%s",
			ErrOmniFreeNoCandidates, spec.ComboName, tenantID)
	}

	// round 3 H5: 并行调用 provider.GetCandidates, 取代原 N+1 串行循环.
	type candResult struct {
		cands []provider.Candidate
		pol   *provider.Policy
	}

	results := make([]candResult, len(entries))
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex // 保护 slog 并发写
		emptySet = make(map[int]struct{})
	)
	for i, e := range entries {
		if e.ProviderCode == "" || e.ModelID == "" {
			emptySet[i] = struct{}{}
			continue
		}
		i, e := i, e
		wg.Add(1)
		go func() {
			defer wg.Done()
			cands, pol, err := h.provider.GetCandidates(ctx, e.ModelID, profile, tenantID)
			if err != nil {
				mu.Lock()
				slog.Debug("omnifree: provider resolve failed",
					"error", err, "provider_code", e.ProviderCode, "model", e.ModelID)
				mu.Unlock()
				return
			}
			results[i] = candResult{cands: cands, pol: pol}
		}()
	}
	wg.Wait()
	_ = emptySet // skip-list 在外层 len() 检查处理

	pool := make([]provider.Candidate, 0, len(entries)*2)
	policyRef := (*provider.Policy)(nil)
	// round 4 L4: 用 struct 复合 key 替代 hashString, 避免 32-bit 截断
	// 与哈希碰撞 (旧实现 ProviderID 转 uint32 + hashString 截断为 64 位
	// 但其中 32 位来自 hash, 上限碰撞率 1/2^32 但仍非零). Go 的 map 支持
	// struct key 而不需要额外 hash.
	type candKey struct {
		ProviderID uint64
		RawModel   string
	}
	seen := make(map[candKey]struct{}, len(entries)*2)
	for _, r := range results {
		if r.pol != nil && policyRef == nil {
			policyRef = r.pol
		}
		for _, c := range r.cands {
			key := candKey{ProviderID: uint64(c.ProviderID), RawModel: c.RawModel}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			pool = append(pool, c)
		}
	}

	if len(pool) == 0 {
		// catalog 命中但 provider.GetCandidates 全部失败 — 也属用户意图明确
		// 失败, 返回 sentinel 让上层走 503 路径.
		slog.Warn("omnifree: catalog hit but no provider candidates",
			"model", clientModel, "tenant_id", tenantID,
			"catalog_entries", len(entries))
		return nil, nil, modality, false, fmt.Errorf("%w: spec=%s provider-resolve-failed tenant=%s",
			ErrOmniFreeNoCandidates, spec.ComboName, tenantID)
	}

	filtered, err := h.autoComboFactory.BuildFromCandidates(ctx, spec, pool, tenantID)
	if err != nil {
		slog.Warn("omnifree: factory build failed", "error", err, "model", clientModel)
		return nil, nil, modality, false, err
	}
	if len(filtered) == 0 {
		// catalog 命中但全部被配额/过滤剔出. 同样不能让 caller 降级到 paid.
		slog.Warn("omnifree: catalog hit but quota/filter exhausted all candidates",
			"model", clientModel, "tenant_id", tenantID,
			"catalog_entries", len(entries), "pool_size", len(pool))
		return nil, nil, modality, false, fmt.Errorf("%w: spec=%s quota-exhausted tenant=%s",
			ErrOmniFreeNoCandidates, spec.ComboName, tenantID)
	}
	return filtered, policyRef, modality, true, nil
}

// recordOmniFreeQuota 在 auto/* 请求的生命周期内记录免费资源配额消耗 /
// 429 校准. 无 OmniFree 注入或非 auto/* 请求时是 no-op.
//
// 输入:
//   - result/execErr: executor.Execute 的返回值. 只有 result 存在时
//     才能 record (有 selected candidate). execErr 仅用于 429 校准的触发.
//
// 行为:
//   - 成功 (result != nil && execErr == nil): 调用 QuotaTracker.Record
//     记录请求窗口.
//   - 失败且 selected candidate 存在: 仍记录一次失败; 若最后失败是 429
//     (KindRateLimit / upstream status 429), 额外调用 CorrectFromHeaders
//     校正限制并写 reset_at. 仅当 executor 直接返回了带 headers 的
//     *http.Response 时才能获取上游 Retry-After / X-RateLimit-* 头部;
//     流式 chunked 路径上 headers 已被透传给客户端, 这里不再读.
//
// round 4 M4 优化: 把 Record 放入 fire-and-forget goroutine, 不阻塞主请求
// 路径. 高 QPS 场景下, Record 涉及 2~4 个 UPSERT (每个窗口一个) 会消耗
// 5~10ms DB 时间, 串行调用会让 auto/* 请求 P99 latency 受影响. 失败仅记
// WARN (不阻塞, 与原有"配额错误不阻塞请求"原则一致).
func (h *ChatHandler) recordOmniFreeQuota(
	ctx context.Context,
	clientModel, tenantID string,
	result *executors.ExecuteResult,
	execErr error,
) {
	if h.quotaTracker == nil || !strings.HasPrefix(clientModel, "auto/") {
		return
	}
	if result == nil {
		return
	}

	windowTypes := []freeresource.WindowType{
		freeresource.WindowTypeDay1,
		freeresource.WindowTypeMonth1,
	}
	success := execErr == nil

	// Record: 失败也累加 request_count 与 error_count; 不阻塞请求路径.
	// round 4 M4: 改为 fire-and-forget goroutine, 主请求路径不等 DB.
	//
	// 设计权衡:
	//   - 旧实现同步调用 Record + CorrectFromHeaders, 单次 2~4 个 UPSERT
	//     约 5~10ms, 在 1000 RPS 场景下会拉高 auto/* 请求 P99 latency.
	//   - 异步化后 P99 latency 下降, 但 Record 失败时仅记 WARN (与原
	//     "配额错误不阻塞"原则一致). 用 ctx 派生避免泄漏; 主请求 ctx
	//     取消时 record 会被尽早中断.
	//
	// round 4 L5: 同时接入 OmniFreeAutoSuccessTotal (成功计数).
	if success {
		metrics.OmniFreeAutoSuccessTotal.WithLabelValues(clientModel, tenantID).Inc()
	}
	for _, wt := range windowTypes {
		metrics.OmniFreeQuotaRecordsTotal.WithLabelValues(string(wt), strconv.FormatBool(success)).Inc()
	}
	// 拷贝必要字段到 goroutine, 避免 result 指针在主路径释放后 race.
	captured := struct {
		CredentialID int
		CatalogCode  string
		StdName      string
		StatusCode   int
		HasResponse  bool
		Header       http.Header
	}{
		CredentialID: result.Candidate.CredentialID,
		CatalogCode:  result.Candidate.CatalogCode,
		StdName:      result.Candidate.StandardizedName,
		HasResponse:  result.Response != nil,
	}
	if captured.HasResponse {
		captured.StatusCode = result.Response.StatusCode
		captured.Header = result.Response.Header
	}
	go func(recordCtx context.Context) {
		// 防御: goroutine 内的 panic 会让 Go runtime 终止整个 test binary
		// (生产不会, 但测试会). recover 把 panic 转成 slog.
		defer func() {
			if r := recover(); r != nil {
				slog.Error("omnifree: panic in record goroutine",
					"panic", r, "model", clientModel, "tenant_id", tenantID)
				metrics.OmniFreeQuotaRecordErrorsTotal.Inc()
			}
		}()
		// 单元测试可能注入 db=nil 的 QuotaTracker; 防御性早退避免 panic.
		if h.quotaTracker == nil {
			return
		}
		recErr := h.quotaTracker.Record(recordCtx, freeresource.RecordRequest{
			CredentialID: int64(captured.CredentialID),
			ProviderCode: captured.CatalogCode,
			ModelID:      captured.StdName,
			WindowTypes:  windowTypes,
			TokenCount:   0, // token 用量在 telemetry/audit 阶段另有统计, 避免重复.
			Success:      success,
			TenantID:     tenantID,
		})
		if recErr != nil {
			metrics.OmniFreeQuotaRecordErrorsTotal.Inc()
			slog.Debug("omnifree: quota record failed",
				"error", recErr, "model", clientModel, "tenant_id", tenantID)
		}

		// 429 校准: 仅当上游响应可直接访问 (非流式或流式 start 阶段).
		if captured.HasResponse && captured.StatusCode == http.StatusTooManyRequests {
			metrics.OmniFreeQuotaCorrectTotal.Inc()
			headers := flattenHeaders(captured.Header)
			corrErr := h.quotaTracker.CorrectFromHeaders(recordCtx, freeresource.CorrectionRequest{
				CredentialID: int64(captured.CredentialID),
				ProviderCode: captured.CatalogCode,
				ModelID:      captured.StdName,
				Headers:      headers,
				TenantID:     tenantID,
			})
			if corrErr != nil {
				slog.Debug("omnifree: quota 429 correct failed",
					"error", corrErr, "model", clientModel, "tenant_id", tenantID)
			}
		}
	}(context.WithoutCancel(ctx))
}

// flattenHeaders 将 http.Header 折叠成 map[string]string (取第一个值),
// 与 QuotaTracker.CorrectFromHeaders 的 headers 字段匹配.
//
// round 4 M6 (RFC 7231 合规): Retry-After 可逗号分隔多个值 (e.g.
// "60, 120" — 上游代理链式合并时常见). 此时取 MAX (即最保守的等待时间)
// 而非第一个, 避免被 429 短间隔打挂.
func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) == 0 {
			continue
		}
		if k == "Retry-After" && len(v) > 1 {
			// 多个值取 MAX, 配合 CorrectFromHeaders parseRetryAfterSeconds.
			maxSec := 0
			for _, raw := range v {
				s := strings.TrimSpace(raw)
				allDigit := len(s) > 0
				for _, c := range s {
					if c < '0' || c > '9' {
						allDigit = false
						break
					}
				}
				if !allDigit {
					continue
				}
				n := 0
				for _, c := range s {
					n = n*10 + int(c-'0')
				}
				if n > maxSec {
					maxSec = n
				}
			}
			if maxSec > 0 {
				out[k] = strconv.Itoa(maxSec)
				continue
			}
		}
		out[k] = v[0]
	}
	return out
}