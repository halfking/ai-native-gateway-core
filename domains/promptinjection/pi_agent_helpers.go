// Package promptinjection - PI 智能体共用辅助
//
// 2026-11-17 审计轮（主档 docs/audit/2026-11-17-PI智能体审计与完善-主档.md）：
// 本文件收拢 PI 智能体三条职责链共用的低层原语，每一条都对应本仓
// **已经踩过一次并修好**的事故形态（见各函数的注释）。
//
// 三条通用式：
//
//	① 绑定形态必须与驱动协议一致（本仓 pgx simple protocol ⇒ []byte 会变成 bytea）；
//	② 字节切分必须落在 rune 边界上（否则 PG 22021 丢整行）；
//	③ 外部 LLM 回填的值必须先归一到数据库 CHECK 允许的集合（否则 23514 丢结果）。
package promptinjection

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
)

// truncateUTF8Bytes 把 s 截断到**至多 limit 字节**，且保证不会切断一个 rune。
//
// ★ 为什么不能用 s[:limit]（本仓第 2 次同型事故）：
//
//	字节切分可能落在多字节 rune 中间 ⇒ 产出**非法 UTF-8** ⇒
//	写进 PostgreSQL 的 UTF8 列时 INSERT 报 SQLSTATE 22021
//	（invalid byte sequence for encoding "UTF8"）⇒ **整行丢失**。
//	本仓先例：2026-06-11 事故 —— s[:limit] 让 glm-5.1 / minimax / doubao
//	的**中文与 emoji 流量**在 _request_logs 里静默丢行
//	（见 domains/streaming/executors/safe_truncate.go 的同名原语与其事故记述）。
//
// ★ 本包的受害点：error_collector.Collect 把**用户原始输入**截断到 10000 字节
//
//	后落 error_analysis_queue.input_text ⇒ 中文输入会命中切点 ⇒
//	错误队列**整条插不进去**（而队列是 PI 智能体唯一的输入来源）⇒
//	「错误收集与分析」这条职责链在真实中文流量上等于零采集。
func truncateUTF8Bytes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	cut := 0
	for i, r := range s {
		next := i + utf8.RuneLen(r)
		if next > limit {
			cut = i
			break
		}
		cut = next
	}
	return s[:cut]
}

// jsonbArg 把任意可 JSON 序列化的值转成**可直接绑定给 jsonb 列的字符串**。
//
// ★ 为什么不用 []byte 直绑（本仓两次同型事故 + 一次实测订正）：
//
//	(a) 2026-09-17 事故 —— response_format_anomalies 永远 0 行，
//	    根因就是传了 []byte（见 domains/streaming/format_anomaly_recorder_test.go
//	    的守门测试与 internal/dbx/jsonb.go:17 的口径说明）。
//	(b) ★ **影响面订正**（2026-11-17 审计轮，用仓库 vendored pgx v5.9.2 +
//	    隔离 PG 17.10 实测两种 exec mode）：本仓 PG 池的执行模式是
//	    **有条件的**——db/db.go:105-108 先设 SimpleProtocol，随后
//	    `statementCacheEnabledFromEnv()`（未设环境变量时返回 **true**）
//	    会改回 QueryExecModeCacheStatement ⇒ **生产默认 = CacheStatement**
//	    ⇒ pgx 走 JSONBCodec，[]byte 绑定 jsonb **是好的**。
//	    只有在 kill-switch `LLM_GATEWAY_DB_STATEMENT_CACHE=false` 时
//	    才落到 SimpleProtocol ⇒ []byte 被文本编码成 bytea 十六进制
//	    ⇒ PG 报 22P02（invalid input syntax for type json）。
//	    ⇒ 所以这是**条件性 P1**（不是「每次 INSERT 都炸」的 P0），
//	    但 `string` + `::text::jsonb` 是**两种模式都正确**的形态，
//	    也与本仓正典 internal/dbx.NormalizeJSONB 一致 ⇒ 仍然采用。
//
// ★ 空串必须走 NULLIF：
//
//	nil / 空内容在本函数里返回 ""。若直接 `$N::text::jsonb`，
//	空串会被当成 JSON 文本解析 ⇒ **22P02**（把「没内容」换成「SQL 报错」）。
//	⇒ 调用方的 SQL 必须写成 `NULLIF($N, '')::text::jsonb`。
func jsonbArg(v any) (string, error) {
	if v == nil {
		// 与 internal/dbx.NormalizeJSONB 同口径：顶层 null 不是有效文档。
		// 这里返回空串，调用方用 NULLIF($N,'') 把它变成 SQL NULL。
		return "", nil
	}
	if raw, ok := v.([]byte); ok {
		if !utf8.Valid(raw) {
			return "", fmt.Errorf("jsonb arg: invalid UTF-8 payload")
		}
		return string(raw), nil
	}
	if raw, ok := v.(json.RawMessage); ok {
		if len(raw) == 0 {
			return "", nil
		}
		if !utf8.Valid(raw) {
			return "", fmt.Errorf("jsonb arg: invalid UTF-8 payload")
		}
		return string(raw), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("jsonb arg: marshal: %w", err)
	}
	if !utf8.Valid(b) {
		return "", fmt.Errorf("jsonb arg: invalid UTF-8 payload")
	}
	return string(b), nil
}

