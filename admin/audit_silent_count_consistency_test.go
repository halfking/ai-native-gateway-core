// audit_silent_count_consistency_test.go — 审计文档里「灰度前必须处理的静默档
// 条数」必须与登记表一致（2026-10-02 审计 §9.48）。
//
// # 这道门在防什么
//
// §9.36.3 写下一句「灰度前必须先处理的 19 条（silently_empty +
// silently_degraded_content + silently_frozen）」，并称那张表「是**可执行的清单**」。
// 截至 §9.48 建门时，登记表的静默档实测 **70** 条（106 个读点文件里）——
// 那个 19 是当时只评估了 31 个新增读点时的**样本外推**，而文档没有任何地方
// 标明它是外推值。
//
// 于是半年后做灰度排期的人读到的是 19，实际要面对 70，相差 3.7 倍。这不是
// 「数字过时」这种小事：**它是那份「可执行清单」的唯一量化口径。**
//
// §9.37 已经记过同一个家族的事：登记表的字段若门不读它，它在事实层面是装饰。
// 反过来，文档里的结论若没有门对着它，它同样会静默腐烂——而且腐烂得**更安静**，
// 因为没有测试会因为一段散文变旧而变红。
//
// # 为什么不登记每个读点、只登记一个计数
//
// 逐个读点登记会退化成 §9.37 记录过的那种表（代码演进后条目静默过期）。
// 这里只钉**一个**可从代码算出来的数：它是登记表的全量派生量，没有解释空间，
// 也不可能与登记表脱节。
//
// # 为什么钉在「特征句」而不是一个裸数字
//
// 裸数字在文档里有几十个（§9.26 的 14、§9.36 的 31、§9.39 的 19、批次号……），
// 按数字匹配会钉到别的句子上。所以本门要求文档里存在一句**带固定格式**的话，
// 并从那句话里取数：格式本身就是契约，缺了会给出「该写什么」的错误信息。
package admin

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// silentClaimRE 匹配审计文档里那一句清单口径。
//
// 格式是契约的一部分，不要随意改：改了会让本门找不到，然后报「该写什么」——
// 那是有意的失败方向（宁可红，不要静默放行）。
var silentClaimRE = regexp.MustCompile(
	`灰度前必须处理的静默档：\*\*(\d+)\*\* 条`)

// countSilentStopWriteEffects 直接从登记表算静默档条数。
//
// 三个静默档必须**逐个列出来**，不要用「非 errors_out 且非 unaffected」这种
// 取补集的写法：新增一个档位时，取补集会把它悄悄算进静默数，而它是新增的
// 第五种失效形态、该不该算进清单需要人判断。
func countSilentStopWriteEffects() int {
	n := 0
	for _, c := range requestLogsStopWriteClassification {
		switch c.Effect {
		case effectSilentlyEmpty, effectSilentlyDegradedContent, effectSilentlyFrozen:
			n++
		}
	}
	return n
}

func TestAuditDocSilentClaimMatchesRegistry(t *testing.T) {
	const path = "../docs/audit/2026-09-30-session-request-data-re-audit.md"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败 %v —— 这道门不能在没有文档的情况下判定为通过", path, err)
	}
	want := countSilentStopWriteEffects()

	m := silentClaimRE.FindSubmatch(data)
	if m == nil {
		t.Fatalf("%s 里找不到「灰度前必须处理的静默档：**N** 条」这句话（当前登记表算出 **%d** 条）。\n"+
			"  §9.36.3 那句「19 条」是只评估了 31 个读点时的**样本外推**，已被 §9.48 订正为过期口径。\n"+
			"  请把该句改成固定格式：灰度前必须处理的静默档：**%d** 条\n"+
			"  （本门从登记表实时计算，文档里的数不是事实来源，登记表才是。）",
			path, want, want)
	}
	got, convErr := strconv.Atoi(string(m[1]))
	if convErr != nil {
		t.Fatalf("解析文档里的条数 %q 失败：%v", m[1], convErr)
	}
	if got != want {
		t.Errorf("审计文档说灰度前要处理 %d 条静默档，登记表实际是 %d 条（差 %+d）。\n"+
			"  文档里那一句被 §9.36.3 称为「可执行的清单」——它的量化口径错了，\n"+
			"  排期会按错的规模做。\n"+
			"  改文档：把「灰度前必须处理的静默档：**%d** 条」里的数字改成 %d。\n"+
			"  若你认为登记表判错了档位，那要改的是登记表（并写明理由），不是改文档去迁就它。",
			got, want, want-got, got, want)
	}
}

