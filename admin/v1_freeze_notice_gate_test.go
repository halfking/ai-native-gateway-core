// v1_freeze_notice_gate_test.go — ③-4 裁决「接受冻结 + UI 标注」的门（审计 §9.73.7）。
//
// # 这道门在防什么
//
// `silently_frozen` 那一档的失效形态是「**没有任何错误信号**」。
// 因此一条关于它的告示，最危险的两种失效都是**静默**的：
//
//	① 永远显示 —— 写门开着也挂横幅。用户学会忽略它，真正停写那天它信息量为零。
//	   而且这类错误在灰度前完全看不出来（那时候写门本来就是开的，横幅本就不该在）。
//	② 永远不显示 —— 挂载点被删/被绕过。所有 UI 页面照常展示停写前的数字，
//	   而**所有门都绿**，因为「没有报错」正是这一档的定义。
//
// ⇒ 这道门必须**逐格**覆盖写门的开与关两种状态。只测其中一种，
// 上面两条里的另一条就是绿的。
//
// # 为什么构造器是纯函数
//
// `settings.RequestLogsWriteEnabled()` 没有 test 注入点（settings store 只有
// `GetPlatformBool` 读器）。门若去改全局 store，同包其它测试会被污染。
// ⇒ 逻辑放在 `v1FreezeNoticeFor(bool)`，门逐格调纯函数；接线那层
// （`v1FreezeNotice()`）由下面那道形状判据钉住。
package admin

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestV1FreezeNoticeIsAbsentWhenWritesAreOn —— 钉住失效形态 ①。
//
// ⚠ 2026-10-03 审计订正：显式传 `"db"`（= **明确读到**停写状态）。
// 原来按单参调用，而单参缺省现刻意表示 **unknown**（见
// TestV1FreezeNoticeDefaultSourceIsConservative）——
// 本门测的是「确知写门开着 ⇒ 不告示」，不是「没读到状态」。
func TestV1FreezeNoticeIsAbsentWhenWritesAreOn(t *testing.T) {
	notice, ok := v1FreezeNoticeFor(true, "db")
	if ok {
		t.Errorf("写门**开着**时返回了告示（ok=true）：%+v\n"+
			"  一个「永远显示」的横幅在真正停写那天的信息量是零，而它的成本是用户学会忽略它。\n"+
			"  更要紧的是：灰度前写门本来就是开的，所以**这一类错误在灰度前完全看不出来**。", notice)
	}
	if notice != nil {
		t.Errorf("写门开着时返回了非 nil 告示：%+v\n"+
			"  调用点若忽略 ok 就会照样显示它。", notice)
	}
}

// TestV1FreezeNoticeIsPresentWhenWritesAreOff —— 钉住失效形态 ②。
//
// ⚠ 2026-10-03 审计订正：同样显式传 `"db"`，理由同上一条。
func TestV1FreezeNoticeIsPresentWhenWritesAreOff(t *testing.T) {
	notice, ok := v1FreezeNoticeFor(false, "db")
	if !ok || notice == nil {
		t.Fatalf("写门**关闭**时没有告示（ok=%v, notice=%v）。\n"+
			"  这正是 silently_frozen 的定义形态：一切照常、只是内容永久停在停写前一刻。\n"+
			"  没有告示 = 没有任何人能从 UI 上看出这件事。", ok, notice)
	}
	if !notice.Frozen {
		t.Error("告示对象存在但 Frozen=false。\n" +
			"  设计上**不存在** Frozen=false 的实例：未冻结时调用方拿到的是 (nil,false)。\n" +
			"  「有对象但说没冻结」在前端是另一条处理路径，混起来就会出现「横幅永远显示」。")
	}
	// 内容断言：最要紧的是 Silence 那句——「接口没报错不代表数据是新的」。
	// 少这一句，横幅就只是在陈述一个技术事实，而用户需要知道的是**怎么读它**。
	for name, got := range map[string]string{
		"Silence": notice.Silence,
		"Effect":  notice.Effect,
		"GateKey": notice.GateKey,
		"Source":  notice.Source,
	} {
		if strings.TrimSpace(got) == "" {
			t.Errorf("告示的 %s 是空的。一条缺字段的告示在 UI 上会渲染成半截话，\n"+
				"  而半截话比没有更糟：它看起来像有解释。", name)
		}
	}
	if !strings.Contains(notice.Silence, "不代表数据是新的") {
		t.Errorf("Silence 字段没有点明「接口没报错不代表数据是新的」，实际是：%q", notice.Silence)
	}
	if notice.GateKey != "storage.request_logs_write_enabled" {
		t.Errorf("GateKey = %q，不是写门那个键。运维需要知道改哪里；\n"+
			"  指错键等于给了一个会去改错地方的人。", notice.GateKey)
	}
	if len(notice.Affects) == 0 {
		t.Error("Affects 为空：告示没说清影响哪些读点档位。")
	}
}

