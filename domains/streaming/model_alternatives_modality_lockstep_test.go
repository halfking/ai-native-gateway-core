package streaming

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/tests/testutil"
)

// alternativesSQL 的 $5 模态过滤块锁步门（2026-10-29，承接批判式审计第二轮 §6
// 遗留项「没有把 alternativesSQL（$5 臂）纳入锁步」）。
//
// 模态接受判据在仓里有三份等价表达：
//
//	① provider.candidateQuerySQLBase 的 $3 接受臂（路由器候选集）
//	② 本包 alternativesSQL 的 $5 过滤块（零候选建议列表）
//	③ provider.ModalityAccepts（诊断的 Go 判据）
//
// ①↔③ 已由 provider/modality_accepts_lockstep_test.go 锁死；本测试锁 ②↔③
// ⇒ 三角闭合，三份表达两两一致。②改判据而不同步 ③（或反过来）时这里当场红。
//
// ★★ 2026-10-29（本轮）：三份表达**同源**了——①② 都由
// provider/modality_accept.go 的唯一片段渲染（ModalityAcceptArmsSQL）。
// ⇒ 本门从「第二份抄本 vs Go 判据」变成「**同源片段 vs Go 判据**」，
//
//	而「两份 SQL 都来自那一片段」由
//	TestAlternativesSQL_UsesSharedModalityFragment 钉住。
//
// ⇒ ★★ **域外分歧已收口**（此前登记在测试头的那两条都作废）：
//   - 请求值 embedding：②曾是【闭世界】（无兜底臂 ⇒ 一切登记全拒），
//     ①是开世界精确，③的 default 臂曾是宽解释（registered == requested ||
//     multimodal）。⇒ 现在②③都收窄/补齐为「精确相等」⇒ 三份表达在
//     **全部**格上同义。
//   - ②曾是闭世界 ⇒ 未知请求值会让建议列表**静默变空**（无 4xx、无日志）。
//     ⇒ 兜底精确臂补上后，这个「静默变空」的形态从根上消失 ⇒
//     「②必须有自己的臂」这条闭世界假设作废，改为「②必须有兜底精确臂」。
//
// ★ 判据形态是【逐处】不是聚合（LESSONS §527 通用式四：聚合会把局部的漂移
// 洗掉）：片段的每条臂逐条解析，每条臂独立对拍，失败信息点名是哪条臂、
// 哪个 (登记, 请求) 格漂了——把任何一条臂的接受集改窄都会当场报红，不会被
// 其它臂的相同内容掩盖。
//
// ★解析器**共享**（tests/testutil.ParseModalityAcceptArms）：此前本包与
// provider 包各有一份臂解析正则，**形态不同**（provider 那份只认接受侧的
// `IN (...)` ⇒ 看不见 `= 'multimodal'` 那条臂）⇒ 同一个判据被两把不同的尺量。
//
// $5 的生产值域（对拍域的事实依据）：findModelAlternatives 只有两个调用点——
// handler.go 零候选臂（requestModality ← resolveCandidatesForRequest →
// detectRequestModality）与诊断臂（mismatch.RequestedModality ⊆ detect 值域）；
// OmniFree 的 modality 同样来自 detectRequestModality（handler_autocombo.go）。
// ⇒ 值域 = {""} ∪ {text, vision, audio, video}。
//
// 对拍域比生产值域宽：判据词表（provider.ModalityRequestModalities）含
// image / multimodal / embedding，且三份表达在这些请求值上逐格一致 ⇒ 纳入对拍
// （这是防御性冗余，漂移必须当场红）。★ 另加**一个未知值样例**（"document"）
// —— 未知请求模态那一格正是【兜底精确臂】的语义所在，不测它等于没测兜底臂。
func TestAlternativesSQL_LockstepWithModalityAccepts(t *testing.T) {
	sqlText := alternativesSQL

	// 全放行臂（$5='' / $5='text'）没有 COALESCE 接受集、不走下面的臂解析，
	// 存在性用锚点单独锁死：删除或改名 ⇒ 这里红，而不是静默改变过滤面。
	for _, frag := range []string{"$5 = ''", "OR $5 = 'text'"} {
		if !strings.Contains(sqlText, frag) {
			t.Fatalf("alternativesSQL 模态过滤块缺全放行臂锚点 %q —— 判据结构变了，本测试的对拍表必须跟着重写", frag)
		}
	}

	arms := testutil.ParseModalityAcceptArms(sqlText, "$5")

	requested := append([]string{""}, provider.ModalityRequestModalities...)
	requested = append(requested, "document") // 未知请求模态的代表值

	// ★★ 登记侧对拍域**从事实源派生**（不硬编码）——这是本轮批判式审计补的
	// 第二个洞。事实源 = models_canonical.modality 的 CHECK 约束（迁移 451
	// 及其后续修订；取仓内最后一份定义该约束的迁移）。
	// ⇒ 为什么必须派生：分母若硬编码，就存在「**缩小分母 + 改判据** ⇒ 零漂移
	//   假绿」这条合谋路径（实测：把 video 从对拍域删掉、同时把 SQL 联合臂的
	//   video 删掉 ⇒ 对拍零漂移 ⇒ 门全绿，而生产上 video 请求的建议列表会
	//   静默变空）。⇒ 分母锚在事实源上 ⇒ 事实源变宽 ⇒ 门立刻要求扩域；
	//   事实源读不到 ⇒ 门红（fail-closed），而不是退回硬编码。
	registered, err := testutil.CanonicalModalityValueDomain("..")
	if err != nil {
		t.Fatalf("派生登记模态值域失败：%v", err)
	}
	// 方向一（事实源 ⊆ 允许集）：事实源新增一个 CHECK 值（将来给 modality 加
	// 一个新登记值）⇒ 必须把它纳入对拍域，否则该值上的漂移检不出。
	allowedRegistered := map[string]bool{
		"": true, "text": true, "vision": true, "audio": true,
		"video": true, "multimodal": true, "embedding": true,
	}
	for _, reg := range registered {
		if !allowedRegistered[reg] {
			t.Errorf("登记模态值域里出现了本对拍域尚未登记的值 %q（事实源=models_canonical.modality 的 CHECK 约束）—— ★ 新登记值必须纳入对拍域并复核 Go 判据（provider.ModalityAccepts）与唯一片段", reg)
		}
	}
	// 方向二（允许集 ⊆ 事实源）：事实源被改坏（CHECK 缩小 / 迁移被改写）⇒
	// 派生域会缺值 ⇒ 门必须红，而不是悄悄用一份残缺的分母继续对拍。
	for reg := range allowedRegistered {
		if reg == "" {
			continue
		}
		found := false
		for _, r := range registered {
			if r == reg {
				found = true
			}
		}
		if !found {
			t.Errorf("登记模态值域（事实源=models_canonical.modality 的 CHECK 约束）里缺少 %q —— ★ 事实源被改小了（迁移被改写 / CHECK 收紧）⇒ 本对拍域的分母已残缺 ⇒ 必须先修事实源", reg)
		}
	}

	// ★ 每个请求模态都必须【有臂或被兜底精确臂覆盖】：②若是闭世界（无兜底
	// 臂），未知请求值会让建议列表**静默变空**（无 4xx、无日志）⇒
	// 「兜底精确臂在不在」是本门必须钉住的结构事实。
	if !arms.HasExactFallback {
		t.Fatalf("alternativesSQL 模态过滤块缺【兜底精确臂】（COALESCE(mc.modality,'text') = $5）—— ★ ②是闭世界 ⇒ 未知请求值（例如将来 detect 新增的 'document'）会让建议列表静默变空 ⇒ ★ 且它与 ①（开世界精确）语义相反 ⇒ 必须补齐")
	}
	for _, req := range requested {
		switch req {
		case "vision", "audio", "video", "multimodal", "image":
			if !arms.CoversRequest(req) {
				t.Errorf("alternativesSQL 模态过滤块没有为请求模态 %q 解析出过滤臂 —— 该请求值只能落到兜底精确臂 ⇒ 语义与其它臂不同形", req)
			}
		}
	}

	// 逐处比对（主判据）：每一条臂独立对拍，失败信息点名臂原文与漂移格。
	for _, arm := range arms.Arms {
		for _, req := range arm.Reqs {
			for _, reg := range registered {
				regN := reg
				if regN == "" {
					regN = "text"
				}
				sqlV := arm.AcceptsFor(req)[regN]
				goV := provider.ModalityAccepts(reg, req)
				if sqlV != goV {
					t.Errorf("alternativesSQL 模态臂 [%s] 对 (登记=%q, 请求=%q) 判 %v，Go 判据 ModalityAccepts 判 %v —— 唯一片段与 Go 判据漂移",
						strings.TrimSpace(arm.Raw), reg, req, sqlV, goV)
				}
			}
		}
	}

	// 逐格兜底网：不依赖臂解析的完整 (登记, 请求) 扫描，兜住「臂解析遗漏 /
	// 全放行臂语义变化 / 兜底臂丢失」这三类解析器自身的盲点。
	for _, req := range requested {
		for _, reg := range registered {
			sqlV := arms.Accepts(reg, req)
			if goV := provider.ModalityAccepts(reg, req); sqlV != goV {
				t.Errorf("alternativesSQL 对 (登记=%q, 请求=%q) 判 %v，Go 判据 ModalityAccepts 判 %v —— 过滤块整体语义漂移",
					reg, req, sqlV, goV)
			}
		}
	}
}

