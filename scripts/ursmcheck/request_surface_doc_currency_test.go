package ursm_test

// request_surface_doc_currency_test.go —— ★★ **设计页漂移门**（2026-10-24 §3.19）。
//
// # 这道门补的是哪块缺口
//
// 计划书 **P2-3「子系统设计页」**的验收条款是三条：
//
//	① 页面在仓内；
//	② 每条断言有 file:line；
//	③ ★★ **与代码漂移有门禁**。
//
// ★ ①② 达成了（`docs/subsystems/request-surface.md`），★ 但 ③ **客观未达成**：
// ★ 全仓 `*_test.go` 对该页的引用数 = **0** ⇒ ★ **那页没有任何门守护**
// ⇒ ⇒ 而且该页**自带一句不实自陈**（「本页的每条断言都有 file:line，且有门禁对拍现实」）
// ⇒ ⇒ ★★ 而它引用的 4 处 `file:line` **从诞生起就是错的**
// （链在 `main.go:7717-7738`，实际是 `7724-7743`；`registry.go:34`，实际是 `:36` …）
// ⇒ ⇒ ★★ **这正是本轮「认证审计」要消除的那一类缺陷**：交付物里的断言失真。
//
// # ★ 为什么这道门不是「扫描器」而是「对拍表」
//
// ★ 本页的断言是**自然语言**（「链在 main.go 的这几行」）⇒ **不可直接机读**。
// ⇒ ⇒ 但它的**证据**是**可机读的**：`file:line` + 「那一行必须含某个关键字」。
// ⇒ ⇒ 所以门里维护一张**对拍表**：每条 = 「页面引用的位置」+「目标范围」+「范围内必须出现的关键字」。
// ⇒ ⇒ ★ 这样：★ 页面保持自然可读；★ 判据**可执行**；★ **页面漂了 ⇒ 门红**。
//
// # ★★ 六条判据
//
//	C1 ★★ **覆盖性**：★ 页面里出现的**每一处** `file:line` 引用，
//	   **都必须**在对拍表里有一项覆盖它（★ 少登记一处 ⇒ 那一处就没门看管 ⇒ 红）。
//	C2 ★ **对拍表每项的行号范围必须在文件内**（引用指向不存在的行 ⇒ 红）。
//	C3 ★★ **对拍表每项的范围内必须出现指定关键字**，
//	   且 ★★ **关键字必须出现在【代码行】或【注释行】——由该项声明**：
//	   · `evidenceCode` ⇒ 关键字必须在**非注释**行（★ 防止「注释里也有这个词」蒙混过关）；
//	   · `evidenceComment` ⇒ 关键字必须在**注释**行（★ 有些断言的证据**就是**自陈注释）。
//	C4 ★ **页面自陈不许失实**：自陈里点名的门必须**真的存在**，且必须点名本门。
//	C5 ★ **指向不存在文件的引用，必须在页面上被显式标为「外部」**
//	   （★ 例：magpie 侧的 `internal/gateway/routing.go`）⇒ 否则红。
//	C6 ★ **量具自检**：本门的三个判别函数都必须有牙（喂合成样本必须判对）。
//
// # ★★ 「被测集合」的问题（★ 本门特意避开的坑）
//
// ⇒ ★ 本门的对拍表在**门文件里**，**不从本页派生** ⇒
//   ⇒ ★★ 改页面 ⇒ 表不变 ⇒ 门会红（正确）；
//   ⇒ ★★ 改代码 ⇒ 表不变 ⇒ 门会红（正确）；
//   ⇒ ★★ 改表 ⇒ 就是**改断言** ⇒ ★★ 这是**显式**的动作（要改门文件）⇒ 不会静默通过。
//
// # ★★ 两条踩过的坑（★ 变异挖出来的）
//
// ① ★★ C1 一开始写成「页面**包含**这条引用」⇒ ★ 页面有**两处**链引用时，
//    ★ 改其中一处 ⇒ 另一处仍满足「包含」⇒ ⇒ **漏检** ⇒ ★ 改成**逐处覆盖**。
// ② ★★ C3 一开始用「字符串出现」判关键字 ⇒ ★ `v2pipe-` 在**注释**里也有
//    ⇒ ★ 把合成逻辑删了它仍绿 ⇒ ⇒ 改成**区分代码行 / 注释行**。

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// requestSurfaceDocRelPath 是被守的设计页。
const requestSurfaceDocRelPath = "docs/subsystems/request-surface.md"

// evidenceKind 声明一条对拍项的证据在**代码行**还是**注释行**。
type evidenceKind int