// severityLevels 是 error_analysis_results.severity_level 的 CHECK 允许集
// （sql/migrations/startup/971_pi_error_analysis_system.sql）。
var severityLevels = map[string]bool{
	"low":      true,
	"medium":   true,
	"high":     true,
	"critical": true,
}

// impactScopes 是 error_analysis_results.impact_scope 的 CHECK 允许集。
var impactScopes = map[string]bool{
	"session": true,
	"tenant":  true,
	"global":  true,
}

// normalizeSeverityLevel 把 LLM 回填的严重程度归一到数据库 CHECK 允许的集合。
//
// ★ 为什么必须归一（否则整条分析结果静默丢失）：
//
//	severity_level 的 CHECK 是 severity_level IN ('low','medium','high','critical')。
//	LLM 回填的是**自由文本**（"Critical"、"high-risk"、"严重"、"p0"）⇒
//	INSERT 直接撞 23514（check_violation）⇒ SaveAnalysisResult 报错 ⇒
//	**分析结果整行丢失**，队列行被标成 failed ⇒
//	「LLM 分析错误根因」这条职责链在最需要它的场景下产不出任何东西。
//	⇒ 归一（大小写 + 别名 + 分隔符切词 + 子串兜底）而不是丢弃：
//	  拿不准的一律降级到 "medium"，但**必须落在允许集内**，否则 DB 拒收。
//
// ★ 2026-11-17 审计轮**复核修正**：原实现只处理「全等 + 少量别名」，
//
//	"high-risk" / "global-ish" 这类**带分隔符**的写法仍会漏到 default
//	⇒ severity 掉到 medium、impact 掉到 session ⇒ 两者都**偏低**（fail-open 方向）。
//	⇒ 补上「分隔符切词 + 子串兜底」两级。
func normalizeSeverityLevel(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "low", "minor", "info", "informational", "低", "轻微":
		return "low"
	case "medium", "med", "moderate", "normal", "中":
		return "medium"
	case "high", "严重", "高":
		return "high"
	case "critical", "crit", "severe", "blocker", "紧急", "致命":
		return "critical"
	}
	// ① 分隔符切词（high-risk / high_risk / high risk / HIGH-RISK）。
	for _, tok := range splitEnumTokens(s) {
		switch tok {
		case "low", "minor", "info", "informational":
			return "low"
		case "medium", "med", "moderate", "normal":
			return "medium"
		case "high":
			return "high"
		case "critical", "crit", "severe", "blocker":
			return "critical"
		}
	}
	// ② 子串兜底（p0 / sev1 / "严重性高"）。顺序即优先级。
	if strings.Contains(s, "critical") || strings.Contains(s, "crit") ||
		strings.Contains(s, "severe") || strings.Contains(s, "blocker") ||
		strings.Contains(s, "致命") || strings.Contains(s, "紧急") {
		return "critical"
	}
	if strings.Contains(s, "high") || strings.Contains(s, "严重") {
		return "high"
	}
	if strings.Contains(s, "medium") || strings.Contains(s, "moderate") {
		return "medium"
	}
	if strings.Contains(s, "low") || strings.Contains(s, "轻微") {
		return "low"
	}
	return "medium"
}

// normalizeImpactScope 把 LLM 回填的影响范围归一到数据库 CHECK 允许的集合。
// 理由同 normalizeSeverityLevel（23514 ⇒ 整条结果丢失）。
func normalizeImpactScope(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "session", "request", "单会话", "会话":
		return "session"
	case "tenant", "租户":
		return "tenant"
	case "global", "all", "cluster", "全局":
		return "global"
	}
	for _, tok := range splitEnumTokens(s) {
		switch tok {
		case "session", "request":
			return "session"
		case "tenant":
			return "tenant"
		case "global", "cluster":
			return "global"
		}
	}
	// 子串兜底（global-ish / 全局范围）。
	if strings.Contains(s, "global") || strings.Contains(s, "cluster") || strings.Contains(s, "全局") {
		return "global"
	}
	if strings.Contains(s, "tenant") || strings.Contains(s, "租户") {
		return "tenant"
	}
	if strings.Contains(s, "session") || strings.Contains(s, "会话") {
		return "session"
	}
	return "session"
}

// splitEnumTokens 按**非字母数字**切词（high-risk / high_risk / high risk）。
//
// ★ 中文不分词（中文没有分隔符）⇒ 中文由调用方的全等分支与子串兜底覆盖。
func splitEnumTokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
}