// TestV1FreezeNoticeIsWiredToTheWriteGateThatTheWritersUse 钉住「真值只有一个」。
//
// 告示必须由**写入方同一个读点**推导。写成「查库里的 MAX(ts)」会在两种情况下
// 给出相反的结论：表被清空时、或第一次写入尚未发生时。
// 而更要紧的是「两份门」——告示读一个键、写入方读另一个键，
// 于是会出现「写门关了但告示不显示」（= 失效形态 ② 的静默版本）。
func TestV1FreezeNoticeIsWiredToTheWriteGateThatTheWritersUse(t *testing.T) {
	raw, err := os.ReadFile("v1_freeze_notice.go")
	if err != nil {
		t.Fatalf("读取 v1_freeze_notice.go 失败 %v", err)
	}
	src := string(raw)
	// 接线层必须调 settings.RequestLogsWriteEnabled()，且**只能**调这一处真值。
	//
	// ⚠ §9.79.8：调用形态在 2026-10-03 从 (bool) 变成 (bool, source) ——
	// 第二个参数是**同一个键**的来源标签（`EffectiveValue` 的 source），
	// 用来区分「显式配置的 true」与「回落来的 true」。
	// ⇒ 判据从「字面量完全相等」改成**块形状**：仍必须调
	// `settings.RequestLogsWriteEnabled()`，且 source 参数必须来自
	// `readV1GateSource()`（不能是别的值）。
	//
	// 为什么不能直接把字面量换成新的：那正是 §9.71 那条教训——
	// 用「逐字等于当前实现」当判据，等于让实现带着门一起变。
	wiringStart := strings.Index(src, "func v1FreezeNotice()")
	wiringEnd := strings.Index(src, "func applyV1FreezeNotice(")
	if wiringStart < 0 || wiringEnd < 0 || wiringEnd <= wiringStart {
		t.Fatalf("找不到接线层 v1FreezeNotice 的边界（start=%d end=%d）。\n"+
			"  解析不到却继续跑 ⇒ 下面「真值唯一」判据会落空。", wiringStart, wiringEnd)
	}
	wiring := src[wiringStart:wiringEnd]
	if !strings.Contains(wiring, "settings.RequestLogsWriteEnabled()") {
		t.Errorf("接线层 v1FreezeNotice() 没有调 settings.RequestLogsWriteEnabled()。\n"+
			"  告示必须与写入方读**同一个**写门；自己再造一个判据 = 两份真相源 =\n"+
			"  「写门关了但告示不显示」，而这一档的错误信号本来就是零。\n"+
			"  当前实现：\n%s", wiring)
	}
	if !strings.Contains(wiring, "readV1GateSource()") {
		t.Errorf("接线层没有传 readV1GateSource() 作为 source。\n"+
			"  §9.79.8：source 决定「显式 true」还是「回落 true」。不传它 =\n"+
			"  第三态永远进不去，而「读不到」会继续被显示成「数据是新的」。\n"+
			"  当前实现：\n%s", wiring)
	}
	// 纯函数那一层不得读全局：它一旦读了全局，就无法逐格测两种状态。
	pureStart := strings.Index(src, "func v1FreezeNoticeFor(")
	pureEnd := strings.Index(src, "// v1FreezeNotice 是接线到 settings")
	if pureStart < 0 || pureEnd < 0 || pureEnd < pureStart {
		t.Fatalf("找不到纯函数 v1FreezeNoticeFor 的边界（start=%d end=%d）。\n"+
			"  解析不到却继续跑 ⇒ 下面那条「纯函数不得读全局」判据会落空。", pureStart, pureEnd)
	}
	pure := src[pureStart:pureEnd]
	if strings.Contains(pure, "settings.RequestLogsWriteEnabled()") {
		t.Error("纯函数 v1FreezeNoticeFor 里读了 settings.RequestLogsWriteEnabled()。\n" +
			"  纯函数必须只依赖入参，否则门无法逐格测「写门开」与「写门关」两种状态 ——\n" +
			"  而只测一种状态正好放过「永远显示」与「永远不显示」中的一个。")
	}
	// ★ 自指断言：边界之间必须真的有代码。
	if len(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(pure), "func v1FreezeNoticeFor("))) < 50 {
		t.Error("纯函数体几乎为空（<50 字符）。\n" +
			"  解析边界可能切错了位置，而本门会因为「没读到读全局」而自动通过。")
	}
}