// TestAlternativesSQL_UsesSharedModalityFragment 是「**只有一份**」门：
// 本包的 SQL 抄本必须**由唯一片段渲染**，且**不含第二份抄本**。
//
// ★ 这道门替代了旧的那条「②必须没有兜底臂」（闭世界形状）—— 旧形状已经
// 作废（兜底精确臂是承重的，见 ModalityAcceptArms 的注释）。
//
// ★ 判据是**可算的**两条：
//
//	① alternativesSQL 逐字包含 provider.ModalityAcceptArmsSQL("$5")；
//	② alternativesSQL 里 `COALESCE(mc.modality` 的出现次数 == 片段里的次数
//	   ⇒ 任何**手抄进**去的第二份接受臂都会让计数对不上 ⇒ 当场红。
//
// ⇒ provider 那份抄本由 provider/modality_accepts_lockstep_test.go 的
//
//	TestCandidateQuerySQL_UsesSharedFragment 用同一把尺子钉住。
func TestAlternativesSQL_UsesSharedModalityFragment(t *testing.T) {
	fragment := provider.ModalityAcceptArmsSQL("$5")
	if !strings.Contains(alternativesSQL, fragment) {
		t.Errorf("alternativesSQL 没有逐字包含唯一片段（provider.ModalityAcceptArmsSQL(\"$5\")）—— ★ 本包不是从唯一片段渲染的 ⇒ 「改一份忘另一份」的风险回来了（本轮要消灭的正是这个）")
	}
	got := countModalityArmOccurrences(alternativesSQL)
	want := countModalityArmOccurrences(fragment)
	if got != want {
		t.Errorf("alternativesSQL 里 `COALESCE(mc.modality` 出现 %d 次，唯一片段里 %d 次 —— ★ 多出来的 %d 处说明有人把手抄的接受臂塞回了本包 ⇒ 判据不再只有一份",
			got, want, got-want)
	}
}

