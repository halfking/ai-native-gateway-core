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
// ★ 判据形态是【逐处】不是聚合（LESSONS §527 通用式四：聚合会把局部的漂移
// 洗掉）：$5 块的五条臂逐条解析，每条臂独立对拍，失败信息点名是哪条臂、
// 哪个 (登记, 请求) 格漂了——把任何一条臂的接受集改窄都会当场报红，不会被
// 其它臂的相同内容掩盖。
//
// $5 的生产值域（对拍域的事实依据）：findModelAlternatives 只有两个调用点——
// handler.go 零候选臂（requestModality ← resolveCandidatesForRequest →
// detectRequestModality）与诊断臂（mismatch.RequestedModality ⊆ detect 值域）；
// OmniFree 的 modality 同样来自 detectRequestModality（handler_autocombo.go）。
// ⇒ 值域 = {""} ∪ {text, vision, audio, video}。
//
// 对拍域比生产值域宽：两份 SQL 抄本都有 multimodal / image 显式臂，且三份
// 表达在这两个请求值上逐格一致 ⇒ 纳入对拍（这是防御性冗余，漂移必须当场红）。
//
// 已知且【有意不改】的语义分歧（超出对拍域，登记防「假绿」质疑）：
//   - 请求值 embedding：①开世界（兜底精确臂 COALESCE = $3，embeddings 路由
//     依赖它）对 embedding 登记判 true；③default 臂对 (multimodal, embedding)
//     判 true；②闭世界（无兜底臂）对一切登记判 false。生产上 $5 与诊断入口
//     都收不到 embedding（embeddings 路径不产建议列表、不接诊断），分歧不可达。
//     若未来把 detect / 诊断 / 建议入口扩到产 "embedding"，必须先统一三份
//     表达再扩本域。
//
// 结构差异登记：②是【闭世界】过滤——未知请求值让整个 AND 不成立 ⇒ 建议
// 列表为空；①是【开世界】——兜底精确臂做精确匹配。已知请求值上两者等价；
// TestAlternativesSQL_ModalityArmsShape 钉住②的「无兜底臂」形状，若未来加了
// 兜底臂，必须回来更新本登记与上面的分歧登记。
func TestAlternativesSQL_LockstepWithModalityAccepts(t *testing.T) {
	sqlText := alternativesSQL

	// 全放行臂（$5='' / $5='text'）没有 COALESCE 接受集、不走下面的臂解析，
	// 存在性用锚点单独锁死：删除或改名 ⇒ 这里红，而不是静默改变过滤面。
	for _, frag := range []string{"$5 = ''", "OR $5 = 'text'"} {
		if !strings.Contains(sqlText, frag) {
			t.Fatalf("alternativesSQL 模态过滤块缺全放行臂锚点 %q —— 判据结构变了，本测试的对拍表必须跟着重写", frag)
		}
	}

	arms := parseAltModalityArms(sqlText)

	requested := []string{"", "text", "vision", "audio", "video", "multimodal", "image"}

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
			t.Errorf("登记模态值域里出现了本对拍域尚未登记的值 %q（事实源=models_canonical.modality 的 CHECK 约束）—— ★ 新登记值必须纳入对拍域并复核 Go 判据（provider.ModalityAccepts）与两份 SQL 抄本", reg)
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

	// 每个媒体请求值必须各有自己的臂：②是闭世界，缺臂时该请求值的建议列表
	// 会被静默清空（Go 判据仍放行）——这里点名是哪个请求值缺臂。
	for _, req := range requested {
		switch req {
		case "vision", "audio", "video", "multimodal", "image":
			found := false
			for _, a := range arms {
				if a.covers(req) {
					found = true
				}
			}
			if !found {
				t.Errorf("alternativesSQL 模态过滤块没有为请求模态 %q 解析出过滤臂 —— 闭世界语义下该请求值的建议列表会静默变空", req)
			}
		}
	}

	// 逐处比对（主判据）：每一条臂独立对拍，失败信息点名臂原文与漂移格。
	for _, arm := range arms {
		for _, req := range arm.reqs {
			for _, reg := range registered {
				regN := reg
				if regN == "" {
					regN = "text"
				}
				sqlV := arm.acceptsFor(req)[regN]
				goV := provider.ModalityAccepts(reg, req)
				if sqlV != goV {
					t.Errorf("alternativesSQL 模态臂 [%s] 对 (登记=%q, 请求=%q) 判 %v，Go 判据 ModalityAccepts 判 %v —— 第二份 SQL 抄本与 Go 判据漂移",
						strings.TrimSpace(arm.raw), reg, req, sqlV, goV)
				}
			}
		}
	}

	// 逐格兜底网：不依赖臂解析的完整 (登记, 请求) 扫描，兜住「臂解析遗漏 /
	// 全放行臂语义变化」这两类解析器自身的盲点。
	for _, req := range requested {
		for _, reg := range registered {
			_, sqlV := altSQLAccepts(arms, reg, req)
			if goV := provider.ModalityAccepts(reg, req); sqlV != goV {
				t.Errorf("alternativesSQL 对 (登记=%q, 请求=%q) 判 %v，Go 判据 ModalityAccepts 判 %v —— 过滤块整体语义漂移",
					reg, req, sqlV, goV)
			}
		}
	}
}

