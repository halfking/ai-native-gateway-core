package provider

import (
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/tests/testutil"
)

// 模板级锁步门：Go 判据 ModalityAccepts 与 candidateQuerySQLBase 的模态接受臂
// 必须对同一组 (登记模态, 请求模态) 给出同一个裁决。
//
// 历史（2026-10-28 批判式审计 §1.2 / LESSONS §497）：此前仓里只有两份 Go 抄本
// 互相对拍（ModalityAccepts vs 测试里手写的镜像），没有任何机制锁住「Go 抄本 ↔
// SQL 原文」——三份表达（SQL / Go 判据 / 测试镜像）里后两份会一起漂。
// 本测试把 SQL 接受臂**解析**成 (请求模态 → 接受集) 的结构化判据，再逐格
// 与 ModalityAccepts 对拍；SQL 原文改判据时这里必须同步改，否则红。
//
// ★★ 2026-10-29：判据的 SQL 形态已**同源**——两份 SQL 抄本都由
// provider/modality_accept.go 的唯一片段渲染（ModalityAcceptArmsSQL）。
// ⇒ 本门对拍的对象从「第一份 SQL 抄本」变成「**那条唯一片段**」；
//
//	「两份抄本都来自它」由 domains/streaming/modality_single_source_test.go
//	与本文件的 TestCandidateQuerySQL_UsesSharedFragment 钉住。
//
// ⇒ ★解析器也换成了共享实现（tests/testutil.ParseModalityAcceptArms）——
//
//	此前 provider 侧与 streaming 侧各有一份正则，**形态不同**（provider 那份
//	只认接受侧的 `IN (...)` ⇒ 看不见 `= 'multimodal'` 那条臂）⇒ 同一个判据
//	被两把不同的尺量。
//
// 对拍域（两轴都取**运行时事实值域**，不是想象的全集）：
//   - 请求侧：provider.ModalityRequestModalities（text/vision/audio/video/
//     image/multimodal/embedding）+ 空串（无过滤）+ **一个未知值样例**
//     （"document"）——★ 未知值那一格正是【兜底精确臂】的语义所在，
//     不测它 ⇒ 兜底臂等于没测。
//   - 登记侧：models_canonical.modality 的 CHECK（migration 451）
//     限 text/vision/audio/video/multimodal/embedding；空值在 SQL 侧经
//     COALESCE(...,'text')、在 Go 侧归一为 text，两边等价。
//
// ★★ 语义分歧登记（2026-10-29 更新）：**域外的分歧已经不存在了**。
// 此前登记过一条：Go default 臂（registered == requested || multimodal）
// 在 (multimodal, embedding) 格判 true、SQL 兜底精确臂判 false ⇒ 两边在
// 域外语义相反。现在 Go default 臂**收窄为精确相等**（与 SQL 兜底臂同形），
// ⇒ ①②③ 三份表达在**全部**格上同义，不只是对拍域内。
// ⇒ 该登记已删除（历史保留在 provider/modality_accept.go 的注释里）。
//
// 解析器教训（第一版当场踩过的）：多值列表 ('vision', 'audio', 'video') 里
// 的引号不能被 [^']* 跨过——值列表用 [^)]* 取整段再拆；接受集里的 $3
// 占位符意思是「请求模态自身」，必须显式展开，否则每个联合臂都会假报漂移。
//
// ★★ 覆盖边界（2026-10-28 批判式审计补登，**别把本门当成「全部模态裁决」**）：
// 生产真正执行的 SQL 是 candidateQuerySQL() = base **替换 verdict gate** 后的
// 产物（modalityVerdictGateSQL）。本门对拍的是**接受臂**（静态接受集），
// 不是叠加 gate 之后的最终裁决。这不是遗漏，是职责分层：
//
//	· 接受臂 = 「登记模态 × 请求模态 ⇒能不能服务」（静态、可枚举）——Go 侧
//	  ModalityAccepts 建模的正是这一层，诊断用它给出「模态不匹配」的因果。
//	· verdict gate = 数据驱动的**排除**层（model_modality_verification 里
//	  有负向读级记录 ⇒ 把该模型从结果里剔掉）——Go 侧不建模这一层，且
//	  **不该**建模：负向读级是「实测这个模型看不见」，与「登记成什么」是两件事。
//
// ⇒ 本门对拍接受臂 ⇒ 覆盖 ModalityAccepts 的**全部语义**；
// ⇒ gate 层的语义由上游两道门管：candidate_query_sql_shape_test.go 钉 gate 的
//
//	模态列表与接受臂不漂移；modality_gate_alignment_test.go 用真库把 gate 与
//	进度视图逐条对齐（含 read_unknown=0 这个真实漂移）。
//
// ⇒ 但「gate 与接受臂的**模态列表**必须一致」这条是本门该管的（否则 vision
//
//	请求走臂、audio 请求走 gate 的排除面，模态集合就分叉了）——
//	见 TestModalityAccepts_LockstepGateModalityListMatchesArms。
//
// ★ 三角闭合（2026-10-29）：模态判据的第三份表达——domains/streaming 的
//
//	alternativesSQL $5 过滤块（零候选建议列表）——已由
//	domains/streaming/model_alternatives_modality_lockstep_test.go 对
//	ModalityAccepts 逐处锁步。本门（①↔③）+ 该门（②↔③）⇒ 三份表达两两一致。
func TestModalityAccepts_LockstepWithCandidateSQL(t *testing.T) {
	sqlText := stripSQLComments(candidateQuerySQLBase())

	// 臂的存在性先锁死（一条臂被改名/删除 ⇒ 这里红，而不是静默兜底通过）。
	for _, frag := range []string{
		"$3 = ''",                            // 无模态过滤：全放行
		"$3 = 'text'",                        // 文本请求：全放行
		"COALESCE(mc.modality, 'text') = $3", // 兜底精确臂（末位）
	} {
		if !strings.Contains(sqlText, frag) {
			t.Fatalf("candidateQuerySQLBase 模态接受臂缺锚点 %q —— 判据结构变了，本测试的对拍表必须跟着重写", frag)
		}
	}

	arms := testutil.ParseModalityAcceptArms(sqlText, "$3")

	// ★ 对拍域：请求侧 = 事实词表 + 空串 + **一个未知值样例**。
	//   未知值那一格是【兜底精确臂】的语义所在（域外分歧的历史现场）。
	requestedSet := append([]string{""}, providerRequestModalities()...)
	requestedSet = append(requestedSet, "document") // 未知请求模态的代表值
	registeredSet := []string{"", "text", "vision", "audio", "video", "multimodal", "embedding"}

	// 派生域断言：登记侧的分母必须与事实源（models_canonical.modality 的
	// CHECK 约束）一致——事实源新增值 ⇒ 要求扩域；事实源被改小 ⇒ 红。
	// ⇒ 这条堵住「**缩小分母 + 改判据 ⇒ 零漂移假绿**」的合谋路径。
	derivedRegistered, err := testutil.CanonicalModalityValueDomain("..")
	if err != nil {
		t.Fatalf("派生登记模态值域失败：%v", err)
	}
	inSet := func(v string, set []string) bool {
		for _, s := range set {
			if s == v {
				return true
			}
		}
		return false
	}
	for _, reg := range derivedRegistered {
		if !inSet(reg, registeredSet) {
			t.Errorf("登记侧对拍域缺少事实源里的值 %q（事实源=models_canonical.modality 的 CHECK 约束）—— 缩小分母即可假绿，必须覆盖全部 CHECK 值", reg)
		}
	}
	for _, reg := range registeredSet {
		if reg == "" {
			continue
		}
		if !inSet(reg, derivedRegistered) {
			t.Errorf("登记侧对拍域里的 %q 不在事实源（models_canonical.modality 的 CHECK 约束）里 —— 事实源被改小了或对拍域写错了", reg)
		}
	}

	// 路由器会发的媒体请求模态必须各有自己的臂——缺臂时兜底臂会静默接管，
	// 过滤面就变了，这里点名。
	for _, req := range requestedSet {
		switch req {
		case "vision", "audio", "video", "image", "multimodal":
			if !arms.CoversRequest(req) {
				t.Errorf("SQL 接受臂没有为请求模态 %q 解析出媒体臂", req)
			}
		}
	}

	for _, req := range requestedSet {
		for _, reg := range registeredSet {
			goVerdict := ModalityAccepts(reg, req)
			sqlVerdict := arms.Accepts(reg, req)
			if goVerdict != sqlVerdict {
				t.Errorf("ModalityAccepts(%q, %q) = %v，SQL 接受臂 = %v —— Go 判据与 candidateQuerySQLBase 漂移",
					reg, req, goVerdict, sqlVerdict)
			}
		}
	}
}

