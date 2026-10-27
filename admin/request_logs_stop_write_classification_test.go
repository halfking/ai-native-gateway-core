package admin

// request_logs_stop_write_classification_test.go — 2026-10-02：S4 停写的
// 逐文件依赖分类与覆盖率门。
//
// 审计 §8.5 的结论是：读 _request_logs 的生产文件有 104 个、调用点 237 处，
// 而逐点评估**一个都没做过**。`TestRequestLogsReadInventoryIsComplete`
// （request_logs_read_inventory_test.go）钉住了「有 104 个」，但那只数个数，
// 不下判定——所以它防的是「缺口扩大时有人知道」，防不了「缺口已被评估」。
//
// 本文件提供后一半：一个逐文件分类登记，以及一道**覆盖率门**。
//
// # 分级按「停写之后会发生什么」，不按「读的是哪张表」
//
// 早先的分类（审计 §5.5.5）按「会话内读 / 全量流量」分。那是**查询形态**的
// 分类，答的是「能不能迁到 session 原生源」。但运维在灰度前要回答的是另一个
// 问题：「这个功能关停之后会怎样？」——而同一形态的两次读可能给出完全不同的
// 答案：一次 COUNT 会静默变成常数，一次 JOIN 缺失会直接报错。**报错是好事**
// （灰度时会立刻发现），**静默才是灰度的真正风险**。所以这里按后果分。
//
// # 为什么「未分类」不能是一种合法状态
//
// 最容易出现的自欺是：把大部分文件随手归到某一类，让表看起来是满的。
// 所以覆盖率门的判据是**未分类集合必须为空**，并且每条登记都必须带一段
// **在该文件里逐字存在**的证据（见 evidence 字段）——证据不存在即红。
// 这样「我大概知道它是会话内读」和「我打开文件确认过它的 WHERE 长什么样」
// 成了两件不同的事，前者进不来。
//
// 门**现在就红**：截至登记时点仍有未分类项，这是事实，不是门坏了。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	dbpkg "github.com/kaixuan/llm-gateway-go/db"
)

// stopWriteEffect 是「S4 停写之后这个读点会怎样」。
//
// 注意每一档都写明**是报错还是静默**——这是本分类存在的理由。
const (
	// effectErrorsOut：查询形态要求新数据，停写后必然查不到 → 端点 500/空。
	// 这是**可接受**的失败模式：灰度时立刻暴露，不会悄悄给出错误答案。
	effectErrorsOut = "errors_out"

	// effectSilentlyEmpty：结果集变空但不报错。危险等级最高的一档——
	// 接口返回 200、字段齐全、数值全是零或空列表。
	effectSilentlyEmpty = "silently_empty"

	// effectSilentlyFrozen：结果是「存量不再增长」的常数。看不到任何错误信号，
	// 但每个基于它的判断（计数、比率、推荐、健康度）从此只反映停写之前。
	effectSilentlyFrozen = "silently_frozen"

	// effectUnaffected：读的是表的**结构/体量/生命周期**，不是流量。
	// 停写不改变这些语义（保留期、占用空间、待清理行数照常统计）。
	effectUnaffected = "unaffected_by_stop_write"

	// effectValidator：刻意比较 v1 与 session 族的对账面。
	// 停写后它的语义由 §8.8 处理（报告 void 而不是给出许可）。
	effectValidator = "validator_dual_read"

	// effectSilentlyDegradedContent：2026-10-02 新增。**行还在，某一列的内容
	// 静默变空或降级**，接口 200、字段齐全、结果集非空。
	//
	// 为什么必须单列一档（batch1 与 batch4 各自独立撞上同一堵墙）：前五档描述
	// 的都是「结果集」——空不空、冻不冻。但带 bodies 腿的读点停写后是另一种
	// 形状：主腿走 710 视图，**行照常出**（session 臂供数），而
	// `LEFT JOIN request_logs_bodies_* … COALESCE(rb.request_body,'')` 的正文腿
	// 没有 session 兜底 ⇒ 拿到的是**有行、没正文**。
	// 填 silently_empty 不准（结果集没空），填 silently_frozen 也不准（不是冻结
	// 在旧值，是这一列变成空串/NULL/0）。硬塞进任何一档，都会让「bodies 腿到底
	// 算不算硬失败」这个问题被分类表的沉默吞掉。
	effectSilentlyDegradedContent = "silently_degraded_content"

	// effectSilentlyDegradedAggregate：2026-10-03 新增（审计 §9.73.6 扩档裁决）。
	// **行在、reward 在，但 reward 的分项退化**——与上一档的区别是退化发生在
	// 「聚合的分项」而不是「结果的某一列」。
	//
	// 为什么必须单列（§9.49.8 记账、本轮裁决扩档）：settle worker 的
	// outcome 腿在会话族下仍命中（真库实测对 selection request_id 覆盖 99.3%），
	// 所以结果集既没空也没冻；退化的是 `baselines[p.taskType]` 取 miss 时
	// 延迟项与成本项**同时塌成 0.5**（bg/auto_route_settle_baseline_metrics.go:23-30）。
	// 塞进 silently_degraded_content 会让那一档装上它原本没有的东西：
	// 该档的语义是「某一**列**内容静默变空」，而这里是「reward 的**分项**退化」。
	//
	// ★ **本档刻意不计入「灰度前必须处理的静默档」清单**（70 条保持不变）。
	// 理由与登记见 silentFormsOutsideGreyList——排除是一个**显式决定**，
	// 不是遗漏：清单的三个档位各自对应「结果集空 / 某一列空 / 整体冻结」三种
	// 可用同一句话向排期人解释的形态，而本档的处置方式（重设基线总体，§9.73.4）
	// 不在这三种里。把它算进 70 会让 70 这个已被订正过两次的对外数字再变一次，
	// 而排期人无法从数字本身看出多出来的那一类该怎么修。
	effectSilentlyDegradedAggregate = "silently_degraded_aggregate"
)

// silentFormsOutsideGreyList 登记「**是静默失效形态、但被显式排除在灰度清单
// 之外**」的档位。
//
// 为什么需要这张表：`countSilentStopWriteEffects`（audit_silent_count_consistency_test.go）
// 刻意逐个列举三个静默档而**不**取补集。取补集会把新增的静默形态悄悄算进
// 灰度清单，而不取补集又意味着「新增一个静默档却什么都不说」会得到同样干净的绿。
// ⇒ 两头都是沉默。这张表把第三种可能显式化：**排除，但写明为什么、且必须真的被用过**。
//
// ⚠ 本表**只登记排除决定**，不改变 70 这个数字。若将来决定把某档纳入清单，
// 必须同时（a）把它加进 countSilentStopWriteEffects 的 switch，
// （b）从本表删除，（c）改文档里那一句——三处缺一都会让门红。
// silentFormsOutsideGreyList 登记「**是静默失效形态、但被显式排除在灰度清单
// 之外**」的档位。
//
// 为什么需要这张表：`countSilentStopWriteEffects`（audit_silent_count_consistency_test.go）
// 刻意逐个列举三个静默档而**不**取补集。取补集会把新增的静默形态悄悄算进
// 灰度清单，而不取补集又意味着「新增一个静默档却什么都不说」会得到同样干净的绿。
// ⇒ 两头都是沉默。这张表把第三种可能显式化：**排除，但写明为什么、且必须真的被用过**。
//
// ⚠ 本表**只登记排除决定**，不改变灰度清单的数字。若将来决定把某档纳入清单，
// 必须同时（a）把它加进 countSilentStopWriteEffects 的 switch，
// （b）从本表删除，（c）改文档里那一句——三处缺一都会让门红。
//
// ★ 2026-10-20（迁移 890，v1 族真删）：本表**已清空** —— 停写分级表随之清空，
// 没有任何档位被使用 ⇒ 排除登记全部过期（门会要求删除）。
var silentFormsOutsideGreyList = map[string]string{
	// 890 之后：停写分级表为空 ⇒ 没有任何档位在使用 ⇒ 无排除登记。
}

// 合法分级集合。新增档位必须同时更新本集合与本文件顶部的说明。
var stopWriteEffects = map[string]string{
	effectErrorsOut:                 "停写后必然查不到，端点报错或返回空——可接受的失败模式",
	effectSilentlyEmpty:             "结果集静默变空但接口仍 200——灰度的真正风险",
	effectSilentlyFrozen:            "结果静默冻结为停写前的常数，无任何错误信号",
	effectUnaffected:                "读表结构/体量/生命周期，不读流量——停写不改变其语义",
	effectValidator:                 "刻意做 v1↔session 对账，语义由 dual-read 门处理",
	effectSilentlyDegradedContent:   "行还在但某一列内容静默变空/降级（典型：bodies 腿无 session 兜底）",
	effectSilentlyDegradedAggregate: "行与 reward 都在但 reward 的分项退化（典型：基线 cohort 取 miss ⇒ 延迟/成本项同时塌成 0.5）",
}

// stopWriteClassification 是逐文件登记。
//
//	Evidence 必须逐字出现在该文件里（门会核）。它不是描述，是**锚**：
//
// 用来把「我读过这个文件」和「我以为我读过这个文件」分开。
// bodiesUnaffectedJustification 登记「读 bodies 族但判 unaffected」的**具名豁免**。
//
// 与其他豁免表不同，本表刻意**极小且每条都写机制**。判据的默认是拒绝：
// 放进这张表等于公开声明「我知道 bodies 没有 session 兜底，我论证了为什么
// 这个文件仍然不受影响」。空字符串或缺项都会被门判红。
//
// 每条豁免都必须回答同一个问题：**停写之后，这个读点返回的东西会变吗？**
// bodiesUnaffectedJustification 登记「读 bodies 族但判 unaffected」的**具名豁免**。
//
// 与其他豁免表不同，本表刻意**极小且每条都写机制**。判据的默认是拒绝：
// 放进这张表等于公开声明「我知道 bodies 没有 session 兜底，我论证了为什么
// 这个文件仍然不受影响」。空字符串或缺项都会被门判红。
//
// 每条豁免都必须回答同一个问题：**停写之后，这个读点返回的东西会变吗？**
//
// ★ 2026-10-20（迁移 890，v1 族真删）：本表**已清空**。
// 四条豁免的对象都是「读 v1 bodies 腿」的文件，而 890 之后
// **没有任何生产文件再读 v1 bodies 腿**（bodies 读端只剩会话族）⇒
// 这条豁免的前提（bodies 腿硬失败）不再发生。
//
// ⚠ 本表**保留**（不删）：它守的是「默认拒绝 + 具名放行」这条规矩本身 ——
// 将来若有人重新引入一条 bodies 腿读点，必须在这里具名论证。
var bodiesUnaffectedJustification = map[string]string{
	// 890 之后：读 v1 bodies 腿的文件为零 ⇒ 无豁免条目。
	//
	// ⚠ 不要在这里手填「已知的例外」：已退役关系的残留引用登记在
	// retiredV1ReferenceExceptions（那是生产缺陷台账）。
}

