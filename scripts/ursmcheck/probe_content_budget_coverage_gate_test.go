package ursm_test

// probe_content_budget_coverage_gate_test.go —— R152 轮（2026-10-27）新增门。
//
// ── 为什么有这道门 ───────────────────────────────────────────────────────────
//
// ★ D9b 把「探针的内容预算」从 32 提到 1024（`internal/providercap` 的
//   `ProbePingMaxTokens`）⇒ ★★ **chat / messages 腿**的内容预算够用了。
//
// ★★ 但 **responses 腿**用的是**另一个常量**（`ResponsesProbeMaxOutputTokens` = 32）
// ⇒ ⇒ ★★★ **D9b 的覆盖是有缺口的**：**同一个缺陷**（推理模型被 32 token
//   吃光正文 ⇒ `assistant_output_missing` ⇒ `probe_response_inconclusive`）
//   在 responses 腿上**原样存在** ⇒ ⇒ 而 R151 实测的 `probe_response_inconclusive`
//   ⇒ 「故意不写回」⇒ 正是「不可逆封禁」那条链的入口。
//
// ★ ⇒ 本门**不**把缺口补上：把 responses 腿的预算也提到 1024 是**探针预算变更**
//   ⇒ 按仓内纪律属**属主裁决项**（D13）⇒ ⇒ 在裁决之前，
//   **缺口必须【可见、可枚举、且不许悄悄漂移】**。
//
// ★★ 本门与 `probe_budget_gates_test.go` 的分工（两门互补，缺一不可）：
//   · 旧门：**值** —— `ProbePingMaxTokens` 解析后的整数值必须 ≥ 1024；
//   · ★ 本门：**覆盖面** —— ★「探针请求体的每一个内容预算位点，
//     都必须显式引用一个【具名常量】；裸字面量必须【具名例外】；
//     responses 腿必须【具名待裁决】」。
//
// ★★ 为什么扫描面是【显式清单】而不是「全仓扫 `max_tokens`」：
//   全仓 `"max_tokens":` 遍布业务代码 / hooks / streaming / compression /
//   approval（实测 40+ 处裸字面量）⇒ ⇒ ★★ 全仓扫描会**误报一片** ⇒
//   ⇒ **判据的扫描面必须选对**（★ §400 通用式一/三的同族教训）。
//
// ★ 阳性对照：故意写坏的合成片段 ⇒ 检测器必须报出来。
// ★ 阴性对照：合规片段 ⇒ 检测器不得误报。

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// probeBudgetCoverageFiles 是「构造探针请求体」的文件清单（★ 显式清单）。
//
// ★★ 清单里的每个文件都必须在仓里存在（清单自检）⇒ 文件改名时本门**自己红**,
// 而不是静默失去覆盖面。
var probeBudgetCoverageFiles = []string{
	"bg/node_probe.go",
	"bg/probe_http.go",
	"bg/active_probe_executor.go",
	"bg/credential_probe_v2.go",
	"bg/self_check_worker.go",
	"bg/credential_selfcheck.go",
	"admin/node_operations.go",
	"admin/provider_probe.go",
}

// probeBudgetResponsesLegFiles 是「responses 腿」的**具名待裁决台账**。
//
// ★★ 这些位点用的是 `ResponsesProbeMaxOutputTokens`（= 32），而 32 对推理模型
// 不足 ⇒ ⇒ ★★ **D13（属主裁决项）**：是否把 responses 腿的预算也提到 1024。
// ★ ⇒ 在裁决之前，缺口必须【具名 + 有理由】；⇒ ⇒ 裁决落地时请把对应条目
//
//	移到「已修」并同步审计档与 LESSONS。
var probeBudgetResponsesLegFiles = map[string]string{
	"bg/node_probe.go":            "node 探针的 responses 腿（desc.ChatProbeEndpoint == EpResponses）",
	"bg/probe_http.go":            "singleResponsesPing（responses 原生探针）",
	"bg/active_probe_executor.go": "ActiveProbeExecutor 的 responses 腿",
	"bg/credential_probe_v2.go":   "CredentialProbeV2 的 responses 腿",
	"admin/node_operations.go":    "admin 的 node 操作探针（responses 腿）",
	"admin/provider_probe.go":     "admin 的 provider 探针（responses 腿）",
}

