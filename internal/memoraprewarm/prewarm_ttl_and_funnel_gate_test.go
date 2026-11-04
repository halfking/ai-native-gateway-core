package memoraprewarm

// prewarm_ttl_and_funnel_gate_test.go —— ★ 「旁路写的 TTL」与「五态漏斗闭合」两道门。
//
// ★ 2026-11-04 12h 审计轮（B-2 / B-3）落地时的配套门。
//
// # B-2：旁路写必须用**会话 hash 自己的 TTL**
//
//	旁路写打的是**同一个 key**（session:{id}）⇒ Redis 的 EXPIRE 是 **key 级**的
//	⇒ 旁路写一次 EXPIRE，就把整个会话 hash 的 TTL 覆盖掉了。
//	⇒ 而会话 hash 的 TTL 由 session.Manager 用 `cfg.SessionTTLHours`
//	（默认 **72h**）续期 ⇒ 两者不一致时，**后写的那个赢**
//	⇒ ⇒ 会话状态（标题/标签/注解/设备/api-key 绑定…）被静默截短。
//
//	⇒ 修法：装配层把**同一个** TTL 传进 Wiring ⇒ 两边写同一个值
//	⇒ ⇒ 「谁先谁后」不再重要 ⇒ 竞态**自动消失**。
//
//	★ 本门守两件事：
//	① 旁路写用的确实是**传进来的**那个 TTL（不是包内兜底常量）；
//	② 生产装配点（cmd/gateway/main.go）**确实**把会话 TTL 传了进去
//	   ——「加了参数但忘了传」是本仓最经典的腐化形态（AGENTS.md 门脚本接线门同族）。
//
// # B-3：请求构建失败也必须记终态
//
//	`Trigger` 先记「requested」，再调 `Prewarm`；若 `Prewarm` 在
//	**构造 HTTP 请求**这一步就失败（`AI_SESSION_MANAGER_URL` 含
//	`url.Parse` 拒绝的字符：空格 / 控制字符 / 非法端口）⇒
//	原先直接返回、**不记任何终态** ⇒ ⇒ 漏斗不闭合（requested 永远大于
//	终态之和）⇒ 且**没有日志** ⇒ 运维只看到一条单调上涨的 requested。
//
//	★ 判据口径：**requested == hit + miss + timeout + unavailable**
//	  （本门只比较「本次调用前后」的增量 ⇒ 不受其它并发用例干扰）。

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// ttlCapturingWriter 是 fakeWriter 的**带 TTL 记录**版本。
type ttlCapturingWriter struct {
	fakeWriter
	lastTTL atomic.Int64
}

func (f *ttlCapturingWriter) WriteMemoraContext(ctx context.Context, sessionID string, payload []byte, ttl time.Duration) error {
	f.lastTTL.Store(int64(ttl))
	return f.fakeWriter.WriteMemoraContext(ctx, sessionID, payload, ttl)
}

// ① 旁路写用的是**传进来的** TTL。
func TestSideWriteUsesInjectedSessionTTL(t *testing.T) {
	const injected = 72 * time.Hour
	c := &fakeClient{ok: true, payload: []byte(`{"items":[{"content":"x"}]}`)}
	w := &ttlCapturingWriter{}
	wr := NewWiring(c, w, newFakeReader(), NewGate(), nil, injected)
	if wr.ttl != injected {
		t.Fatalf("Wiring 持有的 TTL = %v，期望装配层传入的 %v", wr.ttl, injected)
	}

	// 直接走 Trigger（Apply 的门关闭时是透传；这里要的是写侧参数）。
	Trigger(c, w, nil, nil, "t", "sess-ttl-injected", "prompt", wr.ttl)
	waitFor(t, func() bool { return w.calls.Load() > 0 })

	if got := time.Duration(w.lastTTL.Load()); got != injected {
		t.Fatalf("旁路写使用的 TTL = %v，期望装配层传入的 %v", got, injected)
	}
}

// ①' 传 0 ⇒ 落到包内兜底（且**不会**是 24h 那个会截短会话的值）。
func TestSideWriteFallsBackWhenTTLNotProvided(t *testing.T) {
	c := &fakeClient{ok: true, payload: []byte(`{"items":[{"content":"x"}]}`)}
	w := &ttlCapturingWriter{}
	wr := NewWiring(c, w, newFakeReader(), NewGate(), nil, 0)
	if wr.ttl != DefaultSessionTTL {
		t.Fatalf("TTL<=0 时应回落到 DefaultSessionTTL，got %v want %v", wr.ttl, DefaultSessionTTL)
	}
	// ★ 棘轮：兜底值**不得**短于 config 的会话 TTL 默认值（72h）。
	//   24h 那个旧值会截短会话保留期（B-2 的根因）⇒ 不得再回来。
	if DefaultSessionTTL < 72*time.Hour {
		t.Fatalf("DefaultSessionTTL = %v，短于 config 默认的会话 TTL 72h ⇒ 会截短会话保留期", DefaultSessionTTL)
	}
}