// requestLogsStopWriteClassification 是「停写 v1 之后每个读点会怎样」的逐文件登记。
//
// # 890 之后（2026-10-20）：本表**已清空**
//
// 迁移 890 把 v1 族**物理删除**了 ⇒ 「停写 v1 之后会发生什么」这个问题
// **不再有对象**：没有文件读 v1，也就没有读点会在 v1 停写后退化。
//
// ⇒ 表随之清空。但**表保留、门保留** —— 它守的不变式从
// 「每个读点都必须被分级」翻成：
//
//	**不许出现新的 v1 读方**（总体为空 ⇒ 分级表必须为空）。
//
// # 为什么不能把门一起删掉
//
// 删表 + 删门 = 「不许有新的 v1 读方」这条不变式没人守。
// ⇒ 门保留，判据从「逐条登记齐全」翻成「**表必须为空且实测读方必须为空**」，
// 并给判据配**阳性对照**（合成条目必须被判出问题），避免它退化成恒绿。
//
// # 登记格式不变（将来有人重新引入 v1 读点时照旧填）
//
//	Evidence 必须逐字出现在该文件里（门会核）。它不是描述，是**锚**：
//	用来把「我读过这个文件」和「我以为我读过这个文件」分开。
type stopWriteClassification struct {
	Effect   string
	Evidence string
	Note     string
}

// 尚未逐点评估的文件必须显式登记为 unclassified，而不是从表里消失——
// 「表里没有」和「已确认无关」是两件事，只有前者能被门看见。
const effectUnclassified = "unclassified"

var requestLogsStopWriteClassification = map[string]stopWriteClassification{
	// 890 之后：v1 读方总体为零 ⇒ 停写分级表为空。
	//
	// ⚠ **不要在这里手填「已知的例外」** —— 已退役关系的残留引用登记在
	// retiredV1ReferenceExceptions（那是生产缺陷台账，不是读点分级）。
}

// TestRequestLogsStopWriteClassificationProgress 每次都跑，负责两件事：
// ① 反向：分级表里不该有已退出读点清单的文件（否则「104 个已评估」本身陈旧）；
// ② 正向：把「已评估 N/104」打进日志，让缺口在常规测试输出里**看得见**。
//
// 它**不**在未评估时失败。理由：这是「把一件事做完」的进度条，不是「防止事情
// 变坏」的守卫；让它常红只会挡住所有人，却不会让那 98 个文件被评估。
// 硬条件（未评估必须为零）放在 build tag `s4audit` 下，见
// TestRequestLogsStopWriteNothingLeftUnclassified。
//
// 进度门把「正向检查」和「完成条件」拆开，是被自己的第一版实现逼出来的：
// 原写法的 HasNoUnclassified 只遍历分级表里已有的条目，于是「只登记 3 个」时
// 未分类集合恒为空、门恒绿——**它守的正是「没登记」这件事，却因为分母取错而
// 看不见没登记**。凡是「未完成项」类判据，分母必须取自**应当被完成的那份清单**。
// TestRequestLogsStopWriteClassificationProgress 守「停写分级表与读点总体一致」。
//
// # 890 之后（2026-10-20）：分母归零 ⇒ 本门的新不变式是「两者都必须为空」
//
// 原来这道门是**进度条**（它**不**在未评估时失败；硬条件在 build tag `s4audit` 下）。
// 890 之后 v1 读点总体为零 ⇒ 进度条的分母也是零 ⇒ 进度条本身没有信息量了。
// ⇒ 本门翻成：
//
//	① 分级表里的文件必须在已知读点全集里（反向：陈旧登记）——原语义保留；
//	② 已知读点全集必须为空（890 之后没有 v1 读方）；
//	③ 分级表必须为空；
//	④ **阳性对照**：进度计算本身必须有效 —— 用合成条目证明
//	   「未评估清单」这条判据会响（否则总体为空时它是恒真空）。
func TestRequestLogsStopWriteClassificationProgress(t *testing.T) {
	known := map[string]bool{}
	for _, f := range allKnownRequestLogsReaderFiles(t) {
		known[f] = true
	}

	// ① 反向：分级表里不该有已退出读点清单的文件（否则「已评估 N」本身陈旧）。
	for file := range requestLogsStopWriteClassification {
		if !known[file] {
			t.Errorf("分级表里的 %s 不在已知读点全集（直接表 ∪ 间接表）里 —— "+
				"该文件要么不再读 v1，要么清单陈旧；两种情况都让「N 个已评估」这句话失真", file)
		}
	}

	todo := unassessedStopWriteReaders(known, requestLogsStopWriteClassification)
	total := len(known)
	t.Logf("S4 停写逐点评估进度：%d/%d 已评估，未评估 %d 个 —— %s",
		total-len(todo), total, len(todo), progressVerdict(len(todo)))
	if len(todo) > 0 {
		t.Logf("未评估清单：\n  %s", strings.Join(todo, "\n  "))
	}

	// ② + ③ 890 之后：读点总体与分级表都必须为空。
	if total != 0 {
		t.Errorf("已知读点全集仍有 %d 个文件 —— 890 之后 v1 族已物理删除，不该再有读点。\n"+
			"新出现的读点要么是生产侧漏改（登记进 retiredV1ReferenceExceptions），"+
			"要么是本门把「读 canonical 视图」误算成读 v1 —— 后者必须修扫描口径，不得改本门。",
			total)
	}
	if len(requestLogsStopWriteClassification) > 0 {
		t.Errorf("停写分级表仍有 %d 条登记 —— 890 之后没有 v1 读点，也就没有需要分级的对象。",
			len(requestLogsStopWriteClassification))
	}

	// ④ 阳性对照：进度计算本身必须有效（合成条目，不碰真实表）。
	//
	// ⚠ 没有这一条，「未评估清单恒空」这件事无法与「判据没在跑」区分。
	synthKnown := map[string]bool{"synthetic/unclassified.go": true, "synthetic/classified.go": true}
	synthTable := map[string]stopWriteClassification{
		"synthetic/unclassified.go": {Effect: effectUnclassified},
		"synthetic/classified.go":   {Effect: effectErrorsOut},
	}
	got := unassessedStopWriteReaders(synthKnown, synthTable)
	if len(got) != 1 || got[0] != "synthetic/unclassified.go" {
		t.Errorf("合成数据下未评估清单 = %v，期望 [synthetic/unclassified.go] —— "+
			"进度计算坏了（表空着的时候，这是唯一还能证明它有效的手段）", got)
	}
}

// unassessedStopWriteReaders 返回尚未逐点评估的读点清单。
//
// **纯函数**：分母（已知读点全集）与表都从参数进来 ⇒ 可以在合成数据上直接驱动，
// 用来证明「未评估清单」这条判据不是恒真空。
func unassessedStopWriteReaders(known map[string]bool, table map[string]stopWriteClassification) []string {
	var todo []string
	for file := range known {
		c, ok := table[file]
		switch {
		case !ok, c.Effect == effectUnclassified:
			todo = append(todo, file)
		}
	}
	sort.Strings(todo)
	return todo
}

func progressVerdict(todo int) string {
	if todo == 0 {
		return "已全部评估"
	}
	return "缺口仍在，S4 灰度前置条件未达成；填法见 " + classificationHowTo
}

// classificationHowTo 是填分级登记的操作说明。它被三处引用（进度日志、覆盖率门、
// 以及门失败时的提示），所以写成常量而不是复制三份——措辞一旦分叉，三个地方
// 会给出互相矛盾的做法。
const classificationHowTo = `打开该文件、找到读 _request_logs 的位置，按「停写之后会发生什么」归入：

  errors_out              停写后必然查不到 → 报错/空。可接受的失败模式，灰度时立刻暴露
  silently_empty          结果集静默变空，接口仍 200、字段齐全、全零/空列表
  silently_frozen         结果静默冻结为停写前的常数，无任何错误信号
  unaffected_by_stop_write  读表结构/体量/生命周期，不读流量
  validator_dual_read     刻意做 v1↔session 对账
  silently_degraded_content  行还在，但某一列内容静默变空/降级（第 6 档，2026-10-02）

并把该处**逐字**的一段 SQL 片段填进 Evidence（门会核它是否真的在文件里）。

先看族再看后果：只读 710 视图的读点停写后**不会查空**（视图含 session 臂），
判 silently_empty 会被 TestStopWriteEffectAgreesWithSourceFamily 判红。

⚠️ 但「读 710 视图」本身**不**再禁止 silently_empty：migration 710 在 session 臂上
把 30 列补成了 NULL（client_model / provider_id / attachments / outbound_msg_count /
api_key_owner_user / gw_task_id / model_chosen …）。视图**照样返回行**，但只要
WHERE / GROUP BY / JOIN 用到这些列，就是「行级有、谓词级空」——按该列过滤恒 0 行。
族分类器会把这类文件分到 reads_view_with_null_padded_predicate 族。
在该族里判 unaffected 需要在 nullPaddedUnaffectedJustification 里具名论证。
不要按「读哪张表」或「函数名像不像会话查询」来分——审计 §5.5.6 记过：第一版把
gw_task_id 过滤的三处误判成会话内读，差了两档。`