// TestStopWriteEffectValuesAreFromTheDeclaredSet 让「新增一个失效形态」变成一道
// 会红的事。
//
// 为什么必须单独一道：countSilentStopWriteEffects 刻意**逐个列举**三个静默档，
// 而不是取补集（「非 errors_out 且非 unaffected 即为静默」）。理由是补集会在
// 新增第五种档位时把它悄悄算进静默数，而它该不该算进灰度清单需要人判断。
//
// ⚠ 但这个保护**光靠计数测不出来**：当前分布下补集恰好也等于 70
// （106 − 10 errors_out − 24 unaffected − 2 validator = 70），两种写法给出同一个数。
// 也就是说，countSilentStopWriteEffects 里「列举 vs 补集」这个选择，
// 在今天的登记表上**没有任何判据能区分**。本道门把「新增档位」这件事本身变成
// 可观测：登记表里出现未声明的档位值 ⇒ 红 ⇒ 必须显式决定它算不算静默。
func TestStopWriteEffectValuesAreFromTheDeclaredSet(t *testing.T) {
	declared := map[string]bool{
		effectSilentlyEmpty:             true,
		effectSilentlyDegradedContent:   true,
		effectSilentlyFrozen:            true,
		effectSilentlyDegradedAggregate: true,
		effectErrorsOut:                 true,
		effectUnaffected:                true,
		effectValidator:                 true,
		effectUnclassified:              true,
	}
	seen := map[string]int{}
	for f, c := range requestLogsStopWriteClassification {
		seen[c.Effect]++
		if !declared[c.Effect] {
			t.Errorf("登记项 %q 使用了未声明的档位 %q。\n"+
				"  新增失效形态时必须**显式决定**两件事：\n"+
				"  ① 它算不算「灰度前必须处理」的静默档（决定要改文档里那一句）；\n"+
				"  ② 它在门 TestStopWriteEffectAgreesWithSourceFamily 里的判据是什么。\n"+
				"  不声明的后果是它**不进入** countSilentStopWriteEffects —— 而本门正是为了\n"+
				"  防止「悄悄漏掉一整类失效形态」而存在。", f, c.Effect)
		}
	}
	if t.Failed() {
		return
	}
	// `unclassified` 是合法档位，但**它不在灰度清单上**。所以「清单完整」这件事
	// 需要单独断言：只要还有未逐点评估的读点，文档里那个数字就不是全部工作量，
	// 而它在原句里被称作「可执行的清单」。
	//
	// 实测（2026-10-02 建门时）：106 个读点里 unclassified 为 **0** ⇒ 评估已完整。
	// 本门把这个终态钉住：新增一个未评估读点会立刻报红，而不是等到某次灰度排期
	// 才发现清单少了一截。
	if seen[effectUnclassified] > 0 {
		t.Errorf("登记表里有 %d 个 unclassified 读点（共 %d 个）。\n"+
			"  文档里「灰度前必须处理的静默档：N 条」那一句只统计**已评估**的读点，\n"+
			"  所以它现在不是全部工作量——它自己被称作「可执行的清单」。\n"+
			"  请二选一：① 逐点评估它们；② 在文档那一句里同时写明还有多少未评估。\n"+
			"  不要留着 unclassified 又让那个数字读起来像终态。",
			seen[effectUnclassified], len(requestLogsStopWriteClassification))
	}
	// 已声明但登记表里一条都没用到的档位：不是错误（可能刚加），但值得点名，
	// 因为「声明了却从不被使用」通常是档位名写错了。
	for e := range declared {
		if seen[e] == 0 {
			t.Logf("注意：档位 %q 已声明但登记表里 0 条使用（可能是刚加的档位或名字写错）", e)
		}
	}
}