// TestV1FreezeNoticeIsAttachedAtTheCentralJSONOutlets 钉住「挂载点是中央而不是逐端点」。
//
// 逐端点挂载会漏掉**新增**端点，而漏掉的那一个恰恰是最容易被信任的那个
// （它是新的，没人记得它该标注）。⇒ 断言挂在 admin 的两个 JSON 出口上。
func TestV1FreezeNoticeIsAttachedAtTheCentralJSONOutlets(t *testing.T) {
	for _, tc := range []struct{ file, symbol string }{
		{"handler.go", "func writeJSON(w http.ResponseWriter, status int, v any) {"},
		{"auto_route.go", "func writeJSONOk(w http.ResponseWriter, v interface{}) {"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(".", tc.file))
			if err != nil {
				t.Fatalf("读取 %s 失败 %v", tc.file, err)
			}
			src := string(raw)
			at := strings.Index(src, tc.symbol)
			if at < 0 {
				t.Fatalf("%s 里找不到 %q —— 出口改名了？\n"+
					"  本门靠函数体起始位置定位挂载点；找不到就继续跑会让本门自动通过。", tc.file, tc.symbol)
			}
			// 取该函数体：到下一个顶格 `func ` 为止。
			body := src[at:]
			if n := strings.Index(body[1:], "\nfunc "); n > 0 {
				body = body[:n+1]
			}
			if !strings.Contains(body, "applyV1FreezeNotice(") {
				t.Errorf("%s 的 %s 里没有 applyV1FreezeNotice(w)。\n"+
					"  告示必须挂在**中央出口**：逐端点挂载会漏掉新增端点，\n"+
					"  而漏掉的那一个正是最容易被信任的那个（它是新的，没人记得它该标注）。\n"+
					"  另一个方向同样要防：中央挂了、个别端点**又**挂一次，会让告示在\n"+
					"  同一进程内出现两个来源（写门状态随时可翻转，两者可能不一致）。", tc.file, tc.symbol)
			}
		})
	}
}