// TestRequestLogsStopWriteClassificationEvidenceIsReal 钉住「证据必须存在」。
//
// 这是整个登记的承重点：没有它，104 条登记可以在一个下午凭印象写完，而门全绿。
// 变异验证：把任一 Evidence 改成文件里没有的片段即红。
// TestWaterfallStopWriteAssessmentUsesCanonicalSource 守 waterfall API 的取源。
//
// # 890 之后（2026-10-20）：「v1-only 404 风险」已不存在，但「仍读 canonical 视图」仍在
//
// 原来这道门断言 waterfall 的分级是 errors_out、证据是 `FROM request_logs_with_current_month`，
// 理由是「canonical 视图当时**含 v1 臂** ⇒ 只有 v1 有的行查不到 ⇒ 404」。
//
// 890 之后 canonical 视图 = **会话族两臂体**（118 列同名同序）⇒ 它仍然返回行，
// 「v1-only 404」这个风险**不存在了** ⇒ waterfall 也不再是 v1 读方
// （它不在停写分级表里，也不在读点清单里）。
//
// ⇒ 本门改为守两件仍然成立的事：
//
//	① waterfall 仍然读 canonical 视图（没有偷换成别的源）；
//	② waterfall **不是** v1 读方（不在停写分级表 / 读点清单里）。
func TestWaterfallStopWriteAssessmentUsesCanonicalSource(t *testing.T) {
	root := repoRootFromCaller(t)
	raw, err := os.ReadFile(filepath.Join(root, "cmd/gateway/waterfall_by_request.go"))
	if err != nil {
		t.Fatalf("read waterfall source: %v", err)
	}
	if !strings.Contains(string(raw), "FROM "+canonicalView) {
		t.Errorf("cmd/gateway/waterfall_by_request.go 不再读 canonical 视图（%s）—— "+
			"取源被换掉了的话，waterfall 的返回口径就变了，必须重新评估。", canonicalView)
	}
	if hits := retiredV1RelationsInSQL(string(raw)); len(hits) > 0 {
		t.Errorf("cmd/gateway/waterfall_by_request.go 读到了已退役的 v1 族关系 %v —— "+
			"890 之后这些关系已被物理删除，运行时会 42P01。", hits)
	}
	if c, ok := requestLogsStopWriteClassification["cmd/gateway/waterfall_by_request.go"]; ok {
		t.Errorf("cmd/gateway/waterfall_by_request.go 仍登记在停写分级表里（%+v）—— "+
			"890 之后它不再读 v1，不该有停写分级。", c)
	}
}

// TestRequestLogsStopWriteClassificationEvidenceIsReal 钉住「证据必须存在」。
//
// 这是整个登记的承重点：没有它，分级登记可以在一个下午凭印象写完，而门全绿。
// 变异验证：把任一 Evidence 改成文件里没有的片段即红。
//
// # 890 之后（2026-10-20）：表清空 ⇒ 逐条校验变成空转 ⇒ 判据必须有牙
//
// ⇒ ① 分级表必须为空；② **阳性对照**：用一条**合成分级**（Evidence 指向
// 真实存在但内容对不上的片段）驱动同一个校验函数，它**必须**被判出问题；
// ③ 一条各项都合规的合成分级，**必须**不被判出问题。
func TestRequestLogsStopWriteClassificationEvidenceIsReal(t *testing.T) {
	root := repoRootFromCaller(t)

	if len(requestLogsStopWriteClassification) > 0 {
		t.Errorf("停写分级表仍有 %d 条登记 —— 890 之后没有 v1 读点，不该有分级登记。",
			len(requestLogsStopWriteClassification))
	}

	// 阳性对照：证据校验器必须能区分「文件里真有的片段」与「编造的片段」。
	const probeFile = "cmd/gateway/waterfall_by_request.go"
	bad := stopWriteClassification{
		Effect:   effectErrorsOut,
		Evidence: "FROM request_logs_with_current_month_这段片段在任何文件里都不存在",
	}
	if problems := stopWriteEvidenceProblems(t, root, probeFile, bad); len(problems) == 0 {
		t.Errorf("一条 Evidence 指向不存在片段的合成分级没有被判出问题 —— " +
			"证据校验器没有牙（表空着的时候，这是唯一还能证明它有效的手段）")
	}
	good := stopWriteClassification{
		Effect:   effectErrorsOut,
		Evidence: "FROM " + canonicalView,
	}
	if problems := stopWriteEvidenceProblems(t, root, probeFile, good); len(problems) != 0 {
		t.Errorf("一条各项都合规的合成分级被判出问题 %v —— 证据校验器本身坏了", problems)
	}
}

// stopWriteEvidenceProblems 校验一条停写分级登记（返回问题列表，空 = 合格）。
//
// 为什么抽成函数：分级表在 890 之后清空 ⇒ 「逐条校验」这段循环不再执行 ⇒
// 「校验逻辑有没有牙」就没人证明了。抽出来才能在**合成登记**上直接驱动它。
func stopWriteEvidenceProblems(t *testing.T, root, file string, c stopWriteClassification) []string {
	t.Helper()
	var problems []string
	if c.Effect == effectUnclassified {
		return problems
	}
	if _, ok := stopWriteEffects[c.Effect]; !ok {
		return []string{"未知分级 " + c.Effect + "（合法值：" + effectsKeyList() + "）"}
	}
	if strings.TrimSpace(c.Evidence) == "" {
		return []string{"Evidence 为空——分级必须带一段在该文件里逐字存在的证据"}
	}
	raw, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		return []string{"读取失败 " + err.Error() + "（登记里的文件必须存在）"}
	}
	if !inventoryEvidenceInCode(string(raw), c.Evidence) {
		problems = append(problems, "Evidence 未出现在该文件的有效非注释 Go 代码中："+c.Evidence)
	}
	// ★ 两跳校验（§9.232）：Evidence 锚在**切换层调用**上时，
	// 「逐字存在」只证明了「这个文件提到了那个函数」，
	// **不**证明「这个文件读 v1」—— v1 关系名在**切换层那个文件**里。
	//
	// ⇒ 要拦的是反方向：Evidence 里出现一个 `join`/`from` + `fn()` 的拼接点，
	// 而那个 `fn` **没有**被登记为切换层 ⇒ 那种情况下「停写后会退化」的分级
	// **没有任何事实基础**。第二跳按**文件**判，不按 Evidence 判（§9.233）。
	for _, fn := range danglingSourceSQLCalls(t, string(raw)) {
		problems = append(problems, "调用了 "+fn+"，但它既不在 db 包里定义"+
			"（那是会话族 raw helper 的位置），也不是可核的本地固定SQL或已登记的间接读源"+
			"（那是切换层的位置）")
	}
	return problems
}

func danglingSourceSQLCalls(t *testing.T, src string) []string {
	t.Helper()
	re := regexp.MustCompile(`\b(\w*SourceSQL)\s*\(`)
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		fn := m[1]
		if seen[fn] || isRegisteredSwitchFunc(fn) {
			continue
		}
		seen[fn] = true
		if !definedInDBPackage(t, fn) && !localLiteralRequestLogSource(src, fn) {
			out = append(out, fn)
		}
	}
	sort.Strings(out)
	return out
}

// A local, argument-free function returning one immutable SQL literal exposes
// its real relations in this same file. Dynamic builders still need registration.
func localLiteralRequestLogSource(src, name string) bool {
	f, err := parser.ParseFile(token.NewFileSet(), "local.go", src, 0)
	if err != nil {
		return false
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != name || fn.Recv != nil || fn.Type.Params.NumFields() != 0 || fn.Body == nil || len(fn.Body.List) != 1 {
			continue
		}
		ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return false
		}
		lit, ok := ret.Results[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return false
		}
		sql, err := strconv.Unquote(lit.Value)
		if err != nil || !requestLogsReadPattern.MatchString(stripSQLComments(sql)) {
			return false
		}
		// A same-named declaration cannot prove a selector or shadowed closure.
		// Parse with object resolution and require every call to bind this declaration.
		calls, bound := 0, true
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch callee := ast.Unparen(call.Fun).(type) {
			case *ast.Ident:
				if callee.Name == name {
					calls++
					bound = bound && callee.Obj != nil && callee.Obj.Decl == fn
				}
			case *ast.SelectorExpr:
				if callee.Sel.Name == name {
					bound = false
				}
			}
			return true
		})
		return calls > 0 && bound
	}
	return false
}

// dbDefinedFuncs 是 db 包里定义的全部函数名，**每个测试进程只算一次**。
//
// ⚠ 第一版没有这个缓存：每问一个 `fn` 就重做一次**全仓 WalkDir** + 重读
// db 下全部文件。而判据是对**每个已分类文件**、**每个调用到的 `*SourceSQL(`**
// 各问一次 —— 实测全量 admin 门从 509s 涨到 25 分钟以上。
//
// ⇒ 成本从 O(分类文件数 × 函数名数 × 全仓大小) 降到 O(全仓大小) 一次。
// 判据的**结论不变**（同一批函数名），只是不再重复算。
var (
	dbDefinedOnce sync.Once
	dbDefinedSet  map[string]bool
)

func dbDefinedFuncs(t *testing.T) map[string]bool {
	t.Helper()
	dbDefinedOnce.Do(func() {
		dbDefinedSet = map[string]bool{}
		root := repoRootFromCaller(t)
		funcDefRE := regexp.MustCompile(`(?m)^func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)\s*\(`)
		for _, rel := range productionGoFiles(t, root) {
			if !strings.HasPrefix(rel, "db/") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Fatalf("read %s: %v", rel, err)
			}
			for _, m := range funcDefRE.FindAllStringSubmatch(string(raw), -1) {
				dbDefinedSet[m[1]] = true
			}
		}
	})
	return dbDefinedSet
}

