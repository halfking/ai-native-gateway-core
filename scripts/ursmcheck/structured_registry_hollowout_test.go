package ursm_test

// structured_registry_hollowout_test.go —— 2026-10-28 R87 轮（非 `.tsv` 登记册的**行为化掏空验证**）
//
// # 这道门要钉住的事
//
// ★ 承接 §19.25.19.10 优先级 0：★ 给仓内**非 `.tsv`** 的结构化登记册做行为化掏空验证。
//
// ★ R86 轮只做到了【覆盖判据】（★ 每个「像登记册」的文件都在普查表里有归属）
// ⇒ ⇒ 但 `behaviorally_verified` 一律 false ⇒ ⇒ **没有一条真的验过**。
//
// ★★ 本轮补上【行为判据】：★ 对每份登记册真的做一次掏空 ⇒ ⇒ 跑它的读方
// ⇒ ⇒ 读方**必须红** ⇒ ⇒ 而且红的原因必须是**读方自己的**「没有条目可查」
// ⇒ ⇒ 而不是「JSON 解析失败」或「读到 undefined」。
//
// # 为什么 `.tsv` 的那套搬不过来（★ 本轮最值钱的一处认知）
//
// ★ `.tsv` 的掏空 = 「删掉数据行、留下列名行」⇒ ⇒ **文件仍然合法** ⇒ ⇒ 读方红
// ⇒ ⇒ 是因为它「读到 0 行」。
//
// ★★ JSON 没有「列名行」这个概念 ⇒ ⇒ 「删几行」会留下**语法错误**的 JSON
// ⇒ ⇒ 读方红是因为**解析崩了** ⇒ ⇒ **那不是「读到 0 条」** ⇒ ⇒ **假红**。
//
// ★★ ⇒ ⇒ 所以必须有**格式感知的掏空变换**（★ 本文件的核心）⇒ ⇒
//
//	① **`entries` 模式**（★ 清单型登记册）：★ 容器清空 ＋ 数值归零，
//	   ★★ **键集合与类型骨架逐层保留** ⇒ ⇒ 读方不会因为「字段没了」而崩。
//	② **`unusable` 模式**（★ 上限型登记册）：★ 整册替换成 `{}` / `[]`
//	   ⇒ ⇒ 「登记册拿不到真值」⇒ ⇒ 读方必须**主动**判「基线不可用」并红。
//
// ★★ **为什么要有模式②**：★ 上限型登记册（★ 棘轮：记录的是「允许的最大值」）
// ★★ 的实测值**已经触底**（★ 全部是 0）⇒ ⇒ 把上限归零是**空操作**
// ⇒ ⇒ 模式①对它**测不到任何东西**（★ 承本线纪律：★「掏空后仍绿」的第一嫌疑是
// ★★ **探针无效**，不是「门有洞」）⇒ ⇒ 必须换成「真值被取走」这另一种形式。
//
// # 两条硬判据（★ 红因归属）
//
// ★ **判据①（红得对）**：★ 掏空后读方退出码 ≠ 0 **且** 输出里有 `red_signal`
// ⇒ ⇒ 「退出码非 0」只说明红；「含 red_signal」才说明**红的是该红的那件事**。
// ★★ 没有这一条 ⇒ 一个「读到 undefined 就崩」的读方也会被算成「有牙」。
//
// ★ **判据②（探针有效）**：★ 掏空版必须 ① 仍是合法 JSON ② **没改变任何节点的类型**
// ★★ ③ **与原件确实不同**（★ 否则 = 空操作 ⇒ 什么都测不到 ⇒ **inconclusive**）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// ── 掏空模式 ──────────────────────────────────────────────────────────────

// structuredHollowMode 是【掏空模式】的封闭词表。
type structuredHollowMode string

const (
	// structuredHollowEntries：★ 清单型。容器清空 ＋ 数值归零；键集合与类型骨架保留。
	structuredHollowEntries structuredHollowMode = "entries"
	// structuredHollowUnusable：★ 上限型。整册替换成 `{}` / `[]`（读方拿不到真值）。
	structuredHollowUnusable structuredHollowMode = "unusable"
)