// TestV1DataHorizonEndpointShape 钉住端点契约。
//
// ⚠ §9.80.7：这条门在 2026-10-03 第一次跑就红，而**它红得对**。
// 它原来的假设是「`GetPlatformBool` 默认 true ⇒ 端点返回 null（= live）」，
// 但 252 实测 `storage.request_logs_write_enabled` **不在 `settings_kv` 里**
// （表只有 41 行）⇒ source == "default" ⇒ 端点返回 **unknown 对象**。
// 而「该键从未被显式配置过」正是第三态要表达的事实。
//
// ⇒ 门改成断言**三态契约**（而不是「只可能是 null」）：
//
//	· 键必须存在（undefined 与 null 不同，见下）
//	· 值是 null **或** 一个带 `unknown` 字段的对象
//	· 对象形态下 `unknown:true ⇒ frozen:false`（不得同时为 true）
//	· 绝不接受 `frozen:false && unknown:false` 的对象
func TestV1DataHorizonEndpointShape(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.handleV1DataHorizon(rec, httptest.NewRequest("GET", "/api/admin/v1-data-horizon", nil))
	if rec.Code != 200 {
		t.Fatalf("状态码 = %d，期望 200（这个端点不能报错：它报的就是「有没有静默失效」）。", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON：%v（体：%s）", err, rec.Body.String())
	}
	// 键必须存在，且是 null **或**一个告示对象 —— 不是「键不存在」。
	// undefined 与 null 在 `!==` 下是同一种，而前端判据是 `=== null`。
	v, present := body["v1_data_horizon"]
	if !present {
		t.Errorf("响应缺 `v1_data_horizon` 键（体：%s）。\n"+
			"  前端按「该键为 null ⇒ 未冻结」判；键不存在会让它拿到 undefined，\n"+
			"  而 undefined 与 null 在 `!==` 下是同一种 ⇒ 判据必须显式约定。", rec.Body.String())
	}
	if v != nil {
		obj, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("v1_data_horizon 既不是 null 也不是对象：%#v", v)
		}
		unknown, hasUnknown := obj["unknown"].(bool)
		if !hasUnknown {
			t.Errorf("告示对象缺 `unknown` 字段：%#v\n"+
				"  §9.79.8：前端要靠它区分「明确停更」与「读不到」。\n"+
				"  缺了这个字段，前端只剩 `frozen` 可读，而 unknown 态的 frozen 是 false\n"+
				"  ⇒ 会被显示成「没停更」或「已停更」，两头都在编。", obj)
		}
		if unknown {
			if f, _ := obj["frozen"].(bool); f {
				t.Errorf("unknown=true 同时 frozen=true：%#v\n"+
					"  没有测到停更却断言停更，是编造事实。", obj)
			}
		} else if f, _ := obj["frozen"].(bool); !f {
			t.Errorf("出现了 frozen=false && unknown=false 的对象：%#v\n"+
				"  后端在「明确知道」时返回 null、在「不知道」时置 unknown=true。\n"+
				"  这个组合让前端无法判断该显示什么 ⇒ 它是契约外的第三种形态。", obj)
		}
	}
	// 解释性键必须留着：它是给读响应的人看的。
	if _, ok := body["//"]; !ok {
		t.Error("响应缺 `//` 解释键。抓包的人需要知道 null 是什么意思。")
	}
}

// TestV1GateStateThreeStates 逐格钉住 §9.79.8 的第三态。
//
// 为什么这道门必须存在：`GetPlatformBool` 的三个回落点**全部**返回 fallback，
// 所以「读到 true」既可能是 DB 里的 true，也可能是**根本没读到**。
// 两态实现会把后者显示成「数据是新的」——而 252 实测键根本不在 settings_kv
// （41 行里没有），也就是说**今天就是后者**。
func TestV1GateStateThreeStates(t *testing.T) {
	cases := []struct {
		name      string
		writeGate bool
		source    string
		want      V1GateState
	}{
		{"显式 db 的 true = live", true, "db", V1GateLive},
		{"显式 env 的 true = live", true, "env", V1GateLive},
		{"显式 db 的 false = frozen", false, "db", V1GateFrozen},
		{"显式 env 的 false = frozen", false, "env", V1GateFrozen},
		// ⚠ 下面两格是本轮的核心：**回落来的 true 绝不是 live**。
		{"回落 true（键不在 DB）= unavailable", true, V1GateSourceDefault, V1GateUnavailable},
		{"回落 false = unavailable（不是 frozen）", false, V1GateSourceDefault, V1GateUnavailable},
		// source 为空串（读失败）同样不得当成 live。
		{"空 source = unavailable", true, "", V1GateUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := v1GateState(c.writeGate, c.source); got != c.want {
				t.Errorf("v1GateState(%v, %q) = %q，期望 %q", c.writeGate, c.source, got, c.want)
			}
		})
	}
}