// definedInDBPackage 报告 db 包里是否定义了名为 fn 的函数。
func definedInDBPackage(t *testing.T, fn string) bool {
	t.Helper()
	return dbDefinedFuncs(t)[fn]
}

// isRegisteredSwitchFunc 报告 fn 是否是某个已登记切换层返回关系名的函数。
func isRegisteredSwitchFunc(fn string) bool {
	for _, e := range indirectRequestLogsReaders {
		if e.SwitchFunc == fn {
			return true
		}
	}
	return false
}

// stopWriteSourceFamily 是**机械可判定**的那一维：直接看文件读的是哪一族表。
//
// 为什么要单独一维、且放在「后果」之前：后果**依赖**于读的是哪一族，而这个依赖
// 在本项目里被集体判错过一次。2026-10-02 的批量评估中，「只读 710 视图」的一批
// 文件被逐个判成 silently_empty（停写后查不到）——**系统性错误**。真库实测：
//
//	request_logs_with_current_month 的定义含四张基表：
//	  session_turns ∪ session_turns_hot ∪ _request_logs ∪ _request_logs_hot
//	24h 实测：视图 7,221 行 = session 臂 2,639（36.55%）+ v1 臂 4,582（63.45%）
//
// 停写只冻结 v1 那条臂，**session 那条臂继续增长**（镜像链不归 S4 管）。所以视图
// 读者停写后仍拿得到数、只是少计。判「空」会把一批实际还能用的读点误报成最危险档，
// 进而把真正该处置的基表读者淹没——这正是把子代理结论照单全收会发生的事。
//
// 四族的差别本质是**有无 session 侧兜底**：
//
//	familyBodies —— bodies 族只有 v1 一份，session 侧无等价物
//	               （session_bodies 只有增量、无 final_full 全量，见审计 §6）
//	               ⇒ 停写后硬失败，无退路。
//	familyView   —— 710 视图有 session 臂 ⇒ 静默少计。
//	familyBase   —— _request_logs / _request_logs_hot 为 v1 专有 ⇒ 完全停止增长。
//	familyBodiesMixed —— 一并读，但**其中 bodies 腿没有任何 session 兜底**。
//	               「一并读」不等于「部分退化」：主轮次腿可能照常工作，
//	               而正文腿彻底失效，表现为「拿得到轮次、拿不到正文」。
//	familySessionBase —— session 供给（710视图或本地UNION）+ 基表腿（停写即冻结）⇒ 部分退化。
const (
	familyBodies      = "reads_bodies_family"
	familyView        = "reads_710_view_only"
	familyBase        = "reads_base_tables_only"
	familyBodiesMixed = "reads_bodies_plus_other"
	familySessionBase = "reads_session_and_base"
	// familyViewNullPadded：读 710 视图（行级有 session 臂），但**谓词/分组/连接**
	// 用到了 session 臂恒 NULL 的 30 列之一 ⇒ 行级有、谓词级空。
	familyViewNullPadded = "reads_view_with_null_padded_predicate"
	// familySessionOnly：**只**读会话族（既不读 v1、也不读 canonical 视图、
	// 也不带 bodies 腿）。
	//
	// ★ 2026-10-20（迁移 890，v1 族真删）：这是本轮新增的一族。
	//
	// 原来没有这一族，是因为「不读 v1 也不读视图」的文件会被判成
	// **undetermined**（六族都不沾）——而在 890 之前那确实是个病态：
	// 读面要么读 v1、要么读视图、要么读会话族原生源，后者被算成
	// 「只读会话族」而**当时没有这一档**。
	//
	// 890 之后 v1 族整体退役 ⇒ 「只读会话族」成了**唯一正常**的形态
	// （实测：domains/providerprofile/adapters.go、bg/auto_index_refresher.go
	// 都落进这一族）。⇒ 不新增这一族，它们就会被报成「未归入任何一族」，
	// 而那道门会让人以为「有个文件读法不明，需要人看一眼」——
	// 那正是本轮要消除的噪音。
	familySessionOnly = "reads_session_family_only"
)

// sessionFamilyMarkers 是「这个文件读会话族」的标志。
//
// ⚠ 必须同时认**关系名**与 **db 侧 helper 名**：生产代码里两种写法都有
// （`FROM public.session_turns_hot t` 与 `FROM ` + dbpkg.SessionFamilyTurnsHotSourceSQL()）。
// 只认关系名 ⇒ helper 形态的文件被漏判（实测 domains/providerprofile/adapters.go
// 全部走 helper 形态）。
var sessionFamilyMarkers = []string{
	"session_turns",
	"session_bodies",
	"SessionFamilyTurnsSourceSQL",
	"SessionFamilyTurnsHotSourceSQL",
	"SessionFamilyTurnsForSessionSQL",
	"SessionFamilyBodiesSourceSQL",
}

// readsSessionFamily 报告（已剥注释的）源码里是否读会话族。
func readsSessionFamily(code string) bool {
	for _, m := range sessionFamilyMarkers {
		if strings.Contains(code, m) {
			return true
		}
	}
	return false
}

var (
	gateStopWriteLineCommentRE  = regexp.MustCompile(`(?m)//[^\n]*`)
	gateStopWriteBlockCommentRE = regexp.MustCompile(`(?s)/\*.*?\*/`)
	// 注释先剥是硬要求：本仓 40 条 grep 命中里 36 条是注释，不剥的话
	// 「读 710 视图」会被历史注释里的提及带偏。
	familyBodiesRE = regexp.MustCompile(`(?i)_?request_logs_bodies`)
	familyBaseRE   = regexp.MustCompile(`(?i)\bfrom\s+_?request_logs(_hot)?\b`)
	// 视图族**不能**靠名字模式判定：名字带 request_logs_with_ 的中间层视图
	// 可能仍是纯 v1。下面的 requestLogsViewsWithSessionArm 才是判据，
	// 且它由真库门 TestRequestLogsViewSessionArmPinIsCurrent 钉住。
	familyAnyViewRE = regexp.MustCompile(`(?i)\brequest_logs_(with_[a-z_]+|prev)\b`)
)

// requestLogsViewsWithSessionArm 是「停写后仍由 session 臂供数」的视图白名单。
//
// ⛔ 这份名单是**真库实测**得出的，不是从名字或迁移文件推的（2026-10-02）。
// 我最初用 `request_logs_with_[a-z_]+` 判定视图族，结果把两个中间层视图
// 一起算进了「停写后仍供数」——真库 pg_get_viewdef 显示它们是**纯 v1**：
//
//	request_logs_with_current_month                        HAS_SESSION_ARM
//	request_logs_with_current_month_without_customer_id    V1_ONLY   ← 我判错了
//	request_logs_with_current_month_without_request_class_due_at  V1_ONLY  ← 我判错了
//	request_logs_bodies_with_current_month                 V1_ONLY（bodies 族，本就无 session 臂）
//
// 名字带 `request_logs_with_` 的两个 `_without_*` 视图是 577/734 迁移为了
// 规避投影缺失而**故意**建成 v1-only 的中间层（见 audit §9.7）。把它们当视图族
// 会让 7 个生产文件的停写后果被系统性低估。
//
// 白名单必须是**白**名单而不是「凡 view 皆算」：新增一个 v1-only 视图时，
// 它默认落进 base 族（保守、正确方向），要改成视图族得显式加进来并说明理由。
var requestLogsViewsWithSessionArm = map[string]struct{}{
	"request_logs_with_current_month": {},
}

// sessionArmNullPaddedColumns 是 canonical 视图 session 臂上**仍为 NULL 补位**的列。
//
// 2026-10-02（§9.26）把它从硬编码的 30 列改成**从生效投影派生**，因为原来那份
// 钉在 migration 710 的 $proj$ 块上——那是 734 之前的形态，而现网早已是
// 734 的 details-joined 体：
//
//   - 710：30 列 `NULL::` 占位；
//   - 734：其中 **24 列**换成了 session_turn_details 特征层的真实值
//     （client_model / provider_id / attachments / quality_* / request_class …）；
//   - 815：再补 3 列（origin_stage / token_band / client_forwarded_for）。
//
// 真库 details 覆盖率 99.9996%（hot 1,344/1,344 无缺失；parent
// 1,683,104/1,683,098）⇒ 那 24 列在 session 分臂**不是空的**。
//
// 后果不是「多报几个」，而是方向反了：族分类器把「行级有值」判成「谓词级空」，
// 于是会**拒绝正确的判定**（本文件自己的注释就写过这条后果），并让 105 条读端
// 分类里的一批被按错误前提登记。§9.21 的「39 个读方读了恒 NULL 的补位列」同源
// —— 同一个被 710 形态污染的基线。
//
// 派生口径 = db.RequestLogsViewPaddedSessionColumns()（生效投影 withDetails=true
// 里形如 `NULL::` 的表达式对应列名）。权威在 db 包，这里只做转换，理由与
// 逐列裁决一起登记在 db/request_logs_view_padded_columns.go。
//
// **仍然成立的那一半**：这 6 列对读方依旧是「行级有、谓词级空」——视图照常返回
// 行，但按这些列过滤的结果集恒为空。且这 6 列里最要紧的 `id` 是**证明不可投影**
// 的（见同文件裁决表），所以它们只能靠改读法消解，不能靠补投影消解。
var sessionArmNullPaddedColumns = func() map[string]struct{} {
	m := make(map[string]struct{})
	for _, c := range dbpkg.RequestLogsViewPaddedSessionColumns() {
		m[c] = struct{}{}
	}
	return m
}()

