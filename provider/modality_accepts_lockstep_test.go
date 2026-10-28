package provider

import (
	"regexp"
	"strings"
	"testing"
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
	// 其余走媒体臂或兜底精确臂。embedding 不在诊断路径的值域上
	// （见头部注释的分歧登记），不进对拍。
	requestedSet := []string{"", "text", "vision", "audio", "video", "image"}
	registeredSet := []string{"", "text", "vision", "audio", "video", "multimodal"}

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

// TestModalityAccepts_LockstepMutationArms 是锁步门自身的变异臂：对 SQL 原文
// 做三类语义漂移，门必须当场报出「Go 判据与 SQL 裁决不一致」（红）——
// 证明对拍真的在比较两边的裁决，而不是两边都在恒真。
func TestModalityAccepts_LockstepMutationArms(t *testing.T) {
	base := stripSQLComments(candidateQuerySQLBase())
	registeredSet := []string{"", "text", "vision", "audio", "video", "multimodal"}

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
// 前两条臂（$3='' / $3='text'）全放行；媒体臂未覆盖的请求值落兜底精确臂
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
