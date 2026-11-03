package executors

// modality_upstream_chat_shape_guard_test.go —— ★★ 「chat 形状请求**不许**到达登记为
// embedding 的上游」这条不变量，在**探针后刷新**路径上的**上游侧**观测（第三十五轮）。
//
// ── 这道门补的是哪一条缺口 ────────────────────────────────────────────
//
// 第十五轮（F-14）把两道门都做完了：
//
//	① `domains/streaming/modality_embedding_chat_closed_loop_test.go`（从**网关自己**看）
//	② `domains/streaming/executors/modality_upstream_not_called_test.go`（从**上游那边**看）
//
// ⇒ ⇒ ★★ 但那两道门钉的是**「换替代模型」**这一条路径。
//
// ★★ 而 §18.6（第十五轮自己）记下了**残留**：
//
//	「『**探针后刷新**』那条再解析路径（`refreshedCandidatesAfterProbe`）的
//	  **上游侧**后果**仍未被直接观测**（第五轮只有函数级门）⇒ 登记待议」
//
// ⇒ ⇒ ★★ 本门补的正是这一条：★ **探针恢复 → 刷新候选 → 重试** 这条路径上的
//     **上游侧**观测（★ 第五轮那道是**函数级**的 ⇒ 它证明不了上游收到了什么）。
//
// ── ★★ 场景的三个关键建模（★ 这三点决定这道门有没有牙）──────────────
//
//	① **池子 = 未过滤的真相**；★ **快照 = 模态门之后的那一份**
//	   （★ handler 已经带模态解析过了 ⇒ 登记为 embedding 的绑定**本来就不在快照里**）
//	② 快照里的候选**不可路由** ⇒ ★ 路由器选不出候选 ⇒ ★ 走「no candidates after router」
//	   ⇒ ★ 触发**同步探针**分支（★ `executor.go:2580`）
//	③ 探针报「恢复」⇒ ★ 执行器调 `refreshedCandidatesAfterProbe` **重新解析**
//	   ⇒ ★ **危险点就在这里**：★ 这次解析如果**不带模态**（回到无过滤的
//	   `GetCandidates`）⇒ ⇒ 登记为 embedding 的绑定被塞回本次**文本**请求
//	   ⇒ ⇒ 上游收到 **chat 形状** ⇒ 400 ⇒ 该绑定翻红 ⇒ 与 2026-11-02 事故同族。
//
// ── ★★ 为什么判据是「**按形状**数请求」而不是「数请求」────────────────
//
// ★ 探针**本来就要**打上游 ⇒ ⇒ 「embedding 上游收到了请求」**不能**当失败判据
//      （★ 那会把**探针本身**算成事故）
// ⇒ ⇒ ★ 本门数 **chat 形状**（路径含 `chat/completions` 或 body 带 `messages`）
//      与 **非 chat 形状**（探针形状）**两种**，★ 判据只针对前者。
//
// ── 两条臂（★ 只差**一个字段**）──────────────────────────────────────
//
//	主臂：★ 候选 B 登记为 **embedding** ⇒ ★ 它的上游 **0 次** chat 形状
//	对照：★ 候选 B 登记为 **text**     ⇒ ★ 它的上游 **≥ 1 次** chat 形状
//	      ⇒ ★ **装置真的能观测到**（★ 否则主臂的「0 次」是空洞断言）
//	恒绿护栏（★ 对两条臂都成立）：★ 文本候选的上游**必须**收到 ≥ 1 次 chat 形状
//	      ⇒ ★ 场景真的跑起来了
//
// ── ★ 为什么这道门是**纯测试**（不动生产代码）────────────────────────
//
// ★ F10 的修法（★ 刷新必须带模态）**已经在生产代码里**（`executor.go:3155-3163`）
// ⇒ ⇒ 本门要做的只是**把它钉住**：★ 将来谁把 `resolveCandidatesByModality`
// ★ 改回无过滤的 `GetCandidates`，★ 这道门当场红。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// ── 上游侧的形状观测装置 ──────────────────────────────────────────────

// upstreamShapeRecorder 记录某个上游收到的**请求形状**。
type upstreamShapeRecorder struct {
	chatCalls  atomic.Int64 // ★ chat 形状（★ 事故形状）
	otherCalls atomic.Int64 // ★ 其它形状（★ 探针形状，合法）
	mu         sync.Mutex
	seenPaths  []string
}