// sessionArmDetailsSuppliedColumns 是 734 用 session_turn_details 特征层顶掉的
// 那批列：**行级有值、缺 details 行时 NULL**。它们与上面那 6 列不是同一类风险，
// 所以单独登记而不是混进补位表——
//
//   - 补位列：恒 NULL（details 缺行与否都一样），过滤它们恒空；
//   - 本组：99.9996% 有值，过滤它们基本可用，只有 details 缺行的那 6 行会丢。
//
// 登记它是为了让「详情层缺行」这件事有一条独立的、可被单独盯的账，而不是
// 藏在「补位」这个过宽的词里。
var sessionArmDetailsSuppliedColumns = func() map[string]struct{} {
	m := make(map[string]struct{})
	for _, c := range dbpkg.SessionFamilyDetailsProjectionColumns() {
		m[c] = struct{}{}
	}
	return m
}()

// legacySessionArmNullPaddedColumns710 是 710 形态下的 30 列清单，**仅**供
// TestSessionArmNullPaddedColumnsMatchMigration 做差集报告用。
//
// 保留它的理由：那张表是「我们曾经相信过什么」的记录。删掉它，下一个人就只能
// 靠 git log 去考古「为什么 §9.21 说是 39 个」；留着它，差集报告能直接打印
// 「哪些列从恒 NULL 变成了有值」，这正是本轮最重要的那条结论。
var legacySessionArmNullPaddedColumns710 = map[string]struct{}{
	"affinity_hit": {}, "api_key_owner_user": {}, "api_key_prefix": {},
	"application_code": {}, "attachments": {}, "auto_profile": {},
	"client_model": {}, "client_profile": {}, "compression_reason": {},
	"due_at": {}, "gw_task_id": {}, "id": {}, "key_alias": {},
	"model_chosen": {}, "outbound_msg_count": {}, "outbound_msg_hashes": {},
	"outbound_token_est": {}, "owner_user": {}, "provider_id": {},
	"provider_model": {}, "quality_fix_actions": {}, "request_class": {},
	"request_type": {}, "strategy_used": {}, "stream_chunk_errors": {},
	"stream_chunks_sent": {}, "test_tab_indent": {}, "transform_rule_id": {},
	"virtual_ip": {}, "virtual_mac": {},
}

// currentCanonicalViewMigrationPath 返回**当前权威迁移**——即 startup 目录里
// 编号最大的、以 CREATE OR REPLACE VIEW 重建 canonical 视图的 **up** 迁移。
//
// 为什么必须自动发现，不能写死编号（2026-10-02，审计 §9.64）：
//
//	权威源：710 → 734 → 740 → 815 → 816 → 817
//
//	写死 815 时门是绿的；816 把 client_ip 从 NULL 补位改成有源投影后，
//	这张门**当轮就红了**，而且红得像代码坏了。它红了整整一轮没人处理，
//	因为「816 那轮所有 db 包的测试都是绿的」，只有跨包这道 admin 门看得到。
//
//	写死编号的失败模式特别难查：**它要求「每次新增一条重建视图的迁移」都记得
//	回来改这里**，而改漏了的表现不是「门忘了新迁移」，是「门拿旧迁移当权威」
//	——一个看起来完全合理的红，却指向一个不存在的问题。
//
//	. down.sql 必须排除：down 恢复的是**旧形态**（817 down 回到 816 的形态，
//	816 down 回到 NULL 补位）。把 down 算进「当前」会让门在回滚方向上完全失准。
func currentCanonicalViewMigrationPath(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "sql", "migrations", "startup")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读 startup 迁移目录失败：%v", err)
	}
	const needle = "CREATE OR REPLACE VIEW public.request_logs_with_current_month"
	bestNum, bestName := -1, ""
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".down.sql") {
			continue
		}
		num, _, ok := strings.Cut(name, "_")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(num)
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("读 %s 失败：%v", name, err)
		}
		if !strings.Contains(string(b), needle) || n <= bestNum {
			continue
		}
		bestNum, bestName = n, name
	}
	if bestName == "" {
		t.Fatalf("startup 目录里找不到重建 canonical 视图的 up 迁移 —— " +
			"要么迁移被搬走了，要么 CREATE 语句的写法变了。本门会因此拿不到权威源，" +
			"而那正是它唯一能发现的事。")
	}
	return filepath.Join(dir, bestName)
}

// TestSessionArmNullPaddedColumnsMatchMigration 钉住派生集合与**当前权威迁移**
// （= 当前重建该视图的最高号 up 迁移的 $proj$）声明的一致。
//
// 权威源为什么从 710 换到 815 再换到 816：710 的 $proj$ 是 734 **之前**的形态，
// 它的 30 条 `NULL::` 占位里有 24 条在 734 之后已经被 `d.<col>` 取代。原实现拿
// 710 当权威，于是那张表过期了整整两轮而门一直绿——因为它比的是自己（派生自
// 710 的同一份形态）与自己。这正是「门测的不是它声称测的那个东西」的教科书
// 形态：它确实在校验一致性，只是两边的错误抵消了。
//
// 816（2026-10-02）把 session 臂的 client_ip 从 NULL 补位改为有源投影，同时
// b28ad0c98 把 client_ip 从 db.RequestLogsViewPaddedSessionColumns 里移除——
// 权威源停在 815 时门红（迁移里有、表里没有），这是对的：权威必须跟着**最新
// 视图迁移**走，否则又回到「自己比自己」。
//
// 第三次（817 落地时）不再手工把权威源从 816 推到 817，而是改为**自动发现**
// startup 目录里编号最大的那条重建视图的 up 迁移——写死编号本身就是上面那条
// 失败模式的成因：它要求每次新增视图迁移都记得回来改这里，改漏的表现不是
// 「门忘了新迁移」而是「门拿旧迁移当权威」。理由见 currentCanonicalViewMigrationPath。
//
// 权威源又为什么从写死 815/816 换成**自动发现**（2026-10-02）：816 落地后这张门
// 红了一轮（写死 815），改成写死 816 又会在 817 落地时**再红一次**——
// 写死编号这件事本身就是失败模式。并行会话已把权威源切到 816（a0da9066d），
// 本轮把它升级为自动发现：详见 currentCanonicalViewMigrationPath 的注释。
//
// 权威源又为什么从写死 815 换成**自动发现**（2026-10-02）：816 落地后这张门红了，
// 而写死编号这件事本身就是失败模式——详见 currentCanonicalViewMigrationPath
// 的注释。
//
// 现在两侧的独立性来自：左侧派生自 **db 包的生效投影**（withDetails=true），
// 右侧解析自**当前权威迁移文件**的 $proj$ 块。两者由 db 包的 viewdef 等价契约
// （TestRequestLogsViewV2EnsureMatchesMigration）保证同源但不同路径。
func TestSessionArmNullPaddedColumnsMatchMigration(t *testing.T) {
	root := repoRootFromCaller(t)
	migPath := currentCanonicalViewMigrationPath(t, root)
	raw, err := os.ReadFile(migPath)
	if err != nil {
		t.Fatalf("读权威迁移 %s 失败 %v", migPath, err)
	}
	t.Logf("当前权威迁移 = %s", filepath.Base(migPath))
	declared := map[string]struct{}{}
	re := regexp.MustCompile(`NULL::[A-Za-z\[\] ]+ AS ([a-z_]+)`)
	for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
		declared[m[1]] = struct{}{}
	}
	if len(declared) == 0 {
		t.Fatalf("%s 里没解析出任何 NULL 补位列——正则失效或迁移被重写，"+
			"族分类器会静默退化成「视图族一律行级可用」", filepath.Base(migPath))
	}
	var missing, extra []string
	for c := range declared {
		if _, ok := sessionArmNullPaddedColumns[c]; !ok {
			missing = append(missing, c)
		}
	}
	for c := range sessionArmNullPaddedColumns {
		if _, ok := declared[c]; !ok {
			extra = append(extra, c)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("session 臂 NULL 补位表与当前权威迁移 %s 不一致：\n"+
			"  迁移里有、表里没有：%v\n  表里有、迁移里没有：%v\n"+
			"后果：族分类器会把「谓词级空」误判成「行级有」，"+
			"从而拒绝正确的 silently_empty 判定。", filepath.Base(migPath), missing, extra)
	}
}

// TestPaddedAndDetailsSuppliedSetsAreDisjointAndComplete 把两张表的关系钉死：
// 它们必须是**互斥且完备**的两半，而不是两份各自为政的清单。
//
// 上一轮的失效形态是「补位表里塞着 24 个其实有值的列」——它既不互斥（那些列
// 同时是 details 供值列）也不完备（漏了 815 之后的 client_ip 等）。这条门让
// 「两张表重叠」直接变成一次可读的报告，而不是等人从族分类的异常里反推。
func TestPaddedAndDetailsSuppliedSetsAreDisjointAndComplete(t *testing.T) {
	var overlap []string
	for c := range sessionArmNullPaddedColumns {
		if _, ok := sessionArmDetailsSuppliedColumns[c]; ok {
			overlap = append(overlap, c)
		}
	}
	sort.Strings(overlap)
	if len(overlap) > 0 {
		t.Errorf("同一列同时被登记为「恒 NULL 补位」与「details 供值」：%v\n"+
			"两者是相反的风险判断（恒空 vs 99.9996%% 有值），同时成立说明有人没想清楚。", overlap)
	}
	// 完备性：710 的 30 条要么在补位表、要么在 details 表，两边都找不到的
	// 说明「从恒 NULL 变成了有值」这条结论没有登记来源。
	var unaccounted []string
	for c := range legacySessionArmNullPaddedColumns710 {
		_, inPadded := sessionArmNullPaddedColumns[c]
		_, inDetails := sessionArmDetailsSuppliedColumns[c]
		if !inPadded && !inDetails {
			unaccounted = append(unaccounted, c)
		}
	}
	sort.Strings(unaccounted)
	if len(unaccounted) > 0 {
		t.Errorf("710 的补位列里，这些既不在当前补位表也不在 details 供值表：%v\n"+
			"请在 db/request_logs_view_padded_columns.go 给它一条裁决（补投影 / 不同东西 / "+
			"无源 / 随 v1 退役），否则「从恒 NULL 变成了有值」这个结论没有落点。", unaccounted)
	}
	// 报告口径：把「不再恒 NULL」的列数按真实集合算出来，让这条差集在 CI 日志
	// 里可见。不能用 len(legacy)-overlap-unaccounted —— 那样算出来的是
	// 「legacy 与 padded 的差」，而 815 之后新增的列（test_col / client_ip /
	// credits_rate_multiplier）不在 legacy 里，会被这个式子悄悄算进"迁移过来的"。
	stillPadded := 0
	for c := range legacySessionArmNullPaddedColumns710 {
		if _, ok := sessionArmNullPaddedColumns[c]; ok {
			stillPadded++
		}
	}
	t.Logf("session 臂列账：恒 NULL 补位 %d 列（其中 %d 列沿自 710），"+
		"details 供值（缺行时 NULL）%d 列，710 时期记为恒 NULL、现已不是的 %d 列",
		len(sessionArmNullPaddedColumns), stillPadded,
		len(sessionArmDetailsSuppliedColumns),
		len(legacySessionArmNullPaddedColumns710)-stillPadded)
}

