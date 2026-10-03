package sqlguard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestOrphanDetectorCapabilitiesAreRegistered —— 227 号新增。
//
// 缺陷本体（objective 点名的「注入检测」真实能力缺口）：
//
// 本仓有**两套**提示词注入检测实现：
//
//	A. `domains/promptinjection`（父包，31KB）—— **现役**
//	   `cmd/gateway/main_pipeline.go:1384` 与
//	   `domains/security/plugins/prompt_injection_enhanced.go:25` 装的都是它。
//	   `detector.go:176-230` 有 6 层：基础规则 / 高级规则 / 启发式 / Canary /
//	   向量相似度 / LLM 智能检测（**六层都已核实存在**，不是注释吹的）。
//
//	B. `domains/promptinjection/enhanced`（808 行）—— **零生产引用**（115 号已登记为 B 类真孤儿）
//	   且**顶层标识"enhanced"极强误导**：现役那个文件本身就叫
//	   `prompt_injection_enhanced.go`，用的却是**父包**。
//
// **本门要钉住的是能力差，不是"谁在用"**：
// `enhanced` 的 `heuristicDetect`（`detector.go:356-375`）有一段
// **父包完全没有**的能力：解码编码混淆内容并**递归复检**：
//
//	for _, detector := range d.encodingDetectors {      // Base64 / Unicode / ROT13
//	    if isEncoded, decoded, confidence := detector.Detect(content); isEncoded {
//	        …
//	        decodedScore, decodedThreats := d.fastFilter(decoded)   // ← 递归
//
//	父包对 `Base64|base64|unicode|Unicode` 的检索结果为 **0 命中**
// ⇒ **攻击者把 payload 做 base64 编码时，现役检测器完全无感。**
//
// ⚠️ **诚实边界（写进门里，不只写在报告里）**：
// ① 本轮**未起真进程、未连 PG、未跑通一条真实攻击样本**；
//    「父包对编码无感」= 静态检索（`git grep`）的结论，不是运行时观测。
// ② `enhanced` 整体零引用 ⇒ 它**不能**直接上线（它没有 policy / 租户 / 决策 / 内容替换，
//    与 governance 链路不兼容），所以「缺口」是真的，「修法 = 接线 enhanced」是**错的**。
// ③ 正确的修法方向是**把编码解码复检这一段能力搬进父包**，而不是接线整个 enhanced。
// ⇒ 故本门只登记**能力差存在**这一件事实，**不给修法处方**。

// encodingDetectionCallSite 是「编码检测 + 递归复检」这段能力的调用点。
type encodingDetectionCallSite struct {
	where     string // file:line
	recursion string // 递归复检那一行的形态特征（用来证明它不是"只解码不复检"）
}

var encodingDetectionSites = []encodingDetectionCallSite{
	{
		where:     "domains/promptinjection/enhanced/detector.go:371",
		recursion: "d.fastFilter(decoded)",
	},
}

