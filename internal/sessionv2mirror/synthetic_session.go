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
	if isInternalAutoEntry(entry) {
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
