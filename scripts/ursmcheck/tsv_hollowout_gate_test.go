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
		entries = append(entries, hollowEntry{
			Path:       strings.TrimSpace(parts[0]),
			ReaderKind: strings.TrimSpace(parts[1]),
			ReaderSpec: strings.TrimSpace(parts[2]),
			Expect:     strings.TrimSpace(parts[3]),
			HeaderRows: hr,
			Reason:     strings.TrimSpace(parts[5]),
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
		// ★ 优先走【预编译二进制】（★ 每个包只编译一次）；编译不了就回落到
		//   原来的 `go test` 形态（★ 语义完全相同，只是慢）。
		if bin, binDir, binErr := hollowTestBinary(goBin, parts[0], root, timeout); binErr == nil {
			cmd = exec.Command(bin, "-test.run", "^"+parts[1]+"$", "-test.count=1")
			// ★ cwd = 包目录（★ 复刻 `go test` 的语义；★ 不是仓根！）
			cmdDir = binDir
		} else {
			cmd = exec.Command(goBin, "test", parts[0], "-run", "^"+parts[1]+"$", "-count=1")
		}
	case "shell":
		// ★ shell:<command> ⇒ `bash -c <command>`（cwd = 仓根）
		//   ⇒ 允许写任意命令（★ 例如 `cd installer && go test ...`：
		//   installer 是**独立 Go module**，主 module 的 `./installer/...` 不可用）。
		cmd = exec.Command("bash", "-c", spec)
	default:
		return -1, "", fmt.Errorf("未知 reader_kind：%q", kind)
	}
	cmd.Dir = cmdDir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		// ★ 把 go 放进 PATH（★ shell 读方可能自己调 go）
		"PATH="+goBinDir()+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
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
	hollowBinMu.Lock()
	defer hollowBinMu.Unlock()
	if p, ok := hollowBinCache[pkg]; ok {
		return p, hollowPkgDir(pkg, root), nil
	}
	tmpDir, err := os.MkdirTemp("", "hollowout-bin-")
	if err != nil {
		return "", "", err
	}
	bin := filepath.Join(tmpDir, "testbin")
	build := exec.Command(goBin, "test", "-c", "-o", bin, pkg)
	build.Dir = root
	build.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0",
		"PATH="+goBinDir()+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := runWithTimeout(build, timeout); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", "", fmt.Errorf("编译 %s 的测试二进制失败：%v\n%s", pkg, err, tailLines(string(out), 10))
	}
	hollowBinCache[pkg] = bin
	return bin, hollowPkgDir(pkg, root), nil
}

// hollowPkgDir 复刻 `go test` 的 cwd 语义：测试二进制的 cwd = 包目录。
// ★ 清单里的 pkg 都是相对仓库根的 "./xxx/" 形态。
func hollowPkgDir(pkg, root string) string {
	return filepath.Join(root, strings.TrimPrefix(pkg, "./"))
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
				t.Fatalf("%s：掏空是**空操作**（非注释行数 = %d，header_rows = %d）⇒ ⇒ 什么都没测到 ⇒ "+
					"⇒ 这条目不可判定 ⇒ ⇒ 请改 header_rows，或把该文件登记成 expect=exception 并写理由",
					e.Path, dataRowsBefore, e.HeaderRows)
			}
			if kept != e.HeaderRows {
				t.Fatalf("%s：掏空后保留的非注释行数 = %d，与 header_rows = %d 不符 ⇒ 变换形状不对",
					e.Path, kept, e.HeaderRows)
			}
			if got := countDataRows(hollowed); got != kept {
				t.Fatalf("%s：掏空后非注释行数 = %d，与变换报告的 %d 不符 ⇒ 变换与读数口径不一致",
					e.Path, got, kept)
			}

			// ★ ③ 基线：读方必须绿（否则不可判定）
			code, out, err := runHollowReader(e.ReaderKind, e.ReaderSpec, root, 600*time.Second)
			if err != nil {
				t.Fatalf("跑基线读方失败（%s %s）：%v", e.ReaderKind, e.ReaderSpec, err)
			}
			if code != 0 {
				t.Fatalf("%s：读方在【基线】就红（exit=%d）⇒ ⇒ 不可判定 ⇒ "+
					"★ 先把读方修绿，或把该条目改成 expect=exception 并写理由。\n%s",
					e.Path, code, tailLines(out, 20))
			}

			// ★ ④ 掏空 ⇒ 跑读方 ⇒ 必须红
			//
			// ★★ 2026-10-24 12h 审计轮（问题②）改法：**先落盘备份，再掏空**。
			// 原实现把原文只存在内存里、还原靠 defer ⇒ 而 `testing` 的超时是
			// alarm goroutine 里的 panic ⇒ 其它 goroutine 的 defer **不执行**
			// ⇒ 进程被 SIGKILL 时**被跟踪的台账静默留空**
			// ⇒ 实测复现：本机该包 293.047s，而 verify.sh 的预算只有 300s。
			// ⇒ 现在：原文先写到 `<abs>.hollowout-backup` ⇒ 即使被杀，
			//   原文仍在盘上 ⇒ 下次跑的自愈预检会把它还原。
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
			code, out, err = runHollowReader(e.ReaderKind, e.ReaderSpec, root, 600*time.Second)
			if err != nil {
				t.Fatalf("跑掏空版读方失败：%v", err)
			}
			if code == 0 {
				t.Fatalf("%s：★ 掏空后读方**仍然绿** ⇒ ⇒ 读方在「读到 0 行」时是瞎的 ⇒ "+
					"★ 这就是 R84 轮要找的那个洞。\n★ 读方：%s %s\n%s",
					e.Path, e.ReaderKind, e.ReaderSpec, tailLines(out, 20))
			}
		})
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
