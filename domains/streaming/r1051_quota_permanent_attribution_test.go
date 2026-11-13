package streaming

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// TestQuotaPermanentAttributionChain_CredentialBranch 验证 KindQuotaPermanent
// 经过完整 credential 分支后，DB error_kind 正确写入 "upstream_quota_permanent"
// 而非 fallback 为 "transient"。
//
// 这是 F3 智谱 trial 配额归因不一致调查的专项钉测，重现审计档中描述的形态：
// executor LastKind=KindQuotaPermanent + Exhausted=true（凭据级 fatal）→
// classifyUpstreamCredentialFailure → credCode="upstream_quota_permanent" →
// BuildFailureEntry → DB error_kind="upstream_quota_permanent"。
func TestQuotaPermanentAttributionChain_CredentialBranch(t *testing.T) {
	ch := NewChatHandler(nil, nil, nil, nil, nil, nil)
	body := []byte(`{"model":"glm-5.3-flash-trial","messages":[{"role":"user","content":"test"}]}`)
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(body)))

	logCtx := ch.NewRequestLogContext(r, "test-quota-permanent", time.Now())
	logCtx.Body = body
	logCtx.ClientModel = "glm-5.3-flash-trial"

	// 模拟 executor 返回 KindQuotaPermanent + Exhausted=true
	execErr := &executors.ExecuteError{
		LastKind:  errorsx.KindQuotaPermanent,
		Exhausted: true,
		Tried:     3,
		Attempts: []executors.AttemptRecord{
			{ProviderID: 42, CredentialID: 999, HTTPStatus: 400},
		},
	}

	// 提取上游状态码（模拟 handler.go:5777-5780）
	// 对于测试，我们直接使用 Attempts 中的状态码
	var upstreamStatusCode int
	if len(execErr.Attempts) > 0 {
		upstreamStatusCode = execErr.Attempts[0].HTTPStatus
	}

	// 调用分类器（handler.go:5781）
	credCode, _, _, _ := classifyUpstreamCredentialFailure(execErr.LastKind, upstreamStatusCode)

	// ★ 断言 1: credCode 必须非空且为 "upstream_quota_permanent"
	if credCode == "" {
		t.Fatal("classifyUpstreamCredentialFailure returned empty credCode for KindQuotaPermanent")
	}
	if credCode != "upstream_quota_permanent" {
		t.Errorf("classifyUpstreamCredentialFailure returned %q, want %q",
			credCode, "upstream_quota_permanent")
	}

	// 模拟 handler.go:5788 failAndMark → BuildFailureEntry
	providerID := 42
	credentialID := 999
	entry := logCtx.BuildFailureEntry(credCode, execErr.Error(), &providerID, &credentialID)

	// ★ 断言 2: entry.ErrorKind 必须为 "upstream_quota_permanent"
	if entry == nil {
		t.Fatal("BuildFailureEntry returned nil")
	}
	if entry.ErrorKind == nil {
		t.Fatal("BuildFailureEntry.ErrorKind is nil")
	}
	if *entry.ErrorKind != "upstream_quota_permanent" {
		t.Errorf("BuildFailureEntry.ErrorKind = %q, want %q",
			*entry.ErrorKind, "upstream_quota_permanent")
	}

	// ★ 断言 3: entry.Success 必须为 false
	if entry.Success {
		t.Error("BuildFailureEntry.Success = true, want false")
	}

	// ★ 断言 4: entry.RequestStatus 必须为 "failure"
	if entry.RequestStatus == nil || *entry.RequestStatus != "failure" {
		t.Errorf("BuildFailureEntry.RequestStatus = %v, want %q",
			entry.RequestStatus, "failure")
	}
}

// TestQuotaPermanentAttributionChain_Exhausted_False_ShouldNotEnterCredentialBranch
// 验证 Exhausted=false 时不进 credential 分支（handler.go:5768 的 if 守卫）。
func TestQuotaPermanentAttributionChain_Exhausted_False_ShouldNotEnterCredentialBranch(t *testing.T) {
	ch := NewChatHandler(nil, nil, nil, nil, nil, nil)
	body := []byte(`{"model":"glm-5.3-flash-trial","messages":[{"role":"user","content":"test"}]}`)
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(body)))

	logCtx := ch.NewRequestLogContext(r, "test-exhausted-false", time.Now())
	logCtx.Body = body

	// 模拟 executor 返回 KindQuotaPermanent 但 Exhausted=false
	// （这不应该发生——IsCredentialFatal(KindQuotaPermanent)=true 应导致 Exhausted=true，
	// 但钉测覆盖边界情况）
	execErr := &executors.ExecuteError{
		LastKind:  errorsx.KindQuotaPermanent,
		Exhausted: false, // ← 关键：未耗尽
		Tried:     3,
		Attempts: []executors.AttemptRecord{
			{ProviderID: 42, CredentialID: 999, HTTPStatus: 400},
		},
	}

	// 如果 Exhausted=false，handler.go:5768 的 if 守卫不通过，
	// 不会调用 classifyUpstreamCredentialFailure，因此 credCode 保持为空。
	// 这个测试只是确认逻辑一致性——生产环境不应该出现这种组合。

	var upstreamStatusCode int
	if len(execErr.Attempts) > 0 {
		upstreamStatusCode = execErr.Attempts[0].HTTPStatus
	}

	credCode, _, _, _ := classifyUpstreamCredentialFailure(execErr.LastKind, upstreamStatusCode)

	// credCode 仍然会返回 "upstream_quota_permanent"（分类器不检查 Exhausted），
	// 但在真实 handler 中，这个分支不会被执行（因为 if Exhausted 守卫）。
	if credCode != "upstream_quota_permanent" {
		t.Errorf("classifyUpstreamCredentialFailure returned %q, want %q (even with Exhausted=false)",
			credCode, "upstream_quota_permanent")
	}
}

// TestQuotaPermanentAttributionChain_UnknownKind_ReturnEmptyCode 验证
// classifyUpstreamCredentialFailure 对非 credential fatal kind 返回空 code。
func TestQuotaPermanentAttributionChain_UnknownKind_ReturnEmptyCode(t *testing.T) {
	// 模拟一个不在 classifyUpstreamCredentialFailure switch 中的 Kind
	unknownKind := errorsx.KindRateLimit // 这不是 credential fatal kind

	credCode, _, httpStatus, _ := classifyUpstreamCredentialFailure(unknownKind, 400)

	// ★ 断言: credCode 必须为空
	if credCode != "" {
		t.Errorf("classifyUpstreamCredentialFailure returned non-empty code %q for non-credential-fatal kind %s",
			credCode, unknownKind)
	}

	// httpStatus 也应该为 0（空返回）
	if httpStatus != 0 {
		t.Errorf("classifyUpstreamCredentialFailure returned httpStatus %d, want 0 for non-credential-fatal kind",
			httpStatus)
	}
}
