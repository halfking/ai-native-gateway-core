package ursm_test

// magpie_plan_currency_test.go —— ★★ **计划书时效性门**（2026-10-24 §3.18）。
//
// # 这道门补的是哪块缺口
//
// 本次的输入文档是
//
//	docs/research/2026-10-24-magpie-v0.1.1367-能力刷新与网关流程优化（含插件扩展点专题）.md
//
// ★★ 而它对**本仓现状**的陈述（§三 / §5.1）是对**改造前**仓库的快照
// ⇒ ⇒ ★ **本轮改造恰恰改变了它描述的那些事实**（16 条断言里 4 条**结论反转**）
// ⇒ ⇒ ★ 谁在下一轮拿它当「现状」读，就会按**已经过时**的事实做设计。
//
// # ★ 为什么这件事需要一道门（而不是「写个文档就算」）
//
// ★★ 「加一条过期横幅」是**一次性动作** ⇒ ★ 半年后有人清理文档，
// 横幅被删 ⇒ ⇒ **计划书又变回「看起来是现状」** ⇒ ⇒ 而**没有人会再去看审计档**
// ⇒ ⇒ ★★ 所以这件事必须**变成可执行判定**：★ **横幅被删 ⇒ 门红**。
//
// # 五条判据
//
//	C1 ★ **横幅必须还在**：计划书的 5 个锚点位置各须有一份「现状陈述已过期」横幅
//	   （① 文档顶部 ② §3.1 ③ §3.5 ④ §5.1 ⑤ §5.3）。
//	C2 ★ **横幅不许是空壳**：横幅正文里必须含至少一个 `file:line` 形态的证据
//	   ⇒ ⇒ 写成「（已过期）」三个字 ⇒ 红（★ 没有证据的横幅 = 没人能核对的断言）。
//	C3 ★ **认证表不许腐化**：审计档 §3.18 的认证表必须存在、行数达标、
//	   且每行都带 `file:line` ⇒ ⇒ 认证表被清空 ⇒ 红。
//	C4 ★★ **「结论反转」的判定必须与【当前事实】一致** —— ★★ 这是本门最要紧的一条：
//	   计划书说 `executor_hooks`「判死」、说 `internal/phasepolicy`（集中失败策略表）
//	   与 `internal/requestobserver`（统一观察者）「已存在」
//	   ⇒ ⇒ 若有人把它们改回去（`Production: true` / 删文件）⇒
//	   ⇒ ★ **横幅就变成假的** ⇒ ⇒ 门必须红 ⇒ ⇒ ★ 逼着人**同时更新横幅**。
//	C5 ★ **量具自检**：本门自己的「横幅检测」必须**有牙**
//	   ⇒ 喂合成的「有标记但无证据」样本必须判「不合格」⇒ ⇒ 防止 C1/C2 恒绿。
//
// # ★ 为什么「锚点」按**小节**而不是按行号
//
// ⇒ ★ 行号会漂（★ 本轮就漂过：中间件链从 `7724-7740` 变成 `7725-7743`）
// ⇒ ⇒ 判据钉**小节标题**（`### 3.1 请求路径`）⇒ ⇒ 语义判据，不钉形态。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// staleBannerMarker 是「过期横幅」的识别标记。
//
// ★ 用**纯中文短语**而不是 emoji/符号做标记 ⇒ 复制粘贴、跨编辑器都不会丢。
const staleBannerMarker = "现状陈述已过期"

