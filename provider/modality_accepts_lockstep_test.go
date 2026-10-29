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
// 对拍域（两轴都取**运行时事实值域**，不是想象的全集）：
//   - 请求侧：detectRequestModality 只产 text/vision/audio/video
//     （domains/streaming/modality_detect.go）；专用端点产 image
//     （/v1/images/generations）、video（/v1/videos）、audio（/v1/audio）、
//     embedding（/v1/embeddings）；OmniFree 的 modality 同样来自
//     detectRequestModality。ModalityAccepts 的唯一生产调用方是诊断
//     （modality_mismatch.go:109），其入参就是这些值 + ""（无过滤）。
//   - 登记侧：models_canonical.modality 的 CHECK（migration 451）
//     限 text/vision/audio/video/multimodal/embedding；空值在 SQL 侧经
//     COALESCE(...,'text')、在 Go 侧归一为 text，两边等价。
//
// 已知且**有意不改**的语义分歧（超出对拍域，实登记防「假绿」质疑）：
//   - Go default 臂（registered == requested || multimodal）在
//     (multimodal, embedding) 格判 true、SQL 兜底精确臂判 false；同格分歧
//     也出现在任意未知请求值 × multimodal 登记上。路由器到诊断的
//     requestModality 只能是 detect 值域（vision/audio/video/text）∪
//     {image, ""}（image 走专用臂）⇒ 这些格子不可达。若未来把 detect 或
//     诊断入口扩到产 "multimodal"/"embedding"，必须先统一两臂再扩本域。
//
// 解析器教训（第一版当场踩的）：多值列表 ('vision', 'audio', 'video') 里
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

	arms := parseModalityArms(sqlText)

	// 逐格对拍。requestedSet 第一个值 ""/text 走 SQL 的前两条全放行臂；
	// 其余走媒体臂或兜底精确臂。**请求侧**不含 embedding —— embedding
	// 不在诊断路径的值域上（见头部注释的分歧登记），不进对拍。
	requestedSet := []string{"", "text", "vision", "audio", "video", "image"}
	// ★ 登记侧含 embedding（2026-10-29 批判式审计修正「声明 > 事实」）：
	//   门头注释一直写着「登记侧 = CHECK 值域 text/vision/audio/video/
	//   multimodal/embedding」，而 registeredSet 里**独缺 embedding**
	//   ⇒ 声明 > 事实。⇒ 现在补齐，并加派生域断言把分母锚到事实源。
	//   （请求侧仍不含 embedding ⇒ 头部登记的 (multimodal, embedding) 分歧
	//   不在本对拍域内，不受影响。）
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

	for _, req := range requestedSet {
		// 路由器会发的媒体请求模态必须各有自己的臂（或显式落入兜底臂的
		// 精确语义）——缺臂时兜底臂会静默接管，过滤面就变了，这里点名。
		switch req {
		case "vision", "audio", "video", "image":
			if _, ok := arms[req]; !ok {
				t.Errorf("SQL 接受臂没有为请求模态 %q 解析出媒体臂", req)
			}
		}
		for _, reg := range registeredSet {
			goVerdict := ModalityAccepts(reg, req)
			sqlVerdict := sqlAccepts(arms, reg, req)
			if goVerdict != sqlVerdict {
				t.Errorf("ModalityAccepts(%q, %q) = %v，SQL 接受臂 = %v —— Go 判据与 candidateQuerySQLBase 漂移",
					reg, req, goVerdict, sqlVerdict)
			}
		}
	}
}

