package main

// main_pipeline_requestid_test.go —— ★★ **D09 的对拍测试**（2026-10-24 §3.19）。
//
// # 这道门补的是哪块缺口
//
// 计划书 **P2-1** 的验收条款 ② 是「**每条差异有测试覆盖**」。
// ⇒ ★ `internal/pipelinev2` 的 17 条行为差异里，**D09 一直被登记为 `Uncovered`**
// ⇒ ⇒ 字面条款 ② 只达成 **16/17**。
//
// ★★ 而且那条 `Uncovered` 说明**本身是失实的**：它写「全仓搜过：`v2pipe` 字面量**零命中**」，
// ★★ 而实测 `cmd/gateway/main_pipeline.go:749-758` **有**该合成逻辑
// ⇒ ⇒ ★★ 也就是说：**行为是实现了的，只是没有测试** ⇒ ⇒ 本门补的就是那层测试。
//
// # D09 描述的契约（逐字来自 internal/pipelinev2/pipelinev2.go）
//
//	「v2 优先取 `X-Request-Id`；缺失时合成 `v2pipe-<纳秒>` 并**写回** header，
//	  以免一个请求分裂成两条审计链。」
//
// ⇒ ★★ 其中「**写回**」是这半条契约的**要害**（2026-07-27 G-ID-2 修的就是它）：
// ★★ 合成值若只活在 `env.RequestID` 而不写回 header，
// ★★ 下游的 chatHandler 会**重新读 header 并生成另一个 id** ⇒ 一个请求**分裂成两条审计链**。
//
// ⇒ ★★ 契约里的「**<纳秒>**」同样是**字面要求** ⇒ ⇒ 只断言 `v2pipe-` 前缀是不够的
// ⇒ ⇒ 合成值必须携带**本次请求时刻的纳秒时间戳**（★ 变异 M50 证过这一点：
// ★★ 把合成值改成固定串时，只查前缀的断言**仍绿** ⇒ 那不是「测试有牙」，那是「断言太弱」）。
//
// # ★ 三条判据（★ 都要在**同一个请求**上验证 ⇒ 合成与写回必须「是同一个值」）

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

// d09CapturingHook 捕获 v2 envelope 里的 RequestID。
type d09CapturingHook struct {
	seen string
}

func (h *d09CapturingHook) Name() string  { return "d09-request-id-probe" }
func (h *d09CapturingHook) Priority() int { return 100 }

func (h *d09CapturingHook) Enabled(context.Context, *domain.PipelineRequest) bool { return true }

func (h *d09CapturingHook) OnError(context.Context, *domain.PipelineRequest, error) error {
	return nil
}

func (h *d09CapturingHook) Execute(_ context.Context, env *domain.PipelineRequest) error {
	if env != nil && env.Envelope != nil {
		h.seen = env.Envelope.RequestID
	}
	return nil
}

// d09Result 是一次 dispatch 的读数。
type d09Result struct {
	envelopeID string // pipeline hook 看到的 envelope.RequestID
	headerID   string // fallback handler 看到的 X-Request-Id header
	beforeNano int64  // dispatch 之前的 UnixNano（用于校验合成值落在请求窗口内）
	afterNano  int64  // dispatch 之后的 UnixNano
}

// d09Dispatch 跑一次 v2 dispatch，返回「hook 看到的 RequestID」与「fallback 看到的 header 值」。
func d09Dispatch(t *testing.T, incomingRequestID string) d09Result {
	t.Helper()

	hook := &d09CapturingHook{}
	p := pipeline.NewRequestPipeline()
	p.AddStage(&pipeline.PipelineStage{
		Name:  "d09-probe",
		Phase: pipeline.PhaseRouting,
		Hooks: []pipeline.Hook{hook},
		Mode:  pipeline.ModeSequential,
	})

	deps := &v2DispatchDeps{Pipeline: p}
	var headerID string
	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headerID = r.Header.Get("X-Request-Id")
		w.WriteHeader(http.StatusOK)
	})
	h := v2DispatchHandler(deps, fallback)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	if incomingRequestID != "" {
		req.Header.Set("X-Request-Id", incomingRequestID)
	}
	rec := httptest.NewRecorder()

	before := time.Now().UnixNano()
	h.ServeHTTP(rec, req)
	after := time.Now().UnixNano()

	if rec.Code != http.StatusOK {
		t.Fatalf("dispatch 后状态码 = %d，want 200（body: %s）", rec.Code, rec.Body.String())
	}
	return d09Result{
		envelopeID: hook.seen,
		headerID:   headerID,
		beforeNano: before,
		afterNano:  after,
	}
}