// probeBudgetBareLiteralExceptions 是「裸字面量」的**具名例外台账**。
//
// ★★★ key = "<仓库相对路径>|<锚点子串>|<字面量>"，**三段**。
//
// ★★★ 为什么必须是三段（★ 本轮一次「变异不红」换来的教训）：
//
//	★ 第一版只用「文件|字面量」两段 ⇒ ★★ **键粒度太粗** ⇒ ★★ 一个例外
//	★★ **顺带赦免了同一文件里的其它位点** ⇒ ★★ 变异（把 node 探针的 chat 腿
//	★★ 改成裸字面量 32）⇒ 门**照样绿** ⇒ ★★★ **例外台账的键粒度太粗 ⇒
//	★★★ 一个例外赦免了整类**（★ §401 通用式八）。
//	⇒ ⇒ ★★ 加中间那段**锚点子串**：它必须出现在【该位点所在行或其前 3 行】内
//	⇒ ⇒ ★★ 于是「例外」只能赦免**它指名的那一个位点**。
//
// ★ 锚点用**内容**而不是行号 ⇒ ★ 行号平移（★ 本仓已因此踩过）不会让它失配。
//
// ★★ 台账里每一条都必须写清「为什么它不适用推理模型的正文预算下限」
// ★★ 或「它是【已知未修】的同族缺陷，登记为 D13 子项」⇒ ★★ **台账不是万能钥匙**。
var probeBudgetBareLiteralExceptions = map[string]string{
	// 音频（TTS）探针：max_tokens 约束的是【音频分段】，与【文本正文长度】无关
	// ⇒ 不适用推理模型的正文预算下限。
	"bg/node_probe.go|directProbeBody|#1|32": "音频（TTS）探针：max_tokens 约束的是【音频分段】，与【文本正文长度】无关 ⇒ 不适用推理模型的正文预算下限。",
	// 音频输入探针：同上。
	"bg/node_probe.go|directProbeBody|#2|32": "音频输入探针：max_tokens 约束的是【音频分段】，与【文本正文长度】无关 ⇒ 不适用推理模型的正文预算下限。",
	// 工具调用探针：请求体要求模型「调用工具」⇒ max_tokens 的语义是
	// 「工具调用的参数长度」，不是「正文长度」⇒ 不适用推理模型的正文预算下限。
	"bg/credential_selfcheck.go|isSelfcheckRateLimit|#2|64": "工具调用探针体（要求模型只调用 get_current_time）⇒ max_tokens 约束的是【工具调用参数】长度，与正文长度无关 ⇒ 不适用推理模型的正文预算下限。",

	// ★★★ 下面四条是 ★★【已知未修】的同族缺陷★★（★ D13 子项 ⇒ 属主裁决项）：
	// ★ ① admin/node_operations.go 的 chat 腿与 messages 腿仍写死 `max_tokens: 1`
	// ★    ⇒ ★★ **同文件紧挨着的注释就是现场事故证据**：
	// ★    「此前恒走 chat + max_tokens=1，vapeur 对 gpt-5.6-terra 直接 400
	// ★     "Could not finish the message because max_tokens or model output limit was reached"」
	// ★    ⇒ ⇒ 2026-10-13 那轮把 `bg/probe_http.go` 与 `bg/node_probe.go` 迁掉了，
	// ★    ★★ **admin 侧这两处没有一起迁** ⇒ ★★ 同族的「漏迁」。
	// ★ ② admin/provider_probe.go 的 chat 腿写死 `max_tokens: 5`、messages 腿写死 1。
	// ★ ★★ 本台账**只登记、不放行**：★ 门的作用是让这些位点【可见、可枚举】，
	// ★ ★★ 而不是把「已知缺陷」变成「合法常量」。
	"admin/node_operations.go|newCredentialSessionPingResponse|#1|1": "★ D13 子项（未修）：admin 运维探针的 messages 腿仍写死 max_tokens=1；同文件注释自证该值曾导致上游 400（2026-09-25 vapeur 事故）⇒ 与 2026-10-13 修过的 bg/probe_http.go / bg/node_probe.go 同族但【漏迁】。",
	"admin/node_operations.go|newCredentialSessionPingResponse|#3|1": "★ D13 子项（未修）：admin 运维探针的 chat 腿仍写死 max_tokens=1；同文件注释自证该值曾导致上游 400（2026-09-25 vapeur 事故）⇒ 与 2026-10-13 修过的 bg/probe_http.go / bg/node_probe.go 同族但【漏迁】。",
	"admin/provider_probe.go|doChatProbe|#1|5":                       "★ D13 子项（未修）：admin 运维探针的 chat 腿仍写死 max_tokens=5；与 2026-09-25 vapeur 事故同族。",
	"admin/provider_probe.go|doMessagesProbe|#1|1":                   "★ D13 子项（未修）：admin 运维探针的 messages 腿仍写死 max_tokens=1；与 2026-09-25 vapeur 事故同族。",
}

