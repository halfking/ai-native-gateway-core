package admin

// provider_probe_live_vapeur_test.go — 2026-09-28 vapeur（Vapeur AI）
// 事故的**真实上游**回归探针。
//
// 背景：本文件对应的用户报障是「本地与 154 均调不通 grok-4.6，报
// context deadline exceeded」。离线单测
// （TestDoResponsesProbe_SendsResponsesBodyShape）只证明 URL 与请求体
// 形状正确，证明不了真实上游在探针的 10s 预算内能否应答 —— 而后者
// 恰恰是本次事故的真因（实测延迟 3.9~8.95s，最差仅剩 1s 余量）。
//
// 因此这里补一条**可选**的 live 用例：默认 skip，仅当设置了
// VAPEUR_API_KEY 环境变量时才真正打线上，用于复现与回归。
//
// 运行：
//
//	VAPEUR_API_KEY=sk-xxx go test ./admin/ -run TestLiveVapeurResponsesProbe -v
//
// 不设置该变量时用例整体跳过，不影响任何 CI/离线回归。

import (
	"context"
	"os"
	"testing"
	"time"
)

const (
	liveVapeurBaseURL = "https://api.vapeur.ai/v1"
	liveVapeurModel   = "grok-4.6"
)

func liveVapeurKey(t *testing.T) string {
	t.Helper()
	key := os.Getenv("VAPEUR_API_KEY")
	if key == "" {
		t.Skip("VAPEUR_API_KEY 未设置，跳过真实上游探针（离线回归不受影响）")
	}
	return key
}

// TestLiveVapeurResponsesProbe 打真实上游 /v1/responses，复现用户报障路径。
//
// 断言要点：
//  1. credentialProbeDispatch 对 openai-responses 派发到 /v1/responses
//     （而非报障时实际命中的 /v1/chat/completions）；
//  2. 该请求在探针当前预算内能拿到 200。
//
// 若断言 2 间歇性失败，即为「探针超时预算不足」的线上证据，此时应放宽
// doResponsesProbe 的 http.Client.Timeout，而不是改 provider 配置。
func TestLiveVapeurResponsesProbe(t *testing.T) {
	apiKey := liveVapeurKey(t)

	probeURL, probeFn := credentialProbeDispatch("openai-responses", liveVapeurBaseURL)
	t.Logf("dispatch → %s", probeURL)

	if want := liveVapeurBaseURL + "/responses"; probeURL != want {
		t.Fatalf("probeURL = %q, want %q（openai-responses 必须走原生 /v1/responses）", probeURL, want)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	start := time.Now()
	result, err := probeFn(ctx, probeURL, apiKey, liveVapeurModel)
	elapsed := time.Since(start)

	if err != nil {
		// doResponsesProbe 传输层错误时返回 (nil, err)——必须先判 err
		// 再解引用 result，否则取证日志自身先 nil panic（十六轮审计 E2）。
		t.Fatalf("probe transport error after %s: %v (no response)", elapsed.Round(time.Millisecond), err)
	}
	t.Logf("probe elapsed=%s status=%d", elapsed.Round(time.Millisecond), result.statusCode)
	if result.statusCode != 200 {
		t.Fatalf("statusCode = %d, want 200; errorMessage=%q", result.statusCode, result.errorMessage)
	}
	if result.modelInResponse != "" && result.modelInResponse != liveVapeurModel {
		t.Logf("note: upstream echoed model %q (requested %q)", result.modelInResponse, liveVapeurModel)
	}
}

// TestLiveVapeurChatProbeIsNotUsedForResponses 记录事故现场：同一凭据在
// 能力位缺失时的降级目标是 /v1/chat/completions。该路径对图像类模型会
// 挂死到超时（实测 qwen-image-2.0-pro-cn 满 15s 零响应），因此探针不应
// 用它去 ping 非文本模型。
//
// 本用例只做观测不做断言失败，用于现场取证。
func TestLiveVapeurChatProbeIsNotUsedForResponses(t *testing.T) {
	apiKey := liveVapeurKey(t)

	fallbackURL, fallbackFn := credentialProbeDispatch("openai-completions", liveVapeurBaseURL)
	t.Logf("fallback (openai-completions) → %s", fallbackURL)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	start := time.Now()
	result, err := fallbackFn(ctx, fallbackURL, apiKey, liveVapeurModel)
	elapsed := time.Since(start).Round(time.Millisecond)
	// 该路径对图像类模型挂死到超时正是要观测的场景——传输错误时
	// fallbackFn 返回 (nil, err)，直接解引用会在取证前 panic。
	if result != nil {
		t.Logf("chat probe elapsed=%s status=%d err=%v", elapsed, result.statusCode, err)
	} else {
		t.Logf("chat probe elapsed=%s no response (transport error): %v", elapsed, err)
	}
}