// TestCandidateQuerySQL_UsesSharedFragment 是「**只有一份**」门的 provider 侧：
// 候选集查询的接受臂必须**由唯一片段渲染**，且**不含第二份抄本**。
//
// ★ 与 domains/streaming/model_alternatives_modality_lockstep_test.go 的
// TestAlternativesSQL_UsesSharedModalityFragment 是**同一把尺子的两半**：
// 两边都断言「本份 SQL == 唯一片段渲染出来的文本」且「本份 SQL 里的接受臂
// 份数 == 片段里的份数」⇒ 两份 SQL 都来自那一片段 ⇒ 判据只有一份。
//
// ★ 判据是**可算的**：
//
//	① candidateQuerySQLBase() 逐字包含 ModalityAcceptArmsSQL("$3")；
//	② candidateQuerySQLBase() 里 `COALESCE(mc.modality` 的出现次数 ==
//	   片段里的次数 ⇒ 任何**手抄进**去的第二份接受臂都会让计数对不上。
func TestCandidateQuerySQL_UsesSharedFragment(t *testing.T) {
	fragment := ModalityAcceptArmsSQL("$3")
	base := candidateQuerySQLBase()
	if !strings.Contains(base, fragment) {
		t.Errorf("candidateQuerySQLBase 没有逐字包含唯一片段（ModalityAcceptArmsSQL(\"$3\")）—— ★ 本包不是从唯一片段渲染的 ⇒ 「改一份忘另一份」的风险回来了")
	}
	got := strings.Count(base, "COALESCE(mc.modality")
	want := strings.Count(fragment, "COALESCE(mc.modality")
	if got != want {
		t.Errorf("candidateQuerySQLBase 里 `COALESCE(mc.modality` 出现 %d 次，唯一片段里 %d 次 —— ★ 多出来的 %d 处说明有人把手抄的接受臂塞回了本包 ⇒ 判据不再只有一份",
			got, want, got-want)
	}
}

