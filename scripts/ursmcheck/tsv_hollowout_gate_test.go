package ursm_test

// tsv_hollowout_gate_test.go —— 2026-10-23 R85 轮（掏空法门）
//
// # 这道门要钉住的事
//
// ★ R84 轮（§9）用**掏空法**逐份普查了 10 份 `.tsv` 登记册，问同一个问题：
//
//	「**把登记册的数据行全部掏空 ⇒ 读它的门必须红**」
//
//	（「读了个空册子还绿」⇒ 那道门在这种情况下是**瞎**的）。
//
// ★ 该普查的诚实边界（R84 轮原文）：**剩下 18 份没有**被逐份掏空验证
// ⇒ ⇒ **不声称「全仓无此洞」**。
//
// ⇒ ★ 本门把那次普查**变成一道可复跑的登记**：
//	① **覆盖判据**：仓内每一份 `.tsv` 都必须在清单里登记（含例外），
//	   ⇒ ⇒ **新增一份 `.tsv` 而没登记 ⇒ 门红**（这是「防漂移」的引擎）。
//	② **行为判据**：清单里 `expect=red` 的条目 ⇒ **真掏空 + 真跑读方**，
//	   读方必须红（★ 这就是 R84 轮那套手工动作，自动化）。
//	③ **例外表**：`expect=exception` 的条目必须带**实质理由**。
//
// # 为什么必须「行为化」而不是「静态找行数断言」
//
// ★ R84 轮原文：「静态找『哪里写了行数断言』是**模糊**的（写法太多、
// 且断言可能长在调用方）」⇒ ⇒ 本门**不读源码找断言**，而是
// **真的掏空、真的跑读方、看它红不红** ⇒ ⇒ 与写法无关。
//
// # 三条纪律（本线）
//
//	① **判据读自己的输入时，先核对【读出来的形状】与【写进去的形状】一致**
//	   ⇒ ⇒ 掏空变换必须**逐字节**保留被保留的行，且**还原后校验 md5**。
//	② **阴性结论要当缺陷一样复核**：「掏空后仍绿」的第一嫌疑是【探针无效】，
//	   不是「门有洞」⇒ ⇒ 本门把「变换是否真的掏掉了行」单列成一个判据。
//	③ **守门人的计数若与被守的门共用谓词 ⇒ 同源自证** ⇒ ⇒ 本门
//	   **不数登记册的行数**，只问「读方红不红」。

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// hollowInventoryFile 是本门的清单（★ 它自己也是 `.tsv` ⇒ 覆盖判据要豁免它）。
const hollowInventoryFile = "tsv_hollowout_inventory.tsv"

// hollowEntry 是清单里的一行。
type hollowEntry struct {
	Path       string // 仓内相对路径（被掏空的登记册）
	ReaderKind string // "gotest" | "shell"
	ReaderSpec string // gotest:<pkg>:<testName> | shell:<script>
	Expect     string // "red" | "exception"
	HeaderRows int    // 保留几行「列名行」（0 = 该文件没有列名行）
	Reason     string // 理由（expect=exception 时必须非空且非占位）
	MechCheck  string // ★★ expect=exception 时必填：★【机械可核】的那一段断言（★ 逗号分隔）
	// ★★ 「人工审计」的那一段仍然写在 Reason 里 ⇒ ⇒ 两者合起来才是完整理由。
}

// hollowMechKinds 是【机械可核断言】的封闭词表（★ R86 轮新增；★ R87 轮扩容）。
//
// ★★ 为什么要有它（R86 轮，承接 §19.25.19.10 优先级 1 ②）：
//
//	★ 上一轮的例外理由是**一整段散文** ⇒ ⇒ 门的机械部分只能证明「理由非空非占位」
//	  ⇒ ⇒ **理由本身成立与否，机器无从判断**（★ 诚实的边界，但很弱）。
//	★ ⇒ R86 轮把理由**拆成两段**：
//	  ① **机械可核段**（本字段）：★ 门会**真的去核对** ⇒ ⇒ 理由的「骨架」不再靠人说了算。
//	  ② **人工审计段**（Reason）：★ 只能靠人读 ⇒ ⇒ 如实标注，不假装能机检。
//
// ★ 词表刻意**短**（★ 只实现真在用的）⇒ ⇒ 每一条都有真实用例 ⇒ 不留空转机制。
//
// ★★ R87 轮扩容的两条（★ 承接 §19.25.19.10 优先级 1 ②）：
//
//	★ **`reader-red-on-missing`** —— 「读方在【登记册文件**不在**】时必须红」
//	  ⇒ ⇒ 它比 `rows=0` **强得多**：
//	    · `rows=0` 只说「它现在恰好是 0 行」⇒ ⇒ 明天它长出一行 ⇒ ⇒ 理由就过期了
//	    · `reader-red-on-missing` 说「**这个文件本身是承重的**」
//	      ⇒ ⇒ 文件一消失，读方立刻红 ⇒ ⇒ 事实源丢了不会被静默放过
//	  ⇒ ⇒ 用例：`scripts/ursmcheck/cjk_var_sites.tsv`
//	    （★ 它的读方 `TestCJKVarExpansionNoNewSites` 在登记册**读不到**时 `t.Fatalf`）。
//	★ **`reader-red-on-hollow`** —— 「读方在【**掏空版**】下必须红」
//	  ⇒ ⇒ 这是**最强**的一条：★ 它直接断言「掏空这份登记册 ⇒ 门会抓到」
//	  ⇒ ⇒ 用例：**结构化登记册那一侧**（★ `structured-registry-census.json` 的
//	    `hollow_mode` + `red_signal` ⇒ ⇒ 每条登记册都跑这条）⇒ ⇒ 与本词表**同源**
//	    ⇒ ⇒ 两侧的词表不会漂。
var hollowMechKinds = map[string]bool{
	"rows=0":                true, // ★ 该登记册当前 0 数据行（★ 「掏空 = 空操作」这类理由的骨架）
	"reader-green":          true, // ★ 该条目指定的读方在【基线】是绿的（★ 两向判据类理由的骨架）
	"no-code-reader":        true, // ★ 仓内（除文档与本清单外）没有任何被追踪文件提及该路径
	"reader-red-on-missing": true, // ★★ R87 轮：★ 读方在【登记册文件不在】时必须红
	"reader-red-on-hollow":  true, // ★★ R87 轮：★ 读方在【掏空版】下必须红（★ 结构化侧的主力断言）
}

// hollowMechKindList 返回词表的稳定排序列表（★ 只用于报错信息）。
func hollowMechKindList() []string {
	out := make([]string, 0, len(hollowMechKinds))
	for k := range hollowMechKinds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// hollowOutcome 是某一条目的实测结果分类。
type hollowOutcome string

const (
	outcomeRedCaught    hollowOutcome = "red-caught"   // ★ 掏空 ⇒ 读方红 ⇒ 有牙
	outcomeStillGreen   hollowOutcome = "still-green"  // ★ 掏空 ⇒ 读方仍绿 ⇒ 读方瞎
	outcomeInconclusive hollowOutcome = "inconclusive" // ★ 变换是空操作 ⇒ 什么都没测到
	outcomeBaselineRed  hollowOutcome = "baseline-red" // ★ 读方在基线就红 ⇒ 不可判定
)

// hollowRepoRoot 返回仓根。
func hollowRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("解析仓根失败（需要 git 在 PATH）：%v", err)
	}
	return strings.TrimSpace(string(out))
}

