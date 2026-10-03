// v1_freeze_notice.go — 「v1 数据已停更」的单一告示源（审计 §9.73.7，③-4 裁决「接受冻结 + UI 标注」）。
//
// # 这份文件在解决什么
//
// `silently_frozen` 那一档（21 个读点）的性质是：**查询照常成功、照常有行、
// 照常有形状，只是内容永久停在停写前一刻，没有任何错误信号。**
// §9.48.5 把它们称作「最该先处理」的一档，③-4 的裁决是**接受冻结**，
// 但要求在 UI 上让人**看见**这件事。
//
// ⚠ **本文件刻意不解决「让冻结可见到每个读点」**。21 个读点分属 11 个
// UI 可见的 API 与 10 个后台 worker/bench，对后者「UI 标注」不是正确的概念
// （它们不面向用户）。逐个读点接线会产出一个覆盖面看着很全、实际只有一半
// 成立的登记表 —— 那正是本审计反复在记的「装饰面」形态。
//
// ⇒ 这里做的是**页面级**的告示：一个全局横幅。
//   · 粒度诚实：它说的是「本平台的 v1 流量数据当前已停更」这一**平台事实**，
//     而不是一个端点级的断言。
//   · 覆盖面完整：所有 UI 页面都基于 v1 派生数字，一个横幅覆盖全部，
//     而逐端点接线做不到（且会漏掉新增端点）。
//   · 不撒谎：写门**开着**时告示必须消失。若横幅一直挂着，它就成了背景噪音，
//     等真正停写那天没人会当回事 —— 与 §9.65.8「用完即焚的面」同源。
//
// # 真值从哪来
//
// `settings.RequestLogsWriteEnabled()` —— **与写入方同一个读点**。
// 不是「看库里 MAX(ts) 推算」：那种做法在表被清空、或第一次写入尚未发生时
// 会给出相反的结论，而写门是这个系统里已经存在的、明确的意图声明。
//
// 为什么不存一个「停写时间戳」：写门是可以被重新打开的，存时间戳会在
// 闸门重新打开后留下一条过期的告示（另一种「过期的例外登记」）。
//
// # ⚠ 「读到 true」有两种成因，而告示端点必须能区分（审计 §9.79.8）
//
// `settings.RequestLogsWriteEnabled()` → `GetPlatformBool(key, true)`
// 有**三个回落点**（`settings/helpers.go:83-101`）：`Global == nil`、
// `Global.Spec(key) == nil`、`len(raw) == 0`，**三个全部返回 fallback，
// 也就是全部返回 true**。
//
// 对**写入方**这个 fail-open 方向是对的（宁可继续双写，也不要静默丢数据）。
// 但对**告示**方向是反的：横幅会把「我读到了 true」和「**我根本没读到**」
// 显示成同一件事，而后者发生时 UI 会说「数据仍在正常写入」——
// **把「不知道」显示成「知道」**。
//
// ⇒ 本文件**只**在告示层加一个 `unavailable` 态：
//   · 写入方读点 `settings.RequestLogsWriteEnabled()` **一个字都不改**
//     （改它会改 S4 停写语义，且那个 fail-open 是对的）；
//   · 告示层额外读一次 `EffectiveValue` 的 `source`（DB / env / default）。
//     `default` 意味着**这个值不是任何人在 DB 里设的**，是回落来的
//     ⇒ 产出 `unavailable`，不是 `live`。
//
// ⚠ **实测现状**（252，2026-10-03）：`settings_kv` 只有 41 行，
// `storage.request_logs_write_enabled` **不在其中** ⇒ 线上今天就是 `default`。
// ⇒ **这个改动上线后，生产会常显「无法确认」横幅**——那是**正确行为**
// （这个键确实从未被显式配置过，停写与否从未有人工决策过），
// 但它是一个**可见的 UI 变化**，要 seed 键 = true 才能回到干净的 `live`。

package admin

