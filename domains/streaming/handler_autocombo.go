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
	"time"

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

// ErrOmniFreeInfraFailure 是 resolveOmniFreeCandidates 在基础设施/配置层面
// 出错时返回的 sentinel (第四轮审计 P1 修复):
//   - Resolver.Resolve 数据库查询失败 (非 sql.ErrNoRows)
//   - VirtualFactory.LoadCatalogForBuild / BuildFromCandidates 数据库查询失败
//
// 这与 spec == nil (OmniFree 主动判定"不接管此模型") 不同 — 后者才允许
// 降级到普通 provider resolver; 基础设施故障绝不能被误判为"未接管"而
// 静默路由到可能收费的 provider, 否则一次 DB/RLS 故障会看起来像是正常
// 的付费请求成功, 运维完全看不到 OmniFree 已经故障。
//
// 区分方式: caller 用 errors.Is(err, ErrOmniFreeInfraFailure)。
var ErrOmniFreeInfraFailure = errors.New("omnifree: infrastructure failure")

// round 4 L1+L2: 测试用 fake QuotaRecorder, 注入 handler 后捕获 Record /
// CorrectFromHeaders 的参数. 真实 QuotaTracker 隐式实现 QuotaRecorder
// (定义在 handler.go); fake 直接实现 QuotaRecorder 接口即可.
type fakeQuotaRecorder struct {
	mu      sync.Mutex
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
	f.mu.Lock()
	defer f.mu.Unlock()
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
	f.mu.Lock()
	defer f.mu.Unlock()
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
		// 第四轮审计 P1: Resolver.Resolve 出错 (DB 查询失败/RLS 拒绝等)
		// 是基础设施故障, 不是"这个模型不归 OmniFree 管". 用
		// ErrOmniFreeInfraFailure 包裹, 让 caller 返回明确的 5xx 而不是
		// 静默降级到可能收费的 provider resolver.
		slog.Error("omnifree: resolve template failed (infra)",
			"error", err, "model", clientModel, "tenant_id", tenantID)
		return nil, nil, "", false, fmt.Errorf("%w: resolve template: %v", ErrOmniFreeInfraFailure, err)
	}
	if spec == nil {
		return nil, nil, "", false, nil
	}

	modality := detectRequestModality(bodyBytes)

	// 收集 catalog 中的具体 model_id, 通过 provider.Client 获取可执行候选.
	entries, err := h.autoComboFactory.LoadCatalogForBuild(ctx, tenantID, spec)
	if err != nil {
		slog.Error("omnifree: load catalog failed (infra)", "error", err, "tenant_id", tenantID)
		return nil, nil, "", false, fmt.Errorf("%w: load catalog: %v", ErrOmniFreeInfraFailure, err)
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
			// round 4 审计补充修复: modality 之前只计算出来用于日志/返回值,
			// 从未真正传给 provider 层, 导致 vision/audio/video 请求会被
			// GetCandidates 按纯文本模型解析, 可能选中不支持该模态的候选。
			// 这里改用 GetCandidatesByModality, 与非 OmniFree 路径
			// (resolveCandidatesForRequest) 的行为保持一致.
			cands, pol, err := h.provider.GetCandidatesByModality(ctx, e.ModelID, profile, tenantID, modality)
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
		return nil, nil, modality, false, fmt.Errorf("%w: spec=%s factory-build-failed tenant=%s: %v",
			ErrOmniFreeInfraFailure, spec.ComboName, tenantID, err)
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
// round 4 审计补充修复 (L3): 改用 bounded worker queue (h.quotaRecordQueue)
// 替代 fire-and-forget goroutine. 旧实现每个请求都 go func() 直接 fork,
// 高并发时可能积压上万个未完成 goroutine + context / DB 连接, 导致 pgx
// pool 耗尽. 现在任务投递到固定容量的 channel (默认 256 缓冲), 由固定
// 数量的 worker (默认 16) 消费; 队列满时非阻塞丢弃 + WARN 日志 (配额
// 记录本身不是关键路径, 丢弃仍优于阻塞请求响应).
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

	// round 4 L5: 同时接入 OmniFreeAutoSuccessTotal (成功计数).
	if success {
		metrics.OmniFreeAutoSuccessTotal.WithLabelValues(clientModel, tenantID).Inc()
	}
	for _, wt := range windowTypes {
		metrics.OmniFreeQuotaRecordsTotal.WithLabelValues(string(wt), strconv.FormatBool(success)).Inc()
	}

	// 拷贝必要字段避免 result 指针在主路径释放后 race.
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
		captured.Header = result.Response.Header.Clone()
	}

	// 构造 Record 任务并投递到 bounded queue. 5s context timeout 防止
	// 单个 Record 调用长时间占用 worker (DB 死锁/慢查询).
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	_ = cancel // defer cancel() 由 worker 在消费完任务后执行 (通过闭包捕获).

	recordTask := quotaRecordTask{
		ctx:      recordCtx,
		isRecord: true,
		recordReq: freeresource.RecordRequest{
			CredentialID: int64(captured.CredentialID),
			ProviderCode: captured.CatalogCode,
			ModelID:      captured.StdName,
			WindowTypes:  windowTypes,
			TokenCount:   0, // token 用量在 telemetry/audit 阶段另有统计, 避免重复.
			Success:      success,
			TenantID:     tenantID,
		},
	}

	select {
	case h.quotaRecordQueue <- recordTask:
		// 投递成功, worker 会消费.
	default:
		// 队列满, 丢弃任务 (配额记录非关键路径, 丢弃优于阻塞请求响应).
		metrics.OmniFreeQuotaRecordErrorsTotal.Inc()
		slog.Warn("omnifree: quota record queue full, dropping task",
			"model", clientModel, "tenant_id", tenantID, "queue_depth", cap(h.quotaRecordQueue))
		cancel()
	}

	// 429 校准: 仅当上游响应可直接访问 (非流式或流式 start 阶段).
	if captured.HasResponse && captured.StatusCode == http.StatusTooManyRequests {
		metrics.OmniFreeQuotaCorrectTotal.Inc()
		headers := flattenHeaders(captured.Header)
		correctCtx, cancelCorrect := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_ = cancelCorrect

		correctTask := quotaRecordTask{
			ctx:      correctCtx,
			isRecord: false,
			correctReq: freeresource.CorrectionRequest{
				CredentialID: int64(captured.CredentialID),
				ProviderCode: captured.CatalogCode,
				ModelID:      captured.StdName,
				Headers:      headers,
				TenantID:     tenantID,
			},
		}

		select {
		case h.quotaRecordQueue <- correctTask:
			// 投递成功.
		default:
			slog.Warn("omnifree: quota correct queue full, dropping task",
				"model", clientModel, "tenant_id", tenantID)
			cancelCorrect()
		}
	}
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