// readHollowInventory 读清单。
func readHollowInventory(t *testing.T, root string) []hollowEntry {
	t.Helper()
	path := filepath.Join(root, "scripts", "ursmcheck", hollowInventoryFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读清单失败 %s：%v", path, err)
	}
	var entries []hollowEntry
	lines := strings.Split(string(data), "\n")
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.Split(ln, "\t")
		if len(parts) < 6 {
			t.Errorf("清单第 %d 行缺列（需要 6 列，实际 %d）：%q", i+1, len(parts), ln)
			continue
		}
		if strings.TrimSpace(parts[0]) == "tsv_path" {
			continue // ★ 表头行（★ 不依赖它出现在第几行 ⇒ 清单前可以有注释块）
		}
		hr := 0
		if parts[4] != "" {
			n := 0
			for _, c := range parts[4] {
				if c < '0' || c > '9' {
					n = -1
					break
				}
				n = n*10 + int(c-'0')
			}
			if n < 0 {
				t.Errorf("清单第 %d 行的 header_rows 不是整数：%q", i+1, parts[4])
				continue
			}
			hr = n
		}
		mech := ""
		if len(parts) >= 7 {
			mech = strings.TrimSpace(parts[6])
		}
		entries = append(entries, hollowEntry{
			Path:       strings.TrimSpace(parts[0]),
			ReaderKind: strings.TrimSpace(parts[1]),
			ReaderSpec: strings.TrimSpace(parts[2]),
			Expect:     strings.TrimSpace(parts[3]),
			HeaderRows: hr,
			Reason:     strings.TrimSpace(parts[5]),
			MechCheck:  mech,
		})
	}
	return entries
}

// hollowBytes 返回「掏空后」的内容：★ 逐字节保留注释行与前 headerRows 行非注释内容，
// 丢弃其余非注释行（数据行）。
//
// ★ 返回 (新内容, 丢弃的数据行数, 保留的非注释行数)。
// ⇒ 后两个返回值是【判据②「探针是否有效」】的量具：★ 若丢弃数为 0
// ⇒ ⇒ 变换没真的掏空 ⇒ ⇒ 本条目**什么都没测到**（不能报「绿 ⇒ 门有洞」）。
func hollowBytes(data []byte, headerRows int) ([]byte, int, int) {
	lines := strings.SplitAfter(string(data), "\n")
	var out []string
	kept := 0
	dropped := 0
	for _, ln := range lines {
		if ln == "" {
			continue
		}
		body := strings.TrimRight(ln, "\n")
		if strings.TrimSpace(body) == "" || strings.HasPrefix(strings.TrimSpace(body), "#") {
			out = append(out, ln) // ★ 注释行与空行：逐字节保留
			continue
		}
		if kept < headerRows {
			out = append(out, ln)
			kept++
			continue
		}
		dropped++
	}
	return []byte(strings.Join(out, "")), dropped, kept
}

// countDataRows 数「非注释、非空」的行数。
func countDataRows(data []byte) int {
	n := 0
	for _, ln := range strings.Split(string(data), "\n") {
		body := strings.TrimSpace(ln)
		if body == "" || strings.HasPrefix(body, "#") {
			continue
		}
		n++
	}
	return n
}

// goBinDir 返回 go 可执行文件所在目录（用于给子进程补 PATH）。
func goBinDir() string {
	if p, err := exec.LookPath("go"); err == nil {
		return filepath.Dir(p)
	}
	if gr := runtime.GOROOT(); gr != "" {
		return filepath.Join(gr, "bin")
	}
	return ""
}

// runHollowReader 跑读方，返回退出码与合并输出。
//
// ★ gotest:<pkg>:<testName> ⇒ `go test <pkg> -run ^<testName>$ -count=1`
// ★ shell:<script>          ⇒ `bash <script>`
//
// ★★ 2026-10-24 12h 审计轮：gotest 臂改成「**每个包只编译一次**」。
//
//	★ 为什么（原实现跑不完）：
//	  `go test <pkg> -run ^X$ -count=1` 每次都要**重新构建 + 链接**该包的
//	  测试二进制（★ 实测 scripts/ursmcheck：106 个门文件，单次 ≈ 19 s）
//	  ⇒ ⇒ 本门对每条 expect=red 的条目要跑【两次】（基线 + 掏空后）
//	  ⇒ ⇒ 19 条 × 2 次 × 19 s ≈ 12 分钟 ⇒ ⇒ **撞破父套件的 900 s 预算**
//	  ⇒ ⇒ ★ 实测：父套件 `go test ./scripts/ursmcheck -timeout=900s`
//	  在 **origin/main（1c2978c11）上实测 900.591 s 超时**
//	  ⇒ ⇒ ★ 而本门**单独跑**只要 220.81 s ⇒ ⇒ 差值就是这批重复构建。
//
//	★ 修法与它的等价性：
//	  每个被探的包只 `go test -c` 编译一次，之后直接跑那个二进制。
//	  ⇒ 编译 19 s → 1.9 s（一次性）；每次探针 19 s → **0.011 s**
//	  ⇒ ⇒ 36 次探针 ≈ 0.4 s ⇒ ⇒ 本门从 220 s 降到 ~40 s。
//
//	★ ★ 必须复刻 `go test` 的两个语义，否则探针会【假红】：
//	  ① **cwd = 包目录**（★ `go test` 把测试二进制放在【包目录】里跑；
//	     仓内多个门用相对路径读同目录的台账 ⇒ 从仓根跑会
//	     「open xxx.tsv: no such file or directory」⇒ 假红）。
//	  ② **-test.count=1**（★ 禁用测试结果缓存 ⇒ 与原 `-count=1` 同义）。
func runHollowReader(kind, spec string, root string, timeout time.Duration) (int, string, error) {
	return runHollowReaderOverlay(kind, spec, root, timeout, "", "")
}