// countModalityArmOccurrences 数一段 SQL 文本里「接受臂的 COALESCE 落点」
// 出现多少次——每一处接受臂恰好带一个 COALESCE(mc.modality, ...) ⇒ 这个
// 计数就是「接受臂的份数」。
func countModalityArmOccurrences(sqlText string) int {
	return strings.Count(sqlText, "COALESCE(mc.modality")
}

// TestAlternativesSQL_ModalityArmsShape 钉住 $5 过滤块的结构形状：
// 恰好三条带接受集的显式臂（vision/audio/video 联合臂 + multimodal 臂 +
// image 臂）+ **一条兜底精确臂**。
// 注释里也出现过形似臂的文本（模态判据注释），本正则要求 $N 前缀 +
// COALESCE 接受集的完整形状，注释行匹配不上；「恰好 3 条」这条计数本身就是
// 反诱饵判据——未来注释或新增 SQL 片段若真的形似到能被解析，这里先红。
func TestAlternativesSQL_ModalityArmsShape(t *testing.T) {
	arms := testutil.ParseModalityAcceptArms(alternativesSQL, "$5")
	if len(arms.Arms) != 3 {
		t.Fatalf("alternativesSQL 模态过滤臂 %d 条（期望 3：vision/audio/video 联合臂 + multimodal 臂 + image 臂）—— 新增/删除臂必须同步 provider.ModalityAccepts 与对拍域", len(arms.Arms))
	}
	if !arms.HasExactFallback {
		t.Fatalf("alternativesSQL 缺【兜底精确臂】（COALESCE(mc.modality,'text') = $5）—— ★ 闭世界形态会让未知请求值的建议列表静默变空，且与 ①（开世界）语义相反 ⇒ 必须补齐")
	}
}

