package v2

import (
	"encoding/json"
	"sort"
	"unicode/utf8"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// Turn body bounds (W1.4) — aligned with magpie SaveGatewayTurn:
//   - 1 MiB per turn JSON after scrub
//   - secret scrub before persist
//   - truncation marked via a synthetic system marker when cut

const (
	// MaxTurnBodyBytes is the hard ceiling for one turn's persisted payload
	// budget (split across request/response/outbound directions).
	MaxTurnBodyBytes = 1 << 20 // 1 MiB
)

// truncationMarkerText 是截断标记的正文（★ 落库后读者可见）。
const truncationMarkerText = "[truncated: turn body exceeded 1MiB bound]"

// truncationMarker 返回截断标记消息。
func truncationMarker() Message {
	return Message{Role: "system", Content: truncationMarkerText}
}

// truncationMarkerAllowance 是「截断标记」占用的预算余量。
//
// ★ r1041 轮（2026-10-28）：★ 标记（markTruncation）是在【预算之外】
// ★ 追加到 ResponseDelta 的 ⇒ 若不预留，落库行会比 1 MiB 多出标记体积。
// ★
// ★ ★ **实算而不是拍脑袋**：★ 预留量 = 标记落库后占的字节（含数组方括号）
// ★ ⇒ ★ 标记文案改了额度自动跟着走 ⇒ ★ 不会漂。
// ★ ★ 上限 76 字节（实测：`{"role":"system","content":"[truncated: …]"}` = 75
// ★ ★ 字节 + 追加进数组的逗号 1 字节；★ 空数组时还要算上 2 字节方括号
// ★ ★ ⇒ 实算结果 77 字节）。
//
// ★ ★ 之所以要【精确】：★ 预留量大于实际增量 ⇒ 就会在总账上留下
// ★ ★ 【掩盖记账误差】的余量 ⇒ ★ 记账一旦有偏，测试就看不见 ⇒
// ★ ★ ⇒ 预留量必须【贴着实际增量】（★ 实算 ⇒ 永远贴着）。
var truncationMarkerAllowance = func() int {
	b, err := json.Marshal([]Message{truncationMarker()})
	if err != nil {
		return 128
	}
	return len(b)
}()

// ErrRecordingConsentOff surfaces on read paths that must not silently empty.
var ErrRecordingConsentOff = errRecordingConsentOff{}

type errRecordingConsentOff struct{}

func (errRecordingConsentOff) Error() string {
	return "recording_unavailable: session_recording_consent is off"
}

// allowBodiesWriteHook, when non-nil, replaces the live consent check.
// Production leaves it nil. Tests set it via enableRecordingConsentForTest.
var allowBodiesWriteHook func(tenantID string, capturedGeneration int) bool

// RecordingConsentAllowsWrite is the write-path gate: consent on AND the
// caller's captured generation still matches the live generation (magpie
// disable-and-clear race).
func RecordingConsentAllowsWrite(tenantID string, capturedGeneration int) bool {
	if allowBodiesWriteHook != nil {
		return allowBodiesWriteHook(tenantID, capturedGeneration)
	}
	if !settings.RecordingConsentEnabled(tenantID) {
		return false
	}
	if capturedGeneration <= 0 {
		return true
	}
	return capturedGeneration == settings.RecordingConsentGeneration(tenantID)
}

// CaptureRecordingGeneration snapshots the generation at request start.
func CaptureRecordingGeneration(tenantID string) int {
	return settings.RecordingConsentGeneration(tenantID)
}

// boundBodiesRecord scrubs secrets and enforces the 1 MiB/turn ceiling.
// truncated is set when any direction was cut.
//
// ★ r1041 轮（2026-10-28）：★ **上限口径修正** ——
//
//	旧实现：★ 三个方向【各】拿 MaxTurnBodyBytes/2（512 KiB）
//	         ⇒ 实际总量上限 = 1.5 MiB
//	         ⇒ 与本文件头注释「1 MiB per turn JSON after scrub」
//	         ⇒ 与 MaxTurnBodyBytes 的「hard ceiling for one turn's persisted
//	         ⇒ payload budget」**矛盾** ⇒ ★★ 契约说 1 MiB，实现是 1.5 MiB。
//
// ★★ 修法：★ **1 MiB 是【每轮总量】**，三个方向**共享**这一份预算，
// ★ 按「请求 → 响应 → outbound」的顺序依次分配
// ★ （★ outbound 最后 ⇒ 它是「可从别处重建」的那一份 ⇒ 优先级最低）。
// ⇒ ⇒ ★ 与仓内既有纪律一致：★ **契约与实现必须一致**，
// ★★ 且改的是【实现】（★ 实现才是缺陷的一方 —— 注释与常量都说了 1 MiB）。
//
// ★⚠️ 这是**行为变更**（上限从 1.5 MiB 收到 1 MiB）⇒
// ★⇒ 已落库的历史行**不受影响**（只影响新写入）⇒
// ★⇒ 且截断标记（markTruncation）会把被切的那轮标出来 ⇒ 事后可查。
//
// ★★ 第二处修正（r1041 轮实测发现）：★ **「额度」与「裁剪口径」必须同单位** ——
// ★ 旧实现的裁剪按【原始字节】数（`n := budget - 24`），而额度衡量的是
// ★ 【JSON 编码后】的字节 ⇒ ★ Go 的 JSON 编码把 `<` `>` `&` 转成 \uXXXX
// ★ （1 字节 → 6 字节）⇒ ★ 裁完仍可超额度 6 倍
// ★ ⇒ ⇒ 实测：额度 1 MiB 时落库 1,572,892 字节（**超 50%**）
// ★ ⇒ ⇒ 「上限」根本没有上限 ⇒ ⇒ 一律按【编码后大小】裁（见 fitMessage）。
func boundBodiesRecord(rec *BodiesRecord) (truncated bool) {
	if rec == nil {
		return false
	}
	// ★ 保留原始三段（★ 第二遍要从**未裁剪**的原文重算 ⇒ 否则
	// ★ 会重复脱敏，且第二遍的输入已被第一遍裁过 ⇒ 结果不可复现）。
	origReq, origResp, origOut := rec.RequestDelta, rec.ResponseDelta, rec.OutboundBody

	truncated = boundDirections(rec, MaxTurnBodyBytes)
	if !truncated {
		return false
	}
	// ★★ 会被截断 ⇒ 落库行还要追加截断标记 ⇒ ★ 预留标记额度重算 ⇒
	// ★★ 保证【落库总量】仍 ≤ MaxTurnBodyBytes（★ 契约与实现一致）。
	rec.RequestDelta, rec.ResponseDelta, rec.OutboundBody = origReq, origResp, origOut
	truncated = boundDirections(rec, MaxTurnBodyBytes-truncationMarkerAllowance)
	return truncated
}

// boundDirections 把【一份总预算】按「请求 → 响应 → outbound」依次分配。
//
// ★ 扣减口径：★ 扣的是【这一段实际占用的字节】⇒ 剩余 = 给它的额度 − 它占的。
// ★ ★ 首版写成 `left -= messagesWireSize(out)` ⇒ ★ 把「已产出」当成了「剩余」
// ★ ★ ⇒ 第二段拿到的额度变成负数 ⇒ boundMessages 直接原样返回
// ★ ★ ⇒ 总量反而涨到 3.5 MiB（★ 只在【有截断】的用例里才暴露）。
// ⇒ ⇒ ★★ 教训：★ 「共享预算」必须扣【已用】不是【已产出】。
func boundDirections(rec *BodiesRecord, budget int) (truncated bool) {
	var cut bool

	rec.RequestDelta, cut = boundMessages(rec.RequestDelta, budget)
	truncated = truncated || cut
	budget = remainingBudget(budget, rec.RequestDelta)

	rec.ResponseDelta, cut = boundMessages(rec.ResponseDelta, budget)
	truncated = truncated || cut
	budget = remainingBudget(budget, rec.ResponseDelta)

	rec.OutboundBody, cut = boundMessages(rec.OutboundBody, budget)
	truncated = truncated || cut
	return truncated
}

// remainingBudget 算出「这一段占用之后还剩多少额度」。
//
// ★ budget 是给这一段的额度；msgs 是它产出的内容。
// ★⇒ 剩余 = budget − 实际占用字节数（★ 不会为负 ⇒ 夹到 0）。
func remainingBudget(budget int, msgs []Message) int {
	used := messagesWireSize(msgs)
	if used >= budget {
		return 0
	}
	return budget - used
}

// messagesWireSize 返回一组消息【落库时】占用的字节数。
//
// ★ 口径必须与落库一致：★ 落库是 **JSON 数组**（safeJSONMarshal(rec.XxxDelta)）
// ★ ⇒ 含方括号与元素间逗号 ⇒ ★ 与「逐条 messageWireSize 求和」差 (n+1) 字节。
// ★ ★ 旧实现按逐条求和 ⇒ 与落库口径**不一致** ⇒ 上限会算漏。
func messagesWireSize(msgs []Message) int {
	if len(msgs) == 0 {
		return 0
	}
	if b, err := json.Marshal(msgs); err == nil {
		return len(b)
	}
	// ★ 兜底：★ 逐条估算（★ marshal 失败时至少有个数）
	total := 2
	for i, m := range msgs {
		if i > 0 {
			total++
		}
		total += messageWireSize(m)
	}
	return total
}

// boundMessages 把一段消息裁到【数组形态】不超过 budget 字节。
//
// ★ r1041 轮（2026-10-28）：★ **budget <= 0 不再「原样放行」** ——
// ★ 旧实现是 `if len(msgs) == 0 || budget <= 0 { return msgs, false }`
// ★ ⇒ ★★ **共享预算耗尽时，第二三段被【完整保留】** ⇒
// ★★ 总量不是 1 MiB，而是 1 MiB + 后面两段的全部 ⇒
// ★★ 实测三方向合计 3.67 MiB（★ 远超 1.5 MiB 的旧上限）。
// ★ ⇒ ⇒ ★★ 判据含义：★ 「额度用完」= **这段不能留** ⇒ 必须清掉并标截断。
// ★ ⇒ 旧行为「额度 0 ⇒ 全放行」在【单一方向各自限额】的旧口径下无害，
// ★ ⇒ 在【共享预算】的新口径下是**灾难** ⇒ ★ 这就是口径变更必须改量的地方。
func boundMessages(msgs []Message, budget int) ([]Message, bool) {
	if len(msgs) == 0 {
		return msgs, false
	}
	// ★ 「额度用完」= 这段不能留 ⇒ 清掉并标截断（★ 见上）。
	if budget <= 0 {
		return nil, true
	}
	out := make([]Message, 0, len(msgs))
	// ★ 数组的固定开销：★ 方括号 2 字节（★ 元素间逗号逐条另算）。
	left := budget - 2
	cut := false
	for i, m := range msgs {
		if i > 0 {
			// ★ 元素间逗号
			left--
			if left <= 0 {
				cut = true
				break
			}
		}
		m = scrubMessage(m)
		size := messageWireSize(m)
		if size > left {
			var cut2 bool
			m, cut2 = fitMessage(m, left)
			cut = cut || cut2
			size = messageWireSize(m)
			if size > left {
				// ★ 连「只剩 role 的最小消息」都装不下 ⇒ ★ 整条丢弃
				// ★（★ 留着它就会突破上限 ⇒ 上限就成了空话）。
				cut = true
				break
			}
		}
		left -= size
		out = append(out, m)
	}
	return out, cut
}

func scrubMessage(m Message) Message {
	if m.Content != "" {
		m.Content = string(errorsx.RedactCredentialShapes([]byte(m.Content)))
	}
	if len(m.ContentRaw) > 0 {
		raw := errorsx.RedactCredentialShapes(m.ContentRaw)
		// ★ r1041 轮：★ 脱敏后的 raw **必须仍是合法 JSON** ⇒
		// ★ 否则落库时 safeJSONMarshal 报错 ⇒ ★★ 那一轮**根本写不进去**
		// ★★ （不是写小了，是写失败 ⇒ 整轮正文丢失）。
		// ★ 脱敏替换文本含 `<` `>` ⇒ 在 JSON 字符串内合法；跨结构边界的
		// ★ 匹配才可能破坏结构 ⇒ ★ 用 json.Valid 兜底。
		if json.Valid(raw) {
			m.ContentRaw = raw
		} else {
			m.ContentRaw = nil
		}
	}
	if m.Name != "" {
		m.Name = string(errorsx.RedactCredentialShapes([]byte(m.Name)))
	}
	return m
}

func messageWireSize(m Message) int {
	b, err := json.Marshal(m)
	if err != nil {
		return len(m.Content) + len(m.ContentRaw) + 64
	}
	return len(b)
}

// fitMessage 把一条消息裁到【JSON 编码后】不超过 limit 字节。
//
// ★ r1041 轮（2026-10-28）：★ **这是「1 MiB 上限」能真兑现的关键** ——
//
// ★ 旧实现（truncateMessage）按【原始字节】裁（`n := budget - 24`），
// ★ 而额度衡量的是【JSON 编码后】的字节 ⇒ ★ Go 的 JSON 编码把
// ★ `<` `>` `&` 转成 \uXXXX（1 字节 → 6 字节）⇒ ★ 裁完仍可超额度 6 倍
// ★ ⇒ ⇒ 实测：额度 1 MiB 时落库 1,572,892 字节（**超 50%**）⇒
// ★ ★ 「上限」根本没有上限。
//
// ★★ 旧实现还有第二个缺陷：★ 对 ContentRaw 是**任意字节截断**
// ★ ⇒ 截在结构中间会产出**非法 JSON** ⇒ 落库直接报错
// ★ ⇒ ★★ 那一轮**根本写不进去**。
//
// ⇒ ⇒ 修法：★ 一律按【编码后大小】裁；★ ContentRaw 走
// ★ 「解码 → 收缩结构 → 重新编码」⇒ ★ 产出恒为**合法 JSON**。
//
// ⇒ 裁剪阶梯（★ 从「最舍不得丢」到「最舍得丢」）：
// ① ContentRaw：解码后收缩（字符串截断 / 数组丢尾元素 / 对象丢尾键）
// ② Content（纯文本）：按编码后大小二分前缀
// ③ ToolCalls / Name / ToolCallID：结构开销，依次丢弃
func fitMessage(m Message, limit int) (Message, bool) {
	if messageWireSize(m) <= limit {
		return m, false
	}
	cut := false

	// ① ContentRaw —— 解码 → 收缩 → 重新编码（★ 恒产出合法 JSON）。
	if len(m.ContentRaw) > 0 {
		if shrunk, ok := shrinkRawToFit(m.ContentRaw, limit, m); ok {
			m.ContentRaw = shrunk
		} else {
			// ★ 收缩不动（形状不可收缩 / 原始值本就不是合法 JSON）
			// ★ ⇒ ★ 丢弃正文，保住 JSON 合法性与 role / tool_calls 等结构。
			m.ContentRaw = nil
		}
		m.RawContent = nil
		cut = true
		if messageWireSize(m) <= limit {
			return m, cut
		}
	}

	// ② Content（纯文本）—— 按【编码后】大小二分前缀。
	if m.Content != "" {
		m.Content = fitTextToLimit(m.Content, m, limit)
		m.ContentRaw = nil
		m.RawContent = nil
		cut = true
		if messageWireSize(m) <= limit {
			return m, cut
		}
	}

	// ③ 结构开销 —— 依次丢弃（★ 顺序：tool_calls → name → tool_call_id）。
	m.ToolCalls = nil
	if messageWireSize(m) <= limit {
		return m, true
	}
	m.Name = ""
	if messageWireSize(m) <= limit {
		return m, true
	}
	m.ToolCallID = ""
	return m, true
}

// fitTextToLimit 返回 m.Content 的最长前缀，使【整条消息编码后】≤ limit。
//
// ★ 二分的前提是「编码后大小对内容长度单调」⇒ 成立（每多一个字符，
// ★ 编码后至少多 1 字节）。
// ★ ★ 但「前缀是合法 UTF-8」**不单调**（"中" 的前 1/2 字节都非法）
// ★ ⇒ ⇒ 不能把 UTF-8 校验放进二分条件 ⇒ ★ 二分只管大小，
// ★ ★ 最后再回退到 UTF-8 边界（★ 回退只会变小 ⇒ 仍然装得下）。
func fitTextToLimit(content string, m Message, limit int) string {
	lo, hi := 0, len(content)
	best := -1
	for lo <= hi {
		mid := (lo + hi) / 2
		cand := m
		cand.Content = content[:mid]
		if messageWireSize(cand) <= limit {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if best < 0 {
		return ""
	}
	// ★ 回退到 UTF-8 边界
	for best > 0 && !utf8.ValidString(content[:best]) {
		best--
	}
	return content[:best]
}

// shrinkRawToFit 把一段 JSON 原文裁到【连同消息其余字段一起】编码后 ≤ limit。
//
// ★ 收缩方式按 JSON 形状分：
//
//	字符串 ⇒ 截断（UTF-8 边界）
//	数组   ⇒ 保留前 N 个元素（★ 二分 N）
//	对象   ⇒ 保留前 N 个键（★ 按 key 排序取前缀 ⇒ ★ 确定性）
//	其它   ⇒ 不可收缩 ⇒ 返回 false（★ 调用方丢弃正文）
//
// ★ 为什么必须「解码 → 收缩 → 重编码」而不是「直接截字节」：
// ★ 直接截字节会切在结构中间 ⇒ 产出**非法 JSON** ⇒ 落库报错
// ★ ⇒ ★★ 那一轮正文**根本写不进去**。
func shrinkRawToFit(raw []byte, limit int, m Message) ([]byte, bool) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// ★ 原始值本就不是合法 JSON ⇒ 无法收缩
		return nil, false
	}
	// probe：★ 候选原文 + 其余字段 ⇒ 编码后能不能装下。
	probe := func(cand any) ([]byte, bool) {
		b, err := json.Marshal(cand)
		if err != nil {
			return nil, false
		}
		full := m
		full.ContentRaw = b
		full.RawContent = nil
		if messageWireSize(full) > limit {
			return nil, false
		}
		return b, true
	}
	switch tv := v.(type) {
	case string:
		lo, hi := 0, len(tv)
		best := -1
		for lo <= hi {
			mid := (lo + hi) / 2
			if _, ok := probe(tv[:mid]); ok {
				best = mid
				lo = mid + 1
			} else {
				hi = mid - 1
			}
		}
		if best < 0 {
			return nil, false
		}
		// ★ 回退到 UTF-8 边界
		for best > 0 && !utf8.ValidString(tv[:best]) {
			best--
		}
		out, err := json.Marshal(tv[:best])
		if err != nil {
			return nil, false
		}
		return out, true
	case []any:
		lo, hi := 0, len(tv)
		best := -1
		for lo <= hi {
			mid := (lo + hi) / 2
			if _, ok := probe(tv[:mid]); ok {
				best = mid
				lo = mid + 1
			} else {
				hi = mid - 1
			}
		}
		if best < 0 {
			return nil, false
		}
		out, err := json.Marshal(tv[:best])
		if err != nil {
			return nil, false
		}
		return out, true
	case map[string]any:
		// ★ map 迭代顺序随机 ⇒ ★ 先按 key 排序取前缀（★ 确定性）
		keys := make([]string, 0, len(tv))
		for k := range tv {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		lo, hi := 0, len(keys)
		best := -1
		for lo <= hi {
			mid := (lo + hi) / 2
			if _, ok := probe(subsetMap(tv, keys[:mid])); ok {
				best = mid
				lo = mid + 1
			} else {
				hi = mid - 1
			}
		}
		if best < 0 {
			return nil, false
		}
		out, err := json.Marshal(subsetMap(tv, keys[:best]))
		if err != nil {
			return nil, false
		}
		return out, true
	default:
		// ★ 数字 / 布尔 / null ⇒ 不可收缩
		return nil, false
	}
}

// subsetMap 取 src 中 keys 指定的前缀键（★ 保持确定性输出）。
func subsetMap(src map[string]any, keys []string) map[string]any {
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		out[k] = src[k]
	}
	return out
}

// markTruncation appends a synthetic system marker so readers see the cut.
func markTruncation(rec *BodiesRecord) {
	if rec == nil {
		return
	}
	rec.ResponseDelta = append(rec.ResponseDelta, truncationMarker())
}