// runHollowReaderOverlay 是【带 overlay 的读方】（★ R86 轮新增，★ 负控臂用）。
//
// ★ overlay 非空 ⇒ 编译测试二进制时用 `go build -overlay <file>` ⇒ ⇒
// ★★ **仓内工作树一个字节都不用改** ⇒ ⇒ 「把真实读方的红条件去掉」这件事
// ★★ 可以在不动源码的前提下做到（★ 这是负控臂的安全前提：
// ★★ 并行会话在同一个 checkout 里跑全套时，不会编译到一个「被改瞎的」包）。
//
// ★ overlay 为空 ⇒ 与 runHollowReader **完全等价**（★ 同一个实现，只是没有 overlay）。
//
// ★★ contentKey（R87 轮新增，★ 非 `.tsv` 登记册掏空门用）：
// ★ 读方**在运行时**读登记册 ⇒ 掏空落盘后立刻生效 ⇒ contentKey 可以是空串。
// ★ 但**读方把登记册编译进二进制**时（★ Go 的 `//go:embed`）⇒ ⇒ 掏空落盘后
// ★ **旧二进制里的内容不变** ⇒ ⇒ 读方会跑到「没被掏空」的内容 ⇒ ⇒ 分类会误成
// ★ 「仍绿」⇒ ⇒ **假绿**。
// ★ ⇒ ⇒ contentKey 必须带上「被掏空文件的指纹」⇒ ⇒ 二进制缓存键因此不同
// ★ ⇒ ⇒ 掏空后会**重新编译** ⇒ ⇒ `go:embed` 的内容跟着变。
func runHollowReaderOverlay(kind, spec string, root string, timeout time.Duration, overlay string, contentKey string) (int, string, error) {
	goBin := "go"
	if p, err := exec.LookPath("go"); err == nil {
		goBin = p
	} else if gr := runtime.GOROOT(); gr != "" {
		goBin = filepath.Join(gr, "bin", "go")
	}
	var cmd *exec.Cmd
	cmdDir := root
	switch kind {
	case "gotest":
		// ★ 规格 = `<pkg>|<TestName>`（★ 用 `|` 而不是 `:`：Go 的 pkg 路径本身带 `/`，
		//   而 `:` 在 pkg 里也可能出现 ⇒ ⇒ 用 `|` 才无歧义）。
		parts := strings.SplitN(spec, "|", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return -1, "", fmt.Errorf("gotest 规格格式错（要 <pkg>|<TestName>）：%q", spec)
		}
		// ★★ 关键（R87 轮）：★ 包目录不存在 ⇒ 必须**报错**⇒ ⇒ 不能让它落进
		// ★★ `go test <不存在的包>`（★ 那会打印一句错误并**退出码非 0**⇒ ⇒
		// ★★ 分类器会把它判成「red-caught」⇒ ⇒ **假的有牙**）。
		// ★★ 理由同 vitest 臂：★ 「读方没跑起来」必须与「读方跑了并且绿」**可区分**。
		if _, err := os.Stat(hollowPkgDir(parts[0], root)); err != nil {
			return -1, "", fmt.Errorf("gotest 读方的包目录不存在（%s）：%v", parts[0], err)
		}
		// ★ 优先走【预编译二进制】（★ 每个包只编译一次）；编译不了就回落到
		//   原来的 `go test` 形态（★ 语义完全相同，只是慢）。
		if bin, binDir, binErr := hollowTestBinaryOverlay(goBin, parts[0], root, timeout, overlay, contentKey); binErr == nil {
			cmd = exec.Command(bin, "-test.run", "^"+parts[1]+"$", "-test.count=1")
			// ★ cwd = 包目录（★ 复刻 `go test` 的语义；★ 不是仓根！）
			cmdDir = binDir
		} else {
			cmd = exec.Command(goBin, "test", parts[0], "-run", "^"+parts[1]+"$", "-count=1")
		}
	case "node":
		// ★ 规格 = `<脚本路径，相对仓根>`（★ 如 `web/scripts/i18n-empty-state-guard.mjs`）。
		//
		// ★★ cwd 的定法（R87 轮）：★ **离脚本最近的、含 `package.json` 的祖先目录**
		//   ⇒ ⇒ `web/scripts/*.mjs` ⇒ cwd = `web/`（★ 与 `npm run` 的语义一致）
		//   ⇒ ⇒ 仓根没有 `package.json` ⇒ 兜底回仓根。
		//   ★★ 为什么不能固定成仓根：★ `web/scripts/color-baseline-stale.mjs` 用
		//   `process.cwd()` 推算 `scripts/color-token-audit.mjs` 的位置 ⇒ ⇒
		//   cwd 必须是 `web/` ⇒ ⇒ 否则读方会因「找不到文件」而红 ⇒ ⇒ **假红**。
		if strings.TrimSpace(spec) == "" {
			return -1, "", fmt.Errorf("node 规格格式错（要 <脚本路径，相对仓根>）")
		}
		scriptAbs := filepath.Join(root, spec)
		if _, err := os.Stat(scriptAbs); err != nil {
			return -1, "", fmt.Errorf("node 读方脚本不存在（%s）：%v", spec, err)
		}
		nodeBin := "node"
		if p, err := exec.LookPath("node"); err == nil {
			nodeBin = p
		}
		cmd = exec.Command(nodeBin, scriptAbs)
		cmdDir = hollowNearestPackageRoot(root, spec)
	case "vitest":
		// ★ 规格 = `<测试文件路径，相对仓根>`（★ 如 `web/src/i18n/localeKeyParity.test.ts`）。
		//
		// ★★ 用**仓内**的 vitest（`node_modules/.bin/vitest`）⇒ ⇒ 不走 npx ⇒ ⇒
		//   不会联网、不会临时下载 ⇒ ⇒ 读数的确定性更高。
		// ★★ cwd = 离测试文件最近的、含 `package.json` 的祖先目录（★ = `web/`）
		//   ⇒ ⇒ 测试文件参数用**相对 cwd** 的路径（★ 与 `npm test` 的语义一致）。
		if strings.TrimSpace(spec) == "" {
			return -1, "", fmt.Errorf("vitest 规格格式错（要 <测试文件路径，相对仓根>）")
		}
		pkgRoot := hollowNearestPackageRoot(root, spec)
		bin := filepath.Join(pkgRoot, "node_modules", ".bin", "vitest")
		if _, err := os.Stat(bin); err != nil {
			return -1, "", fmt.Errorf("vitest 读方找不到 vitest 可执行文件（%s）：%v\n★ 先在 %s 装依赖", bin, err, pkgRoot)
		}
		testAbs := filepath.Join(root, spec)
		if _, err := os.Stat(testAbs); err != nil {
			// ★★ 关键（R87 轮）：★ 测试文件不存在 ⇒ 必须**报错**⇒ ⇒ 不能让它跑成
			// ★★ 「No test files found」那种**非零退出**⇒ ⇒ 更不能让它变成**退出码 0**。
			// ★★ 理由：★ 「读方没跑起来」与「读方跑了并且绿」在退出码上**不可区分**
			// ★★ ⇒ ⇒ 负控臂的「still-green」结论就可能是**假警报**（★ 探针压根没跑）。
			return -1, "", fmt.Errorf("vitest 读方的测试文件不存在（%s）：%v", spec, err)
		}
		rel, err := filepath.Rel(pkgRoot, testAbs)
		if err != nil {
			return -1, "", fmt.Errorf("vitest 规格路径换算失败（%s）：%v", spec, err)
		}
		cmd = exec.Command(bin, "run", rel)
		cmdDir = pkgRoot
	case "shell":
		// ★ shell:<command> ⇒ `bash -c <command>`（cwd = 仓根）
		//   ⇒ 允许写任意命令（★ 例如 `cd installer && go test ...`：
		//   installer 是**独立 Go module**，主 module 的 `./installer/...` 不可用）。
		cmd = exec.Command("bash", "-c", spec)
	default:
		return -1, "", fmt.Errorf("未知 reader_kind：%q", kind)
	}
	cmd.Dir = cmdDir
	// ★★ r1046：★ 用 `hollowBuildEnv`（★ 而不是裸 `os.Environ()`）
	// ★★ ⇒ ⇒ 它会把 `GOCACHE` **覆盖**成掏空门的**专用目录**
	// ★★ ⇒ ⇒ 子构建不再往用户的全局缓存里塞「随树变化而失效」的一次性产物。
	cmd.Env = hollowBuildEnv(root)
	out, err := runWithTimeout(cmd, timeout)
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			return -1, string(out), err
		}
	}
	return code, string(out), nil
}

// hollowBinCache 缓存「包 → 已编译的测试二进制」路径（★ 每个包只编译一次）。
var (
	hollowBinMu    sync.Mutex
	hollowBinCache = map[string]string{}
)

// hollowTestBinary 为一个包编译测试二进制并缓存（★ 编译失败返回错误 ⇒ 调用方回落）。
//
// ★ 返回的第二个值是【包的绝对目录】——★ `go test` 把测试二进制放在包目录里跑，
//
//	所以直接跑二进制时 cwd 必须复刻成同一个目录，否则仓内那些用相对路径读
//	同目录台账的门会「open xxx.tsv: no such file」⇒ 假红。
func hollowTestBinary(goBin, pkg, root string, timeout time.Duration) (string, string, error) {
	return hollowTestBinaryOverlay(goBin, pkg, root, timeout, "", "")
}

