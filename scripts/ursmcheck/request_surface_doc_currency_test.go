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
// # ★★ 八条判据（C1–C6 ＋ **C8** ＋ **C9**）
//
// ★★ **编号是「跨门共用序列」** ⇒ ★★ 本门**不含 C7**（★ **C7** 归**横幅口径对账门**
// `scripts/ursmcheck/magpie_plan_currency_test.go`）⇒ ★★ C8 / C9 是**后补**的两条
// ⇒ ★★ ⇒ **计数与判据列表必须一起更新**（★ 本文件头与审计档 §四 都登记了这个数 ⇒ ★ 漂了会被**计数对账门**抓到）。
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
//	C8 ★★★ **第三方对账**：★ 认证表（审计档 §3.18.3）↔ 设计页的**同语义区间**必须**精确相等**
//	   ⇒ ★ 只盯页面的门抓不到「两份权威互相矛盾」⇒ ⇒ 必须引入**第三方**来对拍。
//	C9 ★ **C8 的量具自检**：喂合成样本给「认证表行区间提取器」⇒ 必须给出预期值（★ 防 C8 恒绿）。
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
	// certRef 指向审计档 §3.18.3 认证表里**对应同一语义对象**的那一行（如 `A1`）。
	//
	// ★★ 为什么需要它：★★ 本仓出现过「**同一交付里两份文件写了两组行号**」——
	// ★★ 认证表 A1 说链在 `main.go:7725-7743`，而设计页写的是另一组
	// ★★ ⇒ ⇒ 两份都是「权威」，却互相矛盾 ⇒ ⇒ 读者拿到哪份都可能是错的。
	// ★★ ⇒ 门无法从「页面自己」发现这种矛盾 ⇒ ⇒ 必须引入**第三方**（认证表）来对拍。
	//
	// ★ 留空 = 认证表**未以同一语义对象**覆盖本项 ⇒ 不对拍（★ 避免假红）。
	// ★ 非空 = 该项的 (file, from, to) 必须与认证表那一行读到的区间**精确相等**。
	certRef string
	// why 是给红报告看的说明。
	why string
}

