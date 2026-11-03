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
//	  cmd/llm-gw-annotator/llm-gw-annotator   （历史遗留，先例，非豁免）
//	  installer/llm-gw-installer                （历史遗留，先例，非豁免）
//	⇒ 新增一个 ⇒ 门红（★ 这就是防 A14 复发的那只手）。
//	⇒ 基线里的文件被从仓里删掉 ⇒ 门红（★ 强制把基线同步下调，不留「空占位」）。
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
// ★ 两条都是**历史遗留**（先例，不是豁免）—— 它们在 2026-11-03 之前就在仓里。
// ★ 2026-11-03 12h 审计 A14 新增的 `installer/e2e-bootstrap` **不在**本表内
//
//	⇒ ⇒ 它被本门当场抓住 ⇒ ⇒ 并已从索引移除 + 补 `.gitignore`。
var trackedBinaryBaseline = map[string]string{
	"cmd/llm-gw-annotator/llm-gw-annotator": "历史遗留（先例，非豁免）：独立注解工具的编译产物，未清理",
	"installer/llm-gw-installer":            "历史遗留（先例，非豁免）：安装器编译产物，未清理",
}

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

// trackedBinaryScan 扫全仓被跟踪的文件，返回「编译产物文件 → 其 magic」的集合。
//
// ★ 实现要点：逐个 blob 只读**前 4 字节**，其余用 io.CopyN 丢弃 ⇒
//
//	⇒ 不把 300MB+ 的仓内容全读进内存 ⇒ ⇒ 扫描是流式的。
func trackedBinaryScan(t *testing.T) map[string]string {
	t.Helper()
	root := trackedBinaryRepoRoot(t)

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
			fmt.Fprintf(stdin, "HEAD:%s\n", n)
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
	hits := trackedBinaryScan(t)

	// ★★ 恒绿护栏：扫描结果为空 ⇒ 提取器坏了 ⇒ 门必须红
	//   （★ 否则「一个编译产物都没扫到」会假装成「仓很干净」）。
	if len(hits) == 0 {
		t.Fatalf("★ 全仓一个编译产物都没扫到 ⇒ 扫描器坏了 ⇒ 门判红（★ 基线非空 ⇒ 扫不到就是异常）")
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

// TestTrackedBinaryBaselineIsNotEmpty 是基线自身的形状判据。
func TestTrackedBinaryBaselineIsNotEmpty(t *testing.T) {
	if len(trackedBinaryBaseline) == 0 {
		t.Fatalf("★ 编译产物基线是空的 ⇒ 「没有编译产物」这条结论将永远为真 ⇒ 门形同虚设")
	}
	// ★ 基线里的每一条都必须写明理由（★ 免得后来者不知道为什么它被放行）。
	for name, why := range trackedBinaryBaseline {
		if strings.TrimSpace(why) == "" {
			t.Errorf("★ 基线条目 %s 没有写理由 ⇒ 必登记理由", name)
		}
	}
}