// hollowTestBinaryOverlay 是【带 overlay 的测试二进制编译】（★ R86 轮新增）。
//
// ★ overlay 非空 ⇒ 编译时加 `-overlay <file>` ⇒ ⇒ 用**改瞎的副本**替换掉仓内某个源文件
// ★★ 而**不修改工作树里的那个文件**。
//
// ★ 缓存键必须**带上 overlay**（★ 否则「改瞎的二进制」会被缓存下来，
// ★★ 后面那条【未改瞎】的读方就会跑到一个被改瞎的二进制上 ⇒ ⇒ 结论会串）。
//
// ★★ R87 轮新增：★ 缓存键还必须**带上 contentKey**（★ 被掏空文件的指纹）
// ★★ ⇒ 因为 Go 的 `//go:embed` 在**编译期**把文件内容嵌进二进制
// ★★ ⇒ ⇒ 掏空落盘后，**旧二进制里仍是原内容** ⇒ ⇒ 读方会误成「仍绿」⇒ ⇒ **假绿**。
// ★★ ⇒ ⇒ 指纹一变 ⇒ 缓存键变 ⇒ **重新编译** ⇒ ⇒ 嵌入内容跟着变。
func hollowTestBinaryOverlay(goBin, pkg, root string, timeout time.Duration, overlay string, contentKey string) (string, string, error) {
	hollowBinMu.Lock()
	defer hollowBinMu.Unlock()
	cacheKey := pkg
	if overlay != "" {
		cacheKey = pkg + "|overlay:" + overlay
	}
	if contentKey != "" {
		cacheKey += "|content:" + contentKey
	}
	if p, ok := hollowBinCache[cacheKey]; ok {
		return p, hollowPkgDir(pkg, root), nil
	}
	tmpDir, err := os.MkdirTemp("", "hollowout-bin-")
	if err != nil {
		return "", "", err
	}
	// ★★ R87 轮收尾：★ **登记**这个临时目录 ⇒ ⇒ 跑完由 `TestMain` 统一删掉。
	//
	// ★ 为什么必须登记：★ 原实现**只创建、从不删除** ⇒ ⇒ 每次跑泄漏一批
	// ★ （★ 一个二进制 ~74 MB ⇒ ⇒ 35 条目 × 2 变体 ⇒ ⇒ **单次跑 GB 级**）
	// ⇒ ⇒ ⇒ 实测本机 tmp 里堆了 **224 个 ＝ 8.6 GB** ⇒ ⇒ ⇒ 把每用户 tmp 配额撑爆
	// ⇒ ⇒ ⇒ ⇒ 然后门里要写临时文件的门就报 **`no space left on device`** ⇒ ⇒ ⇒
	// ⇒ ⇒ ⇒ ★★ **红的原因不指向真正的原因**（★ 真正的原因是磁盘，不是门有洞）。
	// ★ 详见 `scripts/ursmcheck/hollowout_bin_cleanup_test.go`。
	registerHollowBinTmpDir(tmpDir)
	bin := filepath.Join(tmpDir, "testbin")
	args := []string{"test", "-c", "-o", bin}
	if overlay != "" {
		args = append(args, "-overlay", overlay)
	}
	args = append(args, pkg)
	build := exec.Command(goBin, args...)
	build.Dir = root
	// ★★ r1046：★ 用 `hollowBuildEnv`（★ 而不是裸 `os.Environ()`）
	// ★★ ⇒ ⇒ 它会把 `GOCACHE` **覆盖**成掏空门的**专用目录**
	// ★★ ⇒ ⇒ **子构建**不再往用户的全局缓存里塞「随树变化而失效」的一次性产物
	// ★★ ⇒ ⇒ ★ 依据：★ 实测每个新树状态 ⇒ 这道门往全局缓存里塞 **~1.3 GB**
	// ★★ ⇒ ⇒ ★ 而那些条目在树再变之后**永远不会再被命中** ⇒ ⇒ 事实上的垃圾。
	build.Env = hollowBuildEnv(root)
	if out, err := runWithTimeout(build, timeout); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", "", fmt.Errorf("编译 %s 的测试二进制失败：%v\n%s", pkg, err, tailLines(string(out), 10))
	}
	hollowBinCache[cacheKey] = bin
	return bin, hollowPkgDir(pkg, root), nil
}

// hollowPkgDir 复刻 `go test` 的 cwd 语义：测试二进制的 cwd = 包目录。
// ★ 清单里的 pkg 都是相对仓库根的 "./xxx/" 形态。
func hollowPkgDir(pkg, root string) string {
	return filepath.Join(root, strings.TrimPrefix(pkg, "./"))
}

// hollowNearestPackageRoot 返回「离 rel 最近的、含 `package.json` 的祖先目录」的绝对路径。
//
// ★★ 为什么需要它（R87 轮，★ 非 `.tsv` 登记册掏空门）：
//
//	★ `web/` 下的门（node 脚本 / vitest 测试）**都在 `web/` 里跑**
//	  （★ `npm run` 的语义：cwd = 包目录）
//	⇒ ⇒ 读方里那些 `resolve(process.cwd(), 'scripts/…')` 的路径推算才对
//	⇒ ⇒ 固定成仓根会让读方「找不到文件」⇒ ⇒ **假红**。
//
// ★★ 为什么用「最近的含 package.json 的祖先」而不是写死 `web/`：
//
//	★ 写死 `web/` ⇒ 仓里再加一个前端包（★ `web-mobile/` 已经是第二个）⇒ ⇒
//	  新的读方就会在错误的 cwd 里跑 ⇒ ⇒ 假红 ⇒ ⇒ **判据依赖了当前目录布局**。
//	⇒ ⇒ 「最近的含 package.json 的祖先」是**机械可算**的 ⇒ ⇒ 布局变了也不用改判据。
//
// ★ 兜底：找不到 `package.json` ⇒ 回仓根（★ 仓根没有 package.json ⇒ 但 Go 读方不在这里用）。
func hollowNearestPackageRoot(root, rel string) string {
	dir := filepath.Dir(filepath.Join(root, rel))
	for {
		if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return root
		}
		dir = parent
	}
}

// ── ★★ 崩溃安全的还原（2026-10-24 12h 审计轮） ───────────────────────────
//
// ★ 为什么需要（本门原先只有 `defer` 还原）：
//
//	本门把**已跟踪的台账文件**掏空后再跑读方，靠 `defer` 还原。
//	⇒ 而 `defer` **只在正常返回/panic 展开时**执行
//	⇒ ⇒ 父套件把本门**超时杀掉**（SIGKILL / testing 的 timeout panic
//	   在别的 goroutine 触发时）⇒ ⇒ **子测 goroutine 的 defer 不跑**
//	⇒ ⇒ **已跟踪的台账文件留在掏空态**。
//
//	★ 实测（★ 2026-10-24，origin/main 1c2978c11）：
//	  父套件 900.591 s 超时后，工作树里留下 3 个被掏空的**已跟踪**文件：
//	    scripts/audit/monthly_partition_retention.tsv
//	    scripts/ursmcheck/pipefail_race_ledger.tsv
//	    sql/schema/startup_migration_nonplain.tsv
//	⇒ ⇒ ★ 而这正是本仓 §143.87 登记过的病：「变异台在共享工作树里跑 ⇒
//	  并发改动会被误读成门没牙」⇒ ⇒ 本轮把它从「已登记的风险」
//	  升级成「**已实证会污染工作树**」。
//
// ★ 修法 = **备份 + 自愈**：
//	① 掏空前把原内容写进**确定性路径**的备份文件（放在 git 目录里 ⇒ 不污染工作树）；
//	② 正常路径还原后删掉备份；
//	③ ★ **本门每次开跑先把遗留的备份还原回去**（自愈）⇒ ⇒
//	   上一轮被杀留下的残留，会在下一轮开跑时被自动清掉
//	   ⇒ ⇒ 工作树【不会】长期停在掏空态。