// providerRequestModalities 返回请求侧的判据词表（事实源锚定见
// domains/streaming/modality_single_source_test.go）。
func providerRequestModalities() []string {
	out := make([]string, len(ModalityRequestModalities))
	copy(out, ModalityRequestModalities)
	return out
}

// TestModalityAccepts_LockstepArmsShape 锁接受臂的**形状**：恰好三条带接受
// 集的显式臂（vision/audio/video 联合臂 + multimodal 臂 + image 臂）＋
// **一条兜底精确臂**。
//
// ★ 兜底精确臂是承重的（/v1/embeddings 靠它命中登记为 'embedding' 的行），
//
//	缺了它 ⇒ 嵌入请求零候选 ⇒ 功能事故。⇒ 它的存在性必须被钉住。
//
// ★ 多出的臂要么是新判据（必须同步 ModalityAccepts 与上面的对拍域），
//
//	要么是死代码。
func TestModalityAccepts_LockstepArmsShape(t *testing.T) {
	arms := testutil.ParseModalityAcceptArms(stripSQLComments(candidateQuerySQLBase()), "$3")
	if len(arms.Arms) != 3 {
		t.Fatalf("带接受集的显式臂 %d 条（期望 3：vision/audio/video 联合臂 + multimodal 臂 + image 臂）—— 新增/删除臂必须同步 ModalityAccepts 与对拍表", len(arms.Arms))
	}
	if !arms.HasExactFallback {
		t.Fatalf("接受臂缺【兜底精确臂】（COALESCE(mc.modality,'text') = $3）—— ★ 承重：/v1/embeddings 靠它命中登记为 'embedding' 的行 ⇒ 缺了它嵌入请求零候选 ⇒ 功能事故")
	}
}