// TestOrphanDetectorCapabilitiesAreRegistered —— 能力差登记表的三条职责。
func TestOrphanDetectorCapabilitiesAreRegistered(t *testing.T) {
	root := repoRootForTest(t)

	if len(encodingDetectionSites) < 1 {
		t.Fatalf("能力差登记表为空（下限 1）；按 227 号立论，至少存在 1 处「解码后递归复检」。")
	}

	for _, s := range encodingDetectionSites {
		parts := strings.SplitN(s.where, ":", 2)
		if len(parts) != 2 {
			t.Errorf("锚点格式非法：%q", s.where)
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, parts[0]))
		if err != nil {
			t.Errorf("锚点 %s 读不到：%v", s.where, err)
			continue
		}
		lines := strings.Split(string(b), "\n")
		ln := 0
		for _, c := range parts[1] {
			if c < '0' || c > '9' {
				ln = 0
				break
			}
			ln = ln*10 + int(c-'0')
		}
		if ln < 1 || ln > len(lines) {
			t.Errorf("锚点行号越界：%s（文件共 %d 行）", s.where, len(lines))
			continue
		}
		src := lines[ln-1]

		// (1) 登记的那一行必须仍含递归调用 —— 否则"能力"已经不在这了。
		if !strings.Contains(src, s.recursion) {
			t.Errorf("锚点 %s 不再含 %q：\n  实际: %s\n"+
				"⇒ 编码递归复检已被移除或改写，登记表需要重新确认"+
				"（这不是「父包已补上该能力」）。", s.where, s.recursion, strings.TrimSpace(src))
			continue
		}

		// (2) 权威侧的对照：父包必须**确实没有**编码解码能力。
		//     ⚠️ 这一条是**否定式断言**（"检索不到"），它天然脆弱 ——
		//     §157 的老陷阱：0 命中时人会直接采信。
		//     因此这里**只用于生成证据并打印**，**不作为失败条件**；
		//     真正的失败条件是上面的 (1)。若哪天父包补上了这条能力，
		//     本门会打印一条"缺口可能已消失"，由下一轮人工确认。
		parentFile := filepath.Join(root, "domains/promptinjection/detector.go")
		pb, err := os.ReadFile(parentFile)
		if err != nil {
			t.Errorf("父包 %s 读不到：%v", "domains/promptinjection/detector.go", err)
			continue
		}
		re := regexp.MustCompile(`(?i)base64|unicode|rot13`)
		if re.Match(pb) {
			t.Logf("父包 %s 现在出现了编码相关标识（base64/unicode/rot13）⇒ "+
				"227 号登记的「编码绕过能力差」可能已消失，请人工确认后更新登记表。",
				"domains/promptinjection/detector.go")
		} else {
			t.Logf("【已登记能力差，待裁决 93】现役父包（%s）对 base64/unicode/rot13 **零命中**；"+
				"而 %s 有「解码 + 递归复检」。\n"+
				"  ⇒ 编码混淆的注入 payload 在现役链路上不被检测。\n"+
				"  ⚠️ 本条是**否定式断言**（检索 0 命中），仅作证据打印，不作为失败条件。",
				"domains/promptinjection/detector.go", s.where)
		}
	}
}

// TestEnhancedPackageStaysUnwired —— 钉住 115 号的定性：**真孤儿，不是"名字对上了就在用"**。
//
// 115 号已经把它登记为 B 类（零生产引用），但本轮实测发现一个**新的、更隐蔽**的形态：
// 现役的那个文件**本身就叫 `prompt_injection_enhanced.go`**，
// 而它 import 的是**父包** ⇒ **"文件名 enhanced" 与 "用了 enhanced 包" 完全无关**。
//
// ⇒ 本门把这条钉成机械断言：那个文件里**不得**出现 enhanced 包的 import 路径。
//
//	若将来有人真把它接上，本门会响 —— 那时应该**更新登记表**而不是直接删断言。
func TestEnhancedPackageStaysUnwired(t *testing.T) {
	root := repoRootForTest(t)

	const misleadingFile = "domains/security/plugins/prompt_injection_enhanced.go"
	b, err := os.ReadFile(filepath.Join(root, misleadingFile))
	if err != nil {
		t.Fatalf("读 %s：%v", misleadingFile, err)
	}
	s := string(b)

	// 该文件必须 import 父包（这是它现在的工作方式）。
	if !strings.Contains(s, `"github.com/kaixuan/llm-gateway-go/domains/promptinjection"`) {
		t.Errorf("%s 不再 import 父包。\n"+
			"⇒ 现役的注入检测链路变了。115 号与 227 号的结论需要重新确认"+
			"（不是 enhanced 被接线了）。", misleadingFile)
	}
	// ⚠️ 反向断言：文件名带 enhanced、但**不得** import enhanced 包。
	//    这是 115 号「命名不能当证据」那条的机械形式。
	if strings.Contains(s, "domains/promptinjection/enhanced") {
		t.Logf("【登记表需要更新】%s 现在 import 了 enhanced 包 ⇒ 227 号记的"+
			"「enhanced 零引用」已不成立，请核实后更新 115 号与本门。", misleadingFile)
	} else {
		t.Logf("%s 文件名带 enhanced，但 import 的是**父包** ⇒ "+
			"「文件名/命名 ≠ 实际在用」这一形态仍然成立（115 号已登记，本门钉住它）。",
			misleadingFile)
	}

	// 覆盖面：enhanced 包必须**真的零生产引用**（排除它自己的测试）。
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "node_modules" || info.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		if strings.Contains(p, filepath.FromSlash("domains/promptinjection/enhanced")) {
			return nil // 它自己不算
		}
		cb, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		if strings.Contains(string(cb), "promptinjection/enhanced") {
			rel, _ := filepath.Rel(root, p)
			t.Errorf("enhanced 包现在有生产引用方：%s\n"+
				"⇒ 115 号的「B 类真孤儿」定性已过期，请核实后更新登记表。",
				filepath.ToSlash(rel))
		}
		return nil
	})
}
