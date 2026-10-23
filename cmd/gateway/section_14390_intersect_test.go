//go:build !integration

package main

// §143.90 的门：★ 本轮是【跨源求交器落地轮】⇒ ⇒ 产出是【一个可重跑的求交脚本 + 一次级联订正】。
// ★★★★★★ 这个门要守的是【求交器的两个关键正确性点】+【订正不许被悄悄删掉】：
//
//	① ★★★★ 求交脚本存在
//	② ★★★★ 脚本有【自检入口】（★ 不依赖生产数据即可验证正确性）
//	③ ★★★★★★ 【v2 落空必须按 `body_size == 0` 过滤】—— ★★ 逐字判据：
//	    ★ 这是 §143.90.1 抓到的【量具级错误①】：★ 少了它 ⇒ ⇒ N_v2 会被高估 46%
//	④ ★★★★★★ 【求交必须用【多重集】而不是集合】—— ★★ 逐字判据：
//	    ★ 同一会话可多次进读链 ⇒ ⇒ 库端 calls 也是次数 ⇒ ⇒ 集合交会把重试多的会话低估
//	⑤ ★★★★ 脚本支持【两种服务侧日志形态】（★ journald / ★ jsonl）
//	⑥ ★★★★★ 【订正不许被删】：★ §143.90 段 + 三处订正横幅六 + LESSONS §284 + AGENTS R134
//
// ★ ★★ 纪律提醒：★ 本门是【登记门 + 工具形态门】，★ **不是**端到端门。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	section14390Runbook = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	section14390Lessons = "LESSONS.md"
	section14390Agents  = "AGENTS.md"
	section14390Script  = "scripts/audit/cross_source_intersect.py"
)