// assertSynthesizedNano 断言 id 是「本次请求时刻的纳秒时间戳」形态。
//
// ★ 契约写的是 `v2pipe-<纳秒>` ⇒ 只查前缀是不够的（★ 变异 M50 教训）：
// ★ 一个固定的 `v2pipe-xxx` 同样带前缀，但它**不是纳秒** ⇒ 契约不满足。
//
// ⇒ 判据 = ① 纳秒部分必须是合法十进制整数；② 它必须落在 [请求前 1s, 请求后 1s] 窗口内。
func assertSynthesizedNano(t *testing.T, id string, beforeNano, afterNano int64) {
	t.Helper()

	if !strings.HasPrefix(id, "v2pipe-") {
		t.Fatalf("★ 合成值 %q 缺少 %q 前缀", id, "v2pipe-")
	}
	raw := strings.TrimPrefix(id, "v2pipe-")
	if raw == "" {
		t.Fatalf("★ 合成值 %q 缺少纳秒部分", id)
	}
	ns, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		t.Fatalf("★ 合成值 %q 的纳秒部分 %q 不是合法十进制整数（契约：合成 `v2pipe-<纳秒>`）：%v",
			id, raw, err)
	}
	const slack = int64(time.Second)
	if ns < beforeNano-slack || ns > afterNano+slack {
		t.Errorf("★ 合成值 %q 的纳秒时间戳 %d 不在本次请求窗口 [%d, %d] 内\n"+
			"★ ⇒ 它**不是本次请求的纳秒**（契约：合成 `v2pipe-<纳秒>`）",
			id, ns, beforeNano-slack, afterNano+slack)
	}
}

// TestV2DispatchRequestIDPrefersServerID 是 D09 的前半条：★ 已有服务端 id ⇒ **沿用它**，不合成。
//
// ★ 注意口径：★ v2 这一层**取** `X-Request-Id` header 的现值；
// ★ 而在生产里那个值**总是**服务端生成的（`middleware.RequestIDMiddleware` 永远覆写它），
// ★ 所以「不用客户端的值」是**中间件那一层**的保证，不是 v2 这一层的。
func TestV2DispatchRequestIDPrefersServerID(t *testing.T) {
	const serverID = "srv-7f3a9c1e"

	got := d09Dispatch(t, serverID)

	if got.envelopeID != serverID {
		t.Errorf("v2 envelope 的 RequestID = %q，want %q（★ 已有服务端 id 时应当沿用，不合成）",
			got.envelopeID, serverID)
	}
	if got.headerID != serverID {
		t.Errorf("fallback 看到的 X-Request-Id = %q，want %q（★ 应当原样透传）",
			got.headerID, serverID)
	}
	if strings.HasPrefix(got.envelopeID, "v2pipe-") {
		t.Errorf("★ 已有服务端 id 时**不该**合成 v2pipe- 前缀值（得到 %q）", got.envelopeID)
	}
}

// TestV2DispatchRequestIDSynthesizesAndWritesBack 是 D09 的后半条：★ 缺失时合成 `v2pipe-<纳秒>`
// 并**写回 header** ⇒ ★★ 且**写回的值与 envelope 里的值必须是同一个**（否则一个请求分裂成两条审计链）。
func TestV2DispatchRequestIDSynthesizesAndWritesBack(t *testing.T) {
	got := d09Dispatch(t, "")

	assertSynthesizedNano(t, got.envelopeID, got.beforeNano, got.afterNano)

	if got.headerID != got.envelopeID {
		t.Errorf("★★ 写回 header 的值 (%q) 与 envelope 里的值 (%q) **不一致**\n"+
			"★★ ⇒ 一个请求会**分裂成两条审计链**（这正是 2026-07-27 G-ID-2 修的那个坑）\n"+
			"★★ ⇒ 修法：合成后必须 `r.Header.Set(\"X-Request-Id\", …)`",
			got.headerID, got.envelopeID)
	}
}
