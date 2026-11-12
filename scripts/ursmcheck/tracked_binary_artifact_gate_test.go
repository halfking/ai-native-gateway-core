//go:build !integration

package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。
//
// tracked_binary_artifact_gate_test.go —— 「仓内不得新增**编译产物二进制**」（2026-11-03 12h 审计 A14 引入）。
//
// # 这道门治什么病
//
// 缺口本体（已实测复现）：
//
//	187ec7ead 把一个 **2.9 MB 的 darwin/arm64 Mach-O 可执行文件**
//	（`installer/e2e-bootstrap`）**提交进了仓库** ⇒ 而同批提交的源码
//	（`installer/cmd/e2e-bootstrap/main.go`）也在仓里 ⇒ ⇒ 仓里同时有
//	「源码」与「它的编译产物」。
//
//	三个具体危害：
//	① **平台专用**：该二进制是 darwin/arm64 ⇒ 在 linux/amd64 的 CI 与生产机上
//	   **不可执行** ⇒ 「看起来有 E2E 引导器，实际跑不了」；
//	② **内容与源码可能不同步** ⇒ 不可审计、不可 review（review 的是二进制）；
//	③ **仓内纪律明写**：`.gitignore` 里已有注释「'go build ./cmd/<name>/' drops
//	   the binary in the repo root; keep the compiled cmd tools out of the tree」
//	   ⇒ 本条**直接违反**该纪律。
//
// ★ 根因：`go build ./cmd/e2e-bootstrap/` 把可执行文件落在**模块根**
//   （`installer/`），而不是 `cmd/` 同名目录下 ⇒ ⇒ 于是 `.gitignore` 里
//   只写了 `/llm-gw-installer` 与 `/llm-launcher` ⇒ **`/e2e-bootstrap` 没被覆盖**
//   ⇒ ⇒ `git add -A` 顺手把它带进去了。
//   ⇒ ★ **所以光靠「补一条 .gitignore」不够** ⇒ ⇒ 必须有一道**门**钉住「仓内
//   ⇒ ⇒ 不得存在编译产物」这个**不变量**（否则下一个 `go build` 换个名字又进来）。
//
// # 判据（形态判据，不是具体值）
//
//	扫**全仓**每个**被 git 跟踪**的文件，读它的**前 4 字节**（magic）：
//	  · ELF       `\x7fELF`
//	  · Mach-O    `\xcf\xfa\xed\xfe` / `\xce\xfa\xed\xfe` / `\xca\xfe\xba\xbe`
//	                / `\xfe\xed\xfa\xce` / `\xfe\xed\xfa\xcf`
//	  · PE/MZ     `MZ`
//	命中的文件集合必须**恰好等于**基线集合。
//
//	★ 基线（棘轮，**只许降不许升**）：
//	  2026-11-09 起**为空** —— 最后两条历史遗留（`cmd/llm-gw-annotator/
//	  llm-gw-annotator` 14.3MB + `installer/llm-gw-installer` 12.7MB，两个
//	  darwin/arm64 Mach-O）已 `git rm --cached` + 补 `.gitignore` ⇒ 仓内
//	  编译产物集合归零 ⇒ 基线随之**下调为空**（★ 棘轮只许降不许升；★ 基线
//	  里的文件被从仓里删掉 ⇒ 门红，强制把基线同步下调，不留「空占位」）。
//	⇒ 新增一个 ⇒ 门红（★ 这就是防 A14 复发的那只手）。
//
// # ★ 2026-11-09 两处判据修正（本轮清理带出的活缺陷）
//
//	① **扫描源从 HEAD 改为索引**（`:path` 语义）。
//	   原实现用 `git ls-files` 取**文件名**（索引），却用 `HEAD:%s` 取**内容**
//	   （HEAD 树）⇒ ⇒ 两棵树不一致 ⇒ 一个 `git add` 进来、尚未提交的二进制
//	   在 `HEAD:` 下查不到 ⇒ cat-file 回 `missing` ⇒ **被静默跳过** ⇒ ⇒
//	   ⇒ 门在**最该拦的那一刻**（提交之前）放行了它；等提交完再跑才红，而
//	   那时二进制**已经进了历史**。
//	   ⇒ 现改为内容也取索引（`:path`）⇒ 文件名与内容同源 ⇒ ⇒ 新增二进制在
//	   `git add` 之后、提交之前即被抓住。
//	   ⇒ CI 上索引 == HEAD ⇒ 行为不变（等价）。
//
//	② **「基线非空」这条恒绿护栏被「正控测试」取代**。
//	   原护栏是「一个编译产物都没扫到 ⇒ 判红（扫描器坏了）」，它靠**基线非空**
//	   来区分「仓真的干净」与「扫描器坏了」—— ⇒ 一旦仓里真的一个编译产物都
//	   没有（**本轮清理后的新常态**），这条护栏就**必然误报**，把「干净」判成
//	   「坏了」。
//	   ⇒ 现改为**端到端正控**：在临时仓里造一个已知 magic 的文件，用**同一个**
//	   扫描器扫它 ⇒ 必须命中 ⇒ ⇒ 「扫不到」只有在扫描器真的坏了时才成立。
//	   ⇒ 这比「基线非空」**更强**：它证明的是提取能力本身，而不是「恰好非空」。
//
// ★ **不误伤合法二进制资源**：`docs/**/*.png|jpg` 等**图片资产**也是二进制，
//   但它们的 magic 是 PNG/JPEG，**不在**本门的判据集合里 ⇒ ⇒ 不会被误判。
//   ⇒ 判据是「**可执行文件**的 magic」，不是「二进制」。
//
// ★ 诚实边界：本门只看 **magic**，不判断「这个 ELF 是不是被人需要的」。
//   ⇒ 一个**真的**需要入库的独立可执行工具（未来若有）必须**显式**登记进基线
//   ⇒ ⇒ 并在登记处写清理由 ⇒ ⇒ 而不是「默认放行」。

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// trackedBinaryBaseline 已登记在册的**编译产物**（★ 只许降不许升的棘轮基线）。
//
// ★ 2026-11-09：基线**下调为空** —— 两条历史遗留被清出仓库：
//
//	cmd/llm-gw-annotator/llm-gw-annotator   （14.3MB darwin/arm64 Mach-O）
//	installer/llm-gw-installer                （12.7MB darwin/arm64 Mach-O）
//
//	两者都曾被 2026-11-03 的 A14 轮判为「历史遗留，先例，非豁免」⇒ 本轮按
//	「第 0 批零风险清理」把它们 `git rm --cached` + 补 `.gitignore`
//	（★ 原 `.gitignore` 只写了 `/installer/cmd/llm-gw-installer/…`
//	（**嵌套路径**），而实际入库的是 `installer/llm-gw-installer`（**模块根**）
//	⇒ 规则不匹配 ⇒ 漏网；★ 根级 `/llm-gw-*` 同样只匹配仓库根目录）。
//
//	⇒ 棘轮**下调**是本门自己要求的动作（基线里的文件被删 ⇒ 门红 ⇒ 强制同步
//	下调）⇒ 不是把护栏拆掉，是把护栏按它自己的口径走完。
//
// ★ 基线现在是**空的**：这不代表门形同虚设 —— 「恒绿护栏」已由**端到端正控**
//
//	（TestTrackedBinaryScannerPositiveControl）承担，见文件头 §②。
//
// ★ 未来若真有一个独立可执行工具需要入库，必须**显式**登记进下表并写清理由。
var trackedBinaryBaseline = map[string]string{}

