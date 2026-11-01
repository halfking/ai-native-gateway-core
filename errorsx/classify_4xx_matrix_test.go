package errorsx

import (
	"net/http"
	"testing"
)

// 本文件是 2026-11-02 12h 审计轮 R1 的落地门：**4xx 状态兜底的完整矩阵**。
//
// ── 为什么要有这道门 ──────────────────────────────────────────────────
//
// 2026-11-02 生产事故的放大器段：上游回了一个 4xx，正文不匹配仓内任何
// 形态正则 ⇒ 落 `ClassifyResponseStatus` 的 default ⇒ `KindTransient`
// ⇒ 盲重试 3 次 + 把 (凭据,模型) 按 "http_5xx" 拉黑 5 分钟 + 3 连败后
// 把**整个凭据**置 cooling ⇒ 一个客户端的错单让 `/v1/embeddings` 对
// 所有租户 503。
//
// 那一轮的修复把 400/413/422 收进了 `KindRequestRejected`。但**状态兜底
// 本身**（`ClassifyErrorWithBody` 末尾的 `ClassifyResponseStatus`）是
// 一张**默认返回 KindTransient** 的表 ⇒ 任何**没有**被显式登记的 4xx
// 都会静默落进同一条放大器链。2026-11-02 12h 审计轮用实测矩阵证明了
// 这不是假设：
//
//	404 / 407 / 410 / 414 / 419 / 420 / 427 / 430 / 432..499
//	  → KindTransient，且 IsRetryable=true
//
// ⇒ 「修了主路径、漏了旁路」（LESSONS §606）：只修 400/413/422 而不给
// 兜底加门，下一个没被登记的 4xx 会把同一条链原样带回来。
//
// ★ 本轮据此改了**两格**（均在本文件落地前先取证据）：
//   · **414** URI Too Long —— 纯粹是「这次请求太长」，与节点健康无关；
//   · **404** 裸 404 —— 依据 211 生产证据（45 天窗口 20 行 / 6 对 /
//     7 事件；attempt_index 1→2→3 且相隔 6s/11s ⇒ **同节点盲重试坐实**；
//     三种正文形态全部是确定性、绑定级失败，没有一个是节点临时抖动）
//     ⇒ 「二义性在于归到哪个绑定级桶，不在于要不要按 transient 处理」。
//
// ★ 这道门的作用不是「把所有 4xx 都改成请求侧」，而是**把每一格的裁决
//   变成显式、可复核、可执行的事实**：
//   · 每个 4xx 状态码都必须有一行裁决；
//   · 落 `KindTransient` 的必须**逐条登记**在白名单里并写明理由；
//   · 改动白名单必须同步改理由（否则门会红）——
//     ⇒ 「意外落进放大器链」从此变成「必须显式承认的决定」。
//
// ── 量具口径（先对量具，再读数）──────────────────────────────────────
//
// 用**空正文**分类：`ClassifyErrorWithBody` 的正文形态正则整段被
// `if len(body) > 0` 跳过 ⇒ 本门测的是**纯状态码裁决**，与正文无关。
// （用带正文的载荷会量到正文正则 ⇒ 那条链由
// `TestClassifyErrorWithBody_RequestRejectedDoesNotStealVerdicts` 负责。）

// wantByStatus：状态码 → 期望的 ErrorKind。
//
// 只登记「**被显式裁决**」的状态码——即分类层对它有一个专门的名字
// （而不是落到状态兜底）。
var wantByStatus = map[int]ErrorKind{
	400: KindRequestRejected, // 语义错误：请求侧
	401: KindAuth,            // 鉴权：凭据级
	402: KindQuota,           // 余额/配额：凭据级
	403: KindAuth,            // 鉴权：凭据级
	404: KindRequestRejected, // 裸 404：请求侧（2026-11-02 R1'，据生产证据）
	408: KindTimeout,         // 上游超时
	413: KindRequestRejected, // 载荷过大：请求侧
	414: KindRequestRejected, // URI 过长：请求侧（2026-11-02 R1）
	422: KindRequestRejected, // 不可处理实体：请求侧
	429: KindRateLimit,       // 限流：节点级
}