// probeBudgetCoverageSitePattern 匹配请求体里的内容预算位点。
var probeBudgetCoverageSitePattern = regexp.MustCompile(`"(?:max_output_tokens|max_tokens)"\s*:\s*([^,}\n]+)`)

// probeBudgetCoverageValuePattern 匹配一个已知的、内容显式的值形态。
var probeBudgetCoverageValuePattern = regexp.MustCompile(`^providercap\.(ProbePingMaxTokens|ResponsesProbeMaxOutputTokens)$`)

// probeBudgetCoverageFormatVerbPattern 匹配「把常量当格式化参数传」的形态。
//
// ★ 这是本仓的**主流写法之一**（`bg/self_check_worker.go` 的
// `fmt.Sprintf(... "max_tokens":%d ..., providercap.ProbePingMaxTokens)`）⇒
// ⇒ ★★ 判据若只认「字面量引用」，就会把这种**正当**写法判成违规 ⇒
// ⇒ ★★ 那就是「按旧惯例写的判据对「按本仓惯例写出来的正当代码」全盲」。
var probeBudgetCoverageFormatVerbPattern = regexp.MustCompile(`^%[dv]$`)

// probeBudgetCoverageIdentPattern 匹配「一个裸标识符」（形参 / 局部变量）。
//
// ★★ 这是 R152 v1 退役门踩过的**同一个形态**（「形参取值」）：★ 工具解析不出
// ★★ 形参的取值 ⇒ ★★ 判据若把它当「未知表达式」一律报红 ⇒ ★★ 会误报
// ★★ 本仓的**正当**写法（`bg/credential_probe_v2.go` 的 `miniChat(..., maxTokens int)`
// ★★ 的调用方传的是 `providercap.ProbePingMaxTokens`）。
var probeBudgetCoverageIdentPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// probeBudgetCoverageRepoRoot 定位仓根。
func probeBudgetCoverageRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位仓根")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// probeBudgetCoverageStripComments 去掉**整行注释**后再扫描。
//
// ★ 为什么需要它：这道门是**文本**判据 ⇒ 注释里写 `"max_tokens": 32`
// 也会被命中 ⇒ ⇒ 门把「历史说明」判成「现状」⇒ ★★ 与本仓
// `sql/schema` 的 `stripShellComments`、以及两道 probe 门的
// `probe*StripCommentLines` 同一教训。
func probeBudgetCoverageStripComments(src string) string {
	kept := make([]string, 0, 128)
	for _, ln := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "//") {
			continue
		}
		kept = append(kept, ln)
	}
	return strings.Join(kept, "\n")
}