// evidenceRe 匹配 `file:line` 形态的证据（★ 横幅里必须有它，否则横幅是空壳）。
var evidenceRe = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9_./-]*\.(go|md|sh|tsv|yml|yaml):[0-9]+`)

// planDocRelPath 是计划书在仓内的相对路径。
//
// ★ 用 glob 而不是写死文件名 ⇒ 文件名含中文与括号 ⇒ 写死极易在改名/复制时失配。
const planDocGlob = "docs/research/2026-10-24-magpie-v0.1.1367-*.md"

// auditDocRelPath 是本轮审计档。
const auditDocRelPath = "docs/audit/2026-10-24-magpie-v0.1.1367-改造落地审计.md"

// staleBannerAnchors 是「必须有过期横幅」的位置。
//
// ★ 第一项 = 文档顶部（全文范围）；其余 = `### X.Y ` 小节标题。
var staleBannerAnchors = []struct {
	// section 为空 ⇒ 表示「全文范围」（文档顶部横幅）。
	section string
	// what 是给人的说明（红的时候要能看懂缺的是哪一处）。
	what string
}{
	{"", "文档顶部的总横幅"},
	{"3.1", "§3.1 请求路径（入口清单 + 中间件链行号）"},
	{"3.5", "§3.5 扩展机制全景（执行器钩子活/死 + 阶段数 + 机制计数）"},
	{"5.1", "§5.1 契约差异表（无集中策略表 / 无统一接口）"},
	{"5.3", "§5.3 本仓的三个硬缺口（能力词表数字 + 路由口径）"},
}

// planDocPath 返回计划书路径（★ 要求 glob **恰好命中 1 个** ⇒ 命中 0 个或多个都要红）。
//
// ★★ glob 必须在**仓根**下解析：★ `go test` 的工作目录是**包目录**
// （`scripts/ursmcheck`）⇒ 直接 glob 相对路径会命中 0 个
// ⇒ ★★ 而「命中 0 个」恰好是**本门自己写错路径时**的读数
// ⇒ ⇒ ★ 所以这里显式按仓根解析（★ 本轮真踩到过：门第一次跑就红在这个点）。
func planDocPath(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..")
	matches, err := filepath.Glob(filepath.Join(root, planDocGlob))
	if err != nil {
		t.Fatalf("glob %s 失败: %v", planDocGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("★ 计划书 %s 命中 %d 个（期望恰好 1 个）⇒ 判据的输入集合不确定\n"+
			"★ 命中项: %v", planDocGlob, len(matches), matches)
	}
	rel, err := filepath.Rel(root, matches[0])
	if err != nil {
		t.Fatalf("把 %s 折成仓内相对路径失败: %v", matches[0], err)
	}
	return filepath.ToSlash(rel)
}

// readRepoFile 读仓内文件（相对仓根）。
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatalf("读 %s 失败: %v", rel, err)
	}
	return string(b)
}

// mdSections 把 markdown 切成「小节标题 → 该小节正文」。
//
// ★ 只认 `## ` / `### ` 两级（★ 计划书的小节都是 `### X.Y `）⇒ 判定不依赖更细的层级。
func mdSections(text string) map[string]string {
	out := map[string]string{}
	cur := ""
	var buf strings.Builder
	flush := func() {
		if cur != "" {
			out[cur] = buf.String()
		}
		buf.Reset()
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "### ") || strings.HasPrefix(trimmed, "## ") {
			flush()
			cur = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(trimmed, "### "), "## "))
			continue
		}
		buf.WriteString(line)
		buf.WriteString("\n")
	}
	flush()
	return out
}

// sectionKey 从小节标题里取出 `X.Y` 形式的键（★ 「3.1 请求路径」→「3.1」）。
func sectionKey(title string) string {
	f := strings.Fields(title)
	if len(f) == 0 {
		return ""
	}
	head := f[0]
	if !strings.Contains(head, ".") {
		return ""
	}
	return head
}

// findSection 按 `X.Y` 键取小节正文。
func findSection(sections map[string]string, key string) (string, bool) {
	for title, body := range sections {
		if sectionKey(title) == key {
			return body, true
		}
	}
	return "", false
}

// staleBannerQualified 判断一段文本里有没有一份**合格**的过期横幅。
//
// ★★★ 合格 = ① 含标记 ② 标记所在段里有 `file:line` 证据。
//
// ★ 这个函数是**纯函数**（不读文件）⇒ ⇒ C5 可以喂合成样本量它有没有牙。
func staleBannerQualified(text string) bool {
	idx := strings.Index(text, staleBannerMarker)
	if idx < 0 {
		return false
	}
	// ★ 只看标记所在的那一段（到下一个空行为止）⇒ 避免「别处有 file:line」也算数。
	rest := text[idx:]
	end := strings.Index(rest, "\n\n")
	if end >= 0 {
		rest = rest[:end]
	}
	return evidenceRe.MatchString(rest)
}

