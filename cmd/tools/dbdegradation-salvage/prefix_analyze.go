package main

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"sort"
	"time"
)

// ★★★★★★ 本文件回答 §143.71 的核心问题：
//
//	「捞回来的累积体，能拆出多少轮正文？」
//
// ★ 仓内 `cmd/tools/backfill_session_bodies/derive.go` 的 `DeriveTurnDeltas`
//   是【前缀差分】：delta_i = full_i − full_{i−1}（身份 = (role, content)）。
//   ⇒ 差分能拆出第 i 轮，前提是「第 i−1 轮的累积体确实是第 i 轮累积体的前缀」。
//   ⇒ ★ 所以「可拆轮数」= 前缀性质成立的轮数，不是「捞回第 N 轮就能重建前 N 轮」
//     （那需要前 N−1 轮的累积体【全部】在场）。
//
// ★★★ 两个判据（同时给，因为它们能互相拆穿）：
//
//	A（role 级）：cum_{i−1} 的 role 序列 == cum_i 的 role 序列前缀
//	  ⇒ 「能否按 user 边界把累积体切成 K 段」（切轮可行性）
//	B（content 级）：cum_{i−1} 的 (role,content) 指纹链 == cum_i 的指纹链前缀
//	  ⇒ 「前一轮的内容是否真的一字不差地被下一轮继承」（差分正确性）
//
// ★ A 成立而 B 不成立 ⇒ 能切轮，但差分会多出内容（差分污染）。
// ★ B 成立而 A 不成立 ⇒ 不该发生（content 指纹含 role）⇒ 出现即说明实现有 bug。
//
// ★★ 两条「只出结构与指纹、不出正文」的纪律：
//   - 指纹链只留 64 位摘要 ⇒ 不落任何可还原的内容；
//   - 样本输出只给会话 id 的前 8 位与序列长度。

// role 码：紧凑编码消息角色。
const (
	roleUser byte = 'u'
	roleAsst byte = 'a'
	roleTool byte = 't'
	roleSys  byte = 's'
	roleOthr byte = 'o'
)

// msgSeq 是一条累积体的结构摘要。
type msgSeq struct {
	roles  []byte   // 每条消息 1 字节 role 码
	hashes []uint64 // 每条消息 (role, content) 的 64 位指纹
}

// analyzeEntry 是一条记录的摘要。
type analyzeEntry struct {
	EventAt string
	ReqID   string
	Seq     msgSeq
}

// analyzeResult 是前缀性质分析的结果（★ 不含任何正文内容）。
type analyzeResult struct {
	Base string `json:"base"`
	// Prefix 是文件名日期前缀过滤（空 = 全扫）。
	Prefix string `json:"prefix"`
	// Records = 读到的记录总数；WithBody = 其中带 request_body 的条数。
	Records  int `json:"records"`
	WithBody int `json:"records_with_body"`
	// Unparsed = 解析不出消息数组的条数（★ 必须显式给出，
	// 否则「没解析出来」会被读成「不是累积体」）。
	Unparsed int `json:"records_unparsed"`
	// Sessions = 出现过的会话数；MultiTurn = 其中有 ≥2 条记录的会话数。
	Sessions  int `json:"sessions"`
	MultiTurn int `json:"multi_turn_sessions"`
	// 对同一会话里【时间相邻】的两条累积体做的前缀比较。
	PairRoleOK     int `json:"pair_role_prefix_ok"`
	PairRoleBroken int `json:"pair_role_prefix_broken"`
	PairContentOK  int `json:"pair_content_prefix_ok"`
	PairContentBad int `json:"pair_content_prefix_broken"`
	// AccumRecords = user 消息数 ≥2 的记录数（多轮累积体）。
	AccumRecords int `json:"accum_records"`
	// MaxUserMsgs = 单条记录里最多的 user 消息数（= 最深的累积轮数）。
	MaxUserMsgs int `json:"max_user_msgs"`
	// ★ SplittableTurns = Σ（每个会话所有记录里 user 消息数的最大值）
	//   = 「若该会话的累积体全部在场，可拆出的轮数上界」。
	SplittableTurns int `json:"splittable_turns_upper_bound"`
	// ★★★ 有证据支撑的可恢复轮数（比上界严格得多）：
	//
	//   content 链：同一会话里【时间相邻】的两条累积体满足「前一条是后一条的真前缀
	//   （内容级）」的连续段。链上最深处那条累积体因此包含链上全部内容
	//   ⇒ ⇒ 切轮法可以从它还原出前 [该深度的 user 消息数] 轮。
	//
	//   ⇒ ContentChainTurns = Σ（最长 content 链覆盖的轮数）  ★ 保守口径
	//   ⇒ RoleChainTurns    = Σ（最长 role 链覆盖的轮数）     ★ 宽松口径
	//     （role 级只要求「轮边界可切」，不要求内容逐字继承 ⇒ ⇒ 切出来的段
	//       可能与运行期 delta 不一致 ⇒ 两个数必须并列给）
	ContentChainTurns  int `json:"content_chain_turns"`
	RoleChainTurns     int `json:"role_chain_turns"`
	ContentChainSess   int `json:"content_chain_sessions"`
	RoleChainSess      int `json:"role_chain_sessions"`
	MaxContentChainLen int `json:"max_content_chain_len"`
	MaxRoleChainLen    int `json:"max_role_chain_len"`
	// ChainBreakSamples = 链断裂的样本（★ 只含会话 id 前 8 位与长度）。
	ChainBreakSamples []brokenSample `json:"chain_break_samples,omitempty"`
	// Samples = 少量反例样本（★ 只含会话 id 前 8 位与序列长度）。
	Samples []brokenSample `json:"broken_samples,omitempty"`
	// WallClock = 耗时。
	WallClock string `json:"wall_clock"`
}