// transientAllowlist：**显式承认**落状态兜底（KindTransient）的 4xx。
//
// 每个条目必须写明「为什么它不归请求侧」。理由为空 ⇒ 门红。
var transientAllowlist = map[int]string{
	407: "Proxy Authentication Required：代理侧凭据问题 ⇒ 节点/代理级，" +
		"不是「这条请求不收」⇒ 保持 transient（抑制该对 5 分钟）。" +
		"★ 无生产证据（211 的 45 天窗口里该码 0 行）⇒ 待裁决。",
	410: "2026-08-05 既有决议：裸 410 = 资源被永久移除 ⇒ 走 transient 以便" +
		"换兄弟凭据（早前映射成 KindUnsupportedFeature 是终态，导致" +
		"死模型被连打 22 次）。★ 正文命中 EOL 时走 KindModelDeprecated。" +
		"★ 订正（2026-11-02 R1'）：该决议的**目的**（跨凭据故障转移）在" +
		"KindRequestRejected 下同样成立（ActionSwitchNode）⇒ 「为了能换" +
		"兄弟凭据」这个理由**并不**支持保留 transient。把一个**有日期决议**" +
		"的状态码改口径需要它自己的生产证据（404 有：20 行/6 对/7 事件/" +
		"attempt_index 1→2→3；410 没有）⇒ 本轮保留现状，登记为下一轮项。",
	419: "Page Expired：非标准码，LLM 上游极少发出；仓内无正文规则。" +
		"★ 无生产证据（该码 0 行）⇒ 待裁决：若将来确认某供应商用它表达" +
		"限流，应先加正文规则再改这里。",
	420: "Enhance Your Calm（Twitter 语义）：非标准码，语义上是限流。" +
		"★ 待裁决：仓内对限流**只认状态码 429**、没有任何正文规则 ⇒ " +
		"这里不能直接改成请求侧（会丢掉限流语义），也不宜改成限流" +
		"（等于用状态码引入新的罚节点口径）⇒ 保持现状并登记。",
	427: "Locked：非标准码，LLM 上游极少发出；仓内无正文规则。★ 待裁决。",
	430: "非标准码（Varnish 等）：仓内无正文规则。★ 待裁决。",
}

// transientAllowlistRanges：**整段**落状态兜底的 4xx 区段。
//
// 区段也要写理由——理由为空 ⇒ 门红。逐个列 68 个状态码既噪音又易漏，
// 区段声明把「这一整段是同一个决定」这件事显式化。
var transientAllowlistRanges = []struct {
	from, to int
	reason   string
}{
	{432, 499, "432..499 是 IANA 未分配 + 各代理自造的非标准 4xx 区段" +
		"（nginx 499=客户端提前断开、Varnish 4xx 家族、各云厂商自造码等）。" +
		"LLM 上游几乎不会发这些码，仓内既无状态规则也无正文规则。" +
		"★ 保持 transient（落兜底）而不是归请求侧的理由：这些码的语义" +
		"**未知**——「未知」既不能证明是节点故障，也不能证明是请求问题；" +
		"而把未知码归请求侧会让真正的节点级故障（如上游网关配错路径后" +
		"回自定义码）失去 5 分钟抑制。★ 待裁决：若将来收到某个具体码的" +
		"实采正文，应把它移出本区段并单独登记。"},
}

// protocol4xxStatuses：仓内显式映射为「协议/能力族」的 4xx。
//
// 这一族本来就在请求侧（KindUnsupportedFeature ⊂ IsClientBug ⊂
// IsRequestSideError）⇒ 不罚节点、不盲重试；但它是**终态**语义
// （terminalActionKind），与请求侧的「换兄弟候选」不同 ⇒ 单独登记，
// 避免以后有人把它归并进请求侧而悄悄丢掉终态语义。
var protocol4xxStatuses = []int{
	405, 406, 409, 411, 412, 415, 416, 417, 418,
	421, 423, 424, 425, 426, 428, 431,
}