// TestModalityAccepts_LockstepGateModalityListMatchesArms 钉住「verdict gate
// 的模态列表」与「接受臂的媒体模态集合」的关系。
//
// ★ 为什么本门该管这一条（2026-10-28 批判式审计补登）：
//
//	verdict gate（modalityVerdictGateSQL）用 `AND NOT (... $3 IN (...) ...)`
//	排除「有负向读级记录」的模型。它的模态列表如果与接受臂的媒体模态集合
//	**分叉**（例如 gate少了 audio），就会出现：audio 请求走臂（被臂接纳）
//	但 audio 的负向读级**不触发排除** ⇒ 「实测看不见」的模型仍被路由到。
//	⇒ 这是一个**静默的质量退化**：没有4xx、没有日志、只是路由到了不该去的
//	  模型（上游曾因 Gemini 多模态臂漏 video 造成整族 503，见
//	  candidate_query_sql_shape_test.go 的钉子）。
//	⇒ 判据：gate 的模态列表 ⊇ 接受臂里所有「有媒体臂」的模态。
//	  （反向不要求：image 走专用臂，gate 本就不管 image——那是既有设计。）
//
// ★ 为什么这是**集合包含**而不是集合相等：gate 是排除层，可以比臂窄
//
//	（少排除不致命？）——不，少排除是致命的（上面说的）。所以必须 ⊇。
//	而 gate 可以比臂宽（多排除一些模态）——那是更保守的方向，不致命。
//
// ★★ 2026-10-29 补正（分母锚到事实源）：「臂内媒体模态」这个分母此前是
//
//	**枚举出来的**（手写 {vision,audio,video}）⇒ 事实源加了模态而门没扩
//	⇒ 门绿 ⇒ 真实漂移检不出。⇒ 现在两个方向都锚：
//	 ① **事实源**：model_modality_verification.modality 的 CHECK 约束
//	   （迁移 825：IN ('vision','audio','video')）—— 读级记录**只可能**是这
//	   三个模态 ⇒ gate 的列表**不可能也不应该**覆盖别的模态。
//	 ② **臂**：接受臂里出现的模态 ∩ 事实源值域 ⇒ 这些模态必须有排除面。
//	   ★ 'multimodal' 不在事实源值域里 ⇒ 没有 'multimodal' 的读级记录 ⇒
//	   gate 结构上无法覆盖它 ⇒ 它不进「必须有排除面」的分母（不是豁免，
//	   是事实）。
func TestModalityAccepts_LockstepGateModalityListMatchesArms(t *testing.T) {
	base := stripSQLComments(candidateQuerySQLBase())
	arms := testutil.ParseModalityAcceptArms(base, "$3")

	// 臂里出现的请求模态（媒体臂）。
	mediaInArms := map[string]bool{}
	for _, a := range arms.Arms {
		for _, req := range a.Reqs {
			if req == "image" {
				continue // image 走专用臂，gate 不覆盖 image（既有设计，见函数注释）
			}
			mediaInArms[req] = true
		}
	}
	if len(mediaInArms) == 0 {
		t.Fatalf("接受臂里没解析出任何媒体模态 —— 解析器或 SQL形态变了，本判据失效")
	}

	// ★ 事实源：读级记录的模态词表（迁移 825 的 CHECK）。
	verifiedMods, err := testutil.ModalityVerificationValueDomain("..")
	if err != nil {
		t.Fatalf("派生读级记录模态值域失败：%v", err)
	}
	inVerified := func(v string) bool {
		for _, s := range verifiedMods {
			if s == v {
				return true
			}
		}
		return false
	}

	// gate 的模态列表：**只从 gate 片段里取**，不能扫整条 SQL。
	//
	// ★ 变异自检实测（2026-10-28）：整条 SQL 里还有**第三处** `$3 IN (...)`
	//   —— routing_scope 的 CASE 分支 `WHEN $3 IN ('audio','video','image')`，
	//   它与模态接受/排除无关（管的是凭据路由域）。扫全 SQL 时，audio 会被
	//   这处「顺带算进 gate 列表」⇒ 把 gate 的模态列表改窄（删 audio）也
	//   检不出来 ⇒ 门对目标变异没有牙。
	// ⇒ 判据取法：把 gate 片段（modalityVerdictGateSQL 的产物）单独取出，
	//   只在它里面找 `$3 IN (...)`。
	// ★ 逐档比对（不是把两档的列表并成一个集合）——变异自检实测（2026-10-28）：
	//   gate 有【两档】（缺省档 / 严格档），各带一份模态列表。并成集合时，
	//   改窄其中一档会被另一档掩盖 ⇒ 门对该变异没有牙。
	//   ⇒ 判据：每一档的列表都必须 ⊇ 臂内媒体模态集合。
	// ★★ 判据形态：**逐处列表**，不按档聚合（2026-10-28 变异自检 M-A/M-B 实测）：
	//   gate 里有多处 `$3 IN (...)`（缺省段一处、严格段追加一处），两处的
	//   列表内容相同（vision/audio/video）。
	//   ⇒ 「按档取并集」会互相掩盖（改窄一处，另一处仍含该模态 ⇒ 检不出）；
	//     「按档取差集」也不成立（两处相同 ⇒ 差集为空 ⇒ 判据失效）。
	//   ⇒ 正确形态：**每一处**列表都必须覆盖臂内媒体模态集合。
	//     这样改窄任意一处都会当场报红（点明是第几处）。
	gateRE2 := regexp.MustCompile(`\$3 IN \(([^)]*)\)`)
	type gateList struct {
		idx  int
		mods map[string]bool
	}
	var lists []gateList
	for _, gate := range []string{modalityVerdictGateSQL(false), modalityVerdictGateSQL(true)} {
		for i, m := range gateRE2.FindAllStringSubmatch(stripSQLComments(gate), -1) {
			mods := map[string]bool{}
			for _, v := range strings.Split(m[1], ",") {
				v = strings.ToLower(strings.Trim(strings.TrimSpace(v), "'"))
				if v != "" && v != "$3" {
					mods[v] = true
				}
			}
			if len(mods) == 0 {
				t.Fatalf("verdict gate 里第 %d 处模态列表为空 —— gate 形态变了，本判据失效", i+1)
			}
			lists = append(lists, gateList{idx: i + 1, mods: mods})
		}
	}
	if len(lists) == 0 {
		t.Fatalf("verdict gate 片段里没解析出任何模态列表 —— gate 形态变了（SQL 拼接方式变了），本判据失效")
	}
	for _, l := range lists {
		// 逐处判：这一处必须覆盖「臂内媒体模态 ∩ 读级记录事实源值域」。
		for req := range mediaInArms {
			if !inVerified(req) {
				// 该模态没有读级记录（事实源里没有）⇒ gate 结构上无法覆盖
				// 它 ⇒ 不进分母（不是豁免，是事实）。
				continue
			}
			if !l.mods[req] {
				t.Errorf("verdict gate 的第 %d 处模态列表没覆盖请求模态 %q（该处列表=%v）⇒ 该模态的负向读级不会触发排除 ⇒ 实测「看不见」的模型仍会被路由到（静默的质量退化）",
					l.idx, req, l.mods)
			}
		}
		// ★ 反向：gate 的列表里出现「读级记录事实源里没有」的模态 ⇒ 那条
		//   排除面是**死的**（永远命中不了）⇒ 判红（否则它看起来像有覆盖）。
		for mod := range l.mods {
			if !inVerified(mod) {
				t.Errorf("verdict gate 的第 %d 处模态列表含 %q，但读级记录表（model_modality_verification）的 CHECK 里没有这个模态 ⇒ 这条排除面永远命中不了（死面）", l.idx, mod)
			}
		}
	}
	// 抽样核对：生产 SQL 里确实含这个 gate（占位符替换没坏）。
	if !strings.Contains(stripSQLComments(candidateQuerySQL()), "model_modality_verification") {
		t.Fatalf("生产 SQL（candidateQuerySQL()）里没有 verdict gate 的痕迹 —— 占位符替换可能坏了，本判据失效")
	}
}