// brokenSample 是一条前缀不成立的样本（★ 不含任何内容）。
type brokenSample struct {
	Session   string `json:"session_prefix"`
	PrevLen   int    `json:"prev_msgs"`
	CurrLen   int    `json:"curr_msgs"`
	PrevUsers int    `json:"prev_user_msgs"`
	CurrUsers int    `json:"curr_user_msgs"`
}

// analyzeAccum 是分析器的累积状态（★ 由 runScan 在遍历中填充 ⇒ 一次扫描同时出元数据与分析）。
type analyzeAccum struct {
	base         string
	prefix       string
	bySession    map[string][]analyzeEntry
	records      int
	withBody     int
	unparsed     int
	accumRecords int
	maxUserMsgs  int
	started      time.Time
}

// newAnalyzeAccum 建一个累积状态。
func newAnalyzeAccum(base, prefix string) *analyzeAccum {
	return &analyzeAccum{
		base:      base,
		prefix:    prefix,
		bySession: map[string][]analyzeEntry{},
		started:   time.Now(),
	}
}

// observe 吃进一条记录的 payload（★ 只读，不改 payload）。
func (a *analyzeAccum) observe(payload map[string]any) {
	a.records++
	body, _ := payload["request_body"].(string)
	if body == "" {
		return
	}
	a.withBody++
	msgs, field := extractMsgs(body)
	if msgs == nil || field == "" {
		a.unparsed++
		return
	}
	seq := buildMsgSeq(msgs)
	users := countRole(seq.roles, roleUser)
	if users >= 2 {
		a.accumRecords++
	}
	if users > a.maxUserMsgs {
		a.maxUserMsgs = users
	}
	sid := asString(payload["gw_session_id"])
	rid := asString(payload["request_id"])
	if rid == "" {
		rid = asString(payload["RequestID"])
	}
	a.bySession[sid] = append(a.bySession[sid], analyzeEntry{
		EventAt: asString(payload["event_at"]),
		ReqID:   rid,
		Seq:     seq,
	})
}

// finalize 算出最终结果（★ 链算法在这里）。
func (a *analyzeAccum) finalize() analyzeResult {
	res := analyzeResult{
		Base:         a.base,
		Prefix:       a.prefix,
		Records:      a.records,
		WithBody:     a.withBody,
		Unparsed:     a.unparsed,
		AccumRecords: a.accumRecords,
		MaxUserMsgs:  a.maxUserMsgs,
		Sessions:     len(a.bySession),
	}
	for sid, entries := range a.bySession {
		if len(entries) >= 2 {
			res.MultiTurn++
		}
		// 轮数上界：同一会话所有记录里 user 消息数的最大值。
		maxUsers := 0
		for _, e := range entries {
			if u := countRole(e.Seq.roles, roleUser); u > maxUsers {
				maxUsers = u
			}
		}
		res.SplittableTurns += maxUsers
		if len(entries) < 2 {
			continue
		}
		// ★ 按 event_at 排序后比「时间相邻」的两条（★ 不按文件顺序：
		// 同一会话的记录可能分散在多个文件里）。
		sort.SliceStable(entries, func(i, j int) bool {
			if entries[i].EventAt != entries[j].EventAt {
				return entries[i].EventAt < entries[j].EventAt
			}
			return entries[i].ReqID < entries[j].ReqID
		})
		// ★★★ 链算法：连续满足「前一条是后一条的前缀」的相邻对构成一条链。
		//
		//   链末端那条累积体因此【逐字包含】链上全部内容
		//   ⇒ ⇒ 切轮法可以从它还原出【前 K 轮】（K = 末端的 user 消息数）
		//   ⇒ ⇒ 这是【有证据】的可恢复轮数（content 级最严，role 级最宽）。
		contentChain := 1
		roleChain := 1
		bestContent, bestRole := 0, 0
		for i := 1; i < len(entries); i++ {
			prev, curr := entries[i-1], entries[i]
			roleOK := isRolePrefix(prev.Seq, curr.Seq)
			if roleOK {
				res.PairRoleOK++
				roleChain++
			} else {
				res.PairRoleBroken++
				roleChain = 1
				if len(res.Samples) < 20 {
					res.Samples = append(res.Samples, brokenSample{
						Session:   shortSession(sid),
						PrevLen:   len(prev.Seq.roles),
						CurrLen:   len(curr.Seq.roles),
						PrevUsers: countRole(prev.Seq.roles, roleUser),
						CurrUsers: countRole(curr.Seq.roles, roleUser),
					})
				}
			}
			contentOK := isContentPrefix(prev.Seq, curr.Seq)
			if contentOK {
				res.PairContentOK++
				contentChain++
			} else {
				res.PairContentBad++
				contentChain = 1
			}
			currUsers := countRole(curr.Seq.roles, roleUser)
			if contentChain >= 2 && currUsers > bestContent {
				bestContent = currUsers
			}
			if roleChain >= 2 && currUsers > bestRole {
				bestRole = currUsers
			}
			if contentChain > res.MaxContentChainLen {
				res.MaxContentChainLen = contentChain
			}
			if roleChain > res.MaxRoleChainLen {
				res.MaxRoleChainLen = roleChain
			}
		}
		if bestContent > 0 {
			res.ContentChainSess++
			res.ContentChainTurns += bestContent
		}
		if bestRole > 0 {
			res.RoleChainSess++
			res.RoleChainTurns += bestRole
		}
	}
	res.WallClock = time.Since(a.started).Round(time.Millisecond).String()
	return res
}