// probeBudgetCoverageClassify 把一个位点的值分类。
//
// 返回 (kind, ok)：
//
//	"ping"        —— `providercap.ProbePingMaxTokens`（文本探针腿）★ 合格；
//	"responses"   —— `providercap.ResponsesProbeMaxOutputTokens`（responses 腿）；
//	"format"      —— `%d` / `%v`（常量当格式化参数传）；
//	"indirect"    —— 一个裸标识符（★ **形参 / 局部变量**）；
//	"literal"     —— 裸整数；
//	"unknown"     —— ★ 不认识的形态（**必须红**）。
func probeBudgetCoverageClassify(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if m := probeBudgetCoverageValuePattern.FindStringSubmatch(value); m != nil {
		if m[1] == "ProbePingMaxTokens" {
			return "ping", true
		}
		return "responses", true
	}
	if probeBudgetCoverageFormatVerbPattern.MatchString(value) {
		return "format", true
	}
	if probeBudgetCoverageIdentPattern.MatchString(value) {
		return "indirect", true
	}
	if _, err := strconv.Atoi(value); err == nil {
		return "literal", true
	}
	return "unknown", false
}

// probeBudgetBareLiteralMatch 在例外台账里找一个**能唯一定位这个位点**的条目。
//
// ★★★ 键 = "<文件>|<函数名>|<函数内序号>|<字面量>"（**四段**）。
//
// ★★★ 为什么用「函数名 + 函数内序号」而不是「锚点子串」（★ 本轮两次「变异不红」）：
//
//	★ 第一版：键只有「文件|字面量」两段 ⇒ ★★ **粒度太粗** ⇒ 一个例外
//	★★ **顺带赦免了同一文件里的其它位点** ⇒ ★★ 变异不红。
//	★ 第二版：中间加「锚点子串」⇒ ★★ 但锚点若是**注释**，而扫描前**注释已被剥掉**
//	★★ ⇒ ★★ 锚点永远命中不了 ⇒ ★★ 台账条目全部「命中不到」⇒ ★★ 门自己红了。
//	★ ⇒ ⇒ ★★★ 第三版：**函数名 + 函数内序号** ⇒ ★★ 既唯一、又 **gofmt-proof**
//	★★ （不依赖空白/注释/行号）⇒ ★★ 且 ★★ **新增一个位点必然落在新序号上**
//	★★ ⇒ ★★ 匹配不到任何旧例外 ⇒ ★★ 门**必红**（★ 这正是我们要的）。
func probeBudgetBareLiteralMatch(rel, funcName, ordinal, value string) (string, bool) {
	for key, reason := range probeBudgetBareLiteralExceptions {
		parts := strings.SplitN(key, "|", 4)
		if len(parts) != 4 {
			continue
		}
		if parts[0] != rel || parts[1] != funcName || parts[2] != ordinal || parts[3] != value {
			continue
		}
		return reason, true
	}
	return "", false
}

// probeBudgetCoverageFuncName 找出位点所在函数的���名（★ 向上找最近的 `func ` 行）。
func probeBudgetCoverageFuncName(lines []string, idx int) string {
	for i := idx; i >= 0 && i > idx-80; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(trimmed, "func ") {
			continue
		}
		rest := strings.TrimPrefix(trimmed, "func ")
		if idx := strings.Index(rest, "("); idx > 0 {
			return strings.TrimSpace(rest[:idx])
		}
	}
	return "?"
}