// trackedBinaryMagics 可执行文件的 magic 前缀。
var trackedBinaryMagics = [][]byte{
	{0x7f, 'E', 'L', 'F'},    // ELF
	{0xcf, 0xfa, 0xed, 0xfe}, // Mach-O 64-bit
	{0xce, 0xfa, 0xed, 0xfe}, // Mach-O 32-bit
	{0xca, 0xfe, 0xba, 0xbe}, // Mach-O fat
	{0xfe, 0xed, 0xfa, 0xce}, // Mach-O 32-bit BE
	{0xfe, 0xed, 0xfa, 0xcf}, // Mach-O 64-bit BE
	{'M', 'Z'},               // PE / Windows
}

// trackedBinaryRepoRoot 定位仓根。
func trackedBinaryRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败：%v", err)
	}
	return filepath.Dir(filepath.Dir(wd))
}

// trackedBinaryLooksExecutable 判前 4 字节是不是「可执行文件」的 magic。
func trackedBinaryLooksExecutable(head []byte) bool {
	for _, m := range trackedBinaryMagics {
		if len(head) >= len(m) && string(head[:len(m)]) == string(m) {
			return true
		}
	}
	return false
}

// trackedBinaryScan 扫**索引**里被跟踪的文件，返回「编译产物文件 → 其 magic」。
//
// ★ 实现要点：
//  1. 逐个 blob 只读**前 4 字节**，其余用 io.CopyN 丢弃 ⇒ 不把 300MB+ 的
//     仓内容全读进内存 ⇒ ⇒ 扫描是流式的。
//  2. **内容取自索引**（`:path` 语义）而不是 `HEAD:path` ⇒ ⇒ 文件名清单
//     （`git ls-files`，索引）与内容来源**同源** ⇒ ⇒ 一个刚 `git add` 进来、
//     尚未提交的二进制**也会被扫到**（★ 原实现用 HEAD ⇒ 它在提交前被静默
//     跳过 ⇒ 门在最该拦的那一刻放行；详见文件头 §①）。
//
// ★ root 形参存在，是为了让**正控测试**能把同一个扫描器指向一个临时仓。
func trackedBinaryScan(t *testing.T, root string) map[string]string {
	t.Helper()

	ls := exec.Command("git", "-C", root, "ls-files", "-z")
	out, err := ls.Output()
	if err != nil {
		t.Fatalf("git ls-files 失败：%v", err)
	}
	var names []string
	for _, n := range strings.Split(string(out), "\x00") {
		if n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		t.Fatalf("git ls-files 返回 0 个文件 ⇒ 门判红（★ 否则「没扫到任何文件」会假装通过）")
	}

	// ★ 一次 `git cat-file --batch` 批量取，避免每文件一个进程。
	cmd := exec.Command("git", "-C", root, "cat-file", "--batch")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("开 stdin 失败：%v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("开 stdout 失败：%v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 git cat-file 失败：%v", err)
	}
	go func() {
		for _, n := range names {
			// ★ `:path` = 索引里 stage 0 的 blob（`git cat-file --batch` 支持
			//   这一 extended SHA-1 表达式；实测对不存在的路径回 `missing`）。
			fmt.Fprintf(stdin, ":%s\n", n)
		}
		stdin.Close()
	}()

	hits := map[string]string{}
	br := bufio.NewReaderSize(stdout, 1<<20)
	for _, name := range names {
		header, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("读 cat-file 头失败（%s）：%v", name, err)
		}
		parts := strings.Fields(strings.TrimSpace(header))
		if len(parts) < 3 {
			// ★ `missing` / 非 blob ⇒ 跳过（不是缺陷，是「这个路径在 HEAD 里没有 blob」）。
			continue
		}
		if parts[1] != "blob" {
			continue
		}
		var size int64
		if _, err := fmt.Sscanf(parts[2], "%d", &size); err != nil {
			t.Fatalf("解析 blob 大小失败（%s / %s）：%v", name, header, err)
		}
		head := make([]byte, 4)
		read, err := io.ReadFull(br, head)
		if err != nil && read == 0 {
			t.Fatalf("读 blob 头失败（%s）：%v", name, err)
		}
		// ★ 丢掉这个 blob 的其余内容（连同结尾的换行）。
		if rest := size - int64(read); rest > 0 {
			if _, err := io.CopyN(io.Discard, br, rest); err != nil {
				t.Fatalf("跳过 blob 内容失败（%s）：%v", name, err)
			}
			if _, err := br.Discard(1); err != nil {
				t.Fatalf("跳过 blob 尾换行失败（%s）：%v", name, err)
			}
		}
		if trackedBinaryLooksExecutable(head[:read]) {
			hits[name] = fmt.Sprintf("%x", head[:read])
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("git cat-file 退出码异常：%v", err)
	}
	return hits
}