// TestModalityAccepts_LockstepArmsCount 锁媒体臂数量：当前判据恰好两条
// （vision/audio/video 联合臂 + image 专用臂）。多出的臂要么是新判据（必须
// 同步 ModalityAccepts 与上面的对拍域），要么是死代码。
func TestModalityAccepts_LockstepArmsCount(t *testing.T) {
	n := len(lockstepMediaArmRE.FindAllStringSubmatch(stripSQLComments(candidateQuerySQLBase()), -1))
	if n != 2 {
		t.Fatalf("媒体接受臂 %d 条（期望 2：vision/audio/video 联合臂 + image 臂）—— 新增/删除臂必须同步 ModalityAccepts 与对拍表", n)
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
func TestModalityAccepts_LockstepGateModalityListMatchesArms(t *testing.T) {
	base := stripSQLComments(candidateQuerySQLBase())
	arms := parseModalityArms(base)

	// 臂里的媒体模态（有专属臂的那些）。
	mediaInArms := map[string]bool{}
	for req := range arms {
		if req == "image" {
			continue // image 走专用臂，gate 不覆盖 image（既有设计，见函数注释）
		}
		mediaInArms[req] = true
	}
	if len(mediaInArms) == 0 {
		t.Fatalf("接受臂里没解析出任何媒体模态 —— 解析器或 SQL形态变了，本判据失效")
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
	gateMods := map[string]bool{}
	for _, l := range lists {
		for k, v := range l.mods {
			gateMods[k] = gateMods[k] || v
		}
		// 逐处判：这一处必须覆盖臂内所有媒体模态。
		for req := range mediaInArms {
			if !l.mods[req] {
				t.Errorf("verdict gate 的第 %d 处模态列表没覆盖请求模态 %q（该处列表=%v）⇒ 该模态的负向读级不会触发排除 ⇒ 实测「看不见」的模型仍会被路由到（静默的质量退化）",
					l.idx, req, l.mods)
			}
		}
	}
	// 抽样核对：生产 SQL 里确实含这个 gate（占位符替换没坏）。
	if !strings.Contains(stripSQLComments(candidateQuerySQL()), "model_modality_verification") {
		t.Fatalf("生产 SQL（candidateQuerySQL()）里没有 verdict gate 的痕迹 —— 占位符替换可能坏了，本判据失效")
	}

	_ = gateMods //逐档判定已在上方完成（gateMods 仅作汇总，供失败信息参考）
}

// TestModalityAccepts_LockstepMutationArms 是锁步门自身的变异臂：对 SQL 原文
// 做三类语义漂移，门必须当场报出「Go 判据与 SQL 裁决不一致」（红）——
// 证明对拍真的在比较两边的裁决，而不是两边都在恒真。
func TestModalityAccepts_LockstepMutationArms(t *testing.T) {
	base := stripSQLComments(candidateQuerySQLBase())
	// ★ 与主测试同一登记侧域（含 embedding）——两处域必须一致，否则「主测试
	//   的域」与「变异臂的域」分叉，某一域里的漂移就检不出。
	registeredSet := []string{"", "text", "vision", "audio", "video", "multimodal", "embedding"}

	expectDrift := func(name, mutatedSQL string) {
		t.Helper()
		arms := parseModalityArms(mutatedSQL)
		drift := 0
		for _, req := range []string{"vision", "audio", "video", "image"} {
			for _, reg := range registeredSet {
				if ModalityAccepts(reg, req) != sqlAccepts(arms, reg, req) {
					drift++
				}
			}
		}
		if drift == 0 {
			t.Fatalf("%s：变异后对拍零漂移 —— 门对这类语义漂移没有牙（§497：变异臂的靶点必须真实落在被锁判据上）", name)
		}
	}

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
}

// lockstepMediaArmRE 匹配一条媒体接受臂（剥注释后的可执行文本）：
//
//	$3 IN ('vision', 'audio', 'video') AND COALESCE(mc.modality, 'text') IN ($3, 'multimodal')
//	$3 = 'image' AND COALESCE(mc.modality, 'text') IN ('image', 'vision', 'multimodal')
//
// 组：1=请求模态列表（IN 形态），2=单值请求模态（= 形态），3=接受集列表
// （可含 $3 占位符 = 请求模态自身）。
var lockstepMediaArmRE = regexp.MustCompile(`\$3 (?:IN \(([^)]*)\)|= '([^']*)') AND COALESCE\(mc\.modality, 'text'\) IN \(([^)]*)\)`)

// parseModalityArms 把 SQL 文本里的媒体接受臂解析成 请求模态 → 登记模态集合。
// $3 在两个位置都表示「请求模态自身」，按臂内逐请求模态展开。
func parseModalityArms(sqlText string) map[string]map[string]bool {
	accepts := map[string]map[string]bool{}
	for _, m := range lockstepMediaArmRE.FindAllStringSubmatch(sqlText, -1) {
		reqs := []string{}
		if strings.TrimSpace(m[1]) != "" {
			reqs = append(reqs, strings.Split(m[1], ",")...)
		}
		if strings.TrimSpace(m[2]) != "" {
			reqs = append(reqs, m[2])
		}
		// 接受集先拆原始片段，$3 标记为占位符，按请求模态展开。
		accParts := strings.Split(m[3], ",")
		hasPlaceholder := false
		regs := []string{}
		for _, p := range accParts {
			p = strings.TrimSpace(p)
			if p == "$3" {
				hasPlaceholder = true
				continue
			}
			if v := strings.Trim(p, "'"); v != "" {
				regs = append(regs, strings.ToLower(v))
			}
		}
		for _, req := range reqs {
			req = strings.ToLower(strings.Trim(strings.TrimSpace(req), "'"))
			if req == "" {
				continue
			}
			accepts[req] = map[string]bool{}
			for _, r := range regs {
				accepts[req][r] = true
			}
			if hasPlaceholder {
				accepts[req][req] = true
			}
		}
	}
	return accepts
}

// sqlAccepts 用解析出的裁决表还原 SQL 侧对 (登记模态, 请求模态) 的裁决。
// 前两条臂（$3=” / $3='text'）全放行；媒体臂未覆盖的请求值落兜底精确臂
// COALESCE(mc.modality,'text') = $3；空登记模态经 COALESCE 归一为 text。
func sqlAccepts(arms map[string]map[string]bool, reg, req string) bool {
	if reg == "" {
		reg = "text"
	}
	switch req {
	case "":
		return true
	case "text":
		return true
	}
	if set, ok := arms[req]; ok {
		return set[reg]
	}
	return reg == req
}
