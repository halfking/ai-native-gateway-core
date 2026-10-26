package bg

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/providercap"
)

// 本文件是 2026-10-20 12h 审计轮三条修正的门禁。
//
// 审计对象：origin/main 在 2026-10-19 17:03 → 2026-10-20 的 12 小时窗口内
// 对探针/状态机做的改造（"2xx 不等于健康、必须有正向证据"、"不确定不得定罪也不得恢复"）。
// 审计发现其中三处**行为回退或流程缺口**，本文件逐条钉住修正后的行为。
//
// 判据纪律（AGENTS.md）：
//   - 门必须真的咬得住：每条用例都写明「改回旧写法会红在哪一句」；
//   - 阴性结论不当成通过：下面每条断言都给出具体失败信息；
//   - 门本身不测「跑过了」，测**行为**。

// ─────────────────────────────────────────────────────────────────────────────
// 门 1：/v1/models 的目录体积不再等于「没有证据」
//
// 缺陷：readCredentialProbeBody 对 >64KiB 的正文**返回错误**（而不是截断），
// 而 probeModelsHaveEvidence 要求 json.Valid(完整正文) ⇒ 越线的目录被判成
// probe_response_inconclusive ⇒ 该模型永远到不了 healthy_confirmed，
// 且每轮周期都被重新选中。旧写法是截断后按 2xx 判 ok ⇒ 这是行为回退。
// 修正：目录型端点用 credentialProbeModelsBodyLimit（1MiB）单独放宽。
//
// 变异（改回旧写法会红在哪）：把 probe_http.go 里的
// readCredentialProbeBodyLimit(resp.Body, credentialProbeModelsBodyLimit)
// 改回 readCredentialProbeBody(resp.Body) ⇒
// TestModelsProbeAcceptsCatalogLargerThan64KiB 会以
// "large model catalog was rejected as inconclusive" 失败。
// ─────────────────────────────────────────────────────────────────────────────
func TestModelsProbeAcceptsCatalogLargerThan64KiB(t *testing.T) {
	// 造一个 >64KiB 但 <1MiB 的模型目录。
	var b strings.Builder
	b.WriteString(`{"object":"list","data":[`)
	for i := 0; b.Len() < credentialProbeBodyLimit+4096; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":"model-with-a-fairly-long-identifier-%06d","object":"model","owned_by":"some-provider"}`, i)
	}
	b.WriteString(`]}`)
	body := b.String()
	if len(body) <= credentialProbeBodyLimit {
		t.Fatalf("fixture too small: %d bytes", len(body))
	}
	if len(body) >= credentialProbeModelsBodyLimit {
		t.Fatalf("fixture too large: %d bytes", len(body))
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	srv.Client().Timeout = 10 * time.Second

	oldChat, oldHTTP := probeChatClient, probeHTTPClient
	probeChatClient, probeHTTPClient = srv.Client(), srv.Client()
	defer func() { probeChatClient, probeHTTPClient = oldChat, oldHTTP }()

	r := singleGet(t.Context(), srv.URL, "test-key", providercap.Descriptor{}, 7, "minimax-m3")
	if r.category != probeCategoryOK {
		t.Fatalf("large model catalog was rejected as inconclusive: category=%q status=%q errCode=%q errMsg=%q",
			r.category, r.status, r.errCode, r.errMsg)
	}
}

// 门的另一半：放宽不等于无界。超过 1MiB 的正文仍必须被判为「不确定」，
// 不能变成「读多少都算健康」。
// 变异：把 readCredentialProbeBodyLimit 的上限检查去掉 ⇒ 本用例会红
// （category 变成 ok）。
func TestModelsProbeStillRejectsBodyOverModelsLimit(t *testing.T) {
	oversized := strings.Repeat("x", credentialProbeModelsBodyLimit+1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, oversized)
	}))
	defer srv.Close()
	srv.Client().Timeout = 10 * time.Second

	oldChat, oldHTTP := probeChatClient, probeHTTPClient
	probeChatClient, probeHTTPClient = srv.Client(), srv.Client()
	defer func() { probeChatClient, probeHTTPClient = oldChat, oldHTTP }()

	r := singleGet(t.Context(), srv.URL, "test-key", providercap.Descriptor{}, 7, "minimax-m3")
	if r.category == probeCategoryOK {
		t.Fatalf("oversized body was accepted as healthy evidence: %+v", r)
	}
	if r.errCode != probeResponseInconclusiveErrCode {
		t.Fatalf("oversized body should be inconclusive, got errCode=%q", r.errCode)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 门 2：确定性的 404 不再被当成「不确定的观察」
//
// 缺陷：executor 用 category == probeCategorySkipped 判定「观察」，而
// endpoint_id_required（模型必须配 endpoint id，火山方舟那类）也是这个
// category ⇒ ObservationOnly 分支不写 state manager
// ⇒ 旧行为里的 5 分钟摘流被取消、该 pair 的重试链被终止、
// error_kind 被误报成 probe_direct_inconclusive。
// 本改动之前执行器里**根本没有** ObservationOnly 概念，404 一律按 HTTP
// 状态分类 ⇒ 这是一处行为回退。
// 修正：按 errCode 判定观察，只认 probe_response_inconclusive。
//
// 变异：把 active_probe_executor.go 里的
// `parsed.errCode == probeResponseInconclusiveErrCode` 去掉，退回
// `parsed.category == probeCategorySkipped` ⇒ 本用例会以
// "deterministic 404 was swallowed as an observation" 失败。
// ─────────────────────────────────────────────────────────────────────────────
func TestDeterministicEndpointIDRequired404IsNotObservation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error_code":"InvalidEndpointOrModel.NotFound","message":"endpoint not found"}`)
	}))
	defer srv.Close()
	srv.Client().Timeout = 10 * time.Second

	e := &ActiveProbeExecutor{httpClient: srv.Client()}
	target := &ProbeTarget{CredentialID: 7, RawModel: "minimax-m3", BaseURL: srv.URL, Protocol: "openai-completions"}
	r := e.Run(t.Context(), target)

	if r.ObservationOnly {
		t.Fatalf("deterministic 404 was swallowed as an observation: %+v", r)
	}
	if r.ErrCode != "endpoint_id_required" {
		t.Fatalf("diagnostic errCode was lost (want endpoint_id_required): %+v", r)
	}
	if r.Status == ProbeStatusSkipped {
		t.Fatalf("deterministic 404 should be classified by HTTP status, not skipped: %+v", r)
	}
}

