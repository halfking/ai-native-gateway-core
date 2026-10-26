package compression

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// §143.89：「读链走到 L3」的【服务侧埋点】—— ★ 闭合 s 的必要前置。
//
// ★ 背景（★ runbook §143.88.5/§143.88.6）：★ s ＝ 「库端 L3 调用里，来自 v2 来源的那部分」。
// ★ ★★ 它算不出来，是因为【库端没有 session 级痕迹】⇒ ⇒ 只能靠【服务侧】按 session_id 对拍。
// ★ ⇒ ⇒ 而对拍需要一个【精确对应 L3 调用】的服务侧标记 ⇒ ⇒ 就是本文件。
//
// ★ ★★ 为什么埋点放在【loadFromLegacyDB 内、且在 L3 查询之前】：
//
//	① ★★ 「走到 L3」＝ 进入 loadFromLegacyDB 且 db 非空 ⇒ ⇒ 它【精确对应】一次 L3 查询；
//	② ★★ 它同时覆盖【两条路径】：★ 本地链尾（loadFromDB → loadFromLegacyDB）
//	   与【共享路径直连】（session_cache_shared.go 的 legacy 校验分支）⇒ ⇒
//	   ⇒ 服务侧计数 ＝ 库端 L3 Δcalls 的【同侧对应物】⇒ ⇒ 可以直接对拍；
//	③ ★★ 它【必须】在 §143.86 的 G-2 守卫【之后】⇒ ⇒ 被短路的那些【不算】「走到 L3」
//	   ⇒ ⇒ 开关打开后，服务侧计数 ＝ 【真正发起过 L3 查询】的次数。
//
// ★ ★★ 开关语义（★ 与 §143.86 同形，★ 默认关）：★ 【opt-in 环境变量】，
// ★ 未设置 = 关闭 ⇒ ⇒ 部署后线上行为与改动前【逐字一致】。
// ★ ⇒ ⇒ 【不用 settings.IsEnabled】：★ 那是 kill-switch 语义（★ 未注册 ⇒ fail-open ⇒ 默认「开」）。
// ─────────────────────────────────────────────────────────────────────────────

// EnvL3ReachedLog 是「走到 L3」埋点开关的环境变量名（★ opt-in：★ 不设置 = 关闭）。
const EnvL3ReachedLog = "ENABLE_SESSION_CACHE_L3_REACHED_LOG"

// l3ReachedLogEnabledDefault 读取 opt-in 开关（★ 纯函数形态，★ 便于单测直接覆盖）。
//
// ★ 语义：★ 未设置 / 空 ⇒ 【关】；★ "1"/"true"/"yes"/"on" ⇒ 【开】；
// ★ 其它非空值 ⇒ 【开】（★ 与 settings.readKillSwitch 的取值风格一致，★ 但【方向相反】）。
func l3ReachedLogEnabledDefault() bool {
	v := strings.TrimSpace(os.Getenv(EnvL3ReachedLog))
	if v == "" {
		return false // ★ 默认关
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return v != ""
	}
	return b
}

// l3ReachedLogEnabled 进程内只读一次（★ sync.OnceValue，★ 与仓库既有 opt-in 同形）。
var l3ReachedLogEnabled = sync.OnceValue(l3ReachedLogEnabledDefault)

// LogL3Reached 记录「本次读链走到了 L3 查询」。
//
// ★ ★★ 它是【纯观测】⇒ ⇒ 不改变任何控制流 ⇒ ⇒ 即使开关打开，
// ★ ★★ 也不会影响返回值、错误语义或查询次数（★ 只多一行日志）。
//
// ★ ★★ sessionID 为空时【不记】（★ 不制造无意义的条目；★ 也便于按 session 求交时过滤）。
func LogL3Reached(ctx context.Context, sessionID, tenantID string) {
	if sessionID == "" {
		return
	}
	if !l3ReachedLogEnabled() {
		return
	}
	slog.InfoContext(ctx, "session_cache: read chain reached L3",
		"session", sessionID, "tenant", tenantID)
}