// requestSurfaceDocEvidence 是本门的对拍表。
//
// ★★ **页面上每一处 `file:line` 引用都必须在表里**（C1 会核对覆盖面）。
//
// ★★★ **第三次重钉（2026-10-27 03:4x 合并轮）**：★ 中间件链的行号又漂了
//   —— `7725-7743` → **`7758-7776`**（★ **整体 +33 行**：本仓多模态/元数据
//   两轮往 `cmd/gateway/main.go` 里插了 33 行 ⇒ Recovery 7758 / Logging 注释
//   7760-7767 / maintain 注释 7749-7754 / Auth 7773 / SecurityHeaders 7776）。
//   ⇒ ★ **同一次漂移的三个面必须一起改**，否则门会红：
//   ① **本对拍表**（门的事实源）；② `docs/subsystems/request-surface.md`（被守页面，
//      C1 要求**精确相等**不是「区间重叠」⇒ 差一行就红）；
//   ③ **认证表**（审计档 §3.18.3 的 A1 行，C8 要求与本表**精确相等**）。
//   ⇒ ★★ **漂移史**：`7724-7740` → `7725-7743` → `7758-7776`（★ 三次里有
//   ★★ 两次是**别的会话**改 `main.go` 带的 ⇒ ⇒ 「行号会漂」不是偶发，
//   ★★ 是**常态** ⇒ ⇒ 每次动 `main.go` 的中间件链，**必须回头重钉这三处**）。
var requestSurfaceDocEvidence = []docEvidence{
	{
		file: "cmd/gateway/main.go", from: 7758, to: 7776,
		mustContain: "middleware.NewRecoveryMiddleware()", kind: evidenceCode,
		certRef: "A1",
		why:     "HTTP 中间件链本体（Recovery 起、SecurityHeaders 止）★ 与认证表 A1 同一语义对象",
	},
	{
		file: "cmd/gateway/main.go", from: 7760, to: 7767,
		mustContain: "Logging 必须排在 Auth", kind: evidenceComment,
		why: "「Logging 必须在 Auth 之外」的**自陈注释**",
	},
	{
		file: "cmd/gateway/main.go", from: 7749, to: 7754,
		mustContain: "maintain 与 mobile 两个挂载必须组合在链", kind: evidenceComment,
		why: "「maintain/mobile 必须组合在链内」的**自陈注释**",
	},
	{
		file: "domains/hooks/registry.go", from: 36, to: 36,
		mustContain: "func NewHookRegistry", kind: evidenceCode,
		certRef: "A7",
		why:     "HookRegistry 的定义（判死的机制本体）★ 与认证表 A7 同一语义对象",
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
		file: "domains/pipeline/pipeline.go", from: 25, to: 40,
		mustContain: "PhasePreAuthentication", kind: evidenceCode,
		certRef: "A8",
		why:     "骨架的 16 个 phase 词表（唯一事实源）★ 与认证表 A8 同一语义对象",
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

// ─────────────────────────────────────────────────────────────────────────────
// ★★★ C8：设计页与【认证表】的同语义区间必须**精确相等**
//
// ★★★ 这道判据补的是一种**第三方门**才抓得到的矛盾：
//
//	★ 本仓出现过「**同一交付里两份文件写了两组行号**」——
//	★ 审计档 §3.18.3 的 A1 认证说链在 `cmd/gateway/main.go:7725-7743`，
//	★ 而设计页当时写的是 `cmd/gateway/main.go:7724-7743`（差 1 行）。
//	★ ⇒ 两份都是「权威」，却互相矛盾 ⇒ ⇒ 读者拿到哪一份都可能是错的坐标。
//
// ★★ 而**只盯着设计页的门抓不到它**：★ 设计页与对拍表**一起**改 ⇒ 门照样绿。
// ★★ ⇒ 必须引入**第三方**（认证表）来对拍 ⇒ ⇒ 这就是本判据存在的全部理由。
//
// ★ 为什么「逐项登记 `certRef`」而不是「自动配对」：
// ★ ⇒ 认证表的覆盖面比设计页**大**（它认证整个计划书，设计页只讲请求面）
// ★ ⇒ 自动配对会产生**假红**（认证表提到 `main_pipeline.go:46-47`，设计页没引用它）。
// ★ ⇒ ⇒ 改为**显式登记**：只有「同一语义对象」的项才对拍。
const certTableAnchor = "##### 3.18.3"

// certRowRe 匹配认证表的一行（行首形如 `| A1 |`）。
var certRowRe = regexp.MustCompile(`^\|\s*(A\d+)\s*\|`)

// certRangeRe 在认证表读数列里抽第一个 `file:spec`。
//
// ★ 不要求反引号：★ 认证表读数列里的是 `cmd/gateway/main.go:7725-7743` **裸写**的。
var certRangeRe = regexp.MustCompile(`([A-Za-z0-9_][A-Za-z0-9_./-]*\.(?:go|md|sh|tsv|yml|yaml)):([0-9]+(?:-[0-9]+)?)`)

// certTableSection 取审计档 §3.18.3 认证表小节的正文。
func certTableSection(t *testing.T, lines []string) []string {
	t.Helper()
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), certTableAnchor) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("★ 审计档里找不到 %q ⇒ 认证表没了 ⇒ C8 没有对账对象", certTableAnchor)
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "##### ") {
			end = i
			break
		}
	}
	return lines[start:end]
}

// findCertRow 在认证表里取某一行（如 `A1`）。
func findCertRow(section []string, ref string) (string, bool) {
	for _, l := range section {
		if m := certRowRe.FindStringSubmatch(strings.TrimSpace(l)); len(m) == 2 && m[1] == ref {
			return l, true
		}
	}
	return "", false
}

// certRowRange 取认证表某行**读数列**里的文件与区间。
//
// ★ 读数列 = 第 3 个单元格（切分后下标 3：空 / 行号 / 计划书断言 / **当前读数**）。
func certRowRange(row string) (string, int, int, bool) {
	cells := strings.SplitN(row, "|", 5)
	if len(cells) < 4 {
		return "", 0, 0, false
	}
	m := certRangeRe.FindStringSubmatch(cells[3])
	if len(m) < 3 {
		return "", 0, 0, false
	}
	from, to := refRange(m[2])
	if from == 0 {
		return "", 0, 0, false
	}
	return m[1], from, to, true
}