// 对照臂：真正「不确定」的 2xx（拿到响应但拿不出完整证据）**仍然**是观察。
// 没有这条对照，上面的门有可能被「把所有 skipped 都放行」这种改法骗过。
// 变异：把 executor 里 ObservationOnly 永远置 false ⇒ 本用例会红。
func TestInconclusive2xxRemainsObservation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 200 但没有可解析的模型证据 ⇒ probe_response_inconclusive
		_, _ = io.WriteString(w, `{"object":"list"}`)
	}))
	defer srv.Close()
	srv.Client().Timeout = 10 * time.Second

	e := &ActiveProbeExecutor{httpClient: srv.Client()}
	target := &ProbeTarget{CredentialID: 7, RawModel: "minimax-m3", BaseURL: srv.URL, Protocol: "openai-completions"}
	r := e.runModelsList(t.Context(), target)

	if !r.ObservationOnly {
		t.Fatalf("inconclusive 2xx must stay an observation: %+v", r)
	}
	if r.ErrCode != probeResponseInconclusiveErrCode {
		t.Fatalf("inconclusive observation must carry errCode=%q, got %q", probeResponseInconclusiveErrCode, r.ErrCode)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 门 3：provider 错误「不改状态」但「要记账」
//
// 缺陷：12h 窗口前，provider 错误（auth/network/5xx/rate_limit）在
// computeConsensus 里 newState="recovering" + applied=true ⇒ 会经
// writeAvailabilityCache(available=true) 把 broken_confirmed 的模型恢复回路由
// （这是真缺陷，窗口内修对了）。但修法是 applied=false + applyResult 整段
// return ⇒ model_probe_state 这一行（唯一写入口）**完全不被推进**：
//
//	· next_retry_at 永远停在过去值 ⇒ 每轮周期都被重新选中；
//	· last_status / last_attempt_at / total_attempts 对运维永久显示旧值；
//	· 而 model_probe_runs 一直在写 "provider_error"
//	  ⇒ 审计表与状态表口径分裂。
//
// 修正：状态不动的同时，用 advanceProbeBeat 只推进节拍四列。
//
// 判据是静态的（逐字钉住 SQL）——因为 ModelProbeRunner.db 是具体类型
// *pgxpool.Pool，没有真库就测不出 Exec 的实际效果。
// 变异：往 modelProbeBeatSQL 的 DO UPDATE SET 里加
// `state = EXCLUDED.state,` ⇒ 本用例会红
// （"beat upsert must not touch state"）。
// ─────────────────────────────────────────────────────────────────────────────
func TestBeatUpsertTouchesOnlyCadenceColumns(t *testing.T) {
	sql := modelProbeBeatSQL

	// 必须推进的四列
	for _, col := range []string{"total_attempts", "last_attempt_at", "next_retry_at", "last_status"} {
		if !strings.Contains(sql, col) {
			t.Errorf("beat upsert must advance %q", col)
		}
	}
	// 不得触碰的「状态」列
	for _, col := range []string{
		"consecutive_successes", "consecutive_failures",
		"last_state_change_at", "last_state_change_run",
	} {
		// 这些列出现在 INSERT 的列清单里是允许的（给新行填默认值），
		// 但不得出现在 DO UPDATE SET 里。
		if inUpdateSet(sql, col) {
			t.Errorf("beat upsert must not touch %q in DO UPDATE SET", col)
		}
	}
	if inUpdateSet(sql, "state") {
		t.Errorf("beat upsert must not touch state in DO UPDATE SET")
	}
	// 节拍 upsert 不得改绑定可用性（那是 applyResult 的职责）
	if strings.Contains(sql, "credential_model_bindings") || strings.Contains(sql, "available") {
		t.Errorf("beat upsert must not touch binding availability")
	}
}

// inUpdateSet 报告列名是否出现在 ON CONFLICT ... DO UPDATE SET 子句里。
func inUpdateSet(sql, col string) bool {
	idx := strings.Index(sql, "DO UPDATE SET")
	if idx < 0 {
		return false
	}
	tail := sql[idx:]
	// 只看 SET 之后的第一段（到下一个子句或结尾）
	if end := strings.Index(tail, "WHERE"); end > 0 {
		tail = tail[:end]
	}
	// 逐词匹配，避免 "state" 命中 "last_state_change_at"
	for _, tok := range strings.FieldsFunc(tail, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_')
	}) {
		if tok == col {
			return true
		}
	}
	return false
}