// structuredHollowModeList 返回词表的稳定排序列表（★ 只用于报错信息）。
func structuredHollowModeList() []string {
	out := []string{string(structuredHollowEntries), string(structuredHollowUnusable)}
	sort.Strings(out)
	return out
}

// ── 格式感知的掏空变换 ───────────────────────────────────────────────────

// structuredAllValuesAreObjects 判断「这个映射的每个值都是对象」。
//
// ★★ 为什么需要它：★ JSON 登记册里有两种映射，**掏空方式相反**：
//
//	① **条目映射**（★ `models: {模型名: {…}}`、`slots: {键: {…}}`）
//	   ⇒ ⇒ 掏空 = **整个映射清空**（★ 条目都没了 ⇒ 读方看到「0 条」）
//	   ⇒ ⇒ 若只掏字段不掏条目 ⇒ 读方会撞上「条目在、字段空」⇒ ⇒ 红的原因是
//	   ★★ **字段缺失**（★ 例如 `source_url is required`）⇒ ⇒ **红因不对**。
//	② **计数字典**（★ `perGroup: {桶名: 数字}`）
//	   ⇒ ⇒ 掏空 = **逐项归零**（★ 桶还在、限额变 0 ⇒ 读方看到「上限 0」）。
//
// ★ 两者的区别是**机械可算**的（★ 看值的类型）⇒ ⇒ 不需要人工逐册标注。
func structuredAllValuesAreObjects(m map[string]any) bool {
	if len(m) == 0 {
		return false
	}
	for _, v := range m {
		if _, ok := v.(map[string]any); !ok {
			return false
		}
	}
	return true
}

// structuredHollowJSON 是【格式感知的掏空变换】。
//
// ★ 返回掏空后的值（★ 不动原件）。
//
// ★ `entries` 模式的逐条规则：
//
//	· 数组   ⇒ `[]`（★ 数组的元素**就是**条目 ⇒ 整段清空）
//	· 映射   ⇒ ★ 值全是对象 ⇒ `{}`（★ 条目映射 ⇒ 条目清零）
//	·         ★ 否则 ⇒ 逐键递归（★ 计数字典 ⇒ 逐项归零；键集合保留）
//	· 数值   ⇒ `0`（★ 计数归零）
//	· 字符串 ⇒ **原样保留**（★ 字符串是**元数据 / 标识**，不是「条目」；
//	·         ★ 清空它会让读方因「字段是空的」而红 ⇒ ⇒ **红因不对**）
//	· 布尔   ⇒ `false`
//	· null   ⇒ `null`
//
// ★ `unusable` 模式：★ 对象 ⇒ `{}`；数组 ⇒ `[]`；其余 ⇒ 原样。
func structuredHollowJSON(v any, mode structuredHollowMode) any {
	if mode == structuredHollowUnusable {
		switch v.(type) {
		case []any:
			return []any{}
		case map[string]any:
			return map[string]any{}
		}
		return v
	}
	switch t := v.(type) {
	case []any:
		return []any{}
	case map[string]any:
		if structuredAllValuesAreObjects(t) {
			return map[string]any{}
		}
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = structuredHollowJSON(vv, mode)
		}
		return out
	case json.Number:
		return json.Number("0")
	case string:
		return v
	case bool:
		return false
	}
	return v
}

