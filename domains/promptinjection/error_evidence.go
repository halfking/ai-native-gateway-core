package promptinjection

// error_evidence.go — 取证的材料形状 + 给模型看的摘要（2026-11-25 PI 分析取证轮）。
//
// ★ 为什么分析前要取证：
//
//	旧分析器只拿到队列行里的错误消息与前 500 字节输入——「是哪个模型、哪个
//	供应商、什么状态码、这个会话前后发生了什么、服务日志怎么说的」一概没有
//	⇒ 模型只能在错误消息上空推。三台生产的真实分析里，建议清一色是
//	「请按 request_id 去核对请求日志」——让分析器自己去查，正是本轮补的。
//
// ★ 依赖方向：本包只定义**接口**与摘要渲染；采集实现（查请求账本 / 上游失败
//
//	记录 / 服务日志 / 错误手册）在 admin 包，装配时注入。域与域之间零新增依赖。
//
// ★ 安全口径：取证材料全部是**不可信文本**（日志行、上游错误消息、请求预览
//
//	都可能被请求方影响）⇒ 进提示词前逐项中和框架闭合标签、限长，并整体放在
//	<untrusted_input> 框里（见 buildPrompt）。

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// EvidenceTarget 是一次取证的锚点（来自队列行）。
type EvidenceTarget struct {
	QueueID      int64
	ErrorType    string
	TenantID     string
	SessionID    string
	RequestID    string
	ErrorAt      time.Time
	ErrorDetails map[string]any
}

// ServiceLogLine 是一行服务日志（来自进程内近期日志缓冲，已脱敏）。
type ServiceLogLine struct {
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Line  string    `json:"line"`
}

// EvidenceDegradation 记录一路取证「没算出来」及其原因。
//
//	★ 为什么要有它（而不是只写 Notes）：Notes 是给人看的文本；调用方要区分
//	  「这一路算出来是空」与「这一路根本没算出来」（表不在场 / 查询超时），
//	  必须有一个**机器可读**的标记——与 admin 包 degraded 契约同一条纪律。
type EvidenceDegradation struct {
	Part   string `json:"part"`
	Reason string `json:"reason"`
}

// EvidenceBundle 是一次取证的全部材料。
//
//	每一路都可以缺席（表不在场 / 没有请求号 / 缓冲里没有匹配行）——缺席**不报错**，
//	在 Notes 里如实说明，分析照常进行（只是材料少）。
type EvidenceBundle struct {
	// Request 是请求账本里对应的那一行（精简字段）。
	Request map[string]any `json:"request,omitempty"`
	// Session 是同一会话里相邻的请求（前后各若干条）——「前因后果」的时间线。
	Session []map[string]any `json:"session_context,omitempty"`
	// Attempts 是该请求在各上游上的失败尝试。
	Attempts []map[string]any `json:"upstream_attempts,omitempty"`
	// ErrorWindow 是同型错误在时间窗内的分布（按供应商/模型）。
	ErrorWindow []map[string]any `json:"error_window,omitempty"`
	// ServiceLogs 是本实例服务日志里与该错误相关的行（新 → 旧）。
	ServiceLogs []ServiceLogLine `json:"service_logs,omitempty"`
	// ServiceLogMeta 说明服务日志这一路的覆盖范围（最老/最新一行的时间、
	// 扫过多少行、是否被截断）——「没搜到」与「日志只覆盖到 X 之后」必须可区分。
	ServiceLogMeta map[string]any `json:"service_log_meta,omitempty"`
	// Playbook 是错误处理手册里同型错误已有的条目（标准处置）。
	Playbook []map[string]any `json:"playbook_entries,omitempty"`
	// Degraded 是「没算出来」的那几路（机器可读；与 Notes 同源）。
	Degraded []EvidenceDegradation `json:"degraded,omitempty"`
	// Notes 如实记录哪一路不可用及原因。
	Notes      []string  `json:"notes,omitempty"`
	GatheredAt time.Time `json:"gathered_at"`
	DurationMs int       `json:"duration_ms"`
}

// NoteDegraded 把一路降级同时写进机器可读的 Degraded 与给人看的 Notes。
func (b *EvidenceBundle) NoteDegraded(part, reason string) {
	b.Degraded = append(b.Degraded, EvidenceDegradation{Part: part, Reason: reason})
	b.Notes = append(b.Notes, reason)
}