// shapeUpstream 造一个会记录形状的假上游（★ 对任何路径都回 502 ⇒ 请求失败）。
func shapeUpstream(t *testing.T, rec *upstreamShapeRecorder) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		path := r.URL.Path
		rec.mu.Lock()
		rec.seenPaths = append(rec.seenPaths, path)
		rec.mu.Unlock()

		chatShape := strings.Contains(path, "chat/completions")
		if !chatShape {
			// ★ 兜底：★ 路径没带 chat ⇒ 看 body 有没有 `messages`（★ chat 形状的定义）
			var probe struct {
				Messages []json.RawMessage `json:"messages"`
				Input    json.RawMessage   `json:"input"`
			}
			_ = json.Unmarshal(body, &probe)
			chatShape = len(probe.Messages) > 0 && len(probe.Input) == 0
		}
		if chatShape {
			rec.chatCalls.Add(1)
		} else {
			rec.otherCalls.Add(1)
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"persistent upstream failure"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ── provider 替身：★ 登记模态**按候选**给（★ 不是按模型名）────────────

// probeRefreshProvider 是 provider 的一副替身，模拟「探针把节点恢复了」。
//
// ★ 两份候选集：
//
//	· `stale` ⇒ **不可路由**（★ 模拟「节点刚被翻红」）⇒ 路由器选不出候选
//	· `refreshed` ⇒ **可路由**（★ 模拟「探针恢复了它」）⇒ 刷新后会被排进计划
//
// ★ `GetCandidates`（**无过滤**）刻意返回**可路由**的那份
// ⇒ ⇒ ★ 因为「绕过模态门」正是要拿到**危险**的那份（★ 否则这道门没有牙）
type probeRefreshProvider struct {
	stale     []provider.Candidate
	refreshed []provider.Candidate
	// registered 按候选的 RawModel 给登记模态（★ 权威源：models_canonical.modality）。
	registered map[string]string

	plainCalls    atomic.Int64
	mu            sync.Mutex
	modalityCalls []string
}

func (p *probeRefreshProvider) Enabled() bool { return true }

func (p *probeRefreshProvider) ModelKnown(context.Context, string) bool { return true }

func (p *probeRefreshProvider) GetCandidates(_ context.Context, _, _, _ string) ([]provider.Candidate, *provider.Policy, error) {
	p.plainCalls.Add(1)
	return p.refreshed, &provider.Policy{
		TierFallbackMax:    DefaultUpstreamAttemptLimit,
		RetryPerCredential: dispatch.MaxNodeFailures - 1,
	}, nil
}

func (p *probeRefreshProvider) GetCandidatesByModality(_ context.Context, _, _, _, modality string) ([]provider.Candidate, *provider.Policy, error) {
	p.mu.Lock()
	p.modalityCalls = append(p.modalityCalls, modality)
	p.mu.Unlock()
	var out []provider.Candidate
	for _, c := range p.refreshed {
		if provider.ModalityAccepts(p.registered[c.RawModel], modality) {
			out = append(out, c)
		}
	}
	return out, &provider.Policy{
		TierFallbackMax:    DefaultUpstreamAttemptLimit,
		RetryPerCredential: dispatch.MaxNodeFailures - 1,
	}, nil
}

// probeRefreshCandidate 造一条候选（★ routable 决定它会不会被路由器选中）。
func probeRefreshCandidate(baseURL, model string, credentialID int, tier int, routable bool) provider.Candidate {
	c := overloadTestCandidate(baseURL)
	c.ProviderID = credentialID
	c.CredentialID = credentialID
	c.RawModel = model
	c.OfferRawModel = model
	c.StandardizedName = model
	c.APIKey = "key-" + model
	c.Tier = tier
	c.Routable = routable
	if !routable {
		c.AvailabilityState = "unavailable"
	}
	return c
}

// makeStale 把候选集复制成**不可路由**的那份（★ 模拟「节点刚被翻红」）。
func makeStale(cands []provider.Candidate) []provider.Candidate {
	out := make([]provider.Candidate, 0, len(cands))
	for _, c := range cands {
		c.Routable = false
		c.AvailabilityState = "unavailable"
		out = append(out, c)
	}
	return out
}

// ── 门 ──────────────────────────────────────────────────────────────────

// TestExecute_ProbeRefreshMustNotSendChatShapeToEmbeddingUpstream 钉住：
// ★ 探针恢复 → 刷新 → 重试 这条路径上，★ 登记为 embedding 的上游
// **不许**收到 chat 形状的请求。
func TestExecute_ProbeRefreshMustNotSendChatShapeToEmbeddingUpstream(t *testing.T) {
	const (
		clientModel = "probe-refresh-model"
		textModel   = "probe-refresh-text-model"
		embedModel  = "probe-refresh-embed-model"
	)

	cases := []struct {
		name string
		// embedRegistered 是候选 B（第二个候选）的登记模态。
		embedRegistered string
		// wantChatOnEmbed 是候选 B 那个上游应收到的 **chat 形状**请求数下界。
		wantChatOnEmbed int64
	}{
		{
			name:            "主臂：登记为 embedding ⇒ chat 形状 0 次",
			embedRegistered: "embedding",
			wantChatOnEmbed: 0,
		},
		{
			name:            "对照臂：登记为 text ⇒ chat 形状 ≥ 1 次（证明装置真的能观测到）",
			embedRegistered: "text",
			wantChatOnEmbed: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var textRec, embedRec upstreamShapeRecorder
			textUpstream := shapeUpstream(t, &textRec)
			embedUpstream := shapeUpstream(t, &embedRec)

			// ★ 候选 A（登记为 text，★ tier 1）与候选 B（登记由臂决定，★ tier 2）
			candA := probeRefreshCandidate(textUpstream.URL, textModel, 11, 1, true)
			candB := probeRefreshCandidate(embedUpstream.URL, embedModel, 99, 2, true)

			// ★★ **候选池只放候选 B**（★ 关键设计，★ 决定上游侧判据有没有牙）：
			//
			//	若刷新后的候选集里有**两个**候选 ⇒ ★ 计划会先挑 tier 1 的那个
			//	⇒ 它失败后**执行器不一定扇出**到 tier 2（★ 实测：不会）
			//	⇒ ⇒ 于是「B 收到了多少 chat 形状请求」这个读数**失真**
			//	⇒ ⇒ ★ 上游侧判据就成了**没有牙**的断言。
			//
			//	所以：★ 刷新后的候选集**只有候选 B** ⇒ ★ 它**必然**被选中
			//	⇒ ⇒ ★ 「B 收到了 chat 形状请求」这个读数**就是事故链本身**。
			refreshedSet := []provider.Candidate{candB}

			// ★ gated 快照（★ 模态门之后的那一份）：
			//	主臂 ⇒ 只有文本候选 candA（★ B 登记为 embedding ⇒ 本来就不在快照里）
			//	对照臂 ⇒ B 登记为 text ⇒ 它**在**快照里
			var gated []provider.Candidate
			if tc.embedRegistered == "embedding" {
				gated = []provider.Candidate{candA}
			} else {
				gated = []provider.Candidate{candB}
			}

			prov := &probeRefreshProvider{
				// ★ 陈旧的那份：**不可路由** ⇒ 路由器选不出候选 ⇒ 走探针分支
				stale: makeStale(refreshedSet),
				// ★ 恢复后的那份：**可路由** ⇒ 刷新后会被排进计划
				refreshed: refreshedSet,
				registered: map[string]string{
					textModel:  "text",
					embedModel: tc.embedRegistered,
				},
			}

			cm := newCircuitManagerForTest()
			lim := newLimiterForTest()
			defer lim.Stop()
			exec := &Executor{
				Circuit:                cm,
				Limiter:                lim,
				Router:                 NewRouter(NewStickyCache(), lim),
				UpstreamTimeout:        5 * time.Second,
				StreamTimeout:          10 * time.Second,
				SyncNoCandidateProbe:   true,
				SyncNoCandidateTimeout: 2 * time.Second,
				Provider:               prov,
			}
			wireDispatchPipelineForTest(t, exec)

			probeCalled := atomic.Bool{}
			exec.ProbeSync = func(_ context.Context, candidates []credentialstate.NoCandidatesCandidate, _, _ string) bool {
				probeCalled.Store(true)
				return len(candidates) > 0
			}

			// ★ 让快照里的候选**不可路由** ⇒ 路由器选不出候选 ⇒ 走探针分支
			for i := range gated {
				gated[i].Routable = false
				gated[i].AvailabilityState = "unavailable"
			}

			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
			r.Header.Set("X-Request-Id", "probe-refresh-modality-guard")
			rec := httptest.NewRecorder()

			params := &ExecParams{
				W:             rec,
				R:             r,
				BodyBytes:     []byte(`{"model":"` + clientModel + `","messages":[{"role":"user","content":"hi"}]}`),
				IsStream:      false,
				ClientModel:   clientModel,
				OutboundModel: clientModel,
				Model:         clientModel,
				RequestID:     "probe-refresh-modality-guard",
				StickyKey:     "probe-refresh-modality-guard",
				Candidates:    gated,
				Policy: &provider.Policy{
					TierFallbackMax:    DefaultUpstreamAttemptLimit,
					RetryPerCredential: dispatch.MaxNodeFailures - 1,
				},
				// ★ 本次请求是**文本**请求 ⇒ 刷新必须过 text 模态门
				RequestModality:  "text",
				UpstreamAttempts: NewUpstreamAttemptBudget(DefaultUpstreamAttemptLimit),
				OnProbeHoldStart: func() {},
				OnProbeHoldEnd: func(recovered bool) {
					if recovered {
						// ★ 探针把节点恢复了（★ 模拟 provider 侧的状态已落库）
						for i := range gated {
							gated[i].Routable = true
						}
					}
				},
			}

			_, err := exec.Execute(params)
			if err == nil {
				t.Fatal("上游一直回 502 ⇒ 执行必须以错误收尾，want err != nil")
			}

			// ── 恒绿护栏（★ 对两条臂都成立）：★ 场景真的跑起来了 ──
			//
			// ★★ 用 Errorf **不用** Fatalf（★ 第三十五轮实测踩到）：
			// ★★ 护栏若 Fatal ⇒ **后面的核心断言根本跑不到** ⇒ ★ 变异时看到的
			// ★★ 只有「场景没跑起来」，★ 而**不是**「事故形状真的到了上游」
			// ⇒ ⇒ ★ 证据链就断了（★ 护栏挡住核心信号）。
			if !probeCalled.Load() {
				t.Fatal("★ ProbeSync 没被调用 ⇒ 场景没走到探针分支 ⇒ 下面的计数没有意义")
			}
			guardRec := &textRec
			if tc.embedRegistered != "embedding" {
				guardRec = &embedRec
			}
			if got := guardRec.chatCalls.Load(); got < 1 {
				t.Errorf("★ 恒绿护栏：被刷新选中的那个上游只收到 %d 次 chat 形状请求 ⇒ 场景没跑起来", got)
			}

			// ── ★★ **核心断言**（★ 上游侧的直接观测）──
			if got := embedRec.chatCalls.Load(); got < tc.wantChatOnEmbed {
				t.Errorf("★ 候选 B 的上游收到 **chat 形状**请求 %d 次，want ≥ %d", got, tc.wantChatOnEmbed)
			}
			if tc.wantChatOnEmbed == 0 && embedRec.chatCalls.Load() != 0 {
				t.Errorf("★★ 登记为 embedding 的绑定在**探针后刷新**这条路径上收到了 **chat 形状**请求（%d 次）⇒ "+
					"⇒ 刷新绕过了模态门 ⇒ 上游会 400 ⇒ 该绑定被翻红 ⇒ 2026-11-02 事故链复活",
					embedRec.chatCalls.Load())
			}

			// ── 刷新必须**带着本次请求的模态**（★ F10 的修法在**上游侧**的读数）──
			prov.mu.Lock()
			modalityCalls := append([]string(nil), prov.modalityCalls...)
			prov.mu.Unlock()
			found := false
			for _, m := range modalityCalls {
				if m == "text" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("★ GetCandidatesByModality 的实参 = %v，want 含 %q（★ 刷新必须带本次请求的模态）",
					modalityCalls, "text")
			}
			// ★ 无过滤查询**不许**被刷新用上（★ 那是 F10 修复前的写法）
			if got := prov.plainCalls.Load(); got != 0 {
				t.Errorf("★ GetCandidates（无模态过滤）被调用 %d 次 ⇒ 刷新绕过了模态门", got)
			}
		})
	}
}