// structuredShapePreserved 核对「掏空版**没有改变任何节点的类型**，也没有**凭空多出键**」。
//
// ★ 允许的差异：★ 映射的**键被删掉**（★ 那正是「清空」）
// ⇒ ⇒ 不允许的差异：★ 类型变了（★ `string → number` 之类）★ 或多出键。
//
// ★★★ 为什么这条是硬判据：★ 它保证读方**不会因为「字段形状变了」而崩**
// ⇒ ⇒ 于是「读方红了」只能解释成「它读到了 0 条 / 读不到真值」。
func structuredShapePreserved(orig, hollow any, path string) (bool, string) {
	switch o := orig.(type) {
	case map[string]any:
		h, ok := hollow.(map[string]any)
		if !ok {
			return false, fmt.Sprintf("%s：类型变了（%T → %T）", path, orig, hollow)
		}
		keys := make([]string, 0, len(h))
		for k := range h {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ov, exists := o[k]
			if !exists {
				return false, fmt.Sprintf("%s.%s：掏空版多出一个原版没有的键", path, k)
			}
			ok, why := structuredShapePreserved(ov, h[k], path+"."+k)
			if !ok {
				return false, why
			}
		}
		return true, ""
	case []any:
		h, ok := hollow.([]any)
		if !ok {
			return false, fmt.Sprintf("%s：类型变了（%T → %T）", path, orig, hollow)
		}
		if len(h) != 0 {
			return false, fmt.Sprintf("%s：数组应被清空，却还剩 %d 个元素", path, len(h))
		}
		return true, ""
	case json.Number:
		if _, ok := hollow.(json.Number); !ok {
			return false, fmt.Sprintf("%s：类型变了（number → %T）", path, hollow)
		}
		return true, ""
	case string:
		if _, ok := hollow.(string); !ok {
			return false, fmt.Sprintf("%s：类型变了（string → %T）", path, hollow)
		}
		return true, ""
	case bool:
		if _, ok := hollow.(bool); !ok {
			return false, fmt.Sprintf("%s：类型变了（bool → %T）", path, hollow)
		}
		return true, ""
	case nil:
		if hollow != nil {
			return false, fmt.Sprintf("%s：类型变了（null → %T）", path, hollow)
		}
		return true, ""
	}
	return false, fmt.Sprintf("%s：未知类型 %T", path, orig)
}

// structuredValueEqual 比较两个 JSON 值是否**完全一样**（★ 键序无关）。
//
// ★ 用途：★ 判定「掏空是不是**空操作**」⇒ ⇒ 空操作 ⇒ 什么都没测到 ⇒ **inconclusive**。
func structuredValueEqual(a, b any) bool {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, xv := range x {
			yv, exists := y[k]
			if !exists || !structuredValueEqual(xv, yv) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !structuredValueEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	case json.Number:
		y, ok := b.(json.Number)
		return ok && x.String() == y.String()
	default:
		return a == b
	}
}

// structuredDecodeJSON 解析 JSON（★ 保留数值字面量 ⇒ `json.Number`）。
func structuredDecodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	// ★ 尾部不能有第二个 JSON 值（★ 否则「文件里塞了两个 JSON」也会被当合法）。
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return nil, fmt.Errorf("文件里不止一个 JSON 值")
	}
	return v, nil
}

// ── 读方跑批（★ 复用共享 harness 的 node / vitest / gotest 臂） ───────────

// structuredReaderKinds 是【读方形态】的封闭词表。
//
// ★★★ r1039 第三轮新增 `shell`（★ 2026-10-28 实测）：
//
//	★ 本词表原本**比共享 harness 窄**：★ `runHollowReaderOverlay`（★
//	  tsv_hollowout_gate_test.go）**早就支持 `shell`**（★ `bash -c <spec>` ⇒
//	  cwd = 仓根）⇒ ⇒ TSV 侧一直在用 shell 读方 ⇒ ★ 而本表（结构化侧）把
//	  shell 排除在外 ⇒ ⇒ **同一套 harness、两套词表** ⇒ ⇒ 一条「读方是 shell
//	  脚本」的结构化登记册**没法在本表里登记**（★ 只能被普查门当成「没归属」）。
//	★ ⇒ 那不是「读方没有牙」，而是**本表没有那条腿** ⇒ ⇒ 把词表补齐到
//	  harness 的真实能力（★ 只**加**不**改**：★ 既有 54 条的行为不变）。
var structuredReaderKinds = map[string]bool{
	"node":   true,
	"vitest": true,
	"gotest": true,
	"shell":  true,
}

// structuredOutcome 是某一条登记册的实测分类。
type structuredOutcome string

const (
	structuredRedCaught      structuredOutcome = "red-caught"      // ★ 掏空 ⇒ 读方红（且红因正确）
	structuredStillGreen     structuredOutcome = "still-green"     // ★ 掏空 ⇒ 读方仍绿 ⇒ 门有洞
	structuredInconclusive   structuredOutcome = "inconclusive"    // ★ 变换是空操作 / 探针无效
	structuredBaselineRed    structuredOutcome = "baseline-red"    // ★ 读方在基线就红 ⇒ 不可判定
	structuredWrongRed       structuredOutcome = "wrong-red"       // ★ 红了，但红因不对（★ 崩溃 / 不是那条信号）
	structuredEnvUnavailable structuredOutcome = "env-unavailable" // ★ 读方**没量到**（环境失败）⇒ 不可判定
)