// TestSilentFormsAreEitherListedOrRegisteredAsExcluded 补上「两头都是沉默」那个洞。
//
// # 漏洞的形状
//
// countSilentStopWriteEffects 刻意**不**取补集（理由见该函数注释）：新增一个静默
// 失效形态时，它该不该进灰度清单需要人判断。可是这个设计的另一面是——
//
//	新增一个静默档 → 它不进 switch → 70 不变 → 两道门全绿 → **什么也没发生**。
//
// 也就是说：「刻意不取补集」把「悄悄算进去」挡住了，却把「悄悄漏掉」放行了。
// 而本文件存在的全部理由就是防后者（见 §9.48 那句「可执行的清单」）。
//
// 判据不是数字，而是**三态穷举**：登记表里用到的每一档，必须能被归到
//
//	① 灰度清单（switch 里的三档）之一，或
//	② silentFormsOutsideGreyList 里**带非空理由**的一条，或
//	③ errors_out / unaffected / validator / unclassified（非静默形态）。
//
// 落在这三类之外的档位 ⇒ 红。⇒ 「新增一个静默档却什么都不说」不再是绿的。
func TestSilentFormsAreEitherListedOrRegisteredAsExcluded(t *testing.T) {
	const (
		listedMarker = "" // switch 里的档用空串标记
	)
	listed := map[string]string{
		effectSilentlyEmpty:           listedMarker,
		effectSilentlyDegradedContent: listedMarker,
		effectSilentlyFrozen:          listedMarker,
	}
	nonSilent := map[string]bool{
		effectErrorsOut:    true,
		effectUnaffected:   true,
		effectValidator:    true,
		effectUnclassified: true,
	}

	seen := map[string]int{}
	for _, c := range requestLogsStopWriteClassification {
		seen[c.Effect]++
	}

	// 登记表里用到的每一档都必须能被归类。
	for effect, n := range seen {
		_, inList := listed[effect]
		_, inNonSilent := nonSilent[effect]
		reason, inExcluded := silentFormsOutsideGreyList[effect]
		switch {
		case inList || inNonSilent:
			// 合规。
		case inExcluded && strings.TrimSpace(reason) != "":
			// 合规：静默形态但被显式排除，且写了理由。
		default:
			t.Errorf("档位 %q 在登记表里用了 %d 次，却既不在灰度清单的 switch 里、"+
				"也不在 nonSilent 集合里、也不在 silentFormsOutsideGreyList 里带理由登记。\n"+
				"  新增失效形态时必须**显式决定**它算不算「灰度前必须处理」的静默档：\n"+
				"  ① 要算   → 加进 countSilentStopWriteEffects 的 switch，并改文档里那一句；\n"+
				"  ② 不算   → 登记进 silentFormsOutsideGreyList 并写明为什么；\n"+
				"  ③ 两处都不做 ⇒ 就是本门要挡的「悄悄漏掉一整类失效形态」。", effect, n)
		}
	}

	// 反向：排除登记表自己不能腐烂。
	// ① 登记了但没人用 ⇒ 过期排除（档位改名/读点消失），它在骗人说「我们考虑过」。
	// ② 登记了但理由是空的 ⇒ 同上，且更坏：它连判断都没留下。
	// ③ 登记了却同时在灰度清单的 switch 里 ⇒ 两处矛盾，70 的口径已不可解释。
	for effect, reason := range silentFormsOutsideGreyList {
		if seen[effect] == 0 {
			t.Errorf("silentFormsOutsideGreyList 登记了档位 %q，但登记表里 0 条使用。\n"+
				"  这是**过期排除**——它让读者以为这一类仍被显式考虑过，而实际上它已经不存在了。\n"+
				"  请删除该登记项。", effect)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("silentFormsOutsideGreyList 对档位 %q 的排除理由是空的。\n"+
				"  排除是一个**判断**，判断必须留下理由；空理由的排除与没有排除无法区分。", effect)
		}
		if _, both := listed[effect]; both {
			t.Errorf("档位 %q 同时在灰度清单的 switch 里、又在 silentFormsOutsideGreyList 里。\n"+
				"  这两处必须互斥：在一处意味着计入 70，在另一处意味着不计入。\n"+
				"  同时出现在两处 ⇒ 70 这个对外数字已不可解释。", effect)
		}
	}
}

// 判据钉在**档位枚举**上：清单口径必须同时点名三个静默档，否则某天有人把其中
// 一个档位从这句话里删掉，数字仍然「对得上」，而清单已经少了一类失效形态。
// 这与 §9.26 记的教训同款：口径被悄悄换掉，而门因为只对数字所以没反应。
func TestAuditDocSilentClaimIsMarkedAsMachineChecked(t *testing.T) {
	const path = "../docs/audit/2026-09-30-session-request-data-re-audit.md"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败 %v", path, err)
	}
	loc := silentClaimRE.FindIndex(data)
	if loc == nil {
		t.Skip("清单口径句不存在；TestAuditDocSilentClaimMatchesRegistry 会报出该写什么")
	}
	// 取该句所在的整行 + 紧随其后的两行（说明通常写在下一段）。
	rest := string(data[loc[0]:])
	end := len(rest)
	if i := len(rest); i > 600 {
		end = 600
	}
	window := rest[:end]

	for _, want := range []string{"silently_empty", "silently_degraded_content", "silently_frozen"} {
		if !strings.Contains(window, want) {
			t.Errorf("清单口径句附近没有点名 %q。\n"+
				"  三个静默档必须同时出现：只写数字而删掉档位名，门会因为数字仍然对得上而不反应，\n"+
				"  而清单已经少了一类失效形态（§9.26 的同款教训：口径被换掉，判据没变）。", want)
		}
	}
}