// ② 生产装配点确实把会话 TTL 传进了 NewWiring。
func TestProductionWiringPassesSessionTTL(t *testing.T) {
	b, err := os.ReadFile("../../cmd/gateway/main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	src := string(b)
	idx := strings.Index(src, "memoraprewarm.NewWiring(")
	if idx < 0 {
		t.Fatalf("main.go 里找不到 memoraprewarm.NewWiring( ⇒ 装配点被改名/删除 ⇒ 本门失效")
	}
	// 取调用表达式：从 NewWiring( 到配平的右括号。
	depth := 0
	end := -1
	for i := idx + len("memoraprewarm.NewWiring(") - 1; i < len(src); i++ {
		switch src[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		t.Fatalf("NewWiring( 的括号不配平 ⇒ 本门失效")
	}
	call := src[idx : end+1]
	if !strings.Contains(call, "sessionTTL") {
		t.Errorf("main.go 的 NewWiring 调用**没有**传会话 TTL：\n%s\n"+
			"★ 旁路写的 EXPIRE 会用包内兜底值 ⇒ 与 session.Manager 的 TTL 不一致\n"+
			"  ⇒ 会话 hash 的保留期会被后写的那次 EXPIRE 截短（B-2）。",
			call)
	}
	// 顺带钉住：会话 TTL 在 main() 里**只算一次**（两个消费点共用同一个变量）。
	if n := strings.Count(src, "time.Duration(cfg.SessionTTLHours) * time.Hour"); n != 1 {
		t.Errorf("main.go 里 `time.Duration(cfg.SessionTTLHours) * time.Hour` 出现 %d 次，期望 1 次\n"+
			"★ 会话 TTL 必须**只算一次**、两个消费点（session.Manager / memoraprewarm）共用同一个值，\n"+
			"  否则两处各算一遍又可能分叉 ⇒ 同一个键上的两次 EXPIRE 又变成竞态。", n)
	}
	// 且它必须落在 sessionTTL 变量上（不是散落的内联表达式）。
	re := regexp.MustCompile(`sessionTTL\s*:=\s*time\.Duration\(cfg\.SessionTTLHours\) \* time\.Hour`)
	if !re.MatchString(src) {
		t.Errorf("main.go 里找不到 `sessionTTL := time.Duration(cfg.SessionTTLHours) * time.Hour`\n"+
			"★ 会话 TTL 必须有一个**具名**的真值变量，两个消费点都从它取。")
	}
}

// B-3：构造请求失败 ⇒ 必须记终态（漏斗闭合）。
func TestPrewarmBuildRequestFailureClosesFunnel(t *testing.T) {
	// ★ 前置条件自检：这条 URL 必须**真的**被 url.Parse 拒绝，
	//   否则本测试会因「连接失败」而记 unavailable ⇒ **因错误的原因而绿**。
	badURL := "http://asm:8782 bad host/api/v1/memory-recall/prewarm"
	if _, err := http.NewRequestWithContext(context.Background(), http.MethodPost, badURL, nil); err == nil {
		t.Fatalf("前置条件不成立：%q 竟被 http.NewRequestWithContext 接受 ⇒ "+
			"本测试无法触达「构造请求失败」那条分支（否则会因错误的原因而绿）", badURL)
	}

	beforeReq := testutil.ToFloat64(prewarmTotal.WithLabelValues(OutcomeRequested))
	beforeUnavail := testutil.ToFloat64(prewarmTotal.WithLabelValues(OutcomeUnavailable))

	// 含 url.Parse 会拒绝的字符（空格）⇒ http.NewRequestWithContext 失败。
	c := &PrewarmHTTPClient{
		baseURL:    "http://asm:8782 bad host",
		token:      "t",
		httpClient: &http.Client{Timeout: DefaultHTTPTimeout},
		logger:     slog.Default(),
	}
	// 走完整的 Trigger 路径 ⇒ requested 被记下 ⇒ 必须同时出现一个终态。
	Trigger(c, &fakeWriter{}, nil, nil, "t", "sess-bad-url", "prompt", time.Hour)

	afterReq := testutil.ToFloat64(prewarmTotal.WithLabelValues(OutcomeRequested))
	if afterReq <= beforeReq {
		t.Fatalf("本次调用应记 requested，%v -> %v", beforeReq, afterReq)
	}
	// ★ Trigger 是**异步**的 ⇒ 终态要轮询等它落地（与既有指标门同口径）。
	waitFor(t, func() bool {
		return testutil.ToFloat64(prewarmTotal.WithLabelValues(OutcomeUnavailable)) > beforeUnavail
	})
}