import (
	"net/http"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// V1FreezeHeader 是携带告示的响应头名。
//
// 选响应头而不是只靠 JSON 字段，是因为 admin 的 JSON 出口有多个
// （writeJSON / writeJSONOk / 各 SSE 流），而**头是唯一对全部出口都成立**的通道。
// 前端 axios 层目前只透传 body（见 web/src/api/_core.ts），
// 所以 UI 走的是下面的独立端点；这个头是给非浏览器消费者与抓包用的。
const V1FreezeHeader = "X-LLM-Gateway-V1-Data-Frozen"

// V1FreezeNotice 是告示的机器可读形态。
//
// `Frozen` 为 true 表示**明确读到停写**；为 false 时**必须**看 `Unknown`：
// false + Unknown=false 不会出现（那会退回「有个对象但说没冻结」的老坑），
// false + Unknown=true 才是合法的第二种形态（见 §9.79.8）。
//
// 写门开着时调用方拿到的是 (nil, false)，而不是一个 Frozen=false 的对象：
// 「没冻结」和「有对象但说没冻结」在前端是两种不同的处理路径，
// 混起来就会出现「横幅永远显示」这类静默错误。
type V1FreezeNotice struct {
	// Frozen 恒为 true，见类型注释。
	Frozen bool `json:"frozen"`
	// Unknown 表示「**读点没有给出可信答案**」——值为 true 时
	// Frozen 必为 false，且 Effect/Silence 刻意都不断言数据是新的还是停更的。
	// §9.79.8：这是本轮为「回落 true 与显式 true 不可区分」加的第三态。
	Unknown bool `json:"unknown"`
	// Source 是被冻结的读源族。
	Source string `json:"source"`
	// GateKey 是控制它的那个 settings 键（运维要知道改哪里）。
	GateKey string `json:"gate_key"`
	// Effect 用一句话说明「看到这些数字意味着什么」——这是给人读的。
	Effect string `json:"effect"`
	// Silence 是最要紧的一句：这类读点**没有错误信号**，
	// 所以「接口没报错」不能被读成「数据是新的」。
	Silence string `json:"silence"`
	// Affects 列出受影响的读点档位（不是逐个文件——见文件头的作用域说明）。
	Affects []string `json:"affects"`
}

// v1FreezeNotice 返回当前是否需要告示。
//
// 第二个返回值区分「没有告示」与「告示为 nil」，让调用点无法静默忽略它。
// v1FreezeNoticeFor 是**纯函数**形态的告示构造器。
//
// 为什么要抽成纯函数：`settings.RequestLogsWriteEnabled()` **没有能在测试里翻转
// 的注入口**（它只有 `GetPlatformBool` 读器，settings store 没有 test setter）。
// §9.44 正是被这一点逼着把 settle 的 SQL 抽成 `settleBaselinesSQL(src)`，才让
// 会话族分支第一次可测；同一堵墙在这里再遇到一次。
//
// ⇒ 门可以逐格测两种状态，而不必去改全局 store（改 store 的门会互相干扰，
// 且一个测试翻转了不恢复就会污染同包其它测试）。
// V1GateState 是告示层对写门状态的判定结果。
//
// 三态而不是两态，且 `Unavailable` 刻意**独立于** `Live`/`Frozen`：
// 它表达的是「**读点没有给出可信答案**」，而 live/frozen 表达的是
// 「读点说它是 live/frozen」。合并成一个 bool 就会回到 §9.79.8 那个坑。
type V1GateState string

const (
	// V1GateLive：写门明确为 true（读到了 DB/env 里的值）。
	V1GateLive V1GateState = "live"
	// V1GateFrozen：写门明确为 false。
	V1GateFrozen V1GateState = "frozen"
	// V1GateUnavailable：值不是配置来的，是回落来的（`EffectiveValue` 的
	// source == "default"），或者 settings store 根本不可用。
	// ⇒ **不能**说成 live，也不能说成 frozen。
	V1GateUnavailable V1GateState = "unavailable"
)

// V1GateSourceDefault 是 settings 的回落来源标签。
// `settings/spec.go:284` `EffectiveValue` 返回的 source ∈ {"db","env","default"}。
const V1GateSourceDefault = "default"

// v1GateState 决定告示状态。**纯函数**。
//
// 为什么 `settings.Global` 为 nil 归为 unavailable 而不是 live：
// 写入方在那一刻同样读的是 fallback true（fail-open，继续写），
// 但告示要回答的是「**这批数据的写入门到底有没有被打开过**」，
// store 都没初始化 ⇒ 没人做过这个决策 ⇒ 不能说 live。
//
// ⚠ **空 source 与 "default" 同义**，不是 live：
// `EffectiveValue` 在出错时返回 `("", err)`（`settings/spec.go:287`），
// 而调用方把 err 与空串一起折叠成 default。此处若把空串当「显式」，
// **读失败就会显示成「数据是新的」**——那正是本函数存在的理由被自己破坏。
func v1GateState(logsWriteEnabled bool, source string) V1GateState {
	if source == "" || source == V1GateSourceDefault {
		return V1GateUnavailable
	}
	if logsWriteEnabled {
		return V1GateLive
	}
	return V1GateFrozen
}

// readV1GateSource 返回写门值的来源标签。
//
// ⚠ **只读不写、且不碰写入方读点**。写不出值时返回 V1GateSourceDefault
// （`EffectiveValue` 在 Global/spec 缺失时也走 default 分支，语义一致）。
func readV1GateSource() string {
	if settings.Global == nil {
		return V1GateSourceDefault
	}
	sp := settings.Global.Spec(settings.KeyRequestLogsWriteEnabled)
	if sp == nil {
		return V1GateSourceDefault
	}
	_, source, err := settings.Global.EffectiveValue(sp.Scope, settings.KeyRequestLogsWriteEnabled, "")
	if err != nil || source == "" {
		return V1GateSourceDefault
	}
	return source
}

func v1FreezeNoticeFor(logsWriteEnabled bool, source ...string) (*V1FreezeNotice, bool) {
	// ⚠⚠ 缺省值是 **`default`（unknown），不是 `db`（live）**。
	//
	// 审计订正（2026-10-03 18:40）：我第一稿把缺省写成 `"db"`，
	// 理由是「不破坏既有 (bool) 调用点，让它们继续测两态」。
	// **那个理由是错的**，而且它恰好重新引入了本节要治的病：
	// 将来任何人新增一个 `v1FreezeNoticeFor(x)` 单参调用点，
	// 会**静默落到 live** ——「以为知道」而实际没读。
	// ⇒ 缺省必须选**保守**的一侧：读不到就说读不到。
	// 而真正需要「明确知道」的生产调用点
	// （`v1FreezeNotice()`）**显式传** `readV1GateSource()`，
	// 由 `TestV1FreezeNoticeIsWiredToTheWriteGateThatTheWritersUse` 钉住。
	//
	// 既有单参测试调用点**照样可用**：它们落进 unknown 态，
	// 而那正是「键不在配置里」的真实形态，与 252 现状一致。
	src := V1GateSourceDefault
	if len(source) > 0 {
		src = source[0]
	}

	switch v1GateState(logsWriteEnabled, src) {
	case V1GateLive:
		// 写门开着 ⇒ 数据是新的 ⇒ **不告示**。
		//
		// 这一支必须存在且必须先判：一个「永远显示」的横幅在真正停写那天
		// 的信息量是零，而它的成本是用户学会忽略它。
		return nil, false
	case V1GateUnavailable:
		// ⚠ **不是 live 也不是 frozen**。文案刻意不说「已停更」——
		// 说它就是在编一个没测到的事实；也不说「仍在写入」，
		// 因为那同样没测到。只说「不知道」。
		return &V1FreezeNotice{
			Frozen:  false,
			Unknown: true,
			Source:  "request_logs",
			GateKey: settings.KeyRequestLogsWriteEnabled,
			Effect:  "无法确认 v1 读源族的停写状态：该 gate 键在配置里没有显式取值，当前读到的值来自默认值回落。",
			Silence: "两种可能都未经验证：写入可能仍在继续，也可能已经停写。请按 GateKey 显式配置该键，让下一次读取给出确定答案。",
			Affects: []string{"silently_frozen", "silently_degraded_content", "silently_empty"},
		}, true
	}
	return &V1FreezeNotice{
		Frozen:  true,
		Source:  "request_logs",
		GateKey: settings.KeyRequestLogsWriteEnabled,
		Effect:  "本页及基于本页数据的判断只反映停写之前的流量；新请求不再进入这张表。",
		Silence: "这一类读点没有任何错误信号：接口照常 200、结果集非空、字段齐全。接口没报错不代表数据是新的。",
		Affects: []string{"silently_frozen", "silently_degraded_content", "silently_empty"},
	}, true
}

// v1FreezeNotice 是接线到 settings 的那一层；逻辑全在纯函数里。
func v1FreezeNotice() (*V1FreezeNotice, bool) {
	return v1FreezeNoticeFor(settings.RequestLogsWriteEnabled(), readV1GateSource())
}

// applyV1FreezeNotice 把告示写进响应头。
//
// 挂在**中央**而不是逐端点：逐端点挂载会漏掉新增端点，而漏掉的那一个
// 恰恰是最容易被信任的那个（它是新的，没人记得它该标注）。
func applyV1FreezeNotice(w http.ResponseWriter) {
	notice, ok := v1FreezeNotice()
	if !ok {
		return
	}
	if notice.Unknown {
		// 与 frozen **用不同的头值**：抓包/非浏览器消费者必须能区分
		// 「已停更」和「不知道」，否则它们和前端一样会误读。
		w.Header().Set(V1FreezeHeader, "unknown")
		return
	}
	w.Header().Set(V1FreezeHeader, "1")
}

// handleV1DataHorizon 是 UI 拉取告示的端点。
//
// 单独一个端点而不是「把告示塞进每个响应」：admin 的响应体是各端点自己的结构，
// 往里塞字段要改 20+ 处 struct，而那 20+ 处就是 20+ 个可能漏改的地方。
// 一个独立端点 + 一个全局横幅，读起来也更清楚。
func (h *Handler) handleV1DataHorizon(w http.ResponseWriter, r *http.Request) {
	applyV1FreezeNotice(w)
	if notice, ok := v1FreezeNotice(); ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"v1_data_horizon": notice,
			"//":              "frozen=true 表示 v1 读源族当前已停更；本对象不存在即表示未停更。",
		})
		return
	}
	// 未冻结：**不要**返回一个 frozen=false 的对象（见 V1FreezeNotice 注释）。
	writeJSON(w, http.StatusOK, map[string]any{
		"v1_data_horizon": nil,
		"//":              "frozen=true 表示 v1 读源族当前已停更；本对象不存在即表示未停更。",
	})
}