const (
	// evidenceCode：关键字必须出现在**非注释**行。
	evidenceCode evidenceKind = iota
	// evidenceComment：关键字必须出现在**注释**行（★ 有些断言的证据就是自陈注释）。
	evidenceComment
)

// docEvidence 一条「设计页引用 ↔ 源码」的对拍项。
type docEvidence struct {
	// file 是被引用的仓内文件（相对仓根）。
	file string
	// from/to 是行号区间（闭区间，1-based）。
	from, to int
	// mustContain 是该区间内**必须出现**的关键字（★ 语义锚）。
	mustContain string
	// kind 声明证据在代码行还是注释行。
	kind evidenceKind
	// why 是给红报告看的说明。
	why string
}

// requestSurfaceDocEvidence 是本门的对拍表。
//
// ★★ **页面上每一处 `file:line` 引用都必须在表里**（C1 会核对覆盖面）。
var requestSurfaceDocEvidence = []docEvidence{
	{
		file: "cmd/gateway/main.go", from: 7724, to: 7743,
		mustContain: "middleware.NewBuilder()", kind: evidenceCode,
		why: "HTTP 中间件链的构建（Builder 起、SecurityHeaders 止）",
	},
	{
		file: "cmd/gateway/main.go", from: 7727, to: 7734,
		mustContain: "Logging 必须排在 Auth", kind: evidenceComment,
		why: "「Logging 必须在 Auth 之外」的**自陈注释**",
	},
	{
		file: "cmd/gateway/main.go", from: 7716, to: 7721,
		mustContain: "maintain 与 mobile 两个挂载必须组合在链", kind: evidenceComment,
		why: "「maintain/mobile 必须组合在链内」的**自陈注释**",
	},
	{
		file: "domains/hooks/registry.go", from: 36, to: 36,
		mustContain: "func NewHookRegistry", kind: evidenceCode,
		why: "HookRegistry 的定义（判死的机制本体）",
	},
	{
		file: "domains/hooks/types.go", from: 13, to: 23,
		mustContain: "PhasePostResponse", kind: evidenceCode,
		why: "Hook 的 5 个旧阶段名（别名视图的词表）",
	},
	{
		file: "domains/streaming/executors/executor_chat.go", from: 60, to: 67,
		mustContain: "type ExecuteHooks struct", kind: evidenceCode,
		why: "执行器生命周期钩子的类型定义",
	},
	{
		file: "domains/hooks/response/types.go", from: 179, to: 179,
		mustContain: "type ResponseInterceptor interface", kind: evidenceCode,
		why: "响应拦截链的接口（挂在 phase 上的那套机制）",
	},
	{
		file: "domains/pipeline/pipeline.go", from: 25, to: 42,
		mustContain: "PhasePreAuthentication", kind: evidenceCode,
		why: "骨架的 16 个 phase 词表（唯一事实源）",
	},
	{
		file: "cmd/gateway/main_pipeline.go", from: 1, to: 80,
		mustContain: "post-flight (telemetry,", kind: evidenceComment,
		why: "v1 的前飞/后飞「全部 inline」的自陈说明（★ 证据是包注释，不是代码）",
	},
	{
		file: "cmd/gateway/main_pipeline.go", from: 160, to: 169,
		mustContain: "func v2UsePipeline() bool", kind: evidenceCode,
		why: "v2 开关的读取（默认关、启动时读一次）",
	},
	{
		file: "cmd/gateway/main_pipeline.go", from: 1112, to: 1112,
		mustContain: "LLM_GATEWAY_USE_V2_PIPELINE=true", kind: evidenceCode,
		why: "v2 打开时的那条启动日志",
	},
	{
		file: "cmd/gateway/main_pipeline.go", from: 749, to: 758,
		mustContain: "v2pipe-", kind: evidenceCode,
		why: "D09 的 v2 request-id 合成与写回（★ 存在 ⇒ 那条差异不是「没实现」）",
	},
}

// docRefRe 匹配页面里的一处 `file:line` 引用。
//
// ★ 允许 `a.go:12`、`a.go:12-34`、`a.go:12,34` 三种形态。
var docRefRe = regexp.MustCompile("`([A-Za-z0-9_][A-Za-z0-9_./-]*\\.(go|md|sh)):([0-9]+(?:-[0-9]+)?(?:,[0-9]+)*)`")

// externalMarker 是「这条引用指向外部仓库」必须在页面上出现的标记。
const externalMarker = "magpie 侧"

// readRepoText 读仓内文件（相对仓根）并按行切开。
func readRepoText(t *testing.T, rel string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatalf("读 %s 失败: %v", rel, err)
	}
	return strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
}