// TestAlternativesSQL_ModalityArmsShape 钉住 $5 过滤块的结构形状：
// 恰好三条带接受集的臂（vision/audio/video 联合臂 + multimodal 专属臂 +
// image 臂），且没有兜底精确臂（闭世界登记，见测试头）。
// 注释里也出现过形似臂的文本（modality 判据注释），本正则要求 $5 前缀 +
// COALESCE 接受集的完整形状，注释行匹配不上；「恰好 3 条」这条计数本身就是
// 反诱饵判据——未来注释或新增 SQL 片段若真的形似到能被解析，这里先红。
func TestAlternativesSQL_ModalityArmsShape(t *testing.T) {
	n := len(altModalityArmRE.FindAllStringSubmatch(alternativesSQL, -1))
	if n != 3 {
		t.Fatalf("alternativesSQL 模态过滤臂 %d 条（期望 3：vision/audio/video 联合臂 + multimodal 臂 + image 臂）—— 新增/删除臂必须同步 provider.ModalityAccepts 并更新对拍域", n)
	}
	if strings.Contains(alternativesSQL, "COALESCE(mc.modality,'text') = $5") ||
		strings.Contains(alternativesSQL, "COALESCE(mc.modality, 'text') = $5") {
		t.Fatalf("alternativesSQL 出现兜底精确臂（COALESCE = $5）—— 闭世界形状变了：两份 SQL 抄本在未知请求值上将从分歧变等价，测试头与收口轮 §8 的分歧登记必须重新核对")
	}
}