// structuredEnvUnavailableToken 是「读方没量到」的**稳定探针 token**。
//
// ★★ 2026-10-29 新增。为什么需要它：
//
//	本门判的是「读方红了 **且** 红因是那条信号」（红信号 = 普查表登记的
//	`red_signal`）。而**浏览器运行时尺**在「Chrome/CDP 连不上」时会以
//	**另一个**失败形态退出 ⇒ ⇒ 门把它判成「红了，但红因不对」⇒
//	★★ **随机误判「门没牙」**（实测：它在 7 次运行里红过 2 次，根因是
//	负载下浏览器驱动 fetch failed）。
//	⇒ ⇒ 必须把「环境失败」与「判据失败」**分成两类**。
//
// ★ 约定（读方侧）：任何读方在【环境不可用】（浏览器连不上、依赖缺失、
//
//	服务没起）时，必须：① 退出码用 **3**（=「未测量」的既有约定；★ 普查表
//	里的其它读方都不用 3 ⇒ 3 在本门语境里是空闲的）；② 输出里打印含
//	`-ENV-UNAVAILABLE` 的探针 token ⇒ ⇒ 改文案不会把门打瞎（与 `red_signal`
//	同一套思路：判据落在「输出里有没有探针」）。
//
// ★ 门侧的行为：基线态遇到「没量到」⇒ **跳过本条**（不是判它没牙，也不是
//
//	判它坏了）⇒ ★ 「不可判定」是一等状态（§531 通用式六：覆盖边界要声明）。
const structuredEnvUnavailableToken = "NOT-MEASURED"

// structuredIsEnvUnavailable 报告读方的这次运行是不是「没量到」。
//
// ★ 两个判据取或：退出码 3（读方侧的约定）**或** 输出里有探针 token
//
//	（读方换了退出码也不怕）。
func structuredIsEnvUnavailable(code int, out string) bool {
	return code == 3 || strings.Contains(out, structuredEnvUnavailableToken)
}

// structuredVerifyEntry 是【行为判据】的**单条目**实现。
//
// ★ 返回 (分类, 细节)。
func structuredVerifyEntry(t *testing.T, root string, e structuredCensusEntry) (structuredOutcome, string) {
	return structuredVerifyEntryWithReader(t, root, e, e.ReaderKind, e.ReaderSpec)
}