// isCommentLine 判断一行是不是注释行（★ 整行注释）。
func isCommentLine(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*")
}

// refRange 解析引用里的行号集合（★ `12` / `12-34` / `12,34` ⇒ 起止区间）。
func refRange(spec string) (int, int) {
	parts := strings.Split(spec, ",")
	min, max := 0, 0
	for _, p := range parts {
		lo, hi := p, p
		if i := strings.Index(p, "-"); i >= 0 {
			lo, hi = p[:i], p[i+1:]
		}
		a, err1 := strconv.Atoi(lo)
		b, err2 := strconv.Atoi(hi)
		if err1 != nil || err2 != nil {
			continue
		}
		if min == 0 || a < min {
			min = a
		}
		if b > max {
			max = b
		}
	}
	return min, max
}

// coveredBy 判断某处引用（file + 行号区间）是否被对拍表某项覆盖。
//
// ★★★ 判据是**精确相等**（file + from + to 全等），★ **不是「区间重叠」**。
// ⇒ ★★ 原因（M38 变异挖出来的）：★ 用「重叠」判覆盖时，
// ★★ 页面把 `7724-7743` 改成 `7717-7738`（**有重叠**）⇒ 仍被判「已覆盖」⇒ 门绿
// ⇒ ⇒ ★★ 而「有重叠」恰恰就是**行号漂了**的形态 ⇒ ⇒ 判据必须精确。
func coveredBy(ev docEvidence, file string, from, to int) bool {
	return ev.file == file && ev.from == from && ev.to == to
}

// TestRequestSurfaceDocEveryReferenceIsCovered 是 C1：★★ **覆盖性**。
//
// ⇒ ★★ 页面里**每一处** `file:line` 引用都必须在对拍表里有对应项。
// ⇒ ⇒ 否则那一处引用就**没有门看管** ⇒ ★ 页面漂了也不会有人知道。
func TestRequestSurfaceDocEveryReferenceIsCovered(t *testing.T) {
	lines := readRepoText(t, requestSurfaceDocRelPath)

	for i, line := range lines {
		for _, m := range docRefRe.FindAllStringSubmatch(line, -1) {
			file, spec := m[1], m[3]
			from, to := refRange(spec)
			if from == 0 {
				continue
			}
			// ★ 指向不存在于本仓的文件 ⇒ 必须是**显式标注的外部引用**（C5 另判）。
			if _, err := os.Stat(filepath.Join("..", "..", file)); err != nil {
				if !strings.Contains(line, externalMarker) {
					t.Errorf("★ 设计页第 %d 行引用了不存在的文件 %s，且**没有**标为外部（%q）\n"+
						"★ ⇒ 这一处引用没有门看管 ⇒ 修法：要么指向仓内真实文件，要么标明是外部系统的路径",
						i+1, file, externalMarker)
				}
				continue
			}
			covered := false
			for _, ev := range requestSurfaceDocEvidence {
				if coveredBy(ev, file, from, to) {
					covered = true
					break
				}
			}
			if !covered {
				t.Errorf("★ 设计页第 %d 行的引用 **没有**被本门覆盖：%s:%s\n"+
					"★ ⇒ 那一处引用**没有门看管** ⇒ 修法：在 request_surface_doc_currency_test.go 的\n"+
					"★   requestSurfaceDocEvidence 里补一条（file/范围/关键字/证据在代码还是注释）",
					i+1, file, spec)
			}
		}
	}
}

// TestRequestSurfaceDocEvidenceStillHolds 是 C2 + C3：★★ **行号与关键字都要对得上**。
func TestRequestSurfaceDocEvidenceStillHolds(t *testing.T) {
	for _, ev := range requestSurfaceDocEvidence {
		t.Run(ev.why, func(t *testing.T) {
			lines := readRepoText(t, ev.file)
			// ── C2：行号范围必须在文件内 ──
			if ev.from < 1 || ev.to > len(lines) || ev.from > ev.to {
				t.Errorf("★ 对拍项 %s 的行号区间 [%d,%d] **越界**（该文件共 %d 行）\n"+
					"★ ⇒ 引用在指一个不存在的位置 ⇒ 修法：按现状改对拍表（并同步页面）",
					ev.file, ev.from, ev.to, len(lines))
				return
			}
			// ── C3：关键字必须在指定的那类行里 ──
			hit := false
			for _, line := range lines[ev.from-1 : ev.to] {
				isComment := isCommentLine(line)
				if (ev.kind == evidenceCode && !isComment) || (ev.kind == evidenceComment && isComment) {
					if strings.Contains(line, ev.mustContain) {
						hit = true
						break
					}
				}
			}
			if !hit {
				kindName := "代码行"
				if ev.kind == evidenceComment {
					kindName = "注释行"
				}
				t.Errorf("★ %s 的区间 [%d,%d] 里**没有**在%s上出现 %q\n"+
					"★ ⇒ 代码挪了位/那个构造没了 ⇒ ★ **设计页已与代码漂移**\n"+
					"★ 修法：按现状改对拍表（并同步页面上的引用）",
					ev.file, ev.from, ev.to, kindName, ev.mustContain)
			}
		})
	}
}