// TestClassifyStatusMatrix4xxIsFullyDecided 是本门的主体。
//
// 判据：400..499 的**每一格**都必须能被本文件解释——
//   · 在 wantByStatus 里 ⇒ 必须等于登记值；
//   · 在 protocol4xxStatuses 里 ⇒ 必须等于 KindUnsupportedFeature；
//   · 在 transientAllowlist 里 ⇒ 必须等于 KindTransient 且理由非空；
//   · 都不在 ⇒ **门红**（这一格是漏网的兜底 ⇒ 它落进放大器链）。
func TestClassifyStatusMatrix4xxIsFullyDecided(t *testing.T) {
	for status := 400; status <= 499; status++ {
		got := ClassifyErrorWithBody(status, nil)

		if want, ok := wantByStatus[status]; ok {
			if got != want {
				t.Errorf("4xx 状态码 %d：ClassifyErrorWithBody(%d, nil) = %q，want %q",
					status, status, got, want)
			}
			continue
		}

		if containsInt(protocol4xxStatuses, status) {
			if got != KindUnsupportedFeature {
				t.Errorf("4xx 状态码 %d：ClassifyErrorWithBody(%d, nil) = %q，want %q（协议/能力族）",
					status, status, got, KindUnsupportedFeature)
			}
			continue
		}

		if reason, ok := transientAllowlist[status]; ok {
			if got != KindTransient {
				t.Errorf("4xx 状态码 %d：白名单登记为 transient，实际却是 %q", status, got)
			}
			if reason == "" {
				t.Errorf("4xx 状态码 %d：落在状态兜底但白名单理由为空 ⇒ 必须写明为什么它不归请求侧", status)
			}
			continue
		}

		if r, ok := transientRangeFor(status); ok {
			if got != KindTransient {
				t.Errorf("4xx 状态码 %d：区段登记为 transient，实际却是 %q", status, got)
			}
			if r == "" {
				t.Errorf("4xx 状态码 %d：落在状态兜底但区段理由为空 ⇒ 必须写明为什么它不归请求侧", status)
			}
			continue
		}

		t.Errorf("4xx 状态码 %d：ClassifyErrorWithBody(%d, nil) = %q —— "+
			"这一格既不在 wantByStatus、也不在 protocol4xxStatuses、也不在 transientAllowlist "+
			"⇒ 它是**漏网的兜底** ⇒ 会静默落进 2026-11-02 事故的放大器链"+
			"（盲重试 + 5 分钟拉黑 + 3 连败整凭据 cooling）。"+
			"请把它登记进三张表之一；若确实该归请求侧，改 errorsx/classify.go 的状态兜底。",
			status, status, got)
	}
}

// TestClassifyStatusMatrix4xxNoStaleAllowlistEntries 钉住白名单不腐化：
// 白名单里登记的状态码若已不再落状态兜底（例如后来被显式裁决了），
// 必须**同步删掉条目**，否则理由会与实际行为脱节 ⇒ 门红。
func TestClassifyStatusMatrix4xxNoStaleAllowlistEntries(t *testing.T) {
	for status := range transientAllowlist {
		got := ClassifyErrorWithBody(status, nil)
		if got != KindTransient {
			t.Errorf("白名单里的 4xx %d 现在分类为 %q（不再是 transient）⇒ "+
				"请从 transientAllowlist 删掉它并把理由移到 wantByStatus/protocol4xxStatuses",
				status, got)
		}
	}
	for _, rg := range transientAllowlistRanges {
		for status := rg.from; status <= rg.to; status++ {
			got := ClassifyErrorWithBody(status, nil)
			if got != KindTransient {
				t.Errorf("区段 %d..%d 里的 4xx %d 现在分类为 %q（不再是 transient）⇒ "+
					"请从 transientAllowlistRanges 删掉它并把理由移到 wantByStatus/protocol4xxStatuses",
					rg.from, rg.to, status, got)
			}
		}
	}
}

// TestClassifyStatusMatrix4xxRequestSideIsNotRetryable 钉住请求侧族的
// **放大器三件套**：请求侧错误不得
//   ① IsRetryable（⇒ 同节点盲重试）；
//   ② 落 transientSuppressErrorCode 的抑制码（⇒ (凭据,模型) 被拉黑 5 分钟）；
//   ③ 落 PenaltyOf 的绑定级罚（⇒ 3 连败后整凭据 cooling）。
//
// 这三条是 2026-11-02 事故的完整放大链；任何一条被打开，事故就会回来。
func TestClassifyStatusMatrix4xxRequestSideIsNotRetryable(t *testing.T) {
	for status, kind := range wantByStatus {
		if !IsRequestSideError(kind) {
			// 只有请求侧族才在这道门里被断言；凭据级/节点级裁决不在此列。
			continue
		}
		if IsRetryable(kind) {
			t.Errorf("4xx %d（%q）：IsRetryable = true ⇒ 同节点盲重试会被重新打开", status, kind)
		}
		if kind == KindTransient {
			t.Errorf("4xx %d：请求侧族不得落 transient", status)
		}
	}
}