// structuredVerifyEntryWithReader 是【行为判据】的可换读方形态（★ 负控臂用）。
//
// ★ ★ 为什么需要它：★ 负控臂（★ scripts/ursmcheck/tsv_hollowout_negative_control_test.go）
// ★★ 要验证「门能不能发现『**读方被改瞎了**』」⇒ ⇒ 必须能把【真实读方】换成
// ★★ 【被改瞎的副本】⇒ ⇒ 而副本住在**另一个路径** ⇒ ⇒ 读方规格必须可覆盖。
func structuredVerifyEntryWithReader(t *testing.T, root string, e structuredCensusEntry, readerKind, readerSpec string) (structuredOutcome, string) {
	t.Helper()

	// ★★ R87 轮收尾：★ 拿【整仓掏空锁】⇒ ⇒ 同一时刻只允许一个掏空跑变异工作树。
	//
	// ★ 与 `.tsv` 侧【同一把锁】（★ 两边都会写空真实文件）⇒ ⇒ 两边也互斥。
	// ★ 拿不到锁 ⇒ **红**（★ 不跳过）⇒ ⇒ 「跳过」在退出码上与「跑了 0 条」不可区分。
	releaseLock, _, err := lockHollowoutShared(root, hollowoutLockWait)
	if err != nil {
		t.Fatalf("★ 拿不到整仓掏空锁 ⇒ ⇒ 本条**不判定**（★ 不能跳过）：%v", err)
	}
	defer releaseLock()

	abs := filepath.Join(root, e.Path)
	orig, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", e.Path, err)
	}
	origMD5 := md5Of(orig)

	// ★ ① 解析原件（★ 必须本来就是合法 JSON）
	origValue, err := structuredDecodeJSON(orig)
	if err != nil {
		t.Fatalf("★ %s 不是合法 JSON（%v）⇒ ⇒ 它不是「结构化登记册」⇒ ⇒ 普查表该改", e.Path, err)
	}

	// ★ ② 掏空变换
	hollowValue := structuredHollowJSON(origValue, structuredHollowMode(e.HollowMode))
	hollowed, err := json.MarshalIndent(hollowValue, "", "  ")
	if err != nil {
		t.Fatalf("序列化掏空版失败：%v", err)
	}
	hollowed = append(hollowed, '\n')

	// ★ ③ 探针有效性（★ 判据②）
	if ok, why := structuredShapePreserved(origValue, hollowValue, "$"); !ok {
		return structuredInconclusive, "★ 掏空版**改变了节点类型** ⇒ ⇒ 探针无效（读方可能只是崩了）：" + why
	}
	if !structuredValueEqual(origValue, hollowValue) {
		// 有差异 ⇒ 探针有效
	} else {
		return structuredInconclusive, fmt.Sprintf(
			"★ %s 的掏空是**空操作**（掏空后与原件逐值相同）⇒ ⇒ 什么都没测到 ⇒ ⇒ 不能判「绿 ⇒ 门有洞」\n"+
				"★ ★ 这一类登记册是**上限型**（★ 棘轮：记录的是「允许的最大值」）⇒ ⇒ 它的实测值已经触底\n"+
				"★ ★ ⇒ ⇒ 把上限归零测不到任何东西 ⇒ ⇒ 必须改用 `unusable` 模式（★ 真值被取走）",
			e.Path)
	}

	// ★ ④ 基线：读方必须绿（★ 否则不可判定）
	code, out, err := runHollowReader(readerKind, readerSpec, root, 900*time.Second)
	if err != nil {
		t.Fatalf("跑基线读方失败（%s %s）：%v", e.ReaderKind, e.ReaderSpec, err)
	}
	if code != 0 {
		// ★★ 环境失败（没量到）⇒ **不是**「读方坏了」⇒ 不可判定 ⇒ 跳过本条。
		//   （旧写法把它算成 baseline-red ⇒ 门会判「先把读方修绿」⇒
		//   ★ 在负载下这是**假指责** ⇒ ★ 本轮修掉。）
		if structuredIsEnvUnavailable(code, out) {
			return structuredEnvUnavailable, fmt.Sprintf(
				"★ 读方在【基线】**没量到**（exit=%d）⇒ ⇒ 环境不可用 ⇒ ⇒ 本条**不可判定**（★ 不是「门没牙」，也不是「读方坏了」）\n%s",
				code, tailLines(out, 25))
		}
		return structuredBaselineRed, fmt.Sprintf("读方在【基线】就红（exit=%d）\n%s", code, tailLines(out, 25))
	}

	// ★ ⑤ 落盘掏空版（★ 先备份 ⇒ 被 SIGKILL 也能自愈）
	backup := hollowBackupPath(abs)
	if err := os.WriteFile(backup, orig, 0o644); err != nil {
		t.Fatalf("写备份失败（%s）：%v", backup, err)
	}
	if err := os.WriteFile(abs, hollowed, 0o644); err != nil {
		t.Fatalf("写掏空版失败：%v", err)
	}
	stopGuard := installHollowSignalGuard(abs)
	defer func() {
		stopGuard()
		if err := restoreHollowBackup(abs); err != nil {
			t.Errorf("还原 %s 失败：%v", e.Path, err)
			return
		}
		back, err := os.ReadFile(abs)
		if err != nil {
			t.Errorf("还原后读不回 %s：%v", e.Path, err)
			return
		}
		if got := md5Of(back); got != origMD5 {
			t.Errorf("还原后 md5 不符（%s）：期望 %s，实际 %s", e.Path, origMD5, got)
		}
	}()

	// ★ ⑥ 掏空 ⇒ 跑读方
	// ★★ contentKey = 掏空版的 md5 ⇒ ⇒ 【go:embed 读方】会**重新编译**
	// ★★ （★ 否则二进制里仍是原内容 ⇒ ⇒ 假绿）。
	code, out, err = runHollowReaderOverlay(readerKind, readerSpec, root, 900*time.Second, "", md5Of(hollowed))
	if err != nil {
		t.Fatalf("跑掏空版读方失败：%v", err)
	}
	if code == 0 {
		return structuredStillGreen, fmt.Sprintf("掏空后读方**仍然绿**（exit=0）\n%s", tailLines(out, 25))
	}
	// ★★ 掏空态「没量到」⇒ 同样不可判定（★ 环境失败 ≠ 判据失败）。
	if structuredIsEnvUnavailable(code, out) {
		return structuredEnvUnavailable, fmt.Sprintf(
			"★ 掏空后读方**没量到**（exit=%d）⇒ ⇒ 环境不可用 ⇒ ⇒ 本条**不可判定**\n%s",
			code, tailLines(out, 25))
	}
	if e.RedSignal == "" {
		return structuredWrongRed, fmt.Sprintf("掏空后读方红了（exit=%d）★★ 但普查表没写 red_signal ⇒ ⇒ 无法判定红因\n%s",
			code, tailLines(out, 25))
	}
	if !strings.Contains(out, e.RedSignal) {
		return structuredWrongRed, fmt.Sprintf(
			"掏空后读方红了（exit=%d）★★ 但输出里**没有**约定的红信号 %q ⇒ ⇒ 红的不是「该红的那件事」\n%s",
			code, e.RedSignal, tailLines(out, 25))
	}
	return structuredRedCaught, fmt.Sprintf("掏空后读方红了（exit=%d）且含红信号 %q", code, e.RedSignal)
}

