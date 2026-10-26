package compression

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// §143.103（R146）：探测型合成 session 的 L3 短路（§143.99/§143.100/§143.101 的 ㉕）。
//
// ★ 背景：压缩读链的 L3 兜底腿（lastOutboundSessionSQL）在 252 生产上
//
//	【结构性恒 0 行】（§143.99.4：每次调用都返回 0 行、却每次拉 218 KB；
//	§143.100.4：L3 三形态累计 rows 全部 = 0 ⇒ 生产上一次都没命中过），
//	而探测流量合成出【按日聚合】的系统会话（sessionv2mirror.SyntheticSessionID
//	的 probe kind：sys:probe:{cred<id>|prov<id>|gw}:{yyyymmdd}）——
//	一天的全部探测流量挤进同一个 session ⇒ 轮次可达数万（252 实测单会话
//	40,381 轮）⇒ 命中它时 L3 单次 24,763 ms（§143.100 实跑读数）。
//
// ★ 为什么对探测型合成 session 短路是【零语义】的：
//
//	① 这类合成会话【不进 mirror】（sessionv2mirror/hook.go）⇒ bodies 族里
//	   没有它们的行 ⇒ L3 的两臂（sb 按 request_id 的 bodies 腿 / ff 的
//	   final_full LATERAL 腿）都取不到 ⇒ WHERE 的「有正文」判据恒假 ⇒
//	   结构性 0 行（★ 生产实测 rows=0 与机制互证）；
//	② 短路返回 (nil, nil, nil) —— 与「查询返回 0 行」的既有映射
//	   （scanLastOutboundRow 把 ErrNoRows 映射成 (nil,nil)）【逐字相同】
//	   ⇒ 调用方走【本来就存在】的 miss 分支，可观测行为不变；
//	③ 腿①②（turn_reader 的 final_full / delta chain）【不受影响】——
//	   它们对探测会话本来就便宜、且是真正在产出的腿（§143.100.4：
//	   L1 累计 12,106 行），本短路只覆盖 L3 那一条结构性空腿。
//
// ★ ★★ 现时收益定性（★ 2026-10-24 批判式审计轮 252 只读实测，★ 别夸大）：
//
//	① 类级读数：bodies hot `sys:%`=0 行 / bodies_2026_09 `sys:%`=0 行 /
//	   bodies_2026_10 `sys:%`=3 行（全部 sys:anon:* 且 has_outbound=f）
//	   ⇒ ⇒ sys:probe:% 会话在整个 bodies 族【三面全 0 行】⇒ 零语义实测闭环；
//	② ★★ 但 turns_2026_10 里 sys:probe:% = 【0 行】（09 月才有：
//	   462,394 行 / 307 会话）⇒ ⇒ 探测带头形态【10 月已消失】
//	   ⇒ ⇒ ★ 本守卫是【防御性】的：当前流量不会触发它（与 G-2 同族，
//	   §143.84 实测 L-2 窗口 0 次也是防御性）；
//	   ★ 收益在探测形态回潮时兑现（09 月形态下 L3 单次实测 24,763 ms）。
//
// ★ 开关语义（与 §143.86 / §143.89 同形）：★【opt-in 环境变量，默认关】。
// ★ 未设置 ⇒ 判定恒 false ⇒ 线上行为与改动前【逐字一致】。
// ★ ⇒ ⇒ 【不用 settings.IsEnabled】：★ 那是 kill-switch 语义
// ★ （★ 未注册的名字 fail-open ⇒ 默认「开」），实现不了「默认关」。
// ─────────────────────────────────────────────────────────────────────────────

// EnvSkipProbeSessions 是探测型 session 短路开关的环境变量名（opt-in：不设置 = 关闭）。
const EnvSkipProbeSessions = "ENABLE_SESSION_CACHE_SKIP_PROBE_SESSIONS"

// probeSyntheticSessionPrefix 是探测型合成会话 ID 的前缀
// （sessionv2mirror.SyntheticSessionID 的 probe kind 形态；完整形态为
// sys:probe:{scope}:{yyyymmdd}，scope ∈ cred<id>|prov<id>|gw）。
//
// ★ 判据用【前缀】而不是完整正则：生成器在仓内且形态稳定（§143.100 的
// 实测目标 sys:probe:cred35:20260923 即此形态）；形态漂移由门里的
// 【跨包一致性测试】钉住（用真实生成器产出 ID 断言本判定认得它）。
const probeSyntheticSessionPrefix = "sys:probe:"

// skipProbeSessionsEnabledDefault 读取 opt-in 开关（纯函数形态，便于单测直接覆盖）。
//
// 语义：未设置 / 空 ⇒ 关；"1"/"true"/"yes"/"on" ⇒ 开；
// 其它非空值 ⇒ 开（与 §143.86 的取值风格一致）。
func skipProbeSessionsEnabledDefault() bool {
	v := strings.TrimSpace(os.Getenv(EnvSkipProbeSessions))
	if v == "" {
		return false // 默认关
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return v != ""
	}
	return b
}

// skipProbeSessionsEnabled 进程内只读一次（sync.OnceValue，与仓库既有 opt-in 同形）。
var skipProbeSessionsEnabled = sync.OnceValue(skipProbeSessionsEnabledDefault)

// IsProbeSyntheticSessionID 报告 session ID 是否为探测型合成会话的形态。
//
// ★ 判据 = 前缀 "sys:probe:"。真实用户会话的 gw_session_id 来自会话头，
// 命名空间上与合成系统会话（sys:{kind}:{scope}:{date}）分离；即便有客户端
// 伪造此前缀，短路掉的也只是「腿①② 已落空之后的 L3 结构性空腿」
// ⇒ 可观测行为仍与「查询返回 0 行」一致。
func IsProbeSyntheticSessionID(gwSessionID string) bool {
	return strings.HasPrefix(gwSessionID, probeSyntheticSessionPrefix)
}

// ShouldSkipProbeLoad 报告「对这个 session 的 L3 数据库读可以短路」。
//
// 双条件：① 开关已打开（默认关）；② session ID 是探测型合成形态。
// 判定不含 ctx —— 与 G-2（ShouldSkipLegacyLoad）不同：探测形态由
// session ID 自身完全决定，不存在「ctx 里的会话 ≠ 正在查的会话」的误伤面。
func ShouldSkipProbeLoad(gwSessionID string) bool {
	if !skipProbeSessionsEnabled() {
		return false
	}
	return IsProbeSyntheticSessionID(gwSessionID)
}