func repoRoot14390(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile14390(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot14390(t), rel))
	if err != nil {
		t.Fatalf("★ 读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

// code14390 返回一个文件的【可执行行】（★ 剥掉整行注释与行尾注释）。
func code14390(t *testing.T, rel string) string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(readRepoFile14390(t, rel), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if idx := strings.Index(line, "#"); idx >= 0 {
			trimmed = strings.TrimSpace(line[:idx])
		}
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return strings.Join(out, "\n")
}

// funcBody14390 取出某个函数的源码片段（★ 从函数声明到下一个顶层 `def ` 为止）。
func funcBody14390(t *testing.T, rel, sig string) string {
	t.Helper()
	lines := strings.Split(readRepoFile14390(t, rel), "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), sig) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("★ %s 里找不到函数 %s", rel, sig)
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "def ") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

// codeFuncBody14390 取函数体的【可执行行】（★ 剥注释）。
//
// ★ ★★ 为什么要剥：★ 判据③/④ 是【逐字】判据 ⇒ ⇒ 若不剥注释，
// ★ ★★ 只在注释里写出那行也会被判成「有过滤 / 是多重集」。
func codeFuncBody14390(t *testing.T, rel, sig string) string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(funcBody14390(t, rel, sig), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if idx := strings.Index(line, "#"); idx >= 0 {
			trimmed = strings.TrimSpace(line[:idx])
		}
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return strings.Join(out, "\n")
}

// TestSection14390ScriptExistsAndHasSelfCheck（★ 判据①②）
func TestSection14390ScriptExistsAndHasSelfCheck(t *testing.T) {
	code := code14390(t, section14390Script)
	// ★★★ 逐字钉住【argparse 的那条】⇒ ⇒ 不能只查 `--self-check` 这个串：
	// ★ ★★ Python 的【docstring 不是 # 注释】⇒ ⇒ 本文件的剥注释器剥不掉它
	// ★ ⇒ ⇒ 只查字符串 ⇒ ⇒ 「只改了 docstring 里的写法」也会【假绿】（★ 变异 M3 的实测）。
	if !strings.Contains(code, `add_argument("--self-check", action="store_true")`) {
		t.Errorf("★ %s 里没有逐字的 `add_argument(\"--self-check\", action=\"store_true\")` —— "+
			"★ ★★ 求交器的正确性必须【不依赖生产数据】就能验证（★ 合成夹具）"+
			"★ ⇒ ⇒ ★★ 否则每次跑都要先有生产日志才能知道尺子对不对"+
			"★ ⇒ ⇒ ★★ 判据必须钉【参数注册那一行】、★ 不能只钉字符串（★ docstring 剥不掉）",
			section14390Script)
	}
	if !strings.Contains(code, "def self_check(") {
		t.Errorf("★ %s 里没有 `def self_check(` —— ★ 同上（★ 自检入口）", section14390Script)
	}
}

// TestSection14390V2MissIsFilteredByBodySize（★ 判据③）—— ★★ 量具级错误①的守卫
//
// ★★★★★★ 【v2 落空必须按 `body_size == 0` 过滤】—— ★★★
// ★ §143.90.1 实测：★ 「v2 cache loaded successfully」里 body_size ≠ 0 的那些
// ★ ⇒ ⇒ v2 侧【有历史】⇒ ⇒ 压根不回落 V1 ⇒ ⇒ 【不是】落空事件。
// ★ ⇒ ⇒ 少了这个过滤 ⇒ ⇒ N_v2 被高估 46% ⇒ ⇒ 前四轮的数字全部带偏。
func TestSection14390V2MissIsFilteredByBodySize(t *testing.T) {
	body := codeFuncBody14390(t, section14390Script, "def collect(")
	needle := "if require_zero_body and body_size not in (0, None):"
	if !strings.Contains(body, needle) {
		t.Errorf("★ %s 的 `collect` 里没有逐字的 `%s` —— "+
			"★ ★★★★ 「v2 落空」必须按 `body_size == 0` 过滤（★ §143.90.1 的实测）"+
			"★ ⇒ ⇒ 少了它 ⇒ ⇒ N_v2 高估 46%% ⇒ ⇒ 前四轮的数字全错（★ 本轮的真实教训）",
			section14390Script, needle)
	}
}

// TestSection14390IntersectIsMultiset（★ 判据④）—— ★★ 第二个关键正确性点
//
// ★★★★★★ 【求交必须用【多重集】而不是集合】—— ★★★
// ★ 同一会话可能多次进入读链（★ 重试 / 多轮请求）⇒ ⇒ 库端 calls 也是【次数】。
// ★ ⇒ ⇒ 用集合交 ⇒ ⇒ 重试多的会话被【低估】⇒ ⇒ s 偏小 ⇒ ⇒ 收益被低估。
func TestSection14390IntersectIsMultiset(t *testing.T) {
	body := codeFuncBody14390(t, section14390Script, "def multisect(")
	needle := "return a & b"
	if !strings.Contains(body, needle) {
		t.Errorf("★ %s 的 `multiset` 里没有逐字的 `%s` —— "+
			"★ ★★★★ 求交必须用【多重集】（★ Counter 的 & 就是 min-count 交）"+
			"★ ⇒ ⇒ 换成集合交 ⇒ ⇒ 重试多的会话被低估 ⇒ ⇒ s 偏小",
			section14390Script, needle)
	}
	if !strings.Contains(code14390(t, section14390Script), "from collections import Counter") {
		t.Errorf("★ %s 没有 `from collections import Counter` —— "+
			"★ ★★ 多重集的实现依赖 Counter（★ 集合交与多重集交必须可区分）",
			section14390Script)
	}
}

// TestSection14390SupportsBothLogFormats（★ 判据⑤）
func TestSection14390SupportsBothLogFormats(t *testing.T) {
	code := code14390(t, section14390Script)
	for _, needle := range []string{"journald", "jsonl"} {
		if !strings.Contains(code, needle) {
			t.Errorf("★ %s 里没有 %q —— "+
				"★ ★★ 154 走 journald（★ 且行是「头 + JSON payload」的混合形态）、"+
				"★ 245 走 jsonl ⇒ ⇒ ★★ 两种形态都必须支持（★ §143.90.5 的第二个量具故障）",
				section14390Script, needle)
		}
	}
}

// TestSection14390CorrectionsAreRegistered（★ 判据⑥）
//
// ★ ★★ 订正必须【就地留在被订正的段上】⇒ ⇒ 否则读旧段的人看不到它已被改。
func TestSection14390CorrectionsAreRegistered(t *testing.T) {
	runbook := readRepoFile14390(t, section14390Runbook)

	i := strings.Index(runbook, "### §143.90")
	if i < 0 {
		t.Fatal("★ runbook 里找不到 §143.90 段 —— ★ 本轮的级联订正必须落在仓内（★ 过程可查）")
	}
	seg := runbook[i:]
	for _, needle := range []string{
		"220",       // ★ 154 真·v2 落空
		"286",       // ★ 真·v2 落空合计
		"383",       // ★ 订正后的「非 v2 来源的读链进入」下界
		"97.6",      // ★ 订正后的收益占比上界
		"body_size", // ★ 口径过滤的判据字段
	} {
		if !strings.Contains(seg, needle) {
			t.Errorf("★ §143.90 段里没有 %q —— "+
				"★ ★★ 本轮的订正是【数字级】的 ⇒ ⇒ 新数字必须登记"+
				"★ ⇒ ⇒ ★★ 少了它 ⇒ ⇒ 旧数字（61%% / 376 / 180 / 473）就会继续被引用",
				needle)
		}
	}

	// ★★ 三处订正横幅必须都在（★ 原文保留 + 就地标注）。
	for _, sec := range []string{"### §143.84", "### §143.87", "### §143.88"} {
		a := strings.Index(runbook, sec)
		if a < 0 {
			t.Fatalf("★ runbook 里找不到 %s", sec)
		}
		b := strings.Index(runbook[a+1:], "\n### ")
		if b < 0 {
			b = len(runbook) - a
		}
		part := runbook[a : a+b]
		if !strings.Contains(part, "订正横幅六（2026-10-23 §143.90 轮）") {
			t.Errorf("★ %s 段里没有「订正横幅六（2026-10-23 §143.90 轮）」 —— "+
				"★ ★★ 订正必须【就地挂在被订正的段上】（★ 原文保留）"+
				"★ ⇒ ⇒ ★★ 否则读旧段的人看不到它已被改",
				sec)
		}
	}

	if !strings.Contains(readRepoFile14390(t, section14390Lessons), "## §284") {
		t.Error("★ LESSONS.md 里没有 §284 —— " +
			"★ ⇒ ⇒ 「事件计数必须按语义字段过滤」这条通用式必须落进去")
	}
	if !strings.Contains(readRepoFile14390(t, section14390Agents), "R134") {
		t.Error("★ AGENTS.md 里没有 R134 —— " +
			"★ ⇒ ⇒ 本轮「前提被证实了但输入量错了、结论照样要改」必须落在交接段")
	}
}