// TestStructuredIsEnvUnavailable 是【分类器自证】：把「环境失败」与
// 「判据失败」分开的那一刀，必须**只**切该切的那一刀。
//
// ★ 为什么必须自证（§531 通用式三：恒真断言要主动做负控）：
//
//	这条分类决定了「掏空门在负载下会不会误判『门没牙』」⇒
//	切宽了 ⇒ 真违规被判成「环境问题」⇒ 门变瞎；
//	切窄了 ⇒ 环境问题被判成「红了但红因不对」⇒ ★ 回到本轮修的那个缺陷。
//
// ★ 判据是两条**取或**：退出码 3（读方侧的「未测量」约定）或输出里的探针
//
//	token（`-NOT-MEASURED`）⇒ ⇒ 读方换了退出码也不怕。
func TestStructuredIsEnvUnavailable(t *testing.T) {
	cases := []struct {
		name string
		code int
		out  string
		want bool
	}{
		{"退出码 3（读方侧的未测量约定）", 3, "", true},
		{"输出里有探针 token（退出码仍是 1）", 1, "✘ [ICON-SIZE-NOT-MEASURED] 浏览器驱动失败", true},
		{"★ 负控：正常判据红", 1, "✘ [ICON-SIZE-BASELINE-UNUSABLE] 基线不可用", false},
		{"★ 负控：自证失败（退出码 2）", 2, "自证 3/5", false},
		{"★ 负控：绿", 0, "✅ 87 页量完", false},
		{"★ 负控：崩溃但退出码 1", 1, "TypeError: x is not a function", false},
	}
	for _, tc := range cases {
		got := structuredIsEnvUnavailable(tc.code, tc.out)
		if got != tc.want {
			t.Errorf("%s：structuredIsEnvUnavailable(code=%d, out=%q) = %v, want %v",
				tc.name, tc.code, tc.out, got, tc.want)
		}
	}
}

// ── 门 ────────────────────────────────────────────────────────────────────