// ★★ 2026-10-25 R148 合并轮：本地原有的 4 个备份/自愈辅助
//   （hollowBackupDir / hollowBackupPath / hollowSelfHeal / hollowSaveBackup）
//   **已删除** —— 上游 magpie §3.17 把同一套机制拆到了独立文件
//   scripts/ursmcheck/tsv_hollowout_safety_test.go（自带门 `TestHollowBackupSelfHealRestoresLeftover`），
//   且 `hollowBackupPath` **同名** ⇒ 两套并存 ⇒ 重复声明 ⇒ 编译失败。
//   ⇒ 保留本文件**独有**的改动：gotest 臂改成「每个包只编译一次」（见上）。

// runWithTimeout 跑命令并在超时后杀掉它。
func runWithTimeout(cmd *exec.Cmd, timeout time.Duration) ([]byte, error) {
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return []byte(buf.String()), err
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return []byte(buf.String()), fmt.Errorf("读方超时（%v）", timeout)
	}
}

// md5Of 算 md5（十六进制）。
func md5Of(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

// hollowAllTrackedTsv 返回仓内【git 追踪】的全部 `.tsv`（相对仓根，已排序）。
func hollowAllTrackedTsv(root string) ([]string, error) {
	out, err := exec.Command("git", "-C", root, "ls-files", "-z", "*.tsv").Output()
	if err != nil {
		return nil, err
	}
	var rels []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" {
			continue
		}
		rels = append(rels, p)
	}
	sort.Strings(rels)
	return rels, nil
}

// ---------------------------------------------------------------- 判据