// TestAlternativesSQL_LockstepMutationArms 是本锁步门自身的变异臂：对**唯一
// 片段**做四类语义漂移 + 一条负控，证明对拍真的在比较两边的裁决——
// §527 通用式二：变异臂的靶点必须真实落在被锁判据上。
//
// ★ 2026-10-29：变异对象从「本包抄本」改成**唯一片段** ⇒ 这才是「只有一份」
// 的真检验：片段变了，两份 SQL 抄本**同时**变 ⇒ 任何一侧忘了同步都不可能
// （因为没有第二份可忘）。
func TestAlternativesSQL_LockstepMutationArms(t *testing.T) {
	base := provider.ModalityAcceptArmsSQL("$5")
	registered := []string{"", "text", "vision", "audio", "video", "multimodal", "embedding"}
	requested := append(append([]string{}, provider.ModalityRequestModalities...), "")

	driftCells := func(mutatedSQL string) []string {
		arms := testutil.ParseModalityAcceptArms(mutatedSQL, "$5")
		var cells []string
		for _, req := range requested {
			for _, reg := range registered {
				if goV := provider.ModalityAccepts(reg, req); arms.Accepts(reg, req) != goV {
					cells = append(cells, fmt.Sprintf("(登记=%s, 请求=%s)", reg, req))
				}
			}
		}
		return cells
	}

	expectDrift := func(name, mutatedSQL string) {
		t.Helper()
		if cells := driftCells(mutatedSQL); len(cells) == 0 {
			t.Fatalf("%s：变异后对拍零漂移 —— 门对这类语义漂移没有牙", name)
		}
	}

	// M0 负控：非语义改动（空白）必须零漂移——证明判据不是恒红。
	m0 := strings.Replace(base, "$5 = ''", "$5 =  ''", 1)
	if m0 == base {
		t.Fatal("M0 负控的替换锚没命中")
	}
	if cells := driftCells(m0); len(cells) != 0 {
		t.Fatalf("M0 负控：空白改动报出漂移 %v —— 判据把非语义改动当漂移，过宽", cells)
	}

	// M1：联合臂接受集去掉 multimodal ⇒ (multimodal, vision/audio/video) 漂移。
	m1 := strings.Replace(base,
		"COALESCE(mc.modality, 'text') IN ($5, 'multimodal')",
		"COALESCE(mc.modality, 'text') IN ($5)", 1)
	if m1 == base {
		t.Fatal("M1 变异的替换锚没命中")
	}
	expectDrift("M1 联合臂接受集去 multimodal", m1)

	// M2：multimodal 专属臂接受集收窄成 text ⇒ (text/multimodal, multimodal) 漂移。
	m2 := strings.Replace(base,
		"OR ($5 = 'multimodal' AND COALESCE(mc.modality, 'text') = 'multimodal')",
		"OR ($5 = 'multimodal' AND COALESCE(mc.modality, 'text') = 'text')", 1)
	if m2 == base {
		t.Fatal("M2 变异的替换锚没命中")
	}
	expectDrift("M2 multimodal 臂接受集收窄", m2)

	// M3：image 臂整条删除 ⇒ image 请求落兜底精确臂（只接受 image 登记，
	// 不再接受 vision/multimodal）⇒ (vision/multimodal, image) 漂移。
	m3 := strings.Replace(base,
		"OR ($5 = 'image' AND COALESCE(mc.modality, 'text') IN ('image', 'vision', 'multimodal'))",
		"", 1)
	if m3 == base {
		t.Fatal("M3 变异的删除锚没命中")
	}
	expectDrift("M3 image 臂删除", m3)

	// M4：联合臂请求侧去掉 video ⇒ video 请求落兜底精确臂（只接受 video 登记）
	// ⇒ (multimodal, video) 漂移。
	m4 := strings.Replace(base,
		"$5 IN ('vision', 'audio', 'video')",
		"$5 IN ('vision', 'audio')", 1)
	if m4 == base {
		t.Fatal("M4 变异的替换锚没命中")
	}
	expectDrift("M4 联合臂请求侧去 video", m4)

	// M5：★ 兜底精确臂删除 ⇒ 未知请求模态从「精确相等」变成「全拒」⇒
	// (embedding, embedding) 格漂移。★ 这一臂守的是**域外**语义——
	// 它正是本轮收掉的那条历史分歧（Go 宽解释 vs SQL 精确）的现场。
	m5 := strings.Replace(base,
		"OR COALESCE(mc.modality, 'text') = $5",
		"", 1)
	if m5 == base {
		t.Fatal("M5 变异的删除锚没命中")
	}
	expectDrift("M5 兜底精确臂删除", m5)

	// M6：★ 负控（反向）——把唯一片段渲染进 alternativesSQL 后，片段里多一处
	// 接受臂 ⇒ 「只有一份」门的计数判据必须变红。★ 这一条证明那道门有牙
	// （否则「只有一份」只是一句注释）。
	withExtra := strings.Replace(alternativesSQL,
		provider.ModalityAcceptArmsSQL("$5"),
		provider.ModalityAcceptArmsSQL("$5")+"\n          OR ($5 = 'extra' AND COALESCE(mc.modality,'text') = 'extra')",
		1)
	if countModalityArmOccurrences(withExtra) == countModalityArmOccurrences(provider.ModalityAcceptArmsSQL("$5")) {
		t.Fatal("M6 负控：注入额外接受臂后计数没变 ⇒ 「只有一份」门的计数判据没有牙")
	}
}

