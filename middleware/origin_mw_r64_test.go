package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// R64（2026-09-25）：global-auth-passed 哨兵的 stage↔actor 配对校验。
// 静态全局 key 无法区分真实 worker 与持同 key 的任意调用者；修复前哨兵
// 调用者可发送任意合法 stage（如 manual）伪造 origin_stage 绕过自检限流。
// 修复后 stage 只有与 globalAuthStageActorPairs 登记的真实写入方 actor
// 配对才被信任，其余组合降级为非信任 → 剥头 + business。

// runOriginMW 构造一条哨兵/owner 已注入 ctx 的最小链，返回下游观测值与
// 到达下游时仍存活的请求头（剥头语义）。
func runOriginMW(t *testing.T, owner, stageHdr, actorHdr string) (stage, actor, liveStage, liveActor string) {
	t.Helper()
	mw := NewOriginMiddleware()
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stage = ContextOriginStage(r.Context())
		actor = ContextOriginActor(r.Context())
		liveStage = r.Header.Get("X-LLM-Origin-Stage")
		liveActor = r.Header.Get("X-LLM-Origin-Actor")
	})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if stageHdr != "" {
		r.Header.Set("X-LLM-Origin-Stage", stageHdr)
	}
	if actorHdr != "" {
		r.Header.Set("X-LLM-Origin-Actor", actorHdr)
	}
	ctx := RegisterAuthOwnerUser(r.Context(), owner)
	mw.Wrap(downstream).ServeHTTP(httptest.NewRecorder(), r.WithContext(ctx))
	return stage, actor, liveStage, liveActor
}

// 正确配对放行：真实 worker 的 (stage, actor) 组合原样通过，头不被剥。
func TestOriginMiddleware_GlobalAuthPairingAcceptsMatch(t *testing.T) {
	pairs := [][2]string{
		{"self_check", "self-check-worker"},
		{"self_check", "credential-selfcheck-worker"},
		{"self_check", "model-quality-worker"},
		{"node_probe", "node-probe-worker"},
		{"node_probe", "probe-service"},
	}
	for _, p := range pairs {
		stage, actor, liveStage, liveActor := runOriginMW(t, "global-auth-passed", p[0], p[1])
		if stage != p[0] || actor != p[1] {
			t.Fatalf("pair (%s,%s): got stage=%q actor=%q, want passthrough", p[0], p[1], stage, actor)
		}
		if liveStage == "" || liveActor == "" {
			t.Fatalf("pair (%s,%s): inbound headers must survive (got %q/%q)", p[0], p[1], liveStage, liveActor)
		}
	}
}

// 错配 actor 剥头：伪造的 actor 使配对失败 → business/"" 且入站头被剥。
func TestOriginMiddleware_GlobalAuthPairingRejectsSpoofedActor(t *testing.T) {
	stage, actor, liveStage, liveActor := runOriginMW(t, "global-auth-passed", "self_check", "attacker")
	if stage != "business" || actor != "" {
		t.Fatalf("spoofed actor: got stage=%q actor=%q, want business/\"\"", stage, actor)
	}
	if liveStage != "" || liveActor != "" {
		t.Fatalf("spoofed actor: headers must be stripped, got %q/%q", liveStage, liveActor)
	}

	// 跨 stage 借用 actor 同样拒绝：node_probe 的合法 actor 配 self_check。
	stage, actor, _, _ = runOriginMW(t, "global-auth-passed", "self_check", "node-probe-worker")
	if stage != "business" || actor != "" {
		t.Fatalf("cross-stage actor: got stage=%q actor=%q, want business/\"\"", stage, actor)
	}
}

// 缺失 actor 或未登记 stage 剥头：配对要求 actor 非空且 stage 在表内。
func TestOriginMiddleware_GlobalAuthPairingRejectsUnpairedStage(t *testing.T) {
	// stage 有值但无 actor。
	stage, actor, _, _ := runOriginMW(t, "global-auth-passed", "node_probe", "")
	if stage != "business" || actor != "" {
		t.Fatalf("missing actor: got stage=%q actor=%q, want business/\"\"", stage, actor)
	}
	// 伪造未登记 stage（限流绕过向量：manual 在 isValidOriginStage 白名单内）。
	stage, actor, liveStage, _ := runOriginMW(t, "global-auth-passed", "manual", "self-check-worker")
	if stage != "business" || actor != "" {
		t.Fatalf("unregistered stage: got stage=%q actor=%q, want business/\"\"", stage, actor)
	}
	if liveStage != "" {
		t.Fatalf("unregistered stage: header must be stripped, got %q", liveStage)
	}
}

// 不 claim stage 的哨兵调用者不获任何特权：stage 保持 business 默认值。
func TestOriginMiddleware_GlobalAuthNoStageClaimStaysBusiness(t *testing.T) {
	stage, actor, _, _ := runOriginMW(t, "global-auth-passed", "", "some-actor")
	if stage != "business" || actor != "some-actor" {
		t.Fatalf("no stage claim: got stage=%q actor=%q, want business/some-actor", stage, actor)
	}
}

// 非 trusted 调用者（无 owner）既有剥头行为不变（回归锚点）。
func TestOriginMiddleware_NonTrustedCallerStillStripped(t *testing.T) {
	stage, actor, liveStage, liveActor := runOriginMW(t, "", "self_check", "self-check-worker")
	if stage != "business" || actor != "" {
		t.Fatalf("non-trusted: got stage=%q actor=%q, want business/\"\"", stage, actor)
	}
	if liveStage != "" || liveActor != "" {
		t.Fatalf("non-trusted: headers must be stripped, got %q/%q", liveStage, liveActor)
	}
}

// 其他 trustedOwner 的既有语义不变（收敛要求）：DB 已解析的 owner 仍可
// 信任任意合法 stage + 任意非空 actor，不受配对表约束。
func TestOriginMiddleware_DBOwnedWorkersKeepLegacySemantics(t *testing.T) {
	// owner=node-probe-worker 声明未登记在配对表里的 system_health（历史上
	// 该组合依赖 DB 侧 is_system 校验，pre-R64 语义保持）。
	stage, actor, liveStage, _ := runOriginMW(t, "node-probe-worker", "system_health", "manual:42")
	if stage != "system_health" || actor != "manual:42" {
		t.Fatalf("db owner: got stage=%q actor=%q, want system_health/manual:42", stage, actor)
	}
	if liveStage == "" {
		t.Fatal("db owner: header must not be stripped")
	}
}