// TestRequestSurfaceDocAgreesWithCertTable 是 C8。
func TestRequestSurfaceDocAgreesWithCertTable(t *testing.T) {
	lines := readRepoText(t, auditDocRelPath)
	section := certTableSection(t, lines)

	checked := 0
	for _, ev := range requestSurfaceDocEvidence {
		if ev.certRef == "" {
			continue
		}
		checked++
		row, ok := findCertRow(section, ev.certRef)
		if !ok {
			t.Errorf("★ 认证表里没有 %s 行（对拍项 %s 指向它）⇒ 对不了账", ev.certRef, ev.why)
			continue
		}
		f, from, to, ok := certRowRange(row)
		if !ok {
			t.Errorf("★ 认证表 %s 的读数列里读不出 file:line 区间 ⇒ 对不了账（对拍项：%s）",
				ev.certRef, ev.why)
			continue
		}
		if f != ev.file {
			t.Errorf("★ 认证表 %s 指向 %s，而对拍项指向 %s ⇒ **不是同一语义对象**\n"+
				"★ ⇒ 修法：要么改 certRef，要么改对拍项指向的文件",
				ev.certRef, f, ev.file)
			continue
		}
		if from != ev.from || to != ev.to {
			t.Errorf("★★ 认证表 %s 说 %s:%d-%d，而设计页/对拍表说 %s:%d-%d ⇒ ★★ **两份权威互相矛盾**\n"+
				"★★ ⇒ 读者拿到哪一份都可能是错的坐标 ⇒ 修法：两处对齐（改任一处都会红）",
				ev.certRef, f, from, to, ev.file, ev.from, ev.to)
		}
	}
	if checked == 0 {
		t.Fatal("★ 没有任何对拍项登记了 certRef ⇒ C8 恒绿（形同虚设）")
	}
	t.Logf("★ C8 对拍了 %d 项（认证表 ↔ 设计页的同语义区间）", checked)
}

// TestCertRowRangeExtractorHasTeeth 是 C9：★★ C8 的量具自检。
//
// ⇒ C8 依赖「从认证表一行里读出 file:line」这个提取器 ⇒ ★★ 若它恒读不出
// ⇒ ⇒ C8 会因为「对不了账」而…… 其实会红，但**红的原因是量具坏了**而不是数据矛盾
// ⇒ ⇒ ⇒ 必须自证它有牙。
func TestCertRowRangeExtractorHasTeeth(t *testing.T) {
	cases := []struct {
		name     string
		row      string
		wantFile string
		wantFrom int
		wantTo   int
		wantOK   bool
	}{
		{
			name:     "读数列里的裸 file:区间 ⇒ 读出",
			row:      "| A1 | 断言 | `cmd/gateway/main.go:7725-7743` **逐项一致** | ✅ |",
			wantFile: "cmd/gateway/main.go", wantFrom: 7725, wantTo: 7743, wantOK: true,
		},
		{
			name:     "单行号 ⇒ 区间退化为 (N,N)",
			row:      "| A7 | 断言 | 定义在 `domains/hooks/registry.go:36`；零调用 | ✅ |",
			wantFile: "domains/hooks/registry.go", wantFrom: 36, wantTo: 36, wantOK: true,
		},
		{
			name:   "读数列里没有 file:line ⇒ 读不出",
			row:    "| A4 | 断言 | 全仓非测试命中**只有一条注释** ⇒ **零生产引用** | ✅ |",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, from, to, ok := certRowRange(tc.row)
			if ok != tc.wantOK {
				t.Fatalf("★ 提取器判错：ok=%v want=%v（样本 %q）\n"+
					"★ ⇒ C8 的结论不可信", ok, tc.wantOK, tc.row)
			}
			if !ok {
				return
			}
			if f != tc.wantFile || from != tc.wantFrom || to != tc.wantTo {
				t.Errorf("★ 提取器读错：got=(%s,%d-%d) want=(%s,%d-%d)（样本 %q）",
					f, from, to, tc.wantFile, tc.wantFrom, tc.wantTo, tc.row)
			}
		})
	}
}
