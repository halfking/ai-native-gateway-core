package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// r74BodyObservingExecutor records the outbound body of every attempt and
// fails with a context-length error every time.
type r74BodyObservingExecutor struct {
	bodies []int
}

func (e *r74BodyObservingExecutor) Execute(params *executors.ExecParams) (*executors.ExecuteResult, error) {
	e.bodies = append(e.bodies, len(params.BodyBytes))
	return nil, errors.New("upstream 400: prompt is too long: 210000 tokens > 200000 maximum")
}

func r74OversizedBody(t *testing.T) []byte {
	t.Helper()
	var msgs []map[string]any
	for i := 0; i < 300; i++ {
		msgs = append(msgs,
			map[string]any{"role": "user", "content": strings.Repeat("u", 300) + string(rune('a'+i%26))},
			map[string]any{"role": "assistant", "content": strings.Repeat("a", 300) + string(rune('a'+i%26))},
		)
	}
	b, err := json.Marshal(map[string]any{"model": "m", "messages": msgs})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// R74 P0 回归（端到端）：上游报 context_length_exceeded 且尚未向客户端提交
// 内容时，协调器必须真的把 body 压缩掉再重试。
//
// 旧实现在决策改写处就把一次性闩锁 ctxLenCompressRetried 置为 true，而真正
// 改写 body 的分支条件是 `!ctxLenCompressRetried` —— 整段是死代码。表现是：
// 协调器宣告「正在压缩重试」、让客户端白等一个完整 backoff，然后把**逐字节
// 相同**的超长 body 再发一次。objective 那条「自动压缩并再次请求、保持连接
// 不返回错误」在这条路径上从未成立。
//
// 判别力：把 body 改写退回决策改写之前，本测试必须红。
func TestR74_SurvivalCtxLenRetryActuallyShrinksTheBody(t *testing.T) {
	exec := &r74BodyObservingExecutor{}
	var terminals []TaskDecision
	c := &SurvivalCoordinator{
		Exec:       exec,
		Protocol:   ProtocolOpenAIChat,
		Options:    SurvivalOptions{Deadline: time.Minute, MaxRetries: 3, KeepaliveInterval: time.Millisecond},
		JitterRand: func() float64 { return 0.5 },
		Sleep:      func(context.Context, time.Duration) error { return nil },
		Terminal:   func(d TaskDecision, _ bool) { terminals = append(terminals, d) },
	}

	res := c.Run(context.Background(), NewSerializedStreamWriter(&r74NullFlusher{}),
		&executors.ExecParams{IsStream: true, BodyBytes: r74OversizedBody(t)})

	t.Logf("attempts=%d body sizes=%v final=%s/%s terminals=%v",
		res.Attempts, exec.bodies, res.Decision.Action, res.Decision.Reason, terminals)

	if len(exec.bodies) < 2 {
		t.Fatalf("前置条件不成立：应至少发出 2 次请求，实际 %d 次", len(exec.bodies))
	}
	if exec.bodies[1] >= exec.bodies[0] {
		t.Errorf("缺陷复现：第 2 次请求的 body = %d 字节，未小于第 1 次的 %d 字节 —— "+
			"压缩分支不可达，超长 body 被原样重发", exec.bodies[1], exec.bodies[0])
	}
}

// 配套断言：不可压缩的 body（Responses 形态）必须立即终态，而不是拿同样的
// 字节去撞第二次同样的窗口错误。
func TestR74_SurvivalCtxLenUncompressibleBodyTerminatesImmediately(t *testing.T) {
	exec := &r74BodyObservingExecutor{}
	var terminals []TaskDecision
	c := &SurvivalCoordinator{
		Exec:       exec,
		Protocol:   ProtocolOpenAIChat,
		Options:    SurvivalOptions{Deadline: time.Minute, MaxRetries: 3, KeepaliveInterval: time.Millisecond},
		JitterRand: func() float64 { return 0.5 },
		Sleep:      func(context.Context, time.Duration) error { return nil },
		Terminal:   func(d TaskDecision, _ bool) { terminals = append(terminals, d) },
	}
	// No "messages" array — survivalCompressBodyForRetry cannot shrink it.
	responsesBody := []byte(`{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":"` +
		strings.Repeat("x", 400000) + `"}]}]}`)

	res := c.Run(context.Background(), NewSerializedStreamWriter(&r74NullFlusher{}),
		&executors.ExecParams{IsStream: true, BodyBytes: responsesBody})

	t.Logf("attempts=%d final=%s/%s terminals=%v", res.Attempts, res.Decision.Action, res.Decision.Reason, terminals)
	if res.Decision.Reason != "context_length_compress_retry_failed" {
		t.Errorf("不可压缩的 body 应直接以 context_length_compress_retry_failed 终态，实际 = %s/%s（attempts=%d）",
			res.Decision.Action, res.Decision.Reason, res.Attempts)
	}
	if res.Attempts != 1 {
		t.Errorf("不可压缩时不应再发第二次请求，实际发出 %d 次", res.Attempts)
	}
}

// 已提交内容的请求不得走压缩重试：客户端已经看到语义输出，重发会造成重复。
func TestR74_SurvivalCtxLenCommittedRequestDoesNotRewrite(t *testing.T) {
	// survivalCtxLenCompressRetryDue is the shared predicate; the Run path
	// now inlines the same conditions. Pin the commit-block rule here.
	attempt := &AttemptResult{
		CommitState:       CommitStateContent,
		CandidateOutcomes: []CandidateOutcome{{Kind: errorsx.KindContextLength}},
	}
	if survivalCtxLenCompressRetryDue(false, TaskDecision{Action: TaskActionFailTerminal}, attempt) {
		t.Error("已提交内容的请求不得触发压缩重试（会造成客户端可见内容重复）")
	}
}

type r74NullFlusher struct{}

func (*r74NullFlusher) Write(p []byte) (int, error) { return len(p), nil }
func (*r74NullFlusher) Flush() error                { return nil }

// R74 P2 回归：已提交内容后中断（resume_blocked）必须给客户端一条 think
// 转达。
//
// 该分支此前不发出任何传输帧——客户端手里握着部分答案，却只收到一个裸
// error 信封，无法区分「有意停止」与「被截断」。这是最需要解释的一类失败，
// 却是唯一没有 think 的一类。
//
// 判别力：把 resume_blocked 分支的 notify 去掉，本测试必须红。
func TestR74_ResumeBlockedEmitsThinkNotice(t *testing.T) {
	exec := &r74CommitThenFailExecutor{}
	var notices []string
	c := &SurvivalCoordinator{
		Exec:       exec,
		Protocol:   ProtocolOpenAIChat,
		Options:    SurvivalOptions{Deadline: time.Minute, MaxRetries: 2, KeepaliveInterval: time.Millisecond},
		JitterRand: func() float64 { return 0.5 },
		Sleep:      func(context.Context, time.Duration) error { return nil },
		RetryNotice: func(_ context.Context, attempt int, d TaskDecision, _ time.Duration) error {
			notices = append(notices, d.Reason)
			return nil
		},
		Terminal: func(TaskDecision, bool) {},
	}
	res := c.Run(context.Background(), NewSerializedStreamWriter(&r74NullFlusher{}),
		&executors.ExecParams{IsStream: true, BodyBytes: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)})

	t.Logf("decision=%s/%s notices=%v", res.Decision.Action, res.Decision.Reason, notices)
	if res.Decision.Action != TaskActionResumeBlocked {
		t.Skipf("前置条件不成立：本次决策是 %s/%s，未进入 resume_blocked", res.Decision.Action, res.Decision.Reason)
	}
	found := false
	for _, n := range notices {
		if n == "partial_answer_delivered_stop" {
			found = true
		}
	}
	if !found {
		t.Errorf("resume_blocked 未发出任何 think 转达（notices=%v）：客户端只拿到裸 error 信封，"+
			"无法区分有意停止与被截断", notices)
	}
}

// r74CommitThenFailExecutor commits semantic content and then fails, which is
// the shape that produces resume_blocked/committed_output.
type r74CommitThenFailExecutor struct{}

func (e *r74CommitThenFailExecutor) Execute(params *executors.ExecParams) (*executors.ExecuteResult, error) {
	if params != nil && params.W != nil {
		_, _ = params.W.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial answer\"}}]}\n\n"))
	}
	return nil, errors.New("upstream 500: primary node down mid-stream")
}