// TestV1FreezeNoticeUnknownNeverClaimsFreshness 钉住 unavailable 态的**文案**。
//
// 这道门测的是「语义」不是「结构」：结构对了（返回对象、frozen=false）很容易，
// 而「文案里偷偷说了『仍在写入』」是这一档唯一会骗到人的地方。
func TestV1FreezeNoticeUnknownNeverClaimsFreshness(t *testing.T) {
	notice, ok := v1FreezeNoticeFor(true, V1GateSourceDefault)
	if !ok {
		t.Fatal("回落态没有产出告示 —— 那会让「读不到」继续被显示成「数据是新的」。")
	}
	if !notice.Unknown {
		t.Error("Unknown = false：第三态没有自己的标记，前端会按 frozen 分支处理。")
	}
	if notice.Frozen {
		t.Error("Frozen = true：没有测到停写却断言停更，是编造事实。")
	}
	// 措辞红线：不能有**肯定式**的「数据是新的 / 仍在写入」断言。
	//
	// ⚠ 这里刻意**不做**朴素子串匹配：中文里「这不等于『数据是新的』」
	// 也含那个子串，而它恰恰是**正确**的写法。朴素匹配会把正确的否定句判红，
	// 逼着作者把话说得更含糊——那是把判据的反作用力用在了错误的方向。
	// ⇒ 判据检查的是「肯定式断言」，即该短语**没有**被否定词修饰。
	for _, claim := range []string{"数据是新的", "仍在写入", "正常写入"} {
		for _, text := range []string{notice.Effect, notice.Silence} {
			if assertsFreshness(text, claim) {
				t.Errorf("回落态文案肯定地断言了 %q —— 那是**没测到**的事实。\n"+
					"  文本：%q\n"+
					"  可以写「这不等于…」（否定句允许），但不能直接断言。", claim, text)
			}
		}
	}
	// frozen 态**必须**仍然带 Unknown=false，否则两态在 JSON 上无法区分。
	frozen, ok := v1FreezeNoticeFor(false, "db")
	if !ok || !frozen.Frozen || frozen.Unknown {
		t.Errorf("显式停写态被改坏了：ok=%v frozen=%v unknown=%v", ok, frozen.Frozen, frozen.Unknown)
	}
}