// Empty 报告是否一路材料都没有。
func (b *EvidenceBundle) Empty() bool {
	if b == nil {
		return true
	}
	// Notes 也算内容：「取证失败：…」这句说明必须能进提示词（否则模型会以为
	// 根本没有取证这回事），所以带说明的材料不算空。
	return b.Request == nil && len(b.Session) == 0 && len(b.Attempts) == 0 &&
		len(b.ErrorWindow) == 0 && len(b.ServiceLogs) == 0 && len(b.Playbook) == 0 &&
		len(b.Notes) == 0
}

// EvidenceProvider 是取证采集面（实现在 admin 包，装配时注入）。
type EvidenceProvider interface {
	Gather(ctx context.Context, target EvidenceTarget) (*EvidenceBundle, error)
}

// ── 摘要渲染 ───────────────────────────────────────────────────────────────

const (
	evidenceDigestMaxRunes   = 6000 // 摘要总长上限
	evidenceLineMaxRunes     = 240  // 单行日志上限
	evidenceMaxServiceLines  = 40
	evidenceMaxSessionRows   = 8
	evidenceMaxAttemptRows   = 8
	evidenceMaxWindowRows    = 8
	evidenceMaxPlaybookRows  = 5
	evidenceMaxFieldValRunes = 200
)

// requestFieldOrder 是请求账本行的展示顺序（稳定、可读）。
var requestFieldOrder = []string{
	"ts", "request_status", "success", "error_kind", "failure_stage", "failure_detail_code",
	"upstream_status_code", "client_model", "outbound_model", "provider_name", "provider_id",
	"credential_id", "client_protocol", "client_endpoint", "agent_name", "latency_ms",
	"prompt_tokens", "completion_tokens", "total_tokens", "stream_interrupted",
	"routing_attempts", "routing_outcomes", "request_preview",
}

// EvidenceDigest 把取证材料渲染成提示词里的一个有界文本块。
//
//	★ 渲染出来的每个字符都会进提示词 ⇒ 一律先过 neutraliseUntrustedText
//	  （不能让日志里的内容闭合 <untrusted_input> 框架）再限长。
//	★ 截断写在**标题里**（「共 N 行，仅显示最新 40 行」）而不是列表末尾：
//	  整体限长砍的是尾部——砍掉的恰好会是末尾那句「…（还有更多）」，
//	  读的人就把截断后的几行当成了全部。
func EvidenceDigest(b *EvidenceBundle) string {
	if b.Empty() {
		return ""
	}
	var out strings.Builder
	emit := func(s string) {
		out.WriteString(neutraliseUntrustedText(s))
		out.WriteByte('\n')
	}
	shown := func(total, cap int) string {
		if total > cap {
			return fmt.Sprintf("（共 %d 条，仅显示前 %d 条）", total, cap)
		}
		return ""
	}

	if len(b.Request) > 0 {
		emit("【请求账本】该错误对应的请求：")
		seen := map[string]bool{}
		for _, k := range requestFieldOrder {
			v, ok := b.Request[k]
			if !ok {
				continue
			}
			seen[k] = true
			if s := evidenceValueText(v); s != "" {
				emit("  " + k + ": " + s)
			}
		}
		for k, v := range b.Request {
			if seen[k] {
				continue
			}
			if s := evidenceValueText(v); s != "" && len(s) <= 120 {
				emit("  " + k + ": " + s)
			}
		}
	}
	if len(b.Attempts) > 0 {
		emit("【上游尝试】该请求在各上游上的失败记录（时间倒序）" + shown(len(b.Attempts), evidenceMaxAttemptRows) + "：")
		for i, a := range b.Attempts {
			if i >= evidenceMaxAttemptRows {
				break
			}
			emit("  - " + evidenceRowText(a, []string{"ts", "provider_name", "provider_id", "model", "upstream_status_code", "error_kind", "retryable", "latency_ms", "error_message"}))
		}
	}
	if len(b.Session) > 0 {
		emit("【会话上下文】同一会话里相邻的请求（时间正序；标记 ← 的是出错的那一条）" + shown(len(b.Session), evidenceMaxSessionRows) + "：")
		for i, s := range b.Session {
			if i >= evidenceMaxSessionRows {
				break
			}
			mark := "  "
			if isTrue(s["is_target"]) {
				mark = "← "
			}
			emit(mark + evidenceRowText(s, []string{"ts", "request_status", "client_model", "error_kind", "upstream_status_code", "latency_ms", "total_tokens"}))
		}
	}
	if len(b.ErrorWindow) > 0 {
		emit("【同型错误分布】时间窗内同型错误按供应商/模型的分布" + shown(len(b.ErrorWindow), evidenceMaxWindowRows) + "：")
		for i, w := range b.ErrorWindow {
			if i >= evidenceMaxWindowRows {
				break
			}
			emit("  - " + evidenceRowText(w, []string{"provider_name", "provider_id", "model", "count", "last_ts"}))
		}
	}
	if len(b.ServiceLogs) > 0 {
		meta := ""
		if b.ServiceLogMeta != nil {
			if from, ok := b.ServiceLogMeta["oldest_at"].(string); ok && from != "" {
				meta = fmt.Sprintf("（本实例日志只覆盖 %s 之后；共扫过 %v 行）", from, b.ServiceLogMeta["scanned"])
			} else if b.ServiceLogMeta["available"] == false {
				meta = "（本实例日志缓冲不可用）"
			}
		}
		emit(fmt.Sprintf("【服务日志】本实例日志里与该错误相关的行（新 → 旧）%s%s：",
			meta, shown(len(b.ServiceLogs), evidenceMaxServiceLines)))
		for i, l := range b.ServiceLogs {
			if i >= evidenceMaxServiceLines {
				break
			}
			msg := l.Line
			if m := jsonMsgOf(l.Line); m != "" {
				msg = m
			}
			emit(fmt.Sprintf("  %s %-5s %s", l.Time.Format("15:04:05.000"), l.Level, msg))
		}
	}
	if len(b.Playbook) > 0 {
		emit("【错误处理手册】同型错误已有的标准处置" + shown(len(b.Playbook), evidenceMaxPlaybookRows) + "：")
		for i, p := range b.Playbook {
			if i >= evidenceMaxPlaybookRows {
				break
			}
			emit("  - " + evidenceRowText(p, []string{"id", "enabled", "auto_execute", "title", "root_cause", "plan_kind", "hit_count", "success_count", "failure_count"}))
		}
	}
	if len(b.Notes) > 0 {
		emit("【取证说明】")
		for _, n := range b.Notes {
			emit("  - " + n)
		}
	}
	return runeTruncate(out.String(), evidenceDigestMaxRunes)
}

