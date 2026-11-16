package executors

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// ─────────────────────────────────────────────────────────────────────────────
// 2026-11-17（节点状态/可重试性审计轮）：格式类守卫 ⇒ 可重试
//
// owner 报障：「有些请求遇到一些简单的错误，应该是可以重试解决的，但现解决不了」，
// 实例＝ZCode 报障 31：
//
//	Response blocked by output policy (rule=builtin.guard.tool_json_invalid):
//	output policy blocked stream lane ... retryable=false
//
// 根因＝执行层拿不到规则名：非流式路径只回一个裸的 ErrOutputPolicyBlocked
// （domains/streaming/executors/output_policy.go 此前只认 error.code）⇒
// 「内容策略拦截（终态）」与「格式守卫拦下畸形输出（可换节点重试）」被压成
// 同一个 KindContentFilter ⇒ 终态、不可重试。
//
// 本文件钉住修后的三态：
//
//	① 格式守卫 + 客户端尚未收到字节 ⇒ KindRequestRejected（跨候选故障转移，
//	  PenaltyNone ⇒ 不冷却凭据、不计熔断、不扇出探针）
//	② 格式守卫 + 客户端已收到字节 ⇒ 仍是 KindContentFilter（重放会把同一段
//	  内容发两遍）
//	③ 内容策略拦截（任何规则） ⇒ 仍是 KindContentFilter（终态不变）
// ─────────────────────────────────────────────────────────────────────────────

type formatGuardFailureDB struct{ calls atomic.Int64 }

func (db *formatGuardFailureDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	db.calls.Add(1)
	return pgconn.CommandTag{}, nil
}

// newFormatGuardExecutor 组装一个「上游正常返回、正文被脱敏回调整成策略
// 信封」的执行器，与既有 TestDispatchWriteTimePolicyDoesNotRecordProviderFailure
// 同一形状。
func newFormatGuardExecutor(t *testing.T, rule string, failures *formatGuardFailureDB, health *dispatchHealthCapture) (*Executor, provider.Candidate, *httptest.Server) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"synthetic-secret"},"finish_reason":"stop"}]}`))
	}))
	limiter := newLimiterForTest()
	t.Cleanup(limiter.Stop)
	executor := &Executor{
		Limiter: limiter, Circuit: newCircuitManagerForTest(), UpstreamTimeout: 5 * time.Second,
		NodeHealthAdapter: health, FailureLogger: NewCandidateFailureWriter(failures),
		RedactBodyFn: func([]byte, string, string) []byte {
			return []byte(`{"error":{"code":"output_policy_blocked","rule":"` + rule +
				`","reason":"output policy blocked stream lane","field":"$","request_id":"rid-1"}}`)
		},
	}
	candidate := provider.Candidate{ProviderID: 11, CredentialID: 22, BaseURL: upstream.URL,
		Protocol: "openai-completions", CatalogCode: "openai", RawModel: "m", APIKey: "synthetic-key"}
	return executor, candidate, upstream
}

func TestFormatGuardPolicyBlockIsRetryableAcrossCandidates(t *testing.T) {
	failures := &formatGuardFailureDB{}
	health := &dispatchHealthCapture{}
	executor, candidate, upstream := newFormatGuardExecutor(t, errorsx.GuardRuleToolJSONInvalid, failures, health)
	defer upstream.Close()

	params := &ExecParams{
		W: httptest.NewRecorder(), R: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
		Model:     "m", ClientModel: "m", RequestID: "fmt-attempt", TenantID: "tenant-1",
	}
	dctx := &dispatchCtx{params: params, candidates: []provider.Candidate{candidate},
		retryPerCred: 0, tTotal: time.Now(), stickyCredID: &candidate.CredentialID}
	out := executor.forwardForDispatch(dctx, candidate, "fmt-attempt-1", nil, nil)

	if !errors.Is(out.Err, errorsx.ErrOutputPolicyBlocked) {
		t.Fatalf("typed policy cause lost: %+v", out)
	}
	if out.ErrorKind != string(errorsx.KindRequestRejected) {
		t.Fatalf("格式守卫必须可换兄弟节点重试，got kind=%q want %q", out.ErrorKind, errorsx.KindRequestRejected)
	}
	if out.HTTPStatus != http.StatusForbidden {
		t.Fatalf("客户端可见状态被改写：got %d want 403", out.HTTPStatus)
	}
	if out.BytesSent {
		t.Fatal("未提交任何字节却报告 BytesSent")
	}
	// 归责：格式错不是节点故障证据 ⇒ 不写候选失败行、不喂健康决策器。
	if got := failures.calls.Load(); got != 0 {
		t.Errorf("格式守卫写进了候选失败表：calls=%d", got)
	}
	if got := health.snapshot(); len(got) != 0 {
		t.Errorf("格式守卫被当成节点健康证据：%+v", got)
	}
	// 规则名必须能从错误里取回（否则执行层与客户端面都认不出是格式守卫）。
	if got := errorsx.PolicyBlockRuleID(out.Err); got != errorsx.GuardRuleToolJSONInvalid {
		t.Fatalf("规则名丢失：got %q want %q", got, errorsx.GuardRuleToolJSONInvalid)
	}
}