// nullPaddedColWordRE 为某个 NULL 补位列构造词边界匹配。
//
// 用词边界而不是裸子串：`id` 是补位列，但 `provider_id` / `session_id` 里都含
// "id"，裸子串会把几乎每个文件都判成谓词级空，那条族约束就废了。
func nullPaddedColWordRE(col string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(col) + `\b`)
}

// nullPaddedPredicateHit 报告：这个文件里是否存在某个**函数**（顶层函数或闭包），
// 它既引用了会话臂视图、又提到了某个 NULL 补位列。
//
// 为什么要「函数」而不是「字符串字面量」——这是本轮实测逼出来的：
// 逐字面量归属会被**拼接出来的 SQL** 击穿。
//
// ★ R170 更新见证：原见证 `bg/shared_pick.go` 的 Priority 1 读点已改读
// 会话族原生源（不再提 canonical 视图 ⇒ 不再是本族成员）；
// 新的见证是 `admin/usage_enhanced.go` —— 它的视图读与 `id` 命中**分处不同
// 字面量**：视图名在 `"BaseTable": "request_logs_with_current_month rl"`
// （配置常量串，不含 SQL 关键字），`id` 在 `" LEFT JOIN providers p ON
// p.id = ul.provider_id"`（JOIN 片段，不含视图名）。
// 逐字面量归属要求「同一片字面量里既有视图名又有补位列」⇒ 对它**恒不命中**
// ⇒ ⇒ 一个真触发会**悄悄离开本族** ⇒ 所以必须用函数作用域口径。
//
// 为什么不是「整文件」——那是本轮修掉的旧形状：`id` 命中常常来自**别的表**
// （`ak.id` on api_keys、`WHERE id = 1` on rollup cursor、`RETURNING id` on
// incidents 自身）或者根本不是 SQL（`r.PathValue("id")`、`parts[0]`、
// 一条 Go 正则字面量里的 `correlation_id`）。整文件口径下 19 个文件里有 8 个
// 是这样被带进本族的。
//
// 函数作用域是**两者之间**的粒度，也是两者的超集保护：函数体 ⊇ 单个字面量，
// 所以凡是被逐字面量口径判定为触发的，本口径一定也判定为触发（实测
// strictOnly = 0，无新增漏判），同时把「同文件但不同表」的命中挡在外面。
//
// 解析失败时**退回整文件口径**（保守方向：宁可留在本族并要求具名论证，
// 也不要把一个真触发悄悄放出族外）。
func nullPaddedPredicateHit(raw string) (hit bool, via string) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "src.go", raw, 0)
	if err != nil {
		for col := range sessionArmNullPaddedColumns {
			if nullPaddedColWordRE(col).MatchString(raw) {
				return true, col + "（解析失败，退回整文件口径）"
			}
		}
		return false, ""
	}
	// 先看这个文件到底有没有出现补位列——绝大多数文件在这一步就出局。
	present := map[string]bool{}
	for col := range sessionArmNullPaddedColumns {
		if nullPaddedColWordRE(col).MatchString(raw) {
			present[col] = true
		}
	}
	if len(present) == 0 {
		return false, ""
	}

	// 收集作用域：所有函数体（顶层 FuncDecl 与闭包 FuncLit）**加上**那些
	// 不在任何函数内的字符串字面量。
	//
	// 只取函数体是不够的，而且是实测抓到的：包级 `const xxxSQL = \`...\`` 形式的
	// 读点整段都在函数之外——
	//   domains/sessionforensics/export.go:37/:57  const forensicsExportMessagesSQL(Alt)
	//   domains/streaming/model_alternatives.go:180 const alternativesSQL
	// 它们是**真的**读视图 + 真的用补位列，漏掉它们等于放走真触发。
	// 这条不是推演出来的假设，是 TestNullPaddedAttributionNeverLosesALiteralLevelHit
	// 报红后逐个打开文件确认的。
	type span struct{ lo, hi int }
	base := fset.File(file.Pos()).Base()
	off := func(p token.Pos) int { return int(p) - base }
	var spans []span
	ast.Inspect(file, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.FuncDecl, *ast.FuncLit:
			spans = append(spans, span{off(n.Pos()), off(n.End())})
		}
		return true
	})
	inAnySpan := func(lo, hi int) bool {
		for _, sp := range spans {
			if lo >= sp.lo && hi <= sp.hi {
				return true
			}
		}
		return false
	}
	// 包级字面量（函数之外的）各自构成一个作用域。
	ast.Inspect(file, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		lo, hi := off(bl.Pos()), off(bl.End())
		if inAnySpan(lo, hi) {
			return true
		}
		spans = append(spans, span{lo, hi})
		return true
	})
	for _, sp := range spans {
		if sp.lo < 0 || sp.hi > len(raw) || sp.lo >= sp.hi {
			continue
		}
		body := raw[sp.lo:sp.hi]
		low := strings.ToLower(body)
		readsView := false
		for name := range requestLogsViewsWithSessionArm {
			if strings.Contains(low, name) {
				readsView = true
				break
			}
		}
		if !readsView {
			continue
		}
		cols := make([]string, 0, len(present))
		for col := range present {
			cols = append(cols, col)
		}
		sort.Strings(cols)
		for _, col := range cols {
			if nullPaddedColWordRE(col).MatchString(body) {
				return true, col
			}
		}
	}
	return false, ""
}

// localSessionBaseUnion 证明「本地产物同时供给会话族与基表」（= mixed 形态）。
//
// Only a return expression with UNION output arms can prove a local session
// fallback. An anti-join, SQL comment or scalar subquery is not such an arm.
func localSessionBaseUnion(code string) bool {
	mixed, _ := localUnionArmFamilies(code)
	return mixed
}

// localSessionOnlyUnion 证明「本地产物**只有**会话族两条腿、没有任何基表腿」。
//
// ★ 2026-10-20（迁移 890）：v1 族退役后，tenant stats 的四臂收敛成两臂
// （会话 hot ∪ 母表）⇒ 「混合」证明（session && base）恒为假 ⇒
// 需要一条「只有会话腿」的证明来钉住它。
//
// ⚠ 判据与 localSessionBaseUnion 同源（同一套臂切分）⇒ 两者的差别只在
// 「基表腿在不在」这一维 ⇒ 不会出现「一个判据能过、另一个不能」的分叉。
func localSessionOnlyUnion(code string) bool {
	_, sessionOnly := localUnionArmFamilies(code)
	return sessionOnly
}

// localUnionArmFamilies 返回产物里**输出臂**覆盖的族：会话族 / 基表族。
//
// 只有「返回表达式里的 UNION 输出臂」算数：反连接、SQL 注释、标量子查询
// 都不供给行。
func localUnionArmFamilies(code string) (mixed bool, sessionOnly bool) {
	f, err := parser.ParseFile(token.NewFileSet(), "source.go", code, 0)
	if err != nil {
		return false, false
	}
	var literalSQL func(ast.Expr) (string, bool)
	literalSQL = func(e ast.Expr) (string, bool) {
		switch x := ast.Unparen(e).(type) {
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				value, err := strconv.Unquote(x.Value)
				return value, err == nil
			}
		case *ast.BinaryExpr:
			if x.Op == token.ADD {
				left, lok := literalSQL(x.X)
				right, rok := literalSQL(x.Y)
				return left + right, lok && rok
			}
		}
		return "", false
	}
	union := regexp.MustCompile(`(?i)\bUNION\s+ALL\b`)
	quoted := regexp.MustCompile(`'(?:''|[^'])*'`)
	from := regexp.MustCompile(`(?i)\bFROM\s+(?:public\.)?(session_turns_hot|session_turns|_request_logs_hot|_request_logs)\b`)
	// ⚠ 两个标志必须**按返回语句**分别结算，不能跨语句累加：
	// 「返回 A 只供给会话腿、返回 B 只供给基表腿」**不是**混合形态。
	// （跨语句累加会让 mixed 的证明变松 —— 而判据纪律明确要求
	// 「同一次判定里不同判据可以有不同的时间语义」。）
	var sawMixed, sawSessionOnly bool
	ast.Inspect(f, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return true
		}
		literal, complete := literalSQL(ret.Results[0])
		if !complete {
			return true
		}
		sql := quoted.ReplaceAllString(stripSQLComments(literal), " ")
		sql = strings.TrimSpace(sql)
		// Strip only parentheses enclosing the entire query, then split UNIONs
		// at depth zero. A UNION inside EXISTS or a scalar cannot supply rows.
		for strings.HasPrefix(sql, "(") {
			depth, closeAt := 0, -1
			for i, r := range sql {
				if r == '(' {
					depth++
				}
				if r == ')' {
					depth--
					if depth == 0 {
						closeAt = i
						break
					}
				}
			}
			if closeAt != len(sql)-1 {
				break
			}
			sql = strings.TrimSpace(sql[1 : len(sql)-1])
		}
		var arms []string
		start := 0
		for _, loc := range union.FindAllStringIndex(sql, -1) {
			prefix := sql[:loc[0]]
			if strings.Count(prefix, "(") != strings.Count(prefix, ")") {
				continue
			}
			arms = append(arms, sql[start:loc[0]])
			start = loc[1]
		}
		if len(arms) == 0 {
			return true
		}
		arms = append(arms, sql[start:])
		armSession, armBase := false, false
		for _, arm := range arms {
			loc := from.FindStringSubmatchIndex(arm)
			if loc == nil {
				continue
			}
			prefix := arm[:loc[0]]
			if strings.Count(prefix, "(") != strings.Count(prefix, ")") {
				continue
			}
			table := strings.ToLower(arm[loc[2]:loc[3]])
			armSession = armSession || table == "session_turns_hot" || table == "session_turns"
			armBase = armBase || table == "_request_logs_hot" || table == "_request_logs" ||
				table == "request_logs_hot" || table == "request_logs"
		}
		sawMixed = sawMixed || (armSession && armBase)
		sawSessionOnly = sawSessionOnly || (armSession && !armBase)
		return true
	})
	return sawMixed, sawSessionOnly
}

