package compression

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// §143.86：读侧短路（落法 ⓪）的【标志载体】与判定。
//
// ★ 背景：★ 一次「网关自动新建会话」的冷启动，压缩读链会依次尝试
//
//	① LoadLatestOutbound(kind='final_full')   ← 命中即止
//	② LoadLatestOutbound(ORDER BY turn_no DESC) ← 命中即止
//	③ LoadChain
//	④ L3（lastOutboundSessionSQL）
//
// ★ 而这条会话【本轮刚被创建】⇒ ⇒ 库里【必然】没有它的历史 outbound body
// ⇒ ⇒ ⇒ 整条读链【必然落空】⇒ ⇒ ⇒ 可以安全短路。
//
// ★ 设计依据（runbook §143.85）：
//
//	① 载体 = ctx（本文件）★ ⇒ 因为三个触发入口都把 ctx 传给 GetOrLoad，
//	   ★ 而恢复/重试路径【拿不到 assignment】⇒ ⇒ 显式参数覆盖不到它。
//	② 依赖方向 = streaming → compression ⇒ ⇒ 本文件【必须】放在 compression 包。
//	③ 判据 = 【AutoCreated + sessionID 相等】★★ 双条件，不是一个 bool：
//	   ★ 恢复路径的 gwSessionID 来自【请求头】⇒ ⇒ 可能与本次会话不同
//	   ⇒ ⇒ 只传 bool 会误伤别的会话。
//
// ★ ★★ 开关语义（★ 2026-10-23 实测修正，★ 差点写错）：★ 【不能用 settings.IsEnabled】
// ★ settings.IsEnabled 是【kill-switch】语义：★ 未注册的名字【fail-open ⇒ 默认「开」】
// ★ ⇒ ⇒ 用它实现「默认关」【根本实现不了】⇒ ⇒ 这里改用【独立的 opt-in 环境变量】。
// ★ ⇒ ⇒ 与仓库既有 opt-in 先例同形（★ domains/transformation/paramreg_flag.go）。
// ─────────────────────────────────────────────────────────────────────────────

// EnvSkipAutoCreated 是短路开关的环境变量名（★ opt-in：★ 不设置 = 关闭）。
//
// ★ ★★ 【默认关】是本设计的安全底线：★ 即使代码已部署，只要这个变量没打开，
// ★ ⇒ ⇒ 线上行为与改动前【逐字一致】⇒ ⇒ 满足「修正时尽量不破坏现有系统运行」。
const EnvSkipAutoCreated = "ENABLE_SESSION_CACHE_SKIP_AUTO_CREATED"

// skipAutoCreatedEnabledDefault 读取 opt-in 开关（★ 纯函数形态，★ 便于单测直接覆盖）。
//
// ★ 语义：★ 未设置 / 空 ⇒ 【关】；★ "1"/"true"/"yes"/"on" ⇒ 【开】；
// ★ 其它非空值 ⇒ 【开】（★ 与 settings.readKillSwitch 的取值风格保持一致，★ 但【方向相反】）。
func skipAutoCreatedEnabledDefault() bool {
	v := strings.TrimSpace(os.Getenv(EnvSkipAutoCreated))
	if v == "" {
		return false // ★ 默认关
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return v != ""
	}
	return b
}

// skipAutoCreatedEnabled 进程内只读一次（★ sync.OnceValue，★ 与仓库既有 opt-in 同形）。
var skipAutoCreatedEnabled = sync.OnceValue(skipAutoCreatedEnabledDefault)

// autoCreatedSessionKey 是 ctx 键（★ 私有空结构体 ⇒ 不会与其它包的键撞车）。
type autoCreatedSessionKey struct{}

// WithAutoCreatedSession 标记「本次请求由网关自动新建了会话」，并带上那个会话 ID。
//
// ★ ctx 为 nil 或 sessionID 为空时【原样返回 ctx】（★ 不制造空标记）。
func WithAutoCreatedSession(ctx context.Context, sessionID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if sessionID == "" {
		return ctx
	}
	return context.WithValue(ctx, autoCreatedSessionKey{}, sessionID)
}

// AutoCreatedSessionID 返回本次请求自动新建的会话 ID（★ 没有则 ""）。
func AutoCreatedSessionID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(autoCreatedSessionKey{}).(string)
	return v
}

// ShouldSkipLegacyLoad 报告「对 gwSessionID 的数据库读【必然落空】」。
//
// ★ ★★ 【双条件】，缺一不可：
//
//	① 开关已打开（★ 默认关）；
//	② ctx 里的自动新建会话 ID == 正在查的 gwSessionID。
//
// ★ ⇒ ⇒ 第②个条件是【零误伤护栏】：★ 恢复/重试路径的 gwSessionID 来自【请求头】，
// ★ ⇒ ⇒ 它【可能】与本次请求实际使用的会话不同 ⇒ ⇒ 不等就【不短路】。
func ShouldSkipLegacyLoad(ctx context.Context, gwSessionID string) bool {
	if !skipAutoCreatedEnabled() {
		return false
	}
	id := AutoCreatedSessionID(ctx)
	return id != "" && gwSessionID != "" && id == gwSessionID
}