// evidenceRowText 按 key 顺序把一行渲染成「key: value」。
func evidenceRowText(row map[string]any, keys []string) string {
	var parts []string
	seen := map[string]bool{}
	for _, k := range keys {
		v, ok := row[k]
		if !ok {
			continue
		}
		seen[k] = true
		if s := evidenceValueText(v); s != "" {
			parts = append(parts, k+"="+s)
		}
	}
	for k, v := range row {
		if seen[k] || len(parts) >= 9 {
			continue
		}
		if s := evidenceValueText(v); s != "" && len(s) <= 80 {
			parts = append(parts, k+"="+s)
		}
	}
	return truncateRunesSafe(strings.Join(parts, " "), evidenceLineMaxRunes)
}

// evidenceValueText 把一个值渲染成短文本（容器 ⇒ 紧凑 JSON 片段）。
func evidenceValueText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return truncateRunesSafe(strings.TrimSpace(t), evidenceMaxFieldValRunes)
	case bool:
		return fmt.Sprintf("%t", t)
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%.2f", t)
	case int, int64, int32:
		return fmt.Sprintf("%d", t)
	case time.Time:
		return t.Format("2006-01-02 15:04:05")
	case []string:
		return truncateRunesSafe(strings.Join(t, " | "), evidenceMaxFieldValRunes)
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, evidenceValueText(item))
		}
		return truncateRunesSafe(strings.Join(parts, " | "), evidenceMaxFieldValRunes)
	default:
		return truncateRunesSafe(strings.TrimSpace(strings.ReplaceAll(fmt.Sprintf("%v", t), "\n", " ")), evidenceMaxFieldValRunes)
	}
}

func isTrue(v any) bool {
	b, _ := v.(bool)
	return b
}

// truncateRunesSafe 按 rune 截断并压掉换行（一行一行地进提示词）。
func truncateRunesSafe(s string, n int) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " ⏎ ")
	return runeTruncate(s, n)
}

var msgKeyNeedle = []byte(`"msg":"`)

// jsonMsgOf 从一行规范 JSON 日志里取 msg 字段（取不到 ⇒ 空串，调用方用整行）。
func jsonMsgOf(line string) string {
	i := strings.Index(line, string(msgKeyNeedle))
	if i < 0 {
		return ""
	}
	rest := line[i+len(msgKeyNeedle):]
	end := len(rest)
	for j := 0; j < len(rest); j++ {
		if rest[j] == '"' && (j == 0 || rest[j-1] != '\\') {
			end = j
			break
		}
	}
	return truncateRunesSafe(rest[:end], evidenceLineMaxRunes)
}