// TestNoNewTrackedBinaryArtifacts 是门本体。
func TestNoNewTrackedBinaryArtifacts(t *testing.T) {
	hits := trackedBinaryScan(t, trackedBinaryRepoRoot(t))

	// ★ 2026-11-09：原「恒绿护栏」（`len(hits)==0 ⇒ 扫描器坏了 ⇒ 判红`）已**删除**。
	//   它靠「基线非空」区分「仓真的干净」与「扫描器坏了」⇒ ⇒ 而本轮清理后
	//   仓里**真的**一个编译产物都没有了 ⇒ ⇒ 该护栏会**必然误报**，把「干净」
	//   判成「坏了」⇒ ⇒ 门恒红。
	//   ⇒ 恒绿护栏改由**端到端正控**承担：TestTrackedBinaryScannerPositiveControl
	//   在临时仓里造一个已知 magic 的文件，用**同一个**扫描器扫它 ⇒ 必须命中
	//   ⇒ ⇒ 「扫不到」只有在扫描器真的坏了时才成立（★ 比「基线非空」更强：
	//   它证明的是**提取能力本身**，而不是「恰好非空」）。
	if len(hits) == 0 {
		t.Logf("★ 索引里没有编译产物二进制（★ 这正是本轮清理后的目标状态；" +
			"★ 「扫描器是否有效」由 TestTrackedBinaryScannerPositiveControl 证明）")
	}

	var unexpected []string
	for name := range hits {
		if _, ok := trackedBinaryBaseline[name]; !ok {
			unexpected = append(unexpected, name)
		}
	}
	sort.Strings(unexpected)
	if len(unexpected) > 0 {
		t.Errorf("★★ 仓内出现**未登记**的编译产物二进制 ⇒ ⇒ ★ 它们是构建产物，不是源码 ⇒ "+
			"★ ① 平台专用 ⇒ 在别的平台/CI 上不可执行；★ ② 内容与源码可能不同步 ⇒ 无法审计；"+
			"★ ③ 体积虚胖 ⇒ clone 变慢 ⇒ ⇒ ★ 请 `git rm --cached <file>` 并把对应路径补进 .gitignore "+
			"★ （★ 若确实需要入库，必须**显式**登记进 trackedBinaryBaseline 并写清理由）：%v",
			unexpected)
	}

	// ★ 棘轮另一半：基线里的文件已经不在仓里 ⇒ 基线必须同步下调（★ 不留空占位）。
	var stale []string
	for name := range trackedBinaryBaseline {
		if _, ok := hits[name]; !ok {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("★ 基线里的编译产物 %v 已经不在仓里了 ⇒ ⇒ 基线必须**同步下调** "+
			"（★ 棘轮只许降不许升；★ 留着空占位会让「基线」变成一句空话）", stale)
	}
}

// TestTrackedBinaryScannerPositiveControl 是**端到端正控**：证明扫描器本身有效。
//
// ★ 为什么必须有它（2026-11-09）：
//
//	本门断言「仓内没有编译产物二进制」。当这个结论为真时，**门无法从自己的
//	结论里区分**「仓真的干净」与「扫描器坏了、什么都没扫到」⇒ ⇒ 门需要
//	一个**独立于仓内容**的证据来说明「扫描器确实能抓到东西」。
//
//	⇒ 做法：在**临时仓**里造一个**已知 magic** 的文件（+ 一个普通文本文件），
//	  用**同一个** trackedBinaryScan 扫它 ⇒
//	    · 必须命中那个二进制文件（证明提取器没坏）；
//	    · 必须**不**命中那个文本文件（证明判据没变成「什么都是二进制」）。
//	⇒ 这两条合起来 = 「提取能力有效」+「判据仍然精确」。
//
// ★ 这比旧护栏「基线非空」**更强**：基线非空只说明「期望集合非空」，
//
//	而正控证明的是「扫描器在已知阳性样本上必然阳性、已知阴性样本上必然阴性」。
func TestTrackedBinaryScannerPositiveControl(t *testing.T) {
	root := t.TempDir()

	// ★ Mach-O 64-bit little-endian magic（`file` 认得它 ⇒ 是真格式头）。
	//   刻意用**真格式**而非随手 4 字节 ⇒ 万一将来判据改成「调 file(1)」，
	//   正控依然成立。
	macho := []byte{0xcf, 0xfa, 0xed, 0xfe, 0x0c, 0x00, 0x00, 0x01,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	if err := os.WriteFile(filepath.Join(root, "planted.bin"), macho, 0o644); err != nil {
		t.Fatalf("写阳性样本失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("plain text, not a binary\n"), 0o644); err != nil {
		t.Fatalf("写阴性样本失败：%v", err)
	}

	trackedBinaryRunGit(t, root, "init", "-q")
	trackedBinaryRunGit(t, root, "add", "-A")

	hits := trackedBinaryScan(t, root)

	if _, ok := hits["planted.bin"]; !ok {
		t.Fatalf("★ 正控失败：扫描器在**已知阳性样本**（Mach-O magic）上没命中 ⇒ " +
			"★ 提取器坏了 ⇒ ⇒ 「仓内没有编译产物」这条结论将永远为真而门形同虚设")
	}
	if _, ok := hits["notes.txt"]; ok {
		t.Fatalf("★ 负控失败：扫描器把**普通文本**也判成编译产物 ⇒ 判据过宽")
	}
	if len(hits) != 1 {
		t.Errorf("★ 正控只应命中 1 个文件，实测命中 %d 个：%v", len(hits), hits)
	}
}

// trackedBinaryRunGit 在指定仓里跑一条 git 命令（正控用）。
func trackedBinaryRunGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(),
		// ★ 隔离用户全局配置 ⇒ 免得本机的 commit.gpgsign / core.hooksPath
		//   之类设置让 `git add` 在 CI 上行为不同。
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v 失败：%v\n%s", args, err, out)
	}
}

// TestTrackedBinaryBaselineIsJustified 是基线自身的形状判据。
//
// ★ 2026-11-09：原名 TestTrackedBinaryBaselineIsNotEmpty，断言「基线非空」，
//
//	理由是「基线为空 ⇒『没有编译产物』永远为真 ⇒ 门形同虚设」。
//	⇒ 本轮清理把基线下调为空后，这条断言**必然**红 ⇒ ⇒ 它必须被替换。
//	⇒ 替换物不是「删掉这道护栏」，而是**更强的**正控（见上）。
//	⇒ 保留的形状判据是「基线里的每一条都必须写明理由」+「基线条目必须真在仓里」
//	  （后者由门本体的 stale 检查承担）。
func TestTrackedBinaryBaselineIsJustified(t *testing.T) {
	// ★ 基线里的每一条都必须写明理由（★ 免得后来者不知道为什么它被放行）。
	for name, why := range trackedBinaryBaseline {
		if strings.TrimSpace(why) == "" {
			t.Errorf("★ 基线条目 %s 没有写理由 ⇒ 必登记理由", name)
		}
	}
	t.Logf("编译产物基线当前 %d 条（★ 2026-11-09 清理后为空；★ 门是否有效由 "+
		"TestTrackedBinaryScannerPositiveControl 证明，而不是靠「基线非空」）", len(trackedBinaryBaseline))
}