// TestV1DataHorizonEmitsUnknownFieldEndToEnd —— 端到端跑真实端点，不是手写 fixture。
//
// 为什么必须真跑（2026-10-03 审计）：前端判据是 `if (n.unknown)`。
// 若后端因为任何原因**省掉**这个字段，JS 侧读到 `undefined` ⇒ falsy
// ⇒ 落到 `frozen` 分支 ⇒ **「读不到」被显示成「已停更」**，
// 而 §9.79.8 的全部意义就是这两者必须可区分。
// ⚠ 手写 fixture 的单测**测不到这一点**：fixture 里字段一直在。
//
// ⚠ 实测记录（本条在 2026-10-03 真的跑了，没有走 Skip）：
// 单元测试环境里 `settings.Global` 的该键同样**未配置** ⇒ source = "default"
// ⇒ 端点返回 `unknown: true`。**这与 252 生产的现状一致**
// （`settings_kv` 41 行里没有 `storage.request_logs_write_enabled`）。
func TestV1DataHorizonEmitsUnknownFieldEndToEnd(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.handleV1DataHorizon(rec, httptest.NewRequest("GET", "/api/admin/v1-data-horizon", nil))
	if rec.Code != 200 {
		t.Fatalf("状态码 = %d，期望 200。", rec.Code)
	}
	var body struct {
		V1DataHorizon *struct {
			Frozen  *bool  `json:"frozen"`
			Unknown *bool  `json:"unknown"`
			GateKey string `json:"gate_key"`
		} `json:"v1_data_horizon"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON：%v（体：%s）", err, rec.Body.String())
	}
	if body.V1DataHorizon == nil {
		t.Skip("horizon = null：本环境读到了显式 live，跳过 unknown 断言。\n" +
			"  这不是失败——它说明本环境确实有人显式配过该键。")
	}
	if body.V1DataHorizon.Unknown == nil {
		t.Fatalf("端点返回的对象里**没有 unknown 字段**：%s\n"+
			"  前端 `if (n.unknown)` 对 undefined 为 falsy ⇒ 会落到 frozen 分支，\n"+
			"  即把「读不到状态」显示成「数据已停更」——反向的另一种编造。", rec.Body.String())
	}
	if !*body.V1DataHorizon.Unknown {
		t.Errorf("对象存在但 unknown=false（体：%s）。\n"+
			"  后端在「明确知道」时应返回 null、在「不知道」时置 unknown=true；\n"+
			"  这个组合让前端无法判断该显示什么。", rec.Body.String())
	}
	if body.V1DataHorizon.Frozen != nil && *body.V1DataHorizon.Frozen {
		t.Errorf("unknown 态里 frozen=true（体：%s）：没有测到停更却断言停更。", rec.Body.String())
	}
	// 响应头也必须能区分（抓包/非浏览器消费者）。
	if got := rec.Header().Get(V1FreezeHeader); got != "unknown" {
		t.Errorf("响应头 %s = %q，期望 \"unknown\"。\n"+
			"  它与 frozen 的 \"1\" 必须不同，否则非浏览器消费者与前端一样会误读。",
			V1FreezeHeader, got)
	}
}

// TestV1FreezeNoticeDefaultSourceIsConservative 钉住「缺省即 unknown」。
//
// 审计订正（2026-10-03 18:40）：第一稿把变参缺省写成 `"db"`（live），
// 理由是「不破坏既有单参调用点」。**那个理由是错的**——
// 它让任何未来忘记传 source 的新调用点**静默说「知道」**，
// 正是本节要治的病。
//
// ⇒ 这道门**按字面量钉**是有意的：这里要挡的是「有人把缺省改成 live」，
// 而不是「实现怎么组织」。它与接线层判据（块形状）分工不同。
func TestV1FreezeNoticeDefaultSourceIsConservative(t *testing.T) {
	// 行为层：单参调用（不传 source）必须落 unknown，不是 live。
	notice, ok := v1FreezeNoticeFor(true /* 写入方读到 true */)
	if !ok {
		t.Fatal("单参调用没有产出告示 —— 缺省必须落 unknown（显示），而不是 live（不显示）。")
	}
	if !notice.Unknown {
		t.Error("单参调用落到了 live：缺省 source 必须保守（V1GateSourceDefault）。\n" +
			"  否则任何忘记传 source 的新调用点会静默把「没读到」说成「知道」。")
	}
	// 结构层：把缺省常量也钉住，防止有人改成字面量却没改行为测试。
	raw, err := os.ReadFile("v1_freeze_notice.go")
	if err != nil {
		t.Fatalf("读取失败 %v", err)
	}
	src := string(raw)
	start := strings.Index(src, "func v1FreezeNoticeFor(")
	end := strings.Index(src, "// v1FreezeNotice 是接线到 settings")
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("找不到 v1FreezeNoticeFor 边界（start=%d end=%d）。", start, end)
	}
	body := src[start:end]
	if !strings.Contains(body, "src := V1GateSourceDefault") {
		t.Errorf("v1FreezeNoticeFor 的缺省不是 V1GateSourceDefault。\n"+
			"  当前实现：\n%s", body)
	}
	// 反向：不得出现把缺省写成 live 的表达式。
	if strings.Contains(body, `src := "db"`) || strings.Contains(body, `src := "env"`) {
		t.Errorf("缺省被写成了显式来源（db/env）—— 那等于「缺省即知道」。\n"+
			"  当前实现：\n%s", body)
	}
}

// TestReadV1GateSourceIsReadOnly 钉住「不改写入方语义」。
//
// §9.79.8 的修法边界：不碰 settings.RequestLogsWriteEnabled()，
// 只在告示层多读一个 source。若这里出现写操作，fail-open 的方向就反了。
func TestReadV1GateSourceIsReadOnly(t *testing.T) {
	raw, err := os.ReadFile("v1_freeze_notice.go")
	if err != nil {
		t.Fatalf("读取失败 %v", err)
	}
	src := string(raw)
	start := strings.Index(src, "func readV1GateSource()")
	end := strings.Index(src, "func v1FreezeNoticeFor(")
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("找不到 readV1GateSource 的边界（start=%d end=%d）。", start, end)
	}
	body := src[start:end]
	for _, banned := range []string{".Set(", "SetPlatform", "INSERT", "UPDATE ", "upsert"} {
		if strings.Contains(body, banned) {
			t.Errorf("readV1GateSource 里出现 %q —— 它必须只读。\n"+
				"  写入方的 fail-open（读不到就当开着）是对的，告示层只负责**如实说不知道**。\n"+
				"  当前实现：\n%s", banned, body)
		}
	}
	// 它必须真的用了 EffectiveValue 的 source，否则恒返回 default，
	// 于是**永远**进 unavailable 态（那也是一个会骗人的面）。
	if !strings.Contains(body, "EffectiveValue") {
		t.Errorf("readV1GateSource 没有调 EffectiveValue ⇒ source 恒为 default。\n"+
			"  那样 explicit-live 这一支永远不可达，UI 会常显「无法确认」。\n"+
			"  当前实现：\n%s", body)
	}
}

// assertsFreshness 报告 text 是否**肯定地**断言了 claim。
//
// 做法：在 claim 出现位置往前回看一小段窗口，若窗口内出现否定词
// （不 / 未 / 非 / 别 / 勿 / 无法 / 并非 / 不等于）则视为否定句，不算断言。
//
// ⚠ 为什么要这么麻烦：朴素 `strings.Contains` 会把
// 「这不等于『数据是新的』」判成断言。那样做的后果不是「门变严」，
// 而是**逼着作者回避正确的措辞**——判据的反作用力指向了「说得更含糊」，
// 那恰好是反的（§9.71 的教训的另一面：判据不该诱导实现劣化）。
func assertsFreshness(text, claim string) bool {
	negations := []string{"不", "未", "非", "别", "勿", "无法", "并非", "不等于", "不能"}
	from := 0
	for {
		i := strings.Index(text[from:], claim)
		if i < 0 {
			return false
		}
		at := from + i
		lo := at - 12
		if lo < 0 {
			lo = 0
		}
		prefix := text[lo:at]
		negated := false
		for _, n := range negations {
			if strings.Contains(prefix, n) {
				negated = true
				break
			}
		}
		if !negated {
			return true
		}
		from = at + len(claim)
	}
}

// 审计（2026-10-03 18:40）：原 `firstLinesAround` 辅助函数**已删除**。
//
// 它唯一的调用点是被本轮 §9.80.3 的块形状判据取代的那句
// `firstLinesAround(src, "func v1FreezeNotice(")`。
// ⇒ 改动之后它成为**孤儿函数**，而 **Go 不报未使用的函数**
// （只报未使用的局部变量与 import）⇒ 静默腐烂，没有任何门会响。
//
// 这正是本审计反复记的形态：「一个只被门使用的辅助函数，
// 在门自己改写之后就是一个没有读者的函数」。
// ⇒ 留这条记录，免得下一轮又从 git 历史里把它捞回来当「工具函数」。