// TestTsvHollowoutInventoryCoversEveryTsv 是【覆盖判据】：
// ★ 仓内每一份 `.tsv` 都必须在清单里登记 ⇒ ⇒ 新增 `.tsv` 而没登记 ⇒ 门红。
func TestTsvHollowoutInventoryCoversEveryTsv(t *testing.T) {
	root := hollowRepoRoot(t)
	all, err := hollowAllTrackedTsv(root)
	if err != nil {
		t.Fatalf("取 git 追踪的 .tsv 失败：%v", err)
	}
	entries := readHollowInventory(t, root)
	registered := map[string]bool{}
	for _, e := range entries {
		registered[e.Path] = true
	}
	self := "scripts/ursmcheck/" + hollowInventoryFile
	var missing []string
	for _, p := range all {
		if p == self {
			continue // ★ 本门自己的清单豁免自己（否则无限回归）
		}
		if !registered[p] {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("有 %d 份 `.tsv` 没在 %s 里登记：\n  %s\n"+
			"★ 新增登记册必须登记它的读方，并回答「掏空它 ⇒ 读方红不红」；\n"+
			"★ 若【有意允许空】（如 cjk_var_sites.tsv），登记成 expect=exception 并写理由。",
			len(missing), hollowInventoryFile, strings.Join(missing, "\n  "))
	}
}

// TestTsvHollowoutInventoryRowsAreWellFormed 是【清单自身的形状判据】。
func TestTsvHollowoutInventoryRowsAreWellFormed(t *testing.T) {
	root := hollowRepoRoot(t)
	entries := readHollowInventory(t, root)
	if len(entries) == 0 {
		t.Fatalf("清单是空的 ⇒ 门恒绿（形同虚设）")
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Path == "" {
			t.Errorf("有条目没有填 tsv_path")
			continue
		}
		if seen[e.Path] {
			t.Errorf("清单里有重复条目：%s", e.Path)
		}
		seen[e.Path] = true
		if _, err := os.Stat(filepath.Join(root, e.Path)); err != nil {
			t.Errorf("清单条目指向不存在的文件：%s（%v）", e.Path, err)
		}
		switch e.Expect {
		case "red":
			if e.ReaderKind == "" || e.ReaderSpec == "" {
				t.Errorf("%s：expect=red ⇒ 必须给出读方（kind/spec）", e.Path)
			}
		case "exception":
			if len(e.Reason) < 12 {
				t.Errorf("%s：expect=exception ⇒ 必须写实质理由（现在 %d 字：%q）", e.Path, len(e.Reason), e.Reason)
			}
			for _, ph := range []string{"todo", "TBD", "n/a", "NA", "待补", "无", "?"} {
				if e.Reason == ph {
					t.Errorf("%s：例外理由是占位符 %q", e.Path, e.Reason)
				}
			}
			// ★★ R86 轮：例外的理由拆成【机械可核段】＋【人工审计段】⇒
			//   这里校验机械段的**形状**（★ 真不成立由另一道判据去核）。
			if e.MechCheck == "" || e.MechCheck == "-" {
				t.Errorf("%s：expect=exception ⇒ 必须给出【机械可核段】（mech_check 列；★ 可用：%s）",
					e.Path, strings.Join(hollowMechKindList(), " / "))
			} else {
				for _, kind := range strings.Split(e.MechCheck, ",") {
					kind = strings.TrimSpace(kind)
					if kind == "" {
						continue
					}
					if !hollowMechKinds[kind] {
						t.Errorf("%s：未知的机械断言 %q（★ 可用：%s）", e.Path, kind,
							strings.Join(hollowMechKindList(), " / "))
					}
				}
			}
		default:
			t.Errorf("%s：expect 只能是 red / exception（实际 %q）", e.Path, e.Expect)
		}
		if e.ReaderKind != "" && e.ReaderKind != "gotest" && e.ReaderKind != "shell" && e.ReaderKind != "none" {
			t.Errorf("%s：reader_kind 只能是 gotest / shell / none（实际 %q）", e.Path, e.ReaderKind)
		}
		if e.Expect == "red" && e.ReaderKind == "none" {
			t.Errorf("%s：expect=red ⇒ 必须给出读方（reader_kind 不能是 none）", e.Path)
		}
		if e.HeaderRows < 0 {
			t.Errorf("%s：header_rows 不能为负", e.Path)
		}
	}
}

// hollowVerifyEntry 是【行为判据】的**单条目**实现（R86 轮：★ 从测试体里抽出来，
//
//	★ 目的：让「负控臂」（scripts/ursmcheck/tsv_hollowout_negative_control_test.go）
//	  能跑**完全同一条路径** ⇒ ⇒ 门对「读方被改瞎」这件事的**发现能力**
//	  因此可以被行为化验证（★ 而不是只靠合成读方自证）。
//
// ★ kind/spec 可覆盖 ⇒ ⇒ 负控臂可以把【真实读方】换成【被改瞎的读方】。
//
// ★ 返回 (分类, 细节)：分类 ∈ {redCaught, stillGreen, inconclusive, baselineRed}。
func hollowVerifyEntry(t *testing.T, root string, e hollowEntry, kind, spec string) (hollowOutcome, string) {
	return hollowVerifyEntryOverlay(t, root, e, kind, spec, "")
}

// hollowVerifyEntryOverlay 是【带 overlay 的行为判据单条目实现】（★ R86 轮新增）。
//
// ★ overlay 非空 ⇒ 读方跑的是【改瞎的副本】⇒ ⇒ 负控臂可以用**同一条路径**
// ★ 去验证「门能不能发现『读方被改瞎了』」。
func hollowVerifyEntryOverlay(t *testing.T, root string, e hollowEntry, kind, spec, overlay string) (hollowOutcome, string) {
	t.Helper()

	// ★★ R87 轮收尾：★ 拿【整仓掏空锁】⇒ ⇒ 同一时刻只允许一个掏空跑变异工作树。
	//
	// ★ 为什么必须有：★ 本函数会**真的把登记册写空**（下面第 ④ 步）
	// ⇒ ⇒ 而并行会话常在同一个 checkout 上跑 `go test ./...`（★ 它把本包也跑了）
	// ⇒ ⇒ 两个掏空跑会同时动同一批文件 ⇒ ⇒ 竞态有三种结局：
	//   ① A 的掏空被 B 的自愈预检**抹掉**⇒ ⇒ A 在【原文】上读到绿 ⇒ ⇒ **A 误判成 still-green**；
	//   ② B 把 A 的**掏空版**当成了原文备份 ⇒ ⇒ B 收尾「还原」⇒ ⇒ **台账被永久留在掏空态**；
	//   ③ 两者交错 ⇒ ⇒ 还原后 md5 对不上 ⇒ ⇒ 门红（★ 这一种反而是好的）。
	// ★ ⇒ ⇒ 跨进程互斥是唯一的根治办法。
	// ★ 拿不到锁 ⇒ **红**（★ 不跳过）⇒ ⇒ 「跳过」在退出码上与「跑了 0 条」不可区分。
	releaseLock, _, err := lockHollowoutShared(root, hollowoutLockWait)
	if err != nil {
		t.Fatalf("★ 拿不到整仓掏空锁 ⇒ ⇒ 本条**不判定**（★ 不能跳过：★「跳过」与「跑了 0 条」"+
			"在退出码上不可区分）：%v", err)
	}
	defer releaseLock()

	abs := filepath.Join(root, e.Path)
	orig, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", e.Path, err)
	}
	origMD5 := md5Of(orig)

	// ★ ① 掏空前的形状
	dataRowsBefore := countDataRows(orig)
	hollowed, dropped, kept := hollowBytes(orig, e.HeaderRows)
	// ★ ② 变换有效性（探针有没有效）—— 「掏空后仍绿」的第一嫌疑是探针无效
	if dropped == 0 {
		return outcomeInconclusive, fmt.Sprintf("掏空是**空操作**（非注释行数 = %d，header_rows = %d）⇒ ⇒ 什么都没测到",
			dataRowsBefore, e.HeaderRows)
	}
	if kept != e.HeaderRows {
		return outcomeInconclusive, fmt.Sprintf("掏空后保留的非注释行数 = %d，与 header_rows = %d 不符 ⇒ 变换形状不对",
			kept, e.HeaderRows)
	}
	if got := countDataRows(hollowed); got != kept {
		return outcomeInconclusive, fmt.Sprintf("掏空后非注释行数 = %d，与变换报告的 %d 不符 ⇒ 变换与读数口径不一致",
			got, kept)
	}

	// ★ ③ 基线：读方必须绿（否则不可判定）
	code, out, err := runHollowReaderOverlay(kind, spec, root, 600*time.Second, overlay, "")
	if err != nil {
		t.Fatalf("跑基线读方失败（%s %s）：%v", kind, spec, err)
	}
	if code != 0 {
		return outcomeBaselineRed, fmt.Sprintf("读方在【基线】就红（exit=%d）\n%s", code, tailLines(out, 20))
	}

	// ★ ④ 掏空 ⇒ 跑读方 ⇒ 必须红
	//
	// ★★ 2026-10-24 12h 审计轮（问题②）改法：**先落盘备份，再掏空**。
	//   原文先写到 `<abs>.hollowout-backup` ⇒ 即使被杀，原文仍在盘上
	//   ⇒ ⇒ 下次跑的自愈预检会把它还原。
	backup := hollowBackupPath(abs)
	if err := os.WriteFile(backup, orig, 0o644); err != nil {
		t.Fatalf("写备份失败（%s）：%v", backup, err)
	}
	if err := os.WriteFile(abs, hollowed, 0o644); err != nil {
		t.Fatalf("写掏空版失败：%v", err)
	}
	stopGuard := installHollowSignalGuard(abs)
	defer func() {
		stopGuard()
		if err := restoreHollowBackup(abs); err != nil {
			t.Errorf("还原 %s 失败：%v", e.Path, err)
			return
		}
		back, err := os.ReadFile(abs)
		if err != nil {
			t.Errorf("还原后读不回 %s：%v", e.Path, err)
			return
		}
		if got := md5Of(back); got != origMD5 {
			t.Errorf("还原后 md5 不符（%s）：期望 %s，实际 %s", e.Path, origMD5, got)
		}
	}()
	code, out, err = runHollowReaderOverlay(kind, spec, root, 600*time.Second, overlay, "")
	if err != nil {
		t.Fatalf("跑掏空版读方失败：%v", err)
	}
	if code == 0 {
		return outcomeStillGreen, fmt.Sprintf("掏空后读方**仍然绿**（exit=0）\n%s", tailLines(out, 20))
	}
	return outcomeRedCaught, fmt.Sprintf("掏空后读方红了（exit=%d）", code)
}

// TestTsvHollowoutReadersRejectEmptyRegistry 是【行为判据】：
// ★ 对每条 expect=red 的条目：★ 基线绿 ⇒ 掏空 ⇒ 必须红 ⇒ 还原 ⇒ md5 对上。
func TestTsvHollowoutReadersRejectEmptyRegistry(t *testing.T) {
	root := hollowRepoRoot(t)
	// ★★ 2026-10-24 12h 审计轮（问题②）：**开跑前先自愈**。
	// 上一次跑若被 SIGKILL（testing 超时 = alarm goroutine panic ⇒ defer 不跑），
	// 被跟踪的台账会**留空**在盘上 ⇒ ⇒ 这一步把它们**还原回来**，
	// 再开始新一轮掏空 ⇒ ⇒ 「崩溃留下的空洞」会被下一次跑自动修好，
	// 而不是永远留在工作树里等人手工 git checkout。
	//
	// ★★ 2026-10-25 R148 合并轮：自愈/备份/信号守卫的**实现**统一取上游
	//   scripts/ursmcheck/tsv_hollowout_safety_test.go 的版本（上游 magpie §3.17
	//   已把它拆成独立文件并自带门）；本文件原另有一套同名语义的实现
	//   （hollowSelfHeal / hollowSaveBackup / hollowBackupDir / hollowBackupPath）
	//   ⇒ ★ 两套并存会与上游的 `hollowBackupPath` **重复声明**（编译失败）
	//   ⇒ ⇒ 故删除本地那套，保留本地**独有**的性能改动（预编译测试二进制）。
	healed, err := healHollowLeftovers(root)
	if err != nil {
		t.Fatalf("自愈预检失败（还原上一次遗留的掏空台账）：%v", err)
	}
	if healed > 0 {
		t.Logf("自愈预检：还原了 %d 份上一次遗留的掏空台账", healed)
	}
	entries := readHollowInventory(t, root)
	for _, e := range entries {
		if e.Expect != "red" {
			continue
		}
		t.Run(e.Path, func(t *testing.T) {
			outcome, detail := hollowVerifyEntry(t, root, e, e.ReaderKind, e.ReaderSpec)
			switch outcome {
			case outcomeRedCaught:
				t.Logf("✔ %s：掏空 ⇒ 读方红 ⇒ 有牙（%s）", e.Path, detail)
			case outcomeStillGreen:
				t.Fatalf("%s：★ 掏空后读方**仍然绿** ⇒ ⇒ 读方在「读到 0 行」时是瞎的 ⇒ "+
					"★ 这就是 R84 轮要找的那个洞。\n★ 读方：%s %s\n%s",
					e.Path, e.ReaderKind, e.ReaderSpec, detail)
			case outcomeBaselineRed:
				t.Fatalf("%s：读方在【基线】就红 ⇒ ⇒ 不可判定 ⇒ "+
					"★ 先把读方修绿，或把该条目改成 expect=exception 并写理由。\n%s",
					e.Path, detail)
			default:
				t.Fatalf("%s：不可判定 ⇒ %s\n★ 请改 header_rows，或把该文件登记成 expect=exception 并写理由",
					e.Path, detail)
			}
		})
	}
}

