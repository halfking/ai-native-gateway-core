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
		effectSilentlyEmpty:           true,
		effectSilentlyDegradedContent: true,
		effectSilentlyFrozen:          true,
		effectErrorsOut:               true,
		effectUnaffected:              true,
		effectValidator:               true,
		effectUnclassified:            true,
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