// TestRequestSurfaceDocHasNoStaleSelfClaim 是 C4：★★ **不许有不实自陈**。
//
// ★★ 本页开头曾自陈「本页的每条断言都有 file:line，且有门禁对拍现实
// （`scripts/ursmcheck/magpie_p0_extension_adjudication_test.go`）」
// ★★ 而**那个门并不对拍本页** ⇒ ⇒ 页面在给自己盖一个不存在的章。
func TestRequestSurfaceDocHasNoStaleSelfClaim(t *testing.T) {
	doc := strings.Join(readRepoText(t, requestSurfaceDocRelPath), "\n")

	mentioned := map[string]bool{}
	for _, m := range regexp.MustCompile(`scripts/ursmcheck/[A-Za-z0-9_]+\.go`).FindAllString(doc, -1) {
		mentioned[m] = true
	}
	if len(mentioned) == 0 {
		t.Fatalf("★ 设计页里一个门文件都没提到 ⇒ 它的「有门禁对拍现实」自陈已失效")
	}
	if !strings.Contains(doc, "request_surface_doc_currency_test.go") {
		t.Errorf("★ 设计页的自陈里**没有**点名本门（`request_surface_doc_currency_test.go`）\n" +
			"★ ⇒ 页面在声称「有门禁对拍现实」却没指向真的那道 ⇒ 修法：把自陈指向本门")
	}
	// ★★ 页里点名的**每一个**门文件都必须**真的存在**（防止点名一个不存在的门来充数）。
	for gate := range mentioned {
		if _, err := os.Stat(filepath.Join("..", "..", gate)); err != nil {
			t.Errorf("★ 设计页点名了门 %s，但它**不存在**\n"+
				"★ ⇒ 页面在拿一个不存在的门给自己背书", gate)
		}
	}
}

// TestRequestSurfaceDocDetectorHasTeeth 是 C6：★★ 量具自检。
//
// ⇒ 本门有**三个**判别函数（覆盖 / 关键字 / 注释判别）⇒ ★★ 每个都要有牙。
func TestRequestSurfaceDocDetectorHasTeeth(t *testing.T) {
	// ① refRange 的解析
	cases := []struct {
		spec string
		from int
		to   int
	}{
		{"36", 36, 36},
		{"7724-7743", 7724, 7743},
		{"745,757", 745, 757},
		{"1,5,9", 1, 9},
	}
	for _, tc := range cases {
		from, to := refRange(tc.spec)
		if from != tc.from || to != tc.to {
			t.Errorf("★ refRange(%q) = (%d,%d)，期望 (%d,%d)", tc.spec, from, to, tc.from, tc.to)
		}
	}

	// ② isCommentLine 的判别
	commentCases := map[string]bool{
		"    // 这是注释":         true,
		"\t// 这是注释":           true,
		"// 这是注释":             true,
		"   * 注释续行":           true,
		"x := 1 // 尾随注释":      false,
		`fmt.Sprintf("a//b")`: false,
		"func f() {}":         false,
	}
	for line, want := range commentCases {
		if got := isCommentLine(line); got != want {
			t.Errorf("★ isCommentLine(%q) = %v，期望 %v", line, got, want)
		}
	}

	// ③ coveredBy 的判别（★★ 必须**精确相等**：★ 「区间重叠」会把「行号漂了」误判成已覆盖）
	ev := docEvidence{file: "a.go", from: 10, to: 20}
	if !coveredBy(ev, "a.go", 10, 20) {
		t.Errorf("★ coveredBy 判否了「完全一致」的引用")
	}
	if coveredBy(ev, "a.go", 15, 16) {
		t.Errorf("★ coveredBy 判是了「区间不同」的引用（★ 必须是精确相等）")
	}
	if coveredBy(ev, "a.go", 21, 30) {
		t.Errorf("★ coveredBy 判是了「区间不重叠」的引用")
	}
	if coveredBy(ev, "b.go", 10, 20) {
		t.Errorf("★ coveredBy 判是了「不同文件」的引用")
	}
}