// TestStructuredRegistryHollowoutReadersRejectEmptyRegistry 是【行为判据】。
//
// ★ 对每条 `kind=registry` 且 `behaviorally_verified=true` 的条目：
// ★ 基线绿 ⇒ 掏空 ⇒ 必须红 **且红因正确** ⇒ 还原 ⇒ md5 对上。
func TestStructuredRegistryHollowoutReadersRejectEmptyRegistry(t *testing.T) {
	root := hollowRepoRoot(t)
	healed, err := structuredHealLeftovers(root)
	if err != nil {
		t.Fatalf("自愈预检失败（还原上一次遗留的掏空登记册）：%v", err)
	}
	if healed > 0 {
		t.Logf("自愈预检：还原了 %d 份上一次遗留的掏空登记册", healed)
	}

	census := loadStructuredCensus(t, root)
	checked := 0
	for _, e := range census.Entries {
		if e.Kind != "registry" || !e.BehaviorallyVerified {
			continue
		}
		checked++
		t.Run(e.Path, func(t *testing.T) {
			outcome, detail := structuredVerifyEntry(t, root, e)
			switch outcome {
			case structuredRedCaught:
				t.Logf("✔ %s：掏空 ⇒ 读方红且红因正确 ⇒ 有牙（%s）", e.Path, detail)
			case structuredStillGreen:
				t.Fatalf("%s：★ 掏空后读方**仍然绿** ⇒ ⇒ 读方在「登记册没有条目」时是瞎的 ⇒ "+
					"★ 这就是一个真洞（★ 掏空登记册 ⇒ 门就瞎）⇒ ⇒ 请给读方补一条「登记册为空 ⇒ 红」。\n"+
					"★ 读方：%s %s\n%s", e.Path, e.ReaderKind, e.ReaderSpec, detail)
			case structuredBaselineRed:
				t.Fatalf("%s：读方在【基线】就红 ⇒ ⇒ 不可判定 ⇒ ★ 先把读方修绿。\n%s", e.Path, detail)
			case structuredWrongRed:
				t.Fatalf("%s：★ 红了，但红因不对 ⇒ ⇒ 探针或读方都不对 ⇒ ⇒ 不能算「有牙」。\n%s", e.Path, detail)
			case structuredEnvUnavailable:
				// ★★ 「没量到」是一等状态：跳过并说清原因，**不**指责门没牙、
				//   **不**指责读方坏了。
				//   ★ 这条登记仍然是 behaviorally_verified=true ⇒ ⇒ 下一轮
				//   环境正常时它必须被验到 ⇒ ⇒ 这里跳过不会让它永久失监。
				t.Skipf("%s：★ 读方**没量到**（环境不可用）⇒ ⇒ 本条本轮**不可判定** ⇒ ⇒ 跳过（★ 不是「门没牙」，也不是「读方坏了」）\n%s", e.Path, detail)
			default:
				t.Fatalf("%s：不可判定 ⇒ %s\n★ 请改 hollow_mode（★ 可用：%s），或把该条登记成「本轮验不了」并写明理由",
					e.Path, detail, strings.Join(structuredHollowModeList(), " / "))
			}
		})
	}
	if checked == 0 {
		t.Fatalf("★ 没有一条登记册进入行为化验证 ⇒ ⇒ 判据空转（★ 门恒绿）")
	}
}

// structuredHealLeftovers 是【自愈预检】（★ 非 `.tsv` 版）。
//
// ★ 只还原**普查表里登记过的登记册** ⇒ ⇒ 不会误伤仓里其它 `.hollowout-backup`。
func structuredHealLeftovers(root string) (int, error) {
	// ★★ R87 轮收尾：★ 自愈预检**自己也会写盘**⇒ ⇒ 也要拿【整仓掏空锁】（★ 与 `.tsv` 侧同一把）。
	releaseLock, _, err := lockHollowoutShared(root, hollowoutLockWait)
	if err != nil {
		return 0, err
	}
	defer releaseLock()

	paths, err := structuredRegistryPaths(root)
	if err != nil {
		return 0, err
	}
	healed := 0
	for _, rel := range paths {
		abs := filepath.Join(root, rel)
		if _, err := os.Stat(hollowBackupPath(abs)); err != nil {
			continue
		}
		if err := restoreHollowBackup(abs); err != nil {
			return healed, err
		}
		healed++
	}
	return healed, nil
}

// ── 量具自证（★ 判据自己也要被量） ───────────────────────────────────────