// TestForwardCommittedBytesGate pins the「客户端是否已经收到字节」闸门。
//
// ★ 这个闸门是「格式守卫可重试」的**硬边界**：已经提交过内容的响应重放会
// 把同一段内容发两遍 ⇒ 必须保持终态。
//
// 判据与 forwardForDispatch 内部一致（ClientSemanticBytesVisible 优先，
// 流式再退到 Capture 的 chunk 计数）—— 同一个「首字节边界」定义，不能两处
// 各写一套。
func TestForwardCommittedBytesGate(t *testing.T) {
	if forwardCommittedBytes(nil) {
		t.Fatal("nil params 不得判成已提交")
	}
	if forwardCommittedBytes(&ExecParams{}) {
		t.Fatal("无任何信号时不得判成已提交")
	}
	visible := &atomic.Bool{}
	if forwardCommittedBytes(&ExecParams{ClientSemanticBytesVisible: visible}) {
		t.Fatal("未置位的可见性标记不得判成已提交")
	}
	visible.Store(true)
	if !forwardCommittedBytes(&ExecParams{ClientSemanticBytesVisible: visible}) {
		t.Fatal("可见性标记已置位却判成未提交")
	}
}

// TestFormatGuardPolicyBlockInNonStreamPathHasNoCommittedBytes pins the
// premise the retry fix rests on: the non-stream path detects the policy
// envelope **before** anything reaches the client (WriteNonStreamResponse
// returns the typed error without committing the body), so retrying a sibling
// candidate cannot duplicate delivered content.
//
// ⇒ 非流式路径上「格式守卫 ⇒ 可重试」恒成立；闸门
// forwardCommittedBytes 是为「可能已经提交过字节」的那些路径准备的
// （流式面：策略拦截由 handler 的写面拦截器判定）。
func TestFormatGuardPolicyBlockInNonStreamPathHasNoCommittedBytes(t *testing.T) {
	failures := &formatGuardFailureDB{}
	health := &dispatchHealthCapture{}
	executor, candidate, upstream := newFormatGuardExecutor(t, errorsx.GuardRuleToolJSONInvalid, failures, health)
	defer upstream.Close()

	visible := &atomic.Bool{}
	params := &ExecParams{
		W: httptest.NewRecorder(), R: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
		Model:     "m", ClientModel: "m", RequestID: "fmt-sent", TenantID: "tenant-1",
		FirstSemanticByteCallback: func() { visible.Store(true) },
	}
	dctx := &dispatchCtx{params: params, candidates: []provider.Candidate{candidate},
		retryPerCred: 0, tTotal: time.Now(), stickyCredID: &candidate.CredentialID}
	out := executor.forwardForDispatch(dctx, candidate, "fmt-sent-1", func() { visible.Store(true) }, nil)

	if out.BytesSent {
		t.Fatalf("非流式策略拦截点不应有字节已提交：%+v", out)
	}
	if out.ErrorKind != string(errorsx.KindRequestRejected) {
		t.Fatalf("非流式格式守卫必须可重试：got kind=%q want %q", out.ErrorKind, errorsx.KindRequestRejected)
	}
}

func TestContentPolicyBlockStaysTerminal(t *testing.T) {
	failures := &formatGuardFailureDB{}
	health := &dispatchHealthCapture{}
	executor, candidate, upstream := newFormatGuardExecutor(t, "builtin.network_destination", failures, health)
	defer upstream.Close()

	params := &ExecParams{
		W: httptest.NewRecorder(), R: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
		Model:     "m", ClientModel: "m", RequestID: "policy-attempt", TenantID: "tenant-1",
	}
	dctx := &dispatchCtx{params: params, candidates: []provider.Candidate{candidate},
		retryPerCred: 0, tTotal: time.Now(), stickyCredID: &candidate.CredentialID}
	out := executor.forwardForDispatch(dctx, candidate, "policy-attempt-1", nil, nil)

	if out.ErrorKind != string(errorsx.KindContentFilter) {
		t.Fatalf("内容策略拦截必须保持终态：got kind=%q want %q", out.ErrorKind, errorsx.KindContentFilter)
	}
	if got := failures.calls.Load(); got != 0 {
		t.Errorf("内容策略拦截写进了候选失败表：calls=%d", got)
	}
}