// ── detect 值域闭合（★ 本轮批判式审计补的洞）─────────────────────────────
//
// ★★ 洞是什么：对拍域是**枚举出来的**，而 $5 的生产值域来自
//
//	detectRequestModality（domains/streaming/modality_detect.go）。
//	★ 2026-10-29 更新：②曾因缺兜底臂而是**闭世界**（未知请求值 ⇒ 整个
//	AND 不成立 ⇒ **建议列表静默变空**，不是 4xx、不是日志）；而 Go 判据
//	ModalityAccepts 的 default 臂对未知请求值是
//	`registered == requested || registered == "multimodal"` ⇒ 对
//	(multimodal, 未知值) 判 **true**。
//	⇒⇒ 两边在**域外**语义相反：域内一致（现有门锁着）≠ 域外一致。
//	⇒⇒ 只要 detect 将来扩一个值域（例如为「文档/PDF 输入」新增 "document"），
//	现有三道锁步门**全部照样绿**，而生产上该模态的建议列表会**整条消失**。
//	⇒⇒ 「域外不可达」此前只是一句**注释里的登记**（本文件测试头）⇒
//	登记不是门 ⇒ 必须把它变成**可执行判据**。
//
// ★★ 2026-10-29收口：兜底精确臂补齐后，②也是开放世界了 ⇒ 未知请求值
// 不再让列表变空（它精确匹配同名登记行，与 ① 一致）⇒ 「静默变空」这个形态
// 从根上消失。⇒ 但 detect 值域闭合这条判据**仍然要留**：它是「detect 新增
// 一个返回路径」的唯一探测器（新增 'document' 时，②会精确匹配 document 行
// ——语义正确；但 detect 的值域必须仍然被登记在判据里，否则将来有人在
// 判据里漏掉它，两边就又分叉了）。
//
// 两条互补判据：
//
//	① 行为判据：真实调用 detectRequestModality（不复制它的逻辑）跑一组
//	   代表性 body，断言产出值落在「判据有臂的请求值 ∪ 全放行值」之内。
//	② 穷举判据：detect 的**返回常量集合**（源码里全部 `return modalityX`）
//	   必须 ⊆ 允许集 ⇒ detect 新增一种返回路径（哪怕代表性 body 覆盖不到）
//	   也会当场红。①漏「形态」、②漏「路径」，两条合起来才是闭合。
func TestAlternativesSQL_DetectModalityDomainClosed(t *testing.T) {
	arms := testutil.ParseModalityAcceptArms(alternativesSQL, "$5")

	// ② 穷举判据：detect 的返回常量集合。
	src, err := os.ReadFile("modality_detect.go")
	if err != nil {
		t.Fatalf("读 modality_detect.go 失败：%v", err)
	}
	// 常量定义：`modalityText = "text"` —— 取名字与字面值。
	constDefRE := regexp.MustCompile(`(?m)^\s*(modality[A-Za-z0-9_]*)\s*=\s*"([^"]*)"`)
	constValues := map[string]string{}
	for _, m := range constDefRE.FindAllStringSubmatch(string(src), -1) {
		constValues[m[1]] = m[2]
	}
	if len(constValues) == 0 {
		t.Fatalf("modality_detect.go 里没解析出任何 modalityX 常量 —— 源文件形态变了（常量定义写法改了？），本判据失效")
	}
	returnRE := regexp.MustCompile(`return\s+(modality[A-Za-z0-9_]*)`)
	returned := map[string]bool{}
	for _, m := range returnRE.FindAllStringSubmatch(string(src), -1) {
		returned[m[1]] = true
	}
	if len(returned) == 0 {
		t.Fatalf("modality_detect.go 里没解析出任何 `return modalityX` —— 源文件形态变了（返回值写法改了？），本判据失效")
	}

	// ①+② 合起来：每个被返回的常量，其字面值都必须落在 alternativesSQL
	//    的「有臂请求值 ∪ 全放行值」之内。
	covered := map[string]bool{"": true, "text": true} // 全放行臂
	for _, a := range arms.Arms {
		for _, r := range a.Reqs {
			covered[r] = true
		}
	}
	for name := range returned {
		val, ok := constValues[name]
		if !ok {
			t.Errorf("detect 返回了 %s，但它没有对应的字面值常量定义（源文件形态变了？）", name)
			continue
		}
		if !covered[val] {
			t.Errorf("detectRequestModality 可能产出请求模态 %q（%s）⇒ alternativesSQL 的 $5 过滤块没有对应的显式臂 ⇒ 该模态只能落到兜底精确臂（精确匹配同名登记行）⇒ ★ 语义与其它臂不同形 ⇒ 要么给 $5 块补一条臂，要么让 detect 别产出它", val, name)
		}
	}

	// ① 行为判据：真实 detect 的产出必须落在同一个集合内。
	bodies := map[string][]byte{
		"空 body":        nil,
		"坏 JSON":        []byte("{not json"),
		"纯文本":           []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
		"Responses 纯文本": []byte(`{"input":"hello"}`),
		"图（OpenAI）":     []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"http://x/y.png"}}]}]}`),
		"图（Anthropic）":  []byte(`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA"}}]}]}`),
		"图（Gemini）":     []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"AA"}}]}]}`),
		"音频":            []byte(`{"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"AA","format":"wav"}}]}]}`),
		"视频":            []byte(`{"messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"http://x/v.mp4"}}]}]}`),
		"混模（视频优先）":      []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"http://x/y.png"}},{"type":"video_url","video_url":{"url":"http://x/v.mp4"}}]}]}`),
	}
	for name, body := range bodies {
		got := detectRequestModality(body)
		if !covered[got] {
			t.Errorf("detectRequestModality(%s) 产出 %q ⇒ alternativesSQL 无对应臂 ⇒ 该模态的建议列表会静默变空", name, got)
		}
	}
}

// TestAlternativesSQL_DetectModalityDomainMutationArms 是上面那条闭合判据的
// 变异臂：把 detect 的返回值集合改宽（新增一个域外值），闭合判据必须红。
// 变异只作用于**本测试文件里的读入文本**（不碰生产源文件）⇒ 证明判据读的
// 确实是那组返回常量，而不是恒真。
func TestAlternativesSQL_DetectModalityDomainMutationArms(t *testing.T) {
	base := `package streaming