// TestStructuredRegistryHollowTransformHasTeeth 是【量具自证】。
//
// ★ 它证明三件事：
//
//	① **变换本身有效**：★ 给一个合成的登记册 ⇒ 掏空后容器真的空了、数值真的归零。
//	② **变换不动类型**：★ 掏空版的每个节点类型与原件一致 ⇒ ⇒ 读方不会因崩而红。
//	③ **空操作会被认出**：★ 一个「全部是 0」的登记册 ⇒ 掏空后逐值相同
//	   ⇒ ⇒ 必须被判成 inconclusive ⇒ ⇒ **不能**报「绿 ⇒ 门有洞」。
func TestStructuredRegistryHollowTransformHasTeeth(t *testing.T) {
	// ★ ① 合成登记册：条目映射 ＋ 计数字典 ＋ 数组 ＋ 字符串 ＋ 布尔
	src := `{
  "metric": "说明文字",
  "models": {"a": {"price": 1}, "b": {"price": 2}},
  "perGroup": {"web/zh-CN": 3, "web/en-US": 5},
  "tags": ["x", "y"],
  "flag": true
}`
	orig, err := structuredDecodeJSON([]byte(src))
	if err != nil {
		t.Fatalf("解析合成登记册失败：%v", err)
	}
	h := structuredHollowJSON(orig, structuredHollowEntries)

	// ★ 条目映射 ⇒ 整个清空（★ 条目没了 ⇒ 读方看到 0 条）
	models, ok := h.(map[string]any)["models"].(map[string]any)
	if !ok {
		t.Fatalf("★ 掏空后 models 不是对象（★ 类型被改了）：%T", h.(map[string]any)["models"])
	}
	if len(models) != 0 {
		t.Errorf("★ 掏空后条目映射应为空，实际还剩 %d 项", len(models))
	}
	// ★ 计数字典 ⇒ 键保留、数值归零
	perGroup, ok := h.(map[string]any)["perGroup"].(map[string]any)
	if !ok {
		t.Fatalf("★ 掏空后 perGroup 不是对象：%T", h.(map[string]any)["perGroup"])
	}
	if len(perGroup) != 2 {
		t.Errorf("★ 掏空后计数字典的**键集合**必须保留，实际 %d 个键", len(perGroup))
	}
	for k, v := range perGroup {
		n, ok := v.(json.Number)
		if !ok || n.String() != "0" {
			t.Errorf("★ 掏空后 perGroup[%q] 应为 0，实际 %v", k, v)
		}
	}
	// ★ 数组 ⇒ 清空
	tags, ok := h.(map[string]any)["tags"].([]any)
	if !ok {
		t.Fatalf("★ 掏空后 tags 不是数组：%T", h.(map[string]any)["tags"])
	}
	if len(tags) != 0 {
		t.Errorf("★ 掏空后数组应为空，实际 %d 个元素", len(tags))
	}
	// ★ 字符串保留（★ 元数据不是「条目」）
	if got := h.(map[string]any)["metric"]; got != "说明文字" {
		t.Errorf("★ 掏空后字符串叶子应原样保留，实际 %v", got)
	}
	// ★ 类型骨架不变
	if ok, why := structuredShapePreserved(orig, h, "$"); !ok {
		t.Errorf("★ 掏空版改变了节点类型：%s", why)
	}
	// ★ 掏空不是空操作
	if structuredValueEqual(orig, h) {
		t.Errorf("★ 合成登记册的掏空不应是空操作")
	}

	// ★ ② `unusable` 模式：整册 ⇒ `{}`
	empty := structuredHollowJSON(orig, structuredHollowUnusable)
	m, ok := empty.(map[string]any)
	if !ok {
		t.Fatalf("★ unusable 模式下对象应变成 `{}`，实际 %T", empty)
	}
	if len(m) != 0 {
		t.Errorf("★ unusable 模式下对象应变成空对象，实际 %d 个键", len(m))
	}
	// ★ 数组顶层的 unusable ⇒ `[]`
	arr, _ := structuredDecodeJSON([]byte(`[{"a":1}]`))
	ea, ok := structuredHollowJSON(arr, structuredHollowUnusable).([]any)
	if !ok || len(ea) != 0 {
		t.Errorf("★ unusable 模式下数组应变成 `[]`，实际 %v", structuredHollowJSON(arr, structuredHollowUnusable))
	}

	// ★ ③ 空操作必须被认出（★ 否则会把「探针无效」误报成「门有洞」）
	flat, _ := structuredDecodeJSON([]byte(`{"count": 0, "perGroup": {"a": 0}}`))
	flatHollow := structuredHollowJSON(flat, structuredHollowEntries)
	if !structuredValueEqual(flat, flatHollow) {
		t.Fatalf("★ 「全部是 0」的登记册掏空后应逐值相同（★ 空操作），实际变了")
	}
}