// TestProbeContentBudgetEverySiteIsExplicit 钉死「探针的内容预算位点必须显式」。
func TestProbeContentBudgetEverySiteIsExplicit(t *testing.T) {
	root := probeBudgetCoverageRepoRoot(t)

	// ★ 清单自检：文件必须存在（改名 ⇒ 本门自己红，而不是静默失去覆盖面）。
	for _, rel := range probeBudgetCoverageFiles {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("扫描面清单里的 %s 不存在 ⇒ ★★ 请更新清单（★ 本门不允许静默失去覆盖面）: %v", rel, err)
		}
	}

	pingSites, responseSites := 0, 0
	for _, rel := range probeBudgetCoverageFiles {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", rel, err)
		}
		code := probeBudgetCoverageStripComments(string(raw))
		lines := strings.Split(code, "\n")
		// ★ 每个函数内的位点序号（★ 键的第三段）。
		funcOrdinal := map[string]int{}
		for i, ln := range lines {
			m := probeBudgetCoverageSitePattern.FindStringSubmatch(ln)
			if m == nil {
				continue
			}
			funcName := probeBudgetCoverageFuncName(lines, i)
			funcOrdinal[funcName]++
			ordinal := "#" + strconv.Itoa(funcOrdinal[funcName])
			value := strings.TrimSpace(m[1])
			kind, ok := probeBudgetCoverageClassify(value)
			switch kind {
			case "ping":
				pingSites++
			case "responses":
				responseSites++
				if reason, listed := probeBudgetResponsesLegFiles[rel]; !listed {
					t.Errorf("%s 里出现 responses 腿的内容预算位点（值 = %s）⇒ ★★ 它**不在** responses 腿台账里 "+
						"⇒ ⇒ 请补进台账并写清理由（★ D13：是否把 responses 腿的预算也提到 1024）", rel, value)
				} else if strings.TrimSpace(reason) == "" {
					t.Errorf("responses 腿台账里 %s 的理由是空的 ⇒ 例外表必须**具名 + 有理由**", rel)
				}
			case "format", "indirect":
				// ★ 格式动词 / 形参形态：**同文件**必须引用 providercap.ProbePingMaxTokens，
				// 否则这个值的来源不明 ⇒ ⇒ 红。
				//
				// ★★ 诚实边界（★ 与 R152 v1 门同族的残余盲区）：本门只看得到
				// ★★ 「同文件引用了那个具名常量」⇒ ★★ **形参最终被喂成什么，
				// ★★ 本门看不见**（那要跨函数解析）⇒ ⇒ 残余风险登记在审计档。
				if !strings.Contains(string(raw), "providercap.ProbePingMaxTokens") {
					t.Errorf("%s 里出现%s形态的内容预算位点（值 = %s）⇒ ★★ 但该文件**没有**引用 "+
						"providercap.ProbePingMaxTokens ⇒ ⇒ 这个预算的来源不明 ⇒ 请显式化",
						rel, kind, value)
				}
			case "literal":
				reason, listed := probeBudgetBareLiteralMatch(rel, funcName, ordinal, value)
				if !listed {
					t.Errorf("%s|%s|%s|%s 里出现**裸字面量**的内容预算位点（值 = %s）⇒ ★★ 请改成具名常量 "+
						"（providercap.ProbePingMaxTokens）；★ 若确有理由（如音频探针），请登记进例外台账 "+
						"（key = \"<文件>|<函数名>|<函数内序号>|<值>\"）并写清理由",
						rel, funcName, ordinal, value, value)
				} else if strings.TrimSpace(reason) == "" {
					t.Errorf("裸字面量例外台账里 %s|%s|%s|%s 的理由是空的 ⇒ 例外表必须**具名 + 有理由**",
						rel, funcName, ordinal, value)
				}
			default:
				if !ok {
					t.Errorf("%s 里出现**无法识别**的内容预算位点（值 = %q）⇒ ★★ 「不可识别」不允许被默认放行 "+
						"⇒ 请改成显式字面量或具名常量引用", rel, value)
				}
			}
		}
	}

	// ★ 正向判据：文本探针腿必须存在且用 PingMaxTokens。
	// ★ 若有人把 ProbePingMaxTokens 改回「引用 ResponsesProbeMaxOutputTokens」，
	// ★ 本门看不见（值仍是常量引用）⇒ ⇒ 那由 probe_budget_gates_test.go 的
	// ★ 「值 ≥ 1024」那条负责（★ 两门互补）。
	if pingSites == 0 {
		t.Errorf("扫描面里没有任何 providercap.ProbePingMaxTokens 的位点 ⇒ ★★ 文本探针腿消失了？")
	}
	if responseSites == 0 {
		t.Errorf("扫描面里没有任何 providercap.ResponsesProbeMaxOutputTokens 的位点 ⇒ ★★ responses 腿消失了？")
	}
}