// TestClassifyErrorRespBranchMatchesBodyAwareClassifier 钉 R2：
//
// `ClassifyError(err, resp)` 的 resp 分支与 `ClassifyErrorWithBody` 的
// 状态兜底**必须是同一张表**——否则同一个状态码在两条分类路径上分叉。
//
// 2026-11-02 之前：resp 分支直调 `ClassifyResponseStatus` ⇒ 400 给
// transient；而带正文的路径给 request_rejected ⇒ 分叉。
// ⇒ 本门对 400..499 逐格断言两者一致。
func TestClassifyErrorRespBranchMatchesBodyAwareClassifier(t *testing.T) {
	for status := 400; status <= 499; status++ {
		got := ClassifyError(nil, &http.Response{StatusCode: status})
		want := ClassifyErrorWithBody(status, nil)
		if got != want {
			t.Errorf("4xx %d：ClassifyError(resp) = %q 而 ClassifyErrorWithBody(status, nil) = %q "+
				"⇒ 两个分类器对同一状态码分叉了", status, got, want)
		}
	}
}

// TestClassifyErrorRespBranchKeepsNon4xxVerdicts 钉 R2 的**另一侧**：
// 改委托不能动 5xx/429/401 等既有口径。
func TestClassifyErrorRespBranchKeepsNon4xxVerdicts(t *testing.T) {
	cases := []struct {
		status int
		want   ErrorKind
	}{
		{500, KindUpstreamDown},
		{502, KindUpstreamDown},
		{503, KindConcurrent},
		{529, KindConcurrent},
		{429, KindRateLimit},
		{401, KindAuth},
		{403, KindAuth},
		{402, KindQuota},
		{408, KindTimeout},
	}
	for _, tc := range cases {
		got := ClassifyError(nil, &http.Response{StatusCode: tc.status})
		if got != tc.want {
			t.Errorf("ClassifyError(resp %d) = %q，want %q", tc.status, got, tc.want)
		}
	}
}

// TestClassifyStatusMatrix4xxAllowlistIsDocumented 钉白名单的**可读性**：
// 每条理由必须够长（能说清为什么不归请求侧），避免用「待裁决」三个字
// 把决定糊弄过去。
func TestClassifyStatusMatrix4xxAllowlistIsDocumented(t *testing.T) {
	for status, reason := range transientAllowlist {
		if len(reason) < 40 {
			t.Errorf("4xx %d 的白名单理由太短（%d 字）：%q", status, len(reason), reason)
		}
	}
	for _, rg := range transientAllowlistRanges {
		if len(rg.reason) < 40 {
			t.Errorf("4xx 区段 %d..%d 的理由太短（%d 字）", rg.from, rg.to, len(rg.reason))
		}
	}
}

// TestClassifyStatusMatrix4xxAllowlistHasNoRequestSideKind 钉白名单自身
// 的语义：落 transient 的 4xx 不得被判成请求侧（否则就是登记自相矛盾）。
func TestClassifyStatusMatrix4xxAllowlistHasNoRequestSideKind(t *testing.T) {
	for status := range transientAllowlist {
		kind := ClassifyErrorWithBody(status, nil)
		if IsRequestSideError(kind) {
			t.Errorf("4xx %d 在 transientAllowlist 里（= 显式承认为节点侧兜底），"+
				"但分类结果是 %q（请求侧）⇒ 白名单与实际行为矛盾", status, kind)
		}
	}
}

// ── 小工具 ────────────────────────────────────────────────────────────

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// transientRangeFor 返回覆盖该状态码的区段登记（若有）。
func transientRangeFor(status int) (string, bool) {
	for _, rg := range transientAllowlistRanges {
		if status >= rg.from && status <= rg.to {
			return rg.reason, true
		}
	}
	return "", false
}