// normalizeConfidenceScore 把置信度夹到 CHECK 允许的 [0,1]。
// NaN / Inf 一律降级为 0（NaN 过不了 CHECK 比较，且 JSON 里无法表示）。
func normalizeConfidenceScore(v float64) float64 {
	if v != v { // NaN
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// isUniqueViolation 判断 err 是否是 PostgreSQL 的 unique_violation(23505)。
//
// ★ 2026-11-17 审计轮：迁移 972 给 error_analysis_results.queue_id 加了
//
//	唯一索引 ⇒ 同一队列行的**第二次**分析会撞 23505。
//	语义上这**不是故障**（是「已经分析过了」）⇒ 调用方据此走幂等分支。
//
//	实现说明：本包用 database/sql（非 pgx）⇒ 错误来自驱动；
//	这里同时识别 pgconn.PgError 与 pq.Error 两种形态，
//	并保留 SQLSTATE 文本兜底（驱动包装过的错误仍能识别）。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	// 文本兜底：SQLSTATE 23505。
	msg := err.Error()
	if strings.Contains(msg, "23505") || strings.Contains(msg, "duplicate key value") {
		return true
	}
	// 驱动原生类型。
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "23505"
	}
	return false
}

// tailUTF8Bytes 取 s 的**末尾**至多 limit 字节，且保证不从 rune 中间切开。
//
// ★ 为什么需要它（truncateUTF8Bytes 只能取头部）：
//
//	「头尾两段截断」若写成 `truncateUTF8Bytes(input[len(head):], n)`，
//	取到的仍是**中间那段填充**，而不是输入末尾 ⇒
//	在「前置填充」这一攻击形态下尾部载荷根本没被保住。
//	⇒ 尾部必须从末尾倒着取，且起点落在 rune 边界上。
//
// ★ rune 边界处理：从末尾往前找起点，遇到**续字节**（0b10xxxxxx）
//
//	就继续前移 ⇒ 保证返回的切片是合法的 UTF-8 序列开头。
func tailUTF8Bytes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	cut := len(s) - limit
	for cut < len(s) {
		r, size := utf8.DecodeRuneInString(s[cut:])
		// 合法 rune（含合法编码的 U+FFFD）⇒ cut 就是 rune 起点。
		if r != utf8.RuneError || size != 1 {
			break
		}
		// 非法字节 ⇒ 前移继续找。
		cut++
	}
	return s[cut:]
}

// untrustedInputOpen / untrustedInputClose 是包裹**不可信载荷**的标签。
//
// ★ 2026-11-17 审计轮 P1（F-PI-06）：判官提示词必须把「契约」与「载荷」
//
//	分角色（契约放 system 角色、载荷放 user 角色），
//	并且用一对**攻击者无法自行闭合**的标签框住载荷。
const (
	untrustedInputOpen  = "<untrusted_input>"
	untrustedInputClose = "</untrusted_input>"

	// neutralisedInputOpen / neutralisedInputClose 是标签的**中和形态**。
	//
	// ★ 为什么用方括号记号而不是 HTML 实体（&lt; / &gt;）：
	//
	//	载荷最终要过 json.Marshal，而 Go 的 json.Marshal 默认会把
	//	`<` `>` `&` 转成对应的 **u 转义**（形如 6 个 ASCII 字符）
	//	⇒ HTML 实体形态会被**二次转义**，在提示词里变成一串不可读的转义串，
	//	而且「载荷被中和了几次」这件事**从提示词里读不出来**（可断言性打折）。
	//	⇒ 改用**不含任何 JSON 特殊字符**的方括号记号：
	//	  它原样穿过 json.Marshal ⇒ 判官看得懂、
	//	  门禁也能直接数「载荷里被中和的标签有几个」。
	neutralisedInputOpen  = "[untrusted_input_open]"
	neutralisedInputClose = "[untrusted_input_close]"
)

// neutraliseUntrustedText 中和不可信文本里可能出现的**框架闭合标签**。
//
// ★ 为什么不能只靠 JSON 转义：
//
//	buildLLMDetectionPrompt 用 json.Marshal 转义载荷，Go 的 json.Marshal
//	**默认**会把 `<` `>` `&` 转成 u 转义，
//	所以今天它确实挡住了框架逃逸。
//	但那是**标准库的默认行为**（json.Encoder 配 SetEscapeHTML(false)
//	就会失效），不是本仓自己写下的不变量 ⇒
//	一旦有人改用 Encoder，防护会**无声消失**，而门禁里没有任何断言会红。
//
//	⇒ 这里显式中和一次，把「载荷不能闭合框架」变成**本仓自己的可断言不变量**
//	（门：pi_layers_audit_guard_test.go 的
//	  TestNeutraliseUntrustedText_BlocksFrameEscape /
//	  TestBuildLLMDetectionPrompt_PayloadCannotCloseFrame）。
//
// ★ 中和是**幂等**的：中和形态里不含 `<`/`>`，不会被再次替换；
//
//	攻击者直接输入 `[untrusted_input_close]` 也不会造成框架逃逸
//	（那只是一个无害的记号）。
func neutraliseUntrustedText(text string) string {
	text = strings.ReplaceAll(text, untrustedInputOpen, neutralisedInputOpen)
	text = strings.ReplaceAll(text, untrustedInputClose, neutralisedInputClose)
	return text
}
