package streaming

// 流式 **flush 路径**的证据传播判据（2026-10-31 拦截归因轮）。
//
// ## 这组用例钉的是什么
//
// 211 生产实测：24 条真实拦截的审核单 **rule_id / reason / 证据全空**。
//
// ★ 根因之一：**flush 路径丢证据** ★ —— 流式响应结束时（上游没有终帧），
// 守卫在 `FlushStreamPending` 里做 lane合并检查；★ lane 级拦截（PolicyBlockError）
// ★ **自带 Evidence**（被拦的那段参数）★★ 但写者只取了 rule/reason/field/digest
// ★ ⇒ **证据被丢掉** ⇒ ★★ 审核面看不到「当时拦的是哪段内容」★★
// ⇒ ⇒ 审核员判不了「拦得对不对」。
//
// ⇒ 契约：★ flush 路径的拦截必须把**证据**一起登记到审核单 ★。

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// flushErrorChain 是一个在 flush 阶段返回 PolicyBlockError 的拦截链。
type flushErrorChain struct {
	err error
}

func (c flushErrorChain) InterceptNonStream(context.Context, *response.InterceptRequest) (*response.InterceptResult, error) {
	return nil, nil
}

func (c flushErrorChain) InterceptStreamChunk(context.Context, []byte, *response.StreamMeta) (*response.ChunkResult, error) {
	return nil, nil
}

func (c flushErrorChain) InterceptStreamEnd(context.Context, *response.StreamMeta) (*response.EndResult, error) {
	return nil, nil
}

func (c flushErrorChain) FlushStreamPending(context.Context, *response.StreamMeta) ([]byte, error) {
	return nil, c.err
}

// TestFlushPathBlockReviewCarriesEvidence 钉住：flush 路径的拦截 ⇒ 审核单
// 带规则名、原因、**证据**。
func TestFlushPathBlockReviewCarriesEvidence(t *testing.T) {
	sanitizerules.ResetBlockReviews()
	t.Cleanup(func() { sanitizerules.ResetBlockReviews() })

	const evidence = `{"cmd":"cat /etc/shadow"}`
	rec := httptest.NewRecorder()
	w := &interceptingStreamWriter{
		w:    rec,
		ctx:  context.Background(),
		meta: response.StreamMeta{TenantID: "tenant-ev", ClientType: "zcode", RequestID: "req-ev-1"},
		chain: flushErrorChain{err: &errorsx.PolicyBlockError{
			RuleID:   "builtin.guard.rewrite_refused",
			Reason:   "output policy refused to rewrite a signed or sensitive tool stream lane",
			Path:     "chat.tool.1.arguments",
			Digest:   "digest-1",
			Evidence: evidence,
		}},
	}
	w.finish()

	pending := sanitizerules.ListBlockReviews(sanitizerules.BlockReviewPending, "tenant-ev", 10)
	if len(pending) != 1 {
		t.Fatalf("审核单数 = %d，期望 1", len(pending))
	}
	got := pending[0]
	if got.Rule != "builtin.guard.rewrite_refused" {
		t.Fatalf("规则名 = %q，期望 builtin.guard.rewrite_refused", got.Rule)
	}
	if got.Reason == "" {
		t.Fatal("原因必须登记（审核员要看得出「为什么」）")
	}
	if !strings.Contains(got.Evidence, "cat /etc/shadow") {
		t.Fatalf("证据必须登记（★ flush 路径此前把它丢了 ★）: %q", got.Evidence)
	}
	if got.Digest != "digest-1" {
		t.Fatalf("载荷摘要 = %q，期望 digest-1", got.Digest)
	}
}