// TestAlternativesSQL_LockstepMutationArms 是本锁步门自身的变异臂：对 $5 块
// 原文做四类语义漂移 + 一条负控，证明对拍真的在比较两边的裁决——
// §527 通用式二：变异臂的靶点必须真实落在被锁判据上。
func TestAlternativesSQL_LockstepMutationArms(t *testing.T) {
	registered := []string{"", "text", "vision", "audio", "video", "multimodal"}

	driftCells := func(mutatedSQL string) []string {
		arms := parseAltModalityArms(mutatedSQL)
		var cells []string
		for _, req := range []string{"vision", "audio", "video", "multimodal", "image"} {
			for _, reg := range registered {
				_, sqlV := altSQLAccepts(arms, reg, req)
				if goV := provider.ModalityAccepts(reg, req); sqlV != goV {
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
	m0 := strings.Replace(alternativesSQL, "$5 = ''", "$5 =  ''", 1)
	if m0 == alternativesSQL {
		t.Fatal("M0 负控的替换锚没命中")
	}
	if cells := driftCells(m0); len(cells) != 0 {
		t.Fatalf("M0 负控：空白改动报出漂移 %v —— 判据把非语义改动当漂移，过宽", cells)
	}

	// M1：联合臂接受集去掉 multimodal ⇒ (multimodal, vision/audio/video) 漂移。
	m1 := strings.Replace(alternativesSQL,
		"COALESCE(mc.modality,'text') IN ($5, 'multimodal')",
		"COALESCE(mc.modality,'text') IN ($5)", 1)
	if m1 == alternativesSQL {
		t.Fatal("M1 变异的替换锚没命中")
	}
	expectDrift("M1 联合臂接受集去 multimodal", m1)

	// M2：multimodal 专属臂接受集收窄成 text ⇒ (text/multimodal, multimodal) 漂移。
	m2 := strings.Replace(alternativesSQL,
		"OR ($5 = 'multimodal' AND COALESCE(mc.modality,'text') = 'multimodal')",
		"OR ($5 = 'multimodal' AND COALESCE(mc.modality,'text') = 'text')", 1)
	if m2 == alternativesSQL {
		t.Fatal("M2 变异的替换锚没命中")
	}
	expectDrift("M2 multimodal 臂接受集收窄", m2)

	// M3：image 臂整条删除 ⇒ image 请求落闭世界空集 ⇒ (vision/multimodal, image) 漂移。
	m3 := strings.Replace(alternativesSQL,
		"OR ($5 = 'image' AND COALESCE(mc.modality,'text') IN ('image','vision','multimodal'))",
		"", 1)
	if m3 == alternativesSQL {
		t.Fatal("M3 变异的删除锚没命中")
	}
	expectDrift("M3 image 臂删除", m3)

	// M4：联合臂请求侧去掉 video ⇒ video 请求落闭世界空集 ⇒ (video/multimodal, video) 漂移。
	m4 := strings.Replace(alternativesSQL,
		"$5 IN ('vision','audio','video')",
		"$5 IN ('vision','audio')", 1)
	if m4 == alternativesSQL {
		t.Fatal("M4 变异的替换锚没命中")
	}
	expectDrift("M4 联合臂请求侧去 video", m4)
}

// ── detect 值域闭合（★ 本轮批判式审计补的洞）─────────────────────────────
//
// ★★ 洞是什么：对拍域是**枚举出来的**，而 $5 的生产值域来自
//
//	detectRequestModality（domains/streaming/modality_detect.go）。
//	alternativesSQL 的 $5 块是**闭世界**：请求值若落在三条臂之外 ⇒ 整个
//	AND 不成立 ⇒ **建议列表静默变空**（不是 4xx、不是日志）。
//	而 Go 判据 ModalityAccepts 的 default 臂对未知请求值是
//	`registered == requested || registered == "multimodal"` ⇒ 对
//	(multimodal, 未知值) 判 **true**。
//	⇒⇒ 两边在**域外**语义相反：域内一致（现有门锁着）≠ 域外一致。
//	⇒⇒ 只要 detect 将来扩一个值域（例如为「文档/PDF 输入」新增 "document"），
//	现有三道锁步门**全部照样绿**，而生产上该模态的建议列表会**整条消失**。
//	⇒⇒ 「域外不可达」此前只是一句**注释里的登记**（本文件测试头）⇒
//	登记不是门 ⇒ 必须把它变成**可执行判据**。
//
// 两条互补判据：
//
//	① 行为判据：真实调用 detectRequestModality（不复制它的逻辑）跑一组
//	   代表性 body，断言产出值落在「alternativesSQL 有臂的请求值 ∪ 全放行值」
//	   之内 ⇒ detect 产出的一切都被 SQL 侧覆盖。
//	② 穷举判据：detect 的**返回常量集合**（源码里全部 `return modalityX`）
//	   必须 ⊆ 允许集 ⇒ detect 新增一种返回路径（哪怕代表性 body 覆盖不到）
//	   也会当场红。①漏「形态」、②漏「路径」，两条合起来才是闭合。
func TestAlternativesSQL_DetectModalityDomainClosed(t *testing.T) {
	arms := parseAltModalityArms(alternativesSQL)

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
	for _, a := range arms {
		for _, r := range a.reqs {
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
			t.Errorf("detectRequestModality 可能产出请求模态 %q（%s）⇒ alternativesSQL 的 $5 过滤块是【闭世界】（无对应臂）⇒ 该模态的建议列表会**静默变空**（无 4xx、无日志）⇒ ★ 要么给 $5 块补一条臂，要么让 detect 别产出它 ⇒ ★（Go 判据 ModalityAccepts 的 default 臂在域外会判 true，两边语义相反）", val, name)
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

// altModalityArmRE 匹配一条带接受集的模态过滤臂（alternativesSQL $5 块的
// 原文形态）：
//
//	OR ($5 IN ('vision','audio','video') AND COALESCE(mc.modality,'text') IN ($5, 'multimodal'))
//	OR ($5 = 'multimodal' AND COALESCE(mc.modality,'text') = 'multimodal')
//	OR ($5 = 'image' AND COALESCE(mc.modality,'text') IN ('image','vision','multimodal'))
//
// 组：1=请求侧 IN 列表，2=请求侧单值，3=接受侧 IN 列表，4=接受侧单值。
// COALESCE 逗号后的空格两份 SQL 抄本不一致（alternativesSQL 无空格、
// candidateQuerySQLBase 有空格），用 \s* 兼容两种写法。
var altModalityArmRE = regexp.MustCompile(`\$5 (?:IN \(([^)]*)\)|= '([^']*)') AND COALESCE\(mc\.modality,\s*'text'\) (?:IN \(([^)]*)\)|= '([^']*)')`)

// altModalityArm 是解析出的一条模态过滤臂。
type altModalityArm struct {
	raw     string          // 原文片段（失败信息点名用）
	reqs    []string        // 请求侧覆盖的请求模态（已归一）
	accepts map[string]bool // 接受侧字面量登记模态集合（已归一）
	// hasPlaceholder：接受集里的 $5 占位符 = 「请求模态自身」。
	// 不展开它的话，联合臂会对每个 (登记, 请求) 格假报漂移
	// （provider 锁步门第一版当场踩过的坑，见其测试头注释）。
	hasPlaceholder bool
}

func (a altModalityArm) covers(req string) bool {
	for _, r := range a.reqs {
		if r == req {
			return true
		}
	}
	return false
}

// acceptsFor 返回这条臂对请求值 req 的接受集，$5 占位符展开成 req 自身。
func (a altModalityArm) acceptsFor(req string) map[string]bool {
	s := make(map[string]bool, len(a.accepts)+1)
	for k := range a.accepts {
		s[k] = true
	}
	if a.hasPlaceholder {
		s[req] = true
	}
	return s
}

// parseAltModalityArms 把 $5 过滤块的臂解析成结构化判据。
// 全放行臂（$5=” / $5='text'）没有 COALESCE 接受集，不进本解析——
// 它们的存在性由主测试的锚点钉住，语义在 altSQLAccepts 里体现。
func parseAltModalityArms(sqlText string) []altModalityArm {
	var arms []altModalityArm
	for _, m := range altModalityArmRE.FindAllStringSubmatch(sqlText, -1) {
		arm := altModalityArm{raw: m[0], accepts: map[string]bool{}}
		if strings.TrimSpace(m[1]) != "" {
			arm.reqs = append(arm.reqs, strings.Split(m[1], ",")...)
		}
		if strings.TrimSpace(m[2]) != "" {
			arm.reqs = append(arm.reqs, m[2])
		}
		var accParts []string
		if strings.TrimSpace(m[3]) != "" {
			accParts = strings.Split(m[3], ",")
		} else if strings.TrimSpace(m[4]) != "" {
			accParts = []string{m[4]}
		}
		for _, p := range accParts {
			p = strings.TrimSpace(p)
			if p == "$5" {
				arm.hasPlaceholder = true
				continue
			}
			if v := strings.ToLower(strings.Trim(p, "'")); v != "" {
				arm.accepts[v] = true
			}
		}
		for i, req := range arm.reqs {
			req = strings.ToLower(strings.Trim(strings.TrimSpace(req), "'"))
			arm.reqs[i] = req
		}
		arms = append(arms, arm)
	}
	return arms
}

// altSQLAccepts 用解析出的臂还原 alternativesSQL 对 (登记模态, 请求模态) 的
// 裁决，返回 (命中的臂下标, 裁决)。前两条全放行臂的存在性由主测试锚点钉住，
// 这里按其语义放行；媒体臂未覆盖的请求值：闭世界，无兜底臂 ⇒ false。
func altSQLAccepts(arms []altModalityArm, reg, req string) (int, bool) {
	if reg == "" {
		reg = "text"
	}
	switch req {
	case "", "text":
		return -1, true
	}
	for i, a := range arms {
		if a.covers(req) {
			return i, a.acceptsFor(req)[reg]
		}
	}
	return -1, false
}