// mdHeaderScope 返回文档**首部**（第一个 `## ` 级标题之前）的文本。
//
// ★★ 这条是 C1 里「文档顶部总横幅」的**范围限定** —— ★★ 本轮实测踩到过：
//
//	若顶部横幅的检查范围是**整篇文档**，那么删掉顶部横幅后，
//	**别处（§3.1/§3.5/§5.1/§5.3）的横幅仍能满足它** ⇒ ★★ **该检查恒绿**
//	⇒ ⇒ 所以「顶部」必须**位置敏感**（首个 `## ` 之前）。
func mdHeaderScope(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "## ") {
			return text[:strings.Index(text, line)]
		}
	}
	return text
}

// TestPlanDocHasStaleBanners 是 C1 + C2。
func TestPlanDocHasStaleBanners(t *testing.T) {
	rel := planDocPath(t)
	text := readRepoFile(t, rel)
	sections := mdSections(text)

	for _, anchor := range staleBannerAnchors {
		t.Run(anchor.what, func(t *testing.T) {
			scope := text
			if anchor.section == "" {
				// ★ 顶部横幅 ⇒ 范围 = 文档首部（★ **不是**整篇！见 mdHeaderScope 的说明）。
				scope = mdHeaderScope(text)
			} else {
				body, ok := findSection(sections, anchor.section)
				if !ok {
					t.Fatalf("★ 计划书里找不到小节 %q ⇒ 判据的锚点已漂\n"+
						"★ 现有小节: %v", anchor.section, sectionKeys(sections))
				}
				scope = body
			}
			if !staleBannerQualified(scope) {
				if strings.Contains(scope, staleBannerMarker) {
					t.Errorf("★ %s 的过期横幅是**空壳**（有标记但没有 file:line 证据）\n"+
						"★ 没有证据的横幅 = 没人能核对的断言 ⇒ 等于没有横幅", anchor.what)
					return
				}
				t.Errorf("★ %s 缺「%s」横幅\n"+
					"★ ⇒ 计划书会继续被当成【当前现状】读 ⇒ 而它描述的是**改造前**的快照\n"+
					"★ 修法：把该处的过期横幅加回来（逐条认证见审计档 %s §3.18）",
					anchor.what, staleBannerMarker, auditDocRelPath)
			}
		})
	}
}

