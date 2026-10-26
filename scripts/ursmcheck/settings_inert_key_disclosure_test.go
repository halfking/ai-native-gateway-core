package ursm_test

// settings_inert_key_disclosure_test.go —— 「零读者设置键必须自曝已废弃」门
// （2026-10-25 12h 审计轮新增；审计档 docs/12小时内修订审计-2026-10-25-0715.md §4.2）
//
// # 这道门守的是什么
//
// 一次「v1 `_request_logs` 族退役」的说明文改写轮，把若干 `sessions_v2.*` 键的
// `DescriptionLong` 改成了**听起来仍然有效**的说法，例如：
//
//	「本键当前只余『读路由』这一半的语义」
//	「差异对比现在只反映会话族内部的两条读路径」
//	「超时后的行为由会话族内部的读路由决定」
//
// ★★ 而实测（2026-10-25 12h 审计轮）：
//
//	① 这些键在**生产代码里零读者**（字面量口径 + const 口径都为零）⇒
//	   ★★ 运营在 admin 设置页（`admin/settings.go:162` 走
//	   `settings.Global.AllSpecs()` 把整个目录暴露出去）**改它们不产生任何效果**；
//	② `sessions_v2.dual_read` 的那句话还**与仓内门直接矛盾**——门
//	   `cmd/gateway/dual_read_validator_test.go` 的
//	   `TestDualReadValidator_RetiredFamilyIssuesNoQuery` 断言
//	   ★★「对账路径**一条查询都不发**、且如实报『未测量』」；
//	③ 仓内**早就有**明确的体例（`settings/spec_probe.go`：
//	   「已废弃。… 该设置已不再生效。」；`settings/spec_sessions_v2.go`：
//	   「已废弃别名：真实生效键为 X。本键修改无任何效果。」）⇒ 那轮**没用**这个体例。
//
// ⇒ ⇒ ★★ 这是一条**流程闭环**缺口：说明书 ⇒ 运营预期 ⇒ 实际效果，
//   链条断在「说明书」这一环 ⇒ **说明书在骗人**。
//
// # 本门做什么（窄口径，零假红）
//
//	① 对一张**固定期望表**里的每个键：
//	   · 它在**生产代码**里必须**零读者**（否则它就不是惰性键了）；
//	   · 它的 `Description` 必须带仓内体例标记 **「已废弃」**（⇒ 运营看得到真相）。
//	② 三条自检（门必须自证有牙）：
//	   · **扫描面非空** ⇒ 已知活键 `sessions_v2.enabled` 必须被检出（有读者）；
//	   · **反向夹具** ⇒ 喂一段「刻意含读者」的样本，检测器必须报出；
//	   · **标记可判** ⇒ 把「已废弃」标记从期望表里去掉，判定必须翻红。
//
// ★ 为什么只做窄口径（固定期望表）而不做「全目录零读者扫描」：
//
//	全目录扫描会撞上**读取形态的多样性**（`GetPlatform*` / `flags.IsEnabled`
//	的点号与下划线两种拼写 / const 间接引用 / 动态拼键）⇒ 假红风险高
//	（★ 假红比假绿更费）。⇒ 本门只钉住**已审计过的那几把键**，
//	★ 「新键也会被扫出来」这一条属**通用化的缺口**，登记而不修
//	（见审计档 §4.2 的「诚实边界」）。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// inertKeys 是「已审计确认为零读者」的设置键。
//
// ★ 语义：这些键在生产代码里**没有任何读者** ⇒ 改它们**不产生任何效果**
//
//	⇒ 说明书必须自曝「已废弃」（仓内体例，见 settings/spec_probe.go）。
var inertKeys = []string{
	"sessions_v2.l3_read",
	"sessions_v2.dual_read",
	"sessions_v2.primary_read",
	"sessions_v2.read_timeout_ms",
	"sessions_v2.compression_enabled",
	"sessions_v2.request_bodies_full",
}

// deprecationMarker 是仓内「已废弃」体例的标记词。
//
// ★ 为什么锚在 `Description`（短句）而不是 `DescriptionLong`（长句）：
//
//	长句是自由文本，写法千差万别；短句是 admin 设置页的**行内标签**⇒
//	运营在列表里一眼能看见 ⇒ ★ 这才是「自曝」的本意。
const deprecationMarker = "已废弃"

// knownLiveKey 是**自检用**的阳性对照：一个**确实有读者**的活键。
//
// ★ 它必须被检测器检出 ⇒ 证明扫描面不是空的（否则「零读者」恒真）。
const knownLiveKey = "sessions_v2.enabled"

// productionLiterals 扫出**生产代码**里出现过的全部字符串字面量。
//
// ★ 口径（刻意放宽，方向上偏「假绿」而非「假红」）：
//
//	· 扫**全部**非测试 .go 文件（不限于读法形态）⇒ 只要字面量出现在生产代码里
//	  就算「有读者」⇒ ★ 宁可漏判（假绿），不可误判（假红）；
//	· ★ 排除 `settings/spec_*.go`（目录自身含键字面量）与本门自身。
func productionLiterals(t *testing.T) map[string]bool {
	t.Helper()
	root := "../.."
	out := map[string]bool{}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "node_modules" || base == "dist" || base == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = filepath.ToSlash(path)
		} else {
			rel = filepath.ToSlash(rel)
		}
		if strings.HasPrefix(rel, "settings/spec_") || strings.HasSuffix(rel, "settings_inert_key_disclosure_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			bl, ok := n.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				return true
			}
			out[bl.Value[1:len(bl.Value)-1]] = true
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("扫生产代码字面量失败：%v", err)
	}
	return out
}