// hollowNonDocReferrers 返回「仓内（文档与本清单之外）提及该路径的被追踪文件」。
//
// ★ 口径（R86 轮）：
//
//	① 只看 **git 追踪**的文件（★ 与覆盖判据同源 ⇒ 读数只取决于提交的树）；
//	② 排除**文档**（`docs/` 目录 ＋ `.md` / `.markdown` / `.txt` 文件）
//	   ⇒ ⇒ 「文档里提到它」不算「有读方」；
//	③ 排除**它自己**（台账表头会提到自己的路径）＋ 本清单。
//
// ★ 返回值是排序后的相对路径列表。
func hollowNonDocReferrers(root, path string) ([]string, error) {
	// ★★ 用 `-z`（NUL 分隔）★ ⇒ 否则【含非 ASCII 的路径】会被 git **加引号并转义**
	// ★⇒ 形如 "docs/96\345\260\217...md" ⇒ ⇒ 前缀判断会失效 ⇒ ⇒ **假红**。
	// ★★ （★ 本轮第一版就踩了这个：★ 它把 handoff 文档判成了「代码引用者」。）
	out, err := exec.Command("git", "-C", root, "grep", "-l", "-z", "-F", "-e", path).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, nil // ★ 没有命中（git grep 无匹配时 exit 1）
		}
		return nil, err
	}
	selfInventory := "scripts/ursmcheck/" + hollowInventoryFile
	var hits []string
	for _, line := range strings.Split(string(out), "\x00") {
		f := strings.TrimSpace(line)
		if f == "" {
			continue
		}
		if f == path || f == selfInventory {
			continue
		}
		if strings.HasPrefix(f, "docs/") {
			continue
		}
		switch strings.ToLower(filepath.Ext(f)) {
		case ".md", ".markdown", ".txt":
			continue
		}
		hits = append(hits, f)
	}
	sort.Strings(hits)
	return hits, nil
}

// TestTsvHollowoutExceptionMechanicsHold 是【例外表的机械段判据】（★ R86 轮新增）。
//
// ★ 承接 §19.25.19.10 优先级 1 ②：★ 例外的理由拆成【机械可核段】＋【人工审计段】
// ⇒ ⇒ 本门**真的去核对**机械段 ⇒ ⇒ 理由的骨架不再靠人说了算。
//
// ★ 例外表的机械段一旦**不再成立**（★ 例如「0 数据行」的那份后来有了行，
//
//	或「无代码读方」的那份后来有了读方）⇒ ⇒ 本门红 ⇒ ⇒ 理由必须被更新。
func TestTsvHollowoutExceptionMechanicsHold(t *testing.T) {
	root := hollowRepoRoot(t)
	entries := readHollowInventory(t, root)
	checked := 0
	for _, e := range entries {
		if e.Expect != "exception" {
			continue
		}
		checked++
		t.Run(e.Path, func(t *testing.T) {
			for _, raw := range strings.Split(e.MechCheck, ",") {
				kind := strings.TrimSpace(raw)
				if kind == "" {
					continue
				}
				switch kind {
				case "rows=0":
					data, err := os.ReadFile(filepath.Join(root, e.Path))
					if err != nil {
						t.Fatalf("读 %s 失败：%v", e.Path, err)
					}
					if n := countDataRows(data); n != 0 {
						t.Errorf("%s：例外的机械段声明「rows=0」，实测 %d 行 ⇒ ⇒ 理由已过期 ⇒ "+
							"★ 要么它现在可以被行为化验证（登记成 expect=red），要么改理由", e.Path, n)
					}
				case "reader-green":
					if e.ReaderKind == "" || e.ReaderKind == "none" || e.ReaderSpec == "" || e.ReaderSpec == "-" {
						t.Errorf("%s：例外的机械段声明「reader-green」，但该条目没有读方 ⇒ 声明无效", e.Path)
						continue
					}
					code, out, err := runHollowReader(e.ReaderKind, e.ReaderSpec, root, 600*time.Second)
					if err != nil {
						t.Fatalf("跑基线读方失败：%v", err)
					}
					if code != 0 {
						t.Errorf("%s：例外的机械段声明「reader-green」，实测读方在【基线】就红（exit=%d）⇒ ⇒ 理由已过期\n%s",
							e.Path, code, tailLines(out, 20))
					}
				case "no-code-reader":
					hits, err := hollowNonDocReferrers(root, e.Path)
					if err != nil {
						t.Fatalf("扫引用失败：%v", err)
					}
					if len(hits) > 0 {
						t.Errorf("%s：例外的机械段声明「no-code-reader」，实测有 %d 个仓内（文档外）引用者：\n  %s\n"+
							"★ ⇒ ⇒ 理由已过期（★ 要么它其实有读方 ⇒ 登记成 expect=red；要么改理由）",
							e.Path, len(hits), strings.Join(hits, "\n  "))
					}
				case "reader-red-on-missing":
					// ★★ R87 轮新增：★ 「读方在【登记册文件不在】时必须红」。
					//
					// ★★ 为什么这条**比 `rows=0` 强**：★ `rows=0` 只说「它现在恰好是 0 行」
					// ⇒ ⇒ 明天长出一行 ⇒ ⇒ 理由就过期；★ 而这一条说「**文件本身是承重的**」
					// ⇒ ⇒ 文件一消失读方立刻红 ⇒ ⇒ 事实源丢了不会被静默放过。
					//
					// ★★ 落盘安全：★ 文件是**已跟踪**的 ⇒ ⇒ 先备份、再**移走**、跑读方、还原
					// ★★ ⇒ 还原后校验 md5（★ 承本线纪律：★ 备份必须在变异**之前**做）。
					if e.ReaderKind == "" || e.ReaderKind == "none" || e.ReaderSpec == "" || e.ReaderSpec == "-" {
						t.Errorf("%s：例外的机械段声明「reader-red-on-missing」，但该条目没有读方 ⇒ 声明无效", e.Path)
						continue
					}
					abs := filepath.Join(root, e.Path)
					orig, err := os.ReadFile(abs)
					if err != nil {
						t.Fatalf("读 %s 失败：%v", e.Path, err)
					}
					origMD5 := md5Of(orig)
					backup := hollowBackupPath(abs)
					if err := os.WriteFile(backup, orig, 0o644); err != nil {
						t.Fatalf("写备份失败（%s）：%v", backup, err)
					}
					if err := os.Remove(abs); err != nil {
						t.Fatalf("移走登记册失败：%v", err)
					}
					code, out, runErr := runHollowReader(e.ReaderKind, e.ReaderSpec, root, 600*time.Second)
					// ★ 还原（★ 无论跑方成功与否都要还原 ⇒ ⇒ 写死在同一条路径上）。
					restoreErr := restoreHollowBackup(abs)
					back, readErr := os.ReadFile(abs)
					switch {
					case restoreErr != nil:
						t.Errorf("%s：还原失败：%v", e.Path, restoreErr)
					case readErr != nil:
						t.Errorf("%s：还原后读不回：%v", e.Path, readErr)
					case md5Of(back) != origMD5:
						t.Errorf("%s：还原后 md5 不符（期望 %s，实际 %s）", e.Path, origMD5, md5Of(back))
					}
					if runErr != nil {
						t.Errorf("%s：例外的机械段声明「reader-red-on-missing」，但**跑不起来**读方：%v\n"+
							"★ ⇒ ⇒ 声明无效（★ 「跑不起来」与「绿」必须可区分 ⇒ ★ 见 TestStructuredRegistryHarnessDistinguishesNotRunFromGreen）",
							e.Path, runErr)
						continue
					}
					if code == 0 {
						t.Errorf("%s：例外的机械段声明「reader-red-on-missing」，实测读方在【登记册文件不在】时**仍然绿**（exit=0）⇒ ⇒ "+
							"★ 这份登记册**丢了都不会有人发现**⇒ ⇒ 理由已过期\n%s", e.Path, tailLines(out, 20))
					}
				case "reader-red-on-hollow":
					// ★★ R87 轮新增：★ 「读方在【掏空版】下必须红」。
					//
					// ★★ 这是**最强**的一条机械断言：★ 它直接断言「掏空这份登记册 ⇒ 门会抓到」
					// ⇒ ⇒ 复用**同一条**行为判据（`hollowVerifyEntry`）⇒ ⇒ 两边口径同源。
					if e.ReaderKind == "" || e.ReaderKind == "none" || e.ReaderSpec == "" || e.ReaderSpec == "-" {
						t.Errorf("%s：例外的机械段声明「reader-red-on-hollow」，但该条目没有读方 ⇒ 声明无效", e.Path)
						continue
					}
					outcome, detail := hollowVerifyEntry(t, root, e, e.ReaderKind, e.ReaderSpec)
					if outcome != outcomeRedCaught {
						t.Errorf("%s：例外的机械段声明「reader-red-on-hollow」，实测掏空后分类是 %s（期望 red-caught）⇒ ⇒ "+
							"★ 声明已过期（★ 要么它其实适用「掏空 ⇒ 红」⇒ 登记成 expect=red；要么改理由）\n%s",
							e.Path, outcome, detail)
					}
				default:
					t.Errorf("%s：未知的机械断言 %q", e.Path, kind)
				}
			}
		})
	}
	if checked == 0 {
		t.Fatalf("清单里一条 exception 都没有 ⇒ 例外表判据空转（★ 门恒绿）")
	}
}