// analyzePrefix 扫描兜底文件并分析累积体的前缀性质（★ 只读）。
//
// ★ metaOut 非空时【同时】导出元数据 ⇒ 一次遍历出两样（★ 兜底文件是 GB 级，
// 多扫一遍就是多付几十分钟）。
func analyzePrefix(ctx context.Context, base, prefix, metaOut string) (analyzeResult, error) {
	acc := newAnalyzeAccum(base, prefix)
	if _, err := runScanAnalyze(ctx, base, prefix, metaOut, "", acc, ""); err != nil {
		return analyzeResult{}, err
	}
	return acc.finalize(), nil
}

// buildMsgSeq 把消息数组转成结构摘要（★ 只留 role 与 64 位指纹）。
func buildMsgSeq(msgs []any) msgSeq {
	seq := msgSeq{
		roles:  make([]byte, 0, len(msgs)),
		hashes: make([]uint64, 0, len(msgs)),
	}
	for _, m := range msgs {
		role := roleOf(m)
		code := roleCode(role)
		seq.roles = append(seq.roles, code)
		seq.hashes = append(seq.hashes, msgFingerprint(m, code))
	}
	return seq
}

// roleCode 把角色名映射成紧凑码。
func roleCode(role string) byte {
	switch role {
	case "user":
		return roleUser
	case "assistant":
		return roleAsst
	case "tool":
		return roleTool
	case "system":
		return roleSys
	}
	return roleOthr
}

// msgFingerprint 算一条消息的 64 位指纹（★ role + content 投影）。
//
// ★ 与仓内 `derive.go` 的 `plainContent` 同构：字符串 content 逐字、
//
//	数组/多模态 content 取紧凑 JSON ⇒ ⇒ 指纹链可与差分语义对齐。
func msgFingerprint(m any, code byte) uint64 {
	h := fnv.New64a()
	h.Write([]byte{code})
	switch t := m.(type) {
	case map[string]any:
		if c, ok := t["content"]; ok {
			h.Write([]byte(plainContent(c)))
		}
		if tc, ok := t["tool_calls"]; ok {
			h.Write([]byte(plainContent(tc)))
		}
		if tc, ok := t["tool_call_id"]; ok {
			h.Write([]byte(plainContent(tc)))
		}
	case string:
		h.Write([]byte(t))
	}
	return h.Sum64()
}

// plainContent 渲染 content：字符串逐字，数组/对象取紧凑 JSON。
//
// ★ 与仓内 `cmd/tools/backfill_session_bodies/derive.go` 的 `plainContent` 同口径
//
//	（★ 独立实现，但比较是【同口径内部比较】，故不影响结论）。
func plainContent(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(raw)
}

// isRolePrefix 判断 prev 的 role 序列是否为 curr 的前缀。
func isRolePrefix(prev, curr msgSeq) bool {
	if len(prev.roles) > len(curr.roles) {
		return false
	}
	for i := range prev.roles {
		if prev.roles[i] != curr.roles[i] {
			return false
		}
	}
	return true
}

// isContentPrefix 判断 prev 的指纹链是否为 curr 的前缀。
func isContentPrefix(prev, curr msgSeq) bool {
	if len(prev.hashes) > len(curr.hashes) {
		return false
	}
	for i := range prev.hashes {
		if prev.hashes[i] != curr.hashes[i] {
			return false
		}
	}
	return true
}

// countRole 数某个 role 的条数。
func countRole(roles []byte, code byte) int {
	n := 0
	for _, r := range roles {
		if r == code {
			n++
		}
	}
	return n
}

// shortSession 取会话 id 的前 8 位（★ 样本输出不带全 id）。
func shortSession(sid string) string {
	if len(sid) <= 8 {
		return sid
	}
	return sid[:8]
}