// TestModalityAccepts_LockstepMutationArms 是锁步门自身的变异臂：对**唯一片段**
// 做三类语义漂移，门必须当场报出「Go 判据与 SQL 裁决不一致」（红）——
// 证明对拍真的在比较两边的裁决，而不是两边都在恒真。
//
// ★ 2026-10-29：变异对象从「第一份 SQL 抄本」改成**唯一片段**
// （ModalityAcceptArmsSQL）—— ★ 这才是「只有一份」的真检验：片段变了，
// 两份 SQL 抄本**同时**变（它们都由它渲染）⇒ 判据漂移必然被检出。
func TestModalityAccepts_LockstepMutationArms(t *testing.T) {
	base := stripSQLComments(ModalityAcceptArmsSQL("$3"))
	// ★ 与主测试同一登记侧域（含 embedding）——两处域必须一致，否则「主测试
	//   的域」与「变异臂的域」分叉，某一域里的漂移就检不出。
	registeredSet := []string{"", "text", "vision", "audio", "video", "multimodal", "embedding"}
	requestedSet := append(providerRequestModalities(), "")

	expectDrift := func(name, mutatedSQL string) {
		t.Helper()
		arms := testutil.ParseModalityAcceptArms(mutatedSQL, "$3")
		drift := 0
		for _, req := range requestedSet {
			for _, reg := range registeredSet {
				if ModalityAccepts(reg, req) != arms.Accepts(reg, req) {
					drift++
				}
			}
		}
		if drift == 0 {
			t.Fatalf("%s：变异后对拍零漂移 —— 门对这类语义漂移没有牙（§497：变异臂的靶点必须真实落在被锁判据上）", name)
		}
	}

	// M0 负控：非语义改动（空白）必须零漂移——证明判据不是恒红。
	m0 := strings.Replace(base, "$3 = ''", "$3 =  ''", 1)
	if m0 == base {
		t.Fatal("M0 负控的替换锚没命中")
	}
	expectNoDrift := func(name, mutatedSQL string) {
		t.Helper()
		arms := testutil.ParseModalityAcceptArms(mutatedSQL, "$3")
		for _, req := range requestedSet {
			for _, reg := range registeredSet {
				if ModalityAccepts(reg, req) != arms.Accepts(reg, req) {
					t.Fatalf("%s：非语义改动报出漂移 (登记=%q, 请求=%q) —— 判据把非语义改动当漂移，过宽", name, reg, req)
				}
			}
		}
	}
	expectNoDrift("M0 负控", m0)

	// M1：vision 从请求侧联合列表掉出去 ⇒ SQL 不再接受 vision 请求。
	m1 := strings.Replace(base,
		"$3 IN ('vision', 'audio', 'video') AND COALESCE(mc.modality, 'text') IN ($3, 'multimodal')",
		"$3 IN ('audio', 'video') AND COALESCE(mc.modality, 'text') IN ($3, 'multimodal')", 1)
	if m1 == base {
		t.Fatal("M1 变异的替换锚没命中")
	}
	expectDrift("M1 vision 请求侧", m1)

	// M2：multimodal 从接受集掉出去 ⇒ SQL 的媒体请求不再接受 multimodal 登记。
	m2 := strings.Replace(base,
		"$3 IN ('vision', 'audio', 'video') AND COALESCE(mc.modality, 'text') IN ($3, 'multimodal')",
		"$3 IN ('vision', 'audio', 'video') AND COALESCE(mc.modality, 'text') IN ($3)", 1)
	if m2 == base {
		t.Fatal("M2 变异的替换锚没命中")
	}
	expectDrift("M2 multimodal 接受集", m2)

	// M3：image 臂整条删除 ⇒ image 请求回落兜底精确臂（只接受 image 登记，
	// 不再接受 vision/multimodal）⇒ 对 (vision, image)/(multimodal, image) 格漂移。
	m3 := strings.Replace(base,
		"OR ($3 = 'image' AND COALESCE(mc.modality, 'text') IN ('image', 'vision', 'multimodal'))",
		"", 1)
	if m3 == base {
		t.Fatal("M3 变异的删除锚没命中")
	}
	expectDrift("M3 image 臂删除", m3)

	// M4：★ 兜底精确臂删除 ⇒ 未知请求模态从「精确相等」变成「全拒」⇒
	// (embedding, embedding) 格漂移。★ 这一臂守的是**域外**语义——
	// 它正是本轮收掉的那条历史分歧（Go 宽解释 vs SQL 精确）的现场。
	m4 := strings.Replace(base,
		"OR COALESCE(mc.modality, 'text') = $3",
		"", 1)
	if m4 == base {
		t.Fatal("M4 变异的删除锚没命中")
	}
	expectDrift("M4 兜底精确臂删除", m4)
}
