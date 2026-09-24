// Package sessionv2mirror — synthetic_session.go
//
// 存储优化方案 v2 §3-D4：无会话流量落点 = 合成系统会话。
//
// gw_session_id 仅约 40% 填充——探针/系统/无会话头流量占多数，S4 停写
// request_logs 后这类流量必须有落点。本文件把它们统一映射为
// client_type='system' 的合成会话（按日聚合，plan 原文形如
// sys:probe:cred123:20260914），随 session_turns 走同一条单事实管道；
// 分析侧按 client_type 过滤。不新建平行请求表（否则等于换名重建
// request_logs，违背弃用初衷）。
//
// 兼容视图（migration 710）对 'sys:%' 会话输出 gw_session_id=NULL，保真 v1
// 无会话头行在 request_logs_with_current_month 上的 NULL 语义——消费者零
// 感知；原生会话分析面看到的是完整系统会话。
package sessionv2mirror

import (
	"fmt"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// SyntheticSessionID derives the daily-aggregated system session id for a
// no-gw_session_id telemetry entry:
//
//	sys:{kind}:{cred<id>|prov<id>|gw}:{yyyymmdd}
//
// kind ∈ probe（origin_stage/origin_actor/task_type 含 "probe"，本机无会话
// 流量的 100%）| internal（title/summary 等网关内部回环）| anon（其余）。
// scope 优先凭据、退而供应商、兜底 "gw"。日期取事件时间（EventAt 回放安全）。
func SyntheticSessionID(entry *telemetry.RequestLogEntry) string {
	if entry == nil {
		return ""
	}
	ts := time.Now()
	if entry.EventAt != nil && !entry.EventAt.IsZero() {
		ts = *entry.EventAt
	}
	return fmt.Sprintf("sys:%s:%s:%s", syntheticKindOf(entry), syntheticScopeOf(entry), ts.Format("20060102"))
}

func syntheticKindOf(entry *telemetry.RequestLogEntry) string {
	for _, s := range []*string{entry.OriginStage, entry.OriginActor, entry.TaskType} {
		if s != nil && strings.Contains(strings.ToLower(*s), "probe") {
			return "probe"
		}
	}
	if telemetry.IsInternalAutoEntry(entry) {
		return "internal"
	}
	return "anon"
}

func syntheticScopeOf(entry *telemetry.RequestLogEntry) string {
	if entry.CredentialID != nil && *entry.CredentialID != 0 {
		return "cred" + intStr(*entry.CredentialID)
	}
	if entry.ProviderID != nil && *entry.ProviderID != 0 {
		return "prov" + intStr(*entry.ProviderID)
	}
	return "gw"
}

// IsProbeSyntheticSession reports whether the entry would synthesize a
// probe-kind system session（R51 教训落地，R60 S2-F4：这类会话不进 mirror）。
//
// 判据与来源（必须是探针专有的强特征，不得误伤真实用户会话）：
//
//  1. entry.GwSessionID 为空 —— 该条目在 hook 里只会走合成路径（D4）；真实
//     用户会话始终携带 gw_session_id（会话头或 gw_<hosted_task> 派生），第
//     一道判据即排除。注意：判据不查 session id 字符串前缀（"sys:probe:*"），
//     因为那是本函数派生的产物、且客户端可自报同名 GwSessionID——以
//     "无会话头"这一事实为准。
//  2. syntheticKindOf(entry) == "probe" —— origin_stage / origin_actor /
//     task_type 含 "probe"。这三个字段由网关探针 worker 专有写入
//     （bg/node_probe、credential-selfcheck、system-health；origin_stage ∈
//     node_probe | self_check | system_health | business，见
//     telemetry/client.go origin.stage/origin.actor 约定），不来自客户端
//     请求头，不与业务流量重叠。
//
// 两条件合取 ⇒ 只命中"无会话头 + 探针产出"的合成流量（154 复审 §四.5：
// mirror 失败噪声 ~115/min，99.6% 为该类 probe synthetic 会话 advisory lock
// 超时）。有会话头的探针请求（如挂在真实会话上的自检）与无会话头的
// internal/anon 系统流量都不受影响，D4 的计费事实保全面不变。
func IsProbeSyntheticSession(entry *telemetry.RequestLogEntry) bool {
	if entry == nil {
		return false
	}
	if entry.GwSessionID != nil && *entry.GwSessionID != "" {
		return false
	}
	return syntheticKindOf(entry) == "probe"
}