// TestProbeContentBudgetDetectorHasTeeth 是阳性对照：故意写坏的合成片段必须被报出。
func TestProbeContentBudgetDetectorHasTeeth(t *testing.T) {
	bad := []string{
		`"max_tokens": 32`,                         // 裸字面量（未登记）
		`"max_output_tokens": providercap.Unknown`, // 不认识的具名常量
		`"max_output_tokens": 32 + 1`,              // 表达式（来源不明）
		`"max_output_tokens": foo.Bar`,             // 带点的未知表达式
	}
	for _, snippet := range bad {
		m := probeBudgetCoverageSitePattern.FindStringSubmatch(snippet)
		if m == nil {
			t.Fatalf("合成片段 %q 竟未被位点模式识别 ⇒ 检测器本身失效", snippet)
		}
		kind, ok := probeBudgetCoverageClassify(strings.TrimSpace(m[1]))
		if ok && (kind == "ping" || kind == "responses") {
			t.Errorf("合成片段 %q 被判成合格形态 %q ⇒ ★★ 检测器没有牙", snippet, kind)
		}
	}

	good := []string{
		`"max_tokens": providercap.ProbePingMaxTokens`,
		`"max_output_tokens": providercap.ResponsesProbeMaxOutputTokens`,
		`"max_tokens": %d`,
		`"max_tokens": maxTokens`,
	}
	for _, snippet := range good {
		m := probeBudgetCoverageSitePattern.FindStringSubmatch(snippet)
		if m == nil {
			t.Fatalf("合成片段 %q 竟未被位点模式识别 ⇒ 检测器本身失效", snippet)
		}
		kind, ok := probeBudgetCoverageClassify(strings.TrimSpace(m[1]))
		if !ok || kind == "unknown" {
			t.Errorf("合成片段 %q 被判成不合格形态 %q ⇒ ★★ 检测器过严（误报）", snippet, kind)
		}
	}
}

// TestProbeContentBudgetLedgersAreConsistent 台账自洽性：台账里的条目不许指向
// 「已经不存在」的位点（否则台账会无声地变成陈旧文档）。
func TestProbeContentBudgetLedgersAreConsistent(t *testing.T) {
	root := probeBudgetCoverageRepoRoot(t)
	seenResponses := map[string]bool{}
	// ★ 每个台账条目必须**至少命中一个**真实位点 ⇒ 用与主判据同一套四段匹配。
	hitLedger := map[string]bool{}
	funcOrdinal := map[string]int{}

	for _, rel := range probeBudgetCoverageFiles {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", rel, err)
		}
		code := probeBudgetCoverageStripComments(string(raw))
		lines := strings.Split(code, "\n")
		for i, ln := range lines {
			m := probeBudgetCoverageSitePattern.FindStringSubmatch(ln)
			if m == nil {
				continue
			}
			funcName := probeBudgetCoverageFuncName(lines, i)
			funcOrdinal[funcName]++
			ordinal := "#" + strconv.Itoa(funcOrdinal[funcName])
			value := strings.TrimSpace(m[1])
			kind, _ := probeBudgetCoverageClassify(value)
			switch kind {
			case "responses":
				seenResponses[rel] = true
			case "literal":
				for key := range probeBudgetBareLiteralExceptions {
					parts := strings.SplitN(key, "|", 4)
					if len(parts) != 4 || parts[0] != rel || parts[3] != value {
						continue
					}
					if parts[1] == funcName && parts[2] == ordinal {
						hitLedger[key] = true
					}
				}
			}
		}
	}

	for rel := range probeBudgetResponsesLegFiles {
		if !seenResponses[rel] {
			t.Errorf("responses 腿台账里的 %s **已经没有** responses 位点了 ⇒ 台账陈旧 ⇒ "+
				"请删除该条目（★ 台账只许缩小，不许空转）", rel)
		}
	}
	for key := range probeBudgetBareLiteralExceptions {
		if !hitLedger[key] {
			t.Errorf("裸字面量例外台账里的 %s **命中不到任何真实位点** ⇒ 台账陈旧或锚点写错 ⇒ "+
				"请删除该条目或修正锚点（★ 台账只许缩小，不许空转）", key)
		}
	}
}
