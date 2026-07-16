package trace

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SnapshotProvider 在失败事件被记录时,提供当前请求的上下文快照。
//
// 该接口用于"路由/凭据/并发/探测"等分布式状态信息的统一注入,
// 避免每个调用方都各自实现,使 trace 包的通用性更好。
//
// 实现者:
//   - 路由层: SnapshotForRouting(model, requestID)
//   - 凭据层: SnapshotForCredential(credentialID)
//   - 并发层: SnapshotForConcurrency(credentialID)
//   - 探测层: SnapshotForProbe(model, credentialID)
//
// 所有方法都是 best-effort, 调用方不得 panic。
type SnapshotProvider interface {
	SnapshotForRouting(ctx context.Context, model, requestID string) (candidates []CandidateInfo, state string, extra map[string]any)
	SnapshotForCredential(ctx context.Context, credentialID int) (mode string, ok bool)
	SnapshotForConcurrency(ctx context.Context, credentialID int) ConcurrencySnapshot
	SnapshotForProbe(ctx context.Context, model string, credentialID int) map[string]any
}

// CandidateInfo 路由候选凭据的最小摘要, 用于 trace 快照。
type CandidateInfo struct {
	ProviderID    int    `json:"provider_id"`
	CredentialID  int    `json:"credential_id"`
	RawModel      string `json:"raw_model"`
	Tier          string `json:"tier,omitempty"`
	BillingMode   string `json:"billing_mode,omitempty"`
	Available     bool   `json:"available"`
	LastError     string `json:"last_error,omitempty"`
}

// NoopSnapshotProvider 默认空实现。生产环境在 main.go 注入真实 provider。
type NoopSnapshotProvider struct{}

func (NoopSnapshotProvider) SnapshotForRouting(_ context.Context, _, _ string) ([]CandidateInfo, string, map[string]any) {
	return nil, "", nil
}
func (NoopSnapshotProvider) SnapshotForCredential(_ context.Context, _ int) (string, bool) {
	return "", false
}
func (NoopSnapshotProvider) SnapshotForConcurrency(_ context.Context, _ int) ConcurrencySnapshot {
	return ConcurrencySnapshot{}
}
func (NoopSnapshotProvider) SnapshotForProbe(_ context.Context, _ string, _ int) map[string]any {
	return nil
}

// GlobalSnapshotProvider 全局可访问的 provider 实例。
//
// 用法: main.go 启动时调用 SetGlobalSnapshotProvider(realProvider)。
// trace.Recorder.Append 调用方无需感知,Recorder 内部会读取。
//
// 为什么用全局而不是注入到每个 Recorder:
//   - Recorder 实例在 main.go 创建一次,和 ChatHandler 实例数量对齐(也是 1 次)。
//   - SnapshotProvider 注入到 Recorder 同样可行,但增加了 package 间耦合并拖慢初始化。
var (
	globalProviderMu sync.RWMutex
	globalProvider   SnapshotProvider = NoopSnapshotProvider{}
)

// SetGlobalSnapshotProvider 设置全局 provider。生产环境 main.go 调用一次。
// 调用后,所有 Recorder (同一进程内) 都能读到。如果传入 nil,回退到 Noop。
func SetGlobalSnapshotProvider(p SnapshotProvider) {
	globalProviderMu.Lock()
	defer globalProviderMu.Unlock()
	if p == nil {
		globalProviderMu.Unlock()
		globalProvider = NoopSnapshotProvider{}
		globalProviderMu.Lock()
		return
	}
	globalProvider = p
}

// GetGlobalSnapshotProvider 取出全局 provider。
func GetGlobalSnapshotProvider() SnapshotProvider {
	globalProviderMu.RLock()
	defer globalProviderMu.RUnlock()
	if globalProvider == nil {
		return NoopSnapshotProvider{}
	}
	return globalProvider
}

// BuildFailureSnapshot 在事件 status=failed 时构造快照。
//
// 注入时机: 调用方在 Append 一个 failed 事件前调用,
// 把生成的 Snapshot 设置到 TraceEvent.Snapshot 字段。
//
// 这是一个 best-effort 的辅助函数,内部超时 200ms 不会阻塞主流程。
func BuildFailureSnapshot(ctx context.Context, model string, credentialID int, hint string) *Snapshot {
	if ctx == nil {
		return nil
	}
	provider := GetGlobalSnapshotProvider()

	type result struct {
		cands  []CandidateInfo
		state  string
		cred   string
		credOk bool
		conc   ConcurrencySnapshot
		probe  map[string]any
	}

	snapCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()

	// 并发拉取各类快照, 整体 200ms 超时 (依赖外部 provider 的 timeout)
	var (
		rCh = make(chan result, 1)
	)
	go func() {
		cands, state, extra := provider.SnapshotForRouting(snapCtx, model, "")
		mode, ok := provider.SnapshotForCredential(snapCtx, credentialID)
		conc := provider.SnapshotForConcurrency(snapCtx, credentialID)
		probe := provider.SnapshotForProbe(snapCtx, model, credentialID)

		// 把 extra 合并到 probe (单 map 简化序列化)
		if probe == nil {
			probe = map[string]any{}
		}
		for k, v := range extra {
			probe[k] = v
		}

		rCh <- result{
			cands: cands, state: state,
			cred: mode, credOk: ok,
			conc: conc, probe: probe,
		}
	}()

	var r result
	select {
	case r = <-rCh:
	case <-snapCtx.Done():
		// 超时: 返回最小快照, 不阻塞
		return &Snapshot{CapturedAt: time.Now(), FailureHint: hint}
	}

	// 把 CandidateInfo ([]any 不友好, 直接序列化 []CandidateInfo)
	candAny := make([]any, 0, len(r.cands))
	for _, c := range r.cands {
		candAny = append(candAny, c)
	}

	return &Snapshot{
		CapturedAt:       time.Now(),
		Candidates:       candAny,
		RoutingState:     r.state,
		CredentialMode:   r.cred,
		NodeProbeState:   r.probe,
		ConcurrencySlot:  &r.conc,
		FailureHint:      hint,
	}
}

// Avoid unused import warnings for db pool reference (used in trace.go FlushToPG).
var _ = (*pgxpool.Pool)(nil)