// sourceFamilyOf 从文件源码机械判定它读哪一族。
// sourceFamilyOf 按源码文本判定 v1 读法族。
//
// readsVBodies 用于**切换层消费点**（§9.232）：消费点的 bodies 腿走
// `db.SessionBodiesSourceSQL()`，源码里**没有 bodies 关系名字面量**，
// 而它的默认臂读的是 v1 ⇒ `familyBodiesRE` 匹配不到 ⇒ 它会被判成
// 「只读视图 / 只读基表」，**而这不属实**（它的 bodies 腿仍是 v1）。
//
// ⚠ 症状是「归族不报错、只是低估」：`admin/session_compare.go` 与
// `admin/session_export.go` 迁移后直接变成 `undetermined`（它们只剩会话族
// 腿），而那 7 个还读视图的读方从 `reads_bodies_plus_other` 掉到 `reads_710_view_only`
// —— 两个方向都少算 bodies 依赖。
func sourceFamilyOf(code string, readsVBodies bool) string {
	// raw 保留未剥离的原文，供 nullPaddedPredicateHit 做 AST 解析。顺序很重要：
	// 剥注释会吃掉字符串字面量里的 "//"（URL、SQL `--` 注释），把 Go 源码弄成
	// 无法解析，于是 nullPaddedPredicateHit 会静默退回整文件口径。
	// 第一版把原始值丢了（只留被覆盖后的 code），结果 admin/logs_summary.go
	// 靠这条 fallback 留在本族——一个**注释在说用原文、代码在用剥过的**的错。
	raw := code
	code = gateStopWriteLineCommentRE.ReplaceAllString(code, " ")
	code = gateStopWriteBlockCommentRE.ReplaceAllString(code, " ")
	bo := familyBodiesRE.MatchString(code) || readsVBodies
	vi, v1OnlyView := false, false
	for _, m := range familyAnyViewRE.FindAllString(code, -1) {
		if _, ok := requestLogsViewsWithSessionArm[strings.ToLower(m)]; ok {
			vi = true
		} else {
			// 引用了一个 v1-only 视图（如 ..._without_customer_id）：
			// 它没有 session 臂，停写后与读基表同命运 ⇒ 归 base 族。
			v1OnlyView = true
		}
	}
	// 读了 v1-only 视图的，即使 SQL 文本里没有裸 FROM _request_logs，也是
	// 实质上的基表读者——这是「只看 from _request_logs 就会漏掉」的一类。
	ba := familyBaseRE.MatchString(code) || v1OnlyView
	// 视图族的第二维：谓词级 NULL 补位（2026-10-02）。
	//
	// 判据是**函数作用域归属**（见 nullPaddedPredicateHit）：补位列必须出现在
	// 一个既读会话臂视图、又提到该列的函数体里。整文件口径会把 `ak.id`、
	// `WHERE id = 1`、`r.PathValue("id")` 这些**别的表 / 非 SQL** 的命中带进来
	// —— 实测 19 个本族文件里 8 个是这样来的。
	//
	// 注意 np 用的是**原始 raw**（未剥注释）：AST 解析需要能读的源码，而函数体
	// 取的是原文，注释在不在都不影响「这个函数读没读那个视图」。
	np, _ := nullPaddedPredicateHit(raw)
	np = np && vi
	switch {
	case vi && np && !ba:
		return familyViewNullPadded
	case bo && (vi || ba):
		return familyBodiesMixed
	case bo:
		return familyBodies
	case ba && (vi || localSessionBaseUnion(raw)):
		return familySessionBase
	case vi:
		return familyView
	case ba:
		return familyBase
	case readsSessionFamily(code):
		// ★ 2026-10-20（890）：v1 族退役后，「只读会话族」是正常形态，
		// 不是「读法不明」。见 familySessionOnly 的头注。
		return familySessionOnly
	default:
		return "undetermined"
	}
}

// sourceFamilyForFile 判定一个文件读哪一族。
//
// ★ 2026-10-20（迁移 890）：**路径相关的特殊证明已退役**。
//
// 原来这个函数对 `admin/tenant_stats_logs_table.go` 有一条**按路径**的特例：
// 它的 SQL 由 `tenantStatsLogsTableSQL(bool)` 拼出来 ⇒ 纯文本分类器看不见
// 那几条臂 ⇒ 特例去**编译产物**里证明「两态都是混合输出」。
//
// 890 之后那件事不再成立：四臂（四条会话/v1 腿 + 双子反连接）收敛成两臂
// （会话 hot ∪ 母表），而**纯文本分类器已经能认出它**（文件里字面写着
// `FROM session_turns_hot t` / `FROM session_turns t`）⇒ 特例是多余的。
//
// ⇒ 特例删除，分类**不再依赖路径**。
//
// ⚠ 删除它的代价（必须说清）：原来那条「按路径的证明」还顺带守了一件事
// ——「编译产物的证明只对这个文件生效，别泄漏给别的文件」。现在没有按路径
// 的证明了 ⇒ 那条不变式改由 TestSourceFamilyProofIsFileScoped 以
// **路径无关性**的形式接管（同一段源码在不同路径下必须得到同一个族）。
func sourceFamilyForFile(code, file string, readsVBodies bool) string {
	return sourceFamilyOf(code, readsVBodies)
}

// TestRequestLogsStopWriteSourceFamilyCoversInventory 产出并钉住那张四分表。
//
// 它不做判断，只做**机械分类**——正因如此它可以自己给自己当判据：它测的是
// 「按代码测出的族」与「按代码测出的族」相等，没有解释空间，所以不会恒绿
// （除非分类逻辑本身坏了）。价值在于把「104 个文件」变成 4 个可行动的队列。
// TestRequestLogsStopWriteSourceFamilyCoversInventory 产出并钉住那张四分表。
//
// 它不做判断，只做**机械分类**——正因如此它可以自己给自己当判据：它测的是
// 「按代码测出的族」与「按代码测出的族」相等，没有解释空间，所以不会恒绿
// （除非分类逻辑本身坏了）。价值在于把「104 个文件」变成 4 个可行动的队列。
//
// # 890 之后（2026-10-20）：总体归零 ⇒ 分类器的有效性改由**合成样本**证明
//
// 读点总体为空 ⇒ 「每个读点都要归入一族」这条判据变成空转。
// ⇒ 本门改成两件事：
//
//	① 读点总体必须为空（890 之后没有 v1 读方）；
//	② **阳性对照**：族分类器仍然有效 —— 四段合成源码必须分别归入
//	   familyBase / familySessionOnly / familyView / familyBodies。
//	   （分类器本身还活着：source_sql_local_literal_test.go 仍在用它。）
func TestRequestLogsStopWriteSourceFamilyCoversInventory(t *testing.T) {
	root := repoRootFromCaller(t)
	counts := map[string]int{}
	var undetermined []string
	// §9.49：遍历「直接表 ∪ 间接表」。只遍历直接表时，间接读点（表名是
	// Go 表达式，见 indirectRequestLogsReaders）**从未进入过这个分类器**，
	// 于是它报「未归入任何一族」——而它其实一直在读 v1。
	for _, file := range allKnownRequestLogsReaderFiles(t) {
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		fam := sourceFamilyForFile(string(raw), file, isSwitchConsumerFile(t, root, file))
		if ind, isIndirect := indirectRequestLogsReaders[file]; isIndirect {
			// 器眼看不见它，族由登记给定；登记里没有合法族时才算 undetermined。
			if ind.Family == "" {
				undetermined = append(undetermined, file+"(间接登记未给族)")
				continue
			}
			fam = ind.Family
		}
		if fam == "undetermined" {
			undetermined = append(undetermined, file)
			continue
		}
		counts[fam]++
	}
	if len(undetermined) > 0 {
		sort.Strings(undetermined)
		t.Fatalf("以下文件的读法未归入任何一族（需人看一眼，通常是 SQL 在共享构造函数里）：%v",
			undetermined)
	}
	total := 0
	for _, k := range []string{familyBodies, familyBodiesMixed, familyView, familyBase,
		familySessionBase, familyViewNullPadded} {
		total += counts[k]
	}
	// §9.49：分母用「直接表 ∪ 间接表」。用 len(requestLogsReadInventory) 会让
	// 每一个间接读点都算成一次「漏计」——而它既没漏也没重，是分母本身少算了一个。
	known := len(allKnownRequestLogsReaderFiles(t))
	if total != known {
		t.Errorf("六族合计 %d ≠ 读点清单 %d（直接表 %d + 间接表 %d）—— 有文件被重复计数或漏计",
			total, known,
			len(requestLogsReadInventory), len(indirectRequestLogsReaders))
	}
	// ① 890 之后：读点总体必须为空。
	if known != 0 {
		t.Errorf("读点总体仍有 %d 个文件 —— 890 之后 v1 族已物理删除。", known)
	}
	t.Logf("S4 停写影响面（按读表族，机械判定）：\n"+
		"  bodies_only=%d                正文无兜底 ⇒ 硬失败\n"+
		"  bodies_plus_other=%d          正文腿硬失败（轮次腿可能照常）\n"+
		"  view_only=%d                  session 臂仍供数 ⇒ 静默少计\n"+
		"  view_with_null_padded=%d      **行级有、谓词级空**（migration 710 补位 30 列）\n"+
		"  base_only=%d                  完全停止增长\n"+
		"  session_and_base=%d              部分退化",
		counts[familyBodies], counts[familyBodiesMixed], counts[familyView],
		counts[familyViewNullPadded], counts[familyBase], counts[familySessionBase])

	// ② 阳性对照：族分类器仍然有效（合成源码，不碰真实文件）。
	for _, tc := range []struct {
		name string
		code string
		want string
	}{
		{"v1 基表", "SELECT * FROM _request_logs_hot WHERE ts > now()", familyBase},
		{"会话族", "SELECT * FROM session_turns_hot t WHERE t.ts > now()", familySessionOnly},
		{"canonical 视图", "SELECT * FROM request_logs_with_current_month rl", familyView},
		{"v1 bodies", "SELECT * FROM _request_logs_bodies_hot rb", familyBodies},
	} {
		if got := sourceFamilyOf(tc.code, false); got != tc.want {
			t.Errorf("族分类器把合成样本「%s」判成 %q，期望 %q —— 分类器坏了", tc.name, got, tc.want)
		}
	}
}