// tailLines 取输出末尾 n 行。
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------- 自检

// TestTsvHollowoutHarnessHasTeeth 证明【本门的量具有牙】：
// ★ 用合成读方验证「分类器能把红与绿分开」——★ 否则「掏空后仍绿」可能只是
// ★ 分类器恒判「绿」。
func TestTsvHollowoutHarnessHasTeeth(t *testing.T) {
	root := hollowRepoRoot(t)
	dir := t.TempDir()

	// 合成登记册：2 行注释 + 1 行列名 + 1 行数据
	reg := filepath.Join(dir, "synthetic.tsv")
	content := "# 合成登记册\n# 列：a\tb\na\tb\nx\ty\n"
	if err := os.WriteFile(reg, []byte(content), 0o644); err != nil {
		t.Fatalf("写合成登记册失败：%v", err)
	}
	hollowed, dropped, kept := hollowBytes([]byte(content), 1)
	if dropped != 1 {
		t.Fatalf("合成登记册应掏掉 1 行数据，实际 %d", dropped)
	}
	if kept != 1 {
		t.Fatalf("合成登记册掏空后应保留 1 行列名行，实际 %d", kept)
	}
	if got := countDataRows(hollowed); got != 1 {
		t.Fatalf("合成登记册掏空后应剩 1 行非注释内容（列名行），实际 %d", got)
	}
	if !strings.Contains(string(hollowed), "# 合成登记册") {
		t.Fatalf("掏空后必须保留注释行，实际 %q", string(hollowed))
	}
	if !strings.Contains(string(hollowed), "a\tb") {
		t.Fatalf("掏空后必须保留列名行，实际 %q", string(hollowed))
	}

	// ★ 合成读方：① 读到 0 行就退出 1（模拟「有牙」的读方）
	redReader := filepath.Join(dir, "red_reader.sh")
	if err := os.WriteFile(redReader, []byte("#!/usr/bin/env bash\n"+
		"rows=$(grep -vc '^#' \"$1\" 2>/dev/null || true)\n"+
		"[ \"$rows\" -le 1 ] && exit 1 || exit 0\n"), 0o755); err != nil {
		t.Fatalf("写合成读方失败：%v", err)
	}
	// ★ ② 永远退出 0（模拟「瞎」的读方）
	greenReader := filepath.Join(dir, "green_reader.sh")
	if err := os.WriteFile(greenReader, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("写合成读方失败：%v", err)
	}

	code, _, err := runHollowReader("shell", "bash "+redReader+" "+reg, root, 60*time.Second)
	if err != nil {
		t.Fatalf("跑合成读方失败：%v", err)
	}
	if code != 0 {
		t.Fatalf("★ 合成读方（读到 0 行就红）在完整登记册上必须绿，实际 exit=%d ⇒ ⇒ 分类器有问题", code)
	}
	// ★ 掏空版要**落成文件**再喂给读方（★ 不能把内容当命令参数传）
	hollowedPath := filepath.Join(dir, "synthetic_hollowed.tsv")
	if err := os.WriteFile(hollowedPath, hollowed, 0o644); err != nil {
		t.Fatalf("写掏空版失败：%v", err)
	}
	code, _, err = runHollowReader("shell", "bash "+redReader+" "+hollowedPath, root, 60*time.Second)
	if err != nil {
		t.Fatalf("跑合成读方失败：%v", err)
	}
	if code == 0 {
		t.Fatalf("★ 合成读方（读到 0 行就红）在掏空登记册上必须红，实际 exit=0 ⇒ ⇒ 分类器恒判绿")
	}
	code, _, err = runHollowReader("shell", "bash "+greenReader+" "+reg, root, 60*time.Second)
	if err != nil {
		t.Fatalf("跑合成读方失败：%v", err)
	}
	if code != 0 {
		t.Fatalf("★ 合成读方（永远绿）必须绿，实际 exit=%d", code)
	}
	// ★★ 反向自检：★ 「瞎」的读方在完整登记册上绿、在掏空版上**也绿**
	//   ⇒ ⇒ 这正是「掏空后仍绿」的那个形状 ⇒ ⇒ 量具必须能把它与「有牙」区分开
	code, _, err = runHollowReader("shell", "bash "+greenReader+" "+hollowedPath, root, 60*time.Second)
	if err != nil {
		t.Fatalf("跑合成读方失败：%v", err)
	}
	if code != 0 {
		t.Fatalf("★ 合成读方（永远绿）在掏空版上必须仍绿，实际 exit=%d", code)
	}
}