// sectionKeys 列出所有小节键（★ 只给报错信息用）。
func sectionKeys(sections map[string]string) []string {
	var out []string
	for title := range sections {
		if k := sectionKey(title); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// TestAuditDocCurrencyTableIsAlive 是 C3：★ 认证表不许腐化。
func TestAuditDocCurrencyTableIsAlive(t *testing.T) {
	text := readRepoFile(t, auditDocRelPath)

	idx := strings.Index(text, "#### 3.18")
	if idx < 0 {
		t.Fatalf("★ 审计档里没有 §3.18（计划书时效性认证）⇒ 认证表没了\n" +
			"★ ⇒ 计划书的过期横幅会变成**没有出处**的断言")
	}
	section := text[idx:]
	// ★ 只取到下一个同级 `#### ` 为止。
	if end := strings.Index(section[1:], "\n#### "); end >= 0 {
		section = section[:end+1]
	}

	// ★ 认证表的行 = 含「file:line」证据的表格行。
	rows := 0
	for _, line := range strings.Split(section, "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "|") || !strings.HasSuffix(t, "|") {
			continue
		}
		if strings.HasPrefix(t, "|---") {
			continue
		}
		if evidenceRe.MatchString(t) {
			rows++
		}
	}
	if rows < 12 {
		t.Errorf("★ 审计档 §3.18 的认证表只有 %d 行带 file:line 证据（期望 ≥12）\n"+
			"★ ⇒ 认证表被清空/裁剪 ⇒ 横幅会变成没有出处的断言", rows)
	}
}

// TestStaleClaimsStillMatchCurrentFacts 是 C4：★★ 「结论反转」的判定必须与当前事实一致。
//
// ★★ 这条是本门最要紧的：它让「过期横幅」**不是一次性动作**，而是**与活事实绑在一起**。
func TestStaleClaimsStillMatchCurrentFacts(t *testing.T) {
	// ① 计划书说 `executor_hooks`「判死」⇒ 裁决表里它必须仍是 Production=false。
	ext := readRepoFile(t, "internal/extpoints/extpoints.go")
	if !strings.Contains(ext, "IDExecutorHooks") {
		t.Fatalf("★ 裁决表里找不到 executor_hooks ⇒ 计划书横幅的前提已漂\n" +
			"★ ⇒ 门禁表与计划书都要更新")
	}
	if !executorHooksIsNonProduction(ext) {
		t.Errorf("★ 裁决表里 `executor_hooks` 现在是**生产**的\n" +
			"★ ⇒ 计划书 §3.5 横幅里「执行器生命周期钩子『活』」那句**现在成立了**\n" +
			"★ ⇒ ⇒ 横幅与认证表都该更新（否则就是在给一个假断言盖章）")
	}

	// ② 计划书说「无集中策略表」已过期 ⇒ phasepolicy 必须存在。
	if _, err := os.Stat(filepath.Join("..", "..", "internal/phasepolicy/phasepolicy.go")); err != nil {
		t.Errorf("★ `internal/phasepolicy/phasepolicy.go` 不存在了\n" +
			"★ ⇒ 计划书 §5.1 横幅里「有集中策略表了」这句**不再成立** ⇒ 横幅要改")
	}
	// ③ 计划书说「无统一接口」已过期 ⇒ requestobserver 必须存在。
	if _, err := os.Stat(filepath.Join("..", "..", "internal/requestobserver/requestobserver.go")); err != nil {
		t.Errorf("★ `internal/requestobserver/requestobserver.go` 不存在了\n" +
			"★ ⇒ 计划书 §5.1 横幅里「有统一接口了」这句**不再成立** ⇒ 横幅要改")
	}
}

// executorHooksIsNonProduction 判断裁决表里 `executor_hooks` 那行是不是 `Production: false`。
//
// ★ 按「该行块」判定（★ 不是全文找 `Production: false` —— 那会误命中别人的行）。
func executorHooksIsNonProduction(ext string) bool {
	lines := strings.Split(ext, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "IDExecutorHooks") {
			continue
		}
		// ★ 从这一行往后看 30 行，取引用块里第一条 Production: 赋值。
		for j := i; j < i+30 && j < len(lines); j++ {
			if strings.Contains(lines[j], "Production:") {
				return strings.Contains(lines[j], "false")
			}
		}
	}
	return false
}

// TestStaleBannerDetectorHasTeeth 是 C5：★★ 量具自检。
//
// ⇒ 本门的判据是「找横幅」⇒ ★★ 「找不到横幅」和「横幅检测器本身坏了」
// 在输出上**长得一样**（都是「没找到」）⇒ ⇒ 必须自证检测器有牙。
func TestStaleBannerDetectorHasTeeth(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{
			name: "有标记 + 有 file:line ⇒ 合格",
			text: "> #### ⚠ 现状陈述已过期\n> 当前读数见 `cmd/gateway/main.go:7725`。",
			want: true,
		},
		{
			name: "有标记但**无**证据 ⇒ 空壳 ⇒ 不合格",
			text: "> #### ⚠ 现状陈述已过期\n> （已过期）",
			want: false,
		},
		{
			name: "有证据但**无标记** ⇒ 不算横幅",
			text: "> 见 `cmd/gateway/main.go:7725`。",
			want: false,
		},
		{
			name: "证据在**别的段落** ⇒ 不算（防止借远处的证据蒙混）",
			text: "> #### ⚠ 现状陈述已过期\n> （已过期）\n\n> 别的段落：见 `cmd/gateway/main.go:7725`。",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := staleBannerQualified(tc.text)
			if got != tc.want {
				t.Errorf("★ 横幅检测器判错：got=%v want=%v（样本 %q）\n"+
					"★ ⇒ 本门要么恒绿、要么恒红 ⇒ 它的结论不可信", got, tc.want, tc.name)
			}
		})
	}
}