// TestStopWriteEffectAgreesWithSourceFamily 拦住本项目已经犯过一次的错：
// **710 视图读者不可能「静默变空」**——停写后它们仍由 session 臂供数。
//
// 判据打成关系（族 × 档位的合法组合）而不是硬编码清单：新增档位时必须同时
// 说明它与四族的关系，规则跟着走；否则新档位会默认放行所有组合。
//
// # 890 之后（2026-10-20）：表清空 ⇒ 逐条校验变成空转 ⇒ 判据必须有牙
//
// ⇒ ① 分级表必须为空；② **阳性对照**：用**合成分级**驱动同一个校验函数，
// 它必须拦住「族与档位不合法」的组合、放行合法组合。
func TestStopWriteEffectAgreesWithSourceFamily(t *testing.T) {
	if len(requestLogsStopWriteClassification) > 0 {
		t.Errorf("停写分级表仍有 %d 条登记 —— 890 之后没有 v1 读点，不该有停写分级。",
			len(requestLogsStopWriteClassification))
	}

	// 阳性对照（合成族 × 档位，不碰真实文件）。
	//
	// ⚠ 没有这一条，「族 × 档位」这组规则在表空着的时候就是恒真空。
	cases := []struct {
		name string
		fam  string
		eff  string
		bad  bool
	}{
		{"视图族判静默变空", familyView, effectSilentlyEmpty, true},
		{"基表族判内容降级", familyBase, effectSilentlyDegradedContent, true},
		{"基表族判聚合降级", familyBase, effectSilentlyDegradedAggregate, true},
		{"视图族判静默冻结", familyView, effectSilentlyFrozen, false},
		{"基表族判报错", familyBase, effectErrorsOut, false},
	}
	for _, tc := range cases {
		problems := stopWriteEffectProblems(tc.fam, stopWriteClassification{Effect: tc.eff})
		if tc.bad && len(problems) == 0 {
			t.Errorf("合成分级「%s」（族=%s 档位=%s）没有被判出问题 —— "+
				"族 × 档位的规则没有牙（表空着的时候，这是唯一还能证明它有效的手段）",
				tc.name, tc.fam, tc.eff)
		}
		if !tc.bad && len(problems) != 0 {
			t.Errorf("合成分级「%s」（族=%s 档位=%s）被判出问题 %v —— 规则本身坏了",
				tc.name, tc.fam, tc.eff, problems)
		}
	}
}

// stopWriteEffectProblems 校验「族 × 档位」的合法组合（返回问题列表，空 = 合格）。
//
// 为什么抽成函数：分级表在 890 之后清空 ⇒ 逐条校验不再执行 ⇒
// 「规则有没有牙」就没人证明了。抽出来才能在合成分级上直接驱动它。
func stopWriteEffectProblems(fam string, c stopWriteClassification) []string {
	var problems []string
	if c.Effect == effectUnclassified {
		return problems
	}
	// 「静默变空」要求读点**完全**没有 session 侧供给。只读 710 视图不满足
	// 这个前提——真库实测该视图 24h 内 36.55% 的行来自 session_turns。
	//
	// ⚠️ 2026-10-02 收窄：这条原本一刀切套在 familyView 上，**是错的**。
	// migration 710 对 session 臂补了 30 列 NULL ⇒ 凡在 WHERE / GROUP BY / JOIN
	// 用到这些列的读点会「行级有、谓词级空」—— 判 silently_empty 是**正确**的。
	// 现在它们被分到 familyViewNullPadded，只约束 unaffected。
	if fam == familyView && c.Effect == effectSilentlyEmpty {
		problems = append(problems, "只读 710 视图且未触及任何 session 臂 NULL 补位列 ⇒ "+
			"停写后 session 臂仍按行供数，不可能静默变空")
	}
	// bodies 族无 session 侧等价物，判「不受影响」几乎一定是错的。
	// 带 bodies 腿的 mixed 族同理——它们的正文腿没有任何退路。
	//
	// **默认拒绝 + 具名豁免**：放行条件从「门写宽了」变成「有人写下了为什么」。
	if (fam == familyBodies || fam == familyBodiesMixed) && c.Effect == effectUnaffected {
		problems = append(problems, "读 bodies 族却判为「停写不受影响」：bodies 没有 session 侧等价物")
	}
	// 反向约束：纯基表族停写后读点**整体停止**，行都不剩，
	// 「内容降级」描述的不是它——那是 silently_frozen（冻结在停写前常数）。
	for _, degraded := range []string{effectSilentlyDegradedContent, effectSilentlyDegradedAggregate} {
		if fam == familyBase && c.Effect == degraded {
			problems = append(problems, "纯基表族停写后读点整体停止，不存在「行还在但内容退化」的形状")
		}
	}
	return problems
}

func effectsKeyList() string {
	keys := make([]string, 0, len(stopWriteEffects))
	for k := range stopWriteEffects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, "/")
}

// nullPaddedUnaffectedJustification 登记「触及 session 臂 NULL 补位列、但仍判
// unaffected」的**具名论证**。
//
// 与 bodiesUnaffectedJustification 同一模式：**默认要求论证，具名放行**。
// 存在的理由是 sourceFamilyOf 的这一维只能看「列名是否出现」，分不清
// 「用在 WHERE/GROUP BY」（真触发：结果集谓词级恒空）与
// 「只出现在投影 / URL 路径 / Go 结构体字段 / 已被同表达式非补位列 COALESCE
// 兜住」（过度触发）。
//
// 每条必须回答：**停写之后，这个读点过滤/分组用的列会不会变？**
var nullPaddedUnaffectedJustification = map[string]string{
	"admin/attachments_routes.go": "两处命中都不是 SQL：`/api/attachments/` 是 HTTP 路径字面量" +
		"（第 38 行 strings.TrimPrefix），`\"attachments\": []any{}` 是响应 JSON 的键（第 103 行）。" +
		"本文件对 710 视图的读点是 attachmentOwnedByTenant 的 EXISTS/JOIN，只用 request_id 与 " +
		"tenant_id 过滤，两列在 session 臂都有真值 ⇒ 判 unaffected 成立。",
	"admin/live_stream_sse.go": "命中列都不在**行级谓词**上：client_model(:2409) 只在 SELECT 投影内" +
		"且已被同表达式的非补位列双兜底（`COALESCE(NULLIF(mc.canonical_name,''), NULLIF(rl.client_model,''), " +
		"rl.outbound_model,'')`，canonical_name 来自 models_canonical 表、outbound_model 是 710 直映列）；" +
		"provider_id(:2425) 只在 LEFT JOIN 的 ON 条件里、且主值取自 `COALESCE(c.provider_id, rl.provider_id)`" +
		"的 credentials 侧，而 LEFT JOIN 不删行。两个读点的谓词只有 `ts >= NOW() - INTERVAL '1 hour'`、" +
		"`request_id = ANY($1)` 与一个排除语义的 `request_status NOT IN ('','in_progress')`（request_status " +
		"是 815:207-210 由 t.success/t.status_code 计算的派生列，session 臂真值）⇒ 无恒 0 行路径。流量由未挂 " +
		"S4 写门的 turn_writer.go 持续供给，1 小时窗口恒有新行 ⇒ 判 unaffected 成立。⚠ 本条同时记录一次" +
		"**族误触发**：本文件被分到本族是因为补位集里含 `id`，而它的 id 命中（:262/:273-274/:288-290）" +
		"全在 credentials/providers 上，对 710 的两个读点一个补位列都没用到。",
	"admin/credential_monitor_heatmap.go": "两处命中都在**以 outbound_model 打头的 COALESCE 里**：" +
		":311 `lower(COALESCE(rl.outbound_model, rl.client_model))`（whereClause）、" +
		":322 同一表达式做投影别名。outbound_model 在 session 臂是真值且排第一位，" +
		"client_model 只在 outbound_model 为空时才被读到 ⇒ 对有真实 outbound_model 的行不改变结果。",
	"db/probe_views_unified.go": "同形：:96 与 :104 都是 " +
		"`lower(COALESCE(rl.outbound_model, rl.client_model))`，outbound_model 排第一且为真值列；" +
		"其余命中（:148/:149 的 id / provider_id）属于 credentials 与 providers 两张表，列同名但表不同。",
	"admin/route_incidents.go": "31 处命中全部是 `id`，且逐行核实**没有一处是 SQL 列**：:132 parts[0]、:135/:147/:153 " +
		"是 handleDetail/handleEvents/handleTimeline 的入参解析等 Go 代码。" +
		"本文件对 710 视图的读点用 ts / request_status / credential_id，都不是补位列。",
}