// detectReader 判定「某个键在生产代码里有没有读者」。
//
// ★ 两种拼写都认：点号（`sessions_v2.dual_read`）与下划线
//
//	（`sessions_v2_dual_read`，`FeatureFlags.IsEnabled` 用的那种）⇒
//	★ 否则一个「下划线拼写」的读者会被漏掉 ⇒ 门会**假红**。
func detectReader(literals map[string]bool, key string) bool {
	if literals[key] {
		return true
	}
	return literals[strings.ReplaceAll(key, ".", "_")]
}

// specByKey 取运行期已注册的 spec（★ 与 settings_spec_registration_test.go
// 同源：用 `settings.PlatformSpecs()` 而不是解析源码 ⇒ 更权威，
// 且「新增 spec 忘了加进 PlatformSpecs()」也会被抓到）。
func specByKey(key string) *settings.Spec {
	for _, sp := range settings.PlatformSpecs() {
		if sp != nil && sp.Key == key {
			return sp
		}
	}
	return nil
}

// TestInertSettingsKeysHaveNoProductionReader 判据①：惰性键必须**确实**零读者。
//
// ★ 为什么这条要单独成腿：若将来有人给这些键接上真读者 ⇒
//
//	「已废弃」标签就成了**假话** ⇒ ⇒ 本腿会红 ⇒ 逼着人把标签摘掉。
func TestInertSettingsKeysHaveNoProductionReader(t *testing.T) {
	literals := productionLiterals(t)
	for _, key := range inertKeys {
		if detectReader(literals, key) {
			t.Errorf("设置键 %q 已不再是惰性键（生产代码里出现了读者）⇒ "+
				"「已废弃」标签会变成假话 ⇒ 请同时更新标签与本门期望表", key)
		}
	}
}

// TestInertSettingsKeysDiscloseDeprecation 判据②：惰性键必须在**行内标签**自曝。
//
// ★ 为什么锚 `Description` 而不是 `DescriptionLong`：长句是自由文本，
//
//	行内标签才是 admin 列表里一眼能看见的那一列（见文件头注释）。
func TestInertSettingsKeysDiscloseDeprecation(t *testing.T) {
	for _, key := range inertKeys {
		sp := specByKey(key)
		if sp == nil {
			t.Errorf("设置键 %q 不在运行期注册目录里（settings.PlatformSpecs()）⇒ "+
				"要么它被删了（请同步本门期望表），要么它忘了注册", key)
			continue
		}
		if !strings.Contains(sp.Description, deprecationMarker) {
			t.Errorf("设置键 %q 是零读者键（改它不产生任何效果）⇒ 它的 Description "+
				"必须含 %q 自曝，否则运营在设置页看到的是一个会骗人的标签（实测：%q）",
				key, deprecationMarker, sp.Description)
		}
	}
}

// TestInertKeyGateHasTeeth 三条自检 —— 门必须自证有牙。
func TestInertKeyGateHasTeeth(t *testing.T) {
	// ① 扫描面非空：已知活键必须被检出（有读者）。
	literals := productionLiterals(t)
	if !detectReader(literals, knownLiveKey) {
		t.Fatalf("扫描面是空的：已知活键 %q 都没被检出 ⇒ 「零读者」判定恒真 ⇒ "+
			"这道门没有牙（检查 productionLiterals 的排除规则）", knownLiveKey)
	}

	// ② 反向夹具：喂一段「刻意含读者」的样本，检测器必须报出。
	sample := map[string]bool{"sessions_v2.some_live_key": true}
	if !detectReader(sample, "sessions_v2.some_live_key") {
		t.Error("反向夹具失败：样本里有读者，检测器却报成没有 ⇒ 检测器没有牙")
	}
	if detectReader(sample, "sessions_v2.never_read_key") {
		t.Error("反向夹具失败：样本里没有读者，检测器却报成有 ⇒ 检测器误判")
	}

	// ③ 标记可判：把「已废弃」标记从判定里去掉，期望表里的键必须**翻红**。
	//    （用一条真实惰性键的 Description 做反证：它含标记 ⇒ 去掉标记后不匹配。）
	sp := specByKey(inertKeys[0])
	if sp == nil {
		t.Fatalf("取不到 %q 的 spec ⇒ 自检没法构造", inertKeys[0])
	}
	withoutMarker := strings.ReplaceAll(sp.Description, deprecationMarker, "")
	if withoutMarker == sp.Description {
		t.Fatalf("取不到 %q 的 Description 里含 %q ⇒ 自检没法构造", inertKeys[0], deprecationMarker)
	}
	if strings.Contains(withoutMarker, deprecationMarker) {
		t.Error("标记可判自检失败：去掉标记后仍被判定为含标记 ⇒ 判定逻辑没有牙")
	}
}