const (
	modalityText   = "text"
	modalityVision = "vision"
	modalityAudio  = "audio"
	modalityVideo  = "video"
)

func detectRequestModality(b []byte) string {
	if len(b) == 0 {
		return modalityText
	}
	return modalityVideo
}
`
	// 抽出「判据」本身：返回常量集合 + 字面值常量集合。
	extract := func(src string) (returned map[string]bool, values map[string]string) {
		constDefRE := regexp.MustCompile(`(?m)^\s*(modality[A-Za-z0-9_]*)\s*=\s*"([^"]*)"`)
		returnRE := regexp.MustCompile(`return\s+(modality[A-Za-z0-9_]*)`)
		values = map[string]string{}
		for _, m := range constDefRE.FindAllStringSubmatch(src, -1) {
			values[m[1]] = m[2]
		}
		returned = map[string]bool{}
		for _, m := range returnRE.FindAllStringSubmatch(src, -1) {
			returned[m[1]] = true
		}
		return returned, values
	}
	covered := map[string]bool{"": true, "text": true, "vision": true, "audio": true, "video": true}

	// 基线：detect 只返回域内值 ⇒ 无域外违规。
	returned, values := extract(base)
	offDomain := []string{}
	for name := range returned {
		if !covered[values[name]] {
			offDomain = append(offDomain, values[name])
		}
	}
	if len(offDomain) != 0 {
		t.Fatalf("基线就报出域外值 %v —— 判据写错了", offDomain)
	}

	// 变异：新增一个域外返回（document）⇒ 必须被判出。
	mutated := strings.Replace(base,
		`modalityVideo  = "video"`,
		`modalityVideo  = "video"
	modalityDocument = "document"`, 1)
	mutated = strings.Replace(mutated, "return modalityVideo\n", "return modalityVideo\n}\n\nfunc detectDocument(b []byte) string {\n\treturn modalityDocument", 1)
	returned2, values2 := extract(mutated)
	found := false
	for name := range returned2 {
		if !covered[values2[name]] {
			found = true
		}
	}
	if !found {
		t.Fatalf("变异后判据没报出域外返回常量 —— 闭合判据对「detect 扩值域」没有牙")
	}
}
