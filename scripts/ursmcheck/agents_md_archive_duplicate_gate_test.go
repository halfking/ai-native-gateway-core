package ursm_test

// agents_md_archive_duplicate_gate_test.go —— AGENTS.md 与归档档的「逐字重复」门（2026-11-24 合并轮二十二）。
//
// ★★ 为什么立这道门（★ 本轮实测到的根因）：★★
//
// 2026-11-24 合并轮二十二里，`TestAgentsMdSizeRatchet` 报红（★ 552,667 字节 > 上限 524,288）。
// ★ 归因时才发现：**AGENTS.md 里有 17 段、共 263,063 字节与 `AGENTS-archive-20261031.md` 逐字相同**。
// ★ ⇒ ⇒ 根因不是「最近几轮写太多」，★ 而是**分档归档这个动作只做了一半**：
// ★★ 「把原文追加进归档档」做了、★★「把原文从主档摘除」**没做** ⇒
// ★★ ⇒ 主档被同一批内容**顶了两份** ⇒ ⇒ 体积棘轮被**虚高**触发。
//
// ★★ 于是历次归档都在做同一件治标的事（★ 再搬一批老块进归档档、★ 主档又留一份指针），
// ★★ 而那 263 KB 的重复一直躺在那里 ⇒ ★★ **归档量越大、主档越胖**。
//
// ★★ 本门把「归档＝搬移、不是复制」这条纪律变成**可判定的**：
// ★★ ① 扫描面＝**主档全部行**（★ 含未跟踪的）；
// ★★ ② 判据＝**连续 ≥4 行的逐字相同段**（★ 为什么不是 1 行：★ 单行相同极常见、
// ★★ 例如空行、`> ` 引用前缀、markdown 表格分隔行 ⇒ 那不是重复）；
// ★★ ③ 报红时给出**主档行号 ＋ 归档档行号**，★ 便于直接摘除。
//
// ★★ 边界（★ 诚实登记）：★ 本门只看**逐字相同**、★ 不看「语义相同但措辞不同」；
// ★★ 且**连续 ≥4 行**这个阈值下、★ 4 行以内的短重复**不在本门视野**。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// agentsDupRepoRoot 返回仓根目录（★ 本门自带一份，★ 不依赖别的门文件里的私有辅助函数）。
func agentsDupRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

const (
	agentsMdFileForDup = "AGENTS.md"
	agentsArchiveFile  = "AGENTS-archive-20261031.md"
	// agentsDupMinRun 是「算重复」的最小连续行数。
	agentsDupMinRun = 4
)

// agentsDupLongestRun 返回从 a 的第 i 行起、在 b 里【连续】出现的最长段长度（★ 行级逐字比较）。
func agentsDupLongestRun(a, b []string, i int) int {
	// b 的行 -> 下标集合（★ 一个行可能在归档档里出现多次 ⇒ 全部试）。
	pos := map[string][]int{}
	for j, l := range b {
		pos[l] = append(pos[l], j)
	}
	best := 0
	for n := 1; i+n <= len(a); n++ {
		found := false
		for _, j0 := range pos[a[i]] {
			if j0+n > len(b) {
				continue
			}
			ok := true
			for k := 0; k < n; k++ {
				if b[j0+k] != a[i+k] {
					ok = false
					break
				}
			}
			if ok {
				found = true
				break
			}
		}
		if !found {
			break
		}
		best = n
	}
	return best
}

// TestAgentsMdHasNoVerbatimArchiveDuplicate —— 主档里不得有【与归档档逐字相同】的连续段。
func TestAgentsMdHasNoVerbatimArchiveDuplicate(t *testing.T) {
	root := agentsDupRepoRoot(t)
	mdPath := filepath.Join(root, agentsMdFileForDup)
	arcPath := filepath.Join(root, agentsArchiveFile)
	mdRaw, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("读 %s：%v", agentsMdFileForDup, err)
	}
	arcRaw, err := os.ReadFile(arcPath)
	if err != nil {
		t.Fatalf("读 %s：%v", agentsArchiveFile, err)
	}
	md := strings.Split(string(mdRaw), "\n")
	arc := strings.Split(string(arcRaw), "\n")

	hits := 0
	for i := 0; i < len(md); i++ {
		if strings.TrimSpace(md[i]) == "" {
			continue // ★ 空行不参与（★ 否则空行连段会制造大量假命中）
		}
		n := agentsDupLongestRun(md, arc, i)
		if n >= agentsDupMinRun {
			hits++
			t.Errorf("★ %s 第 %d 行起有 **%d 行**与 %s 逐字相同（★ 归档档同段从第 %d 行起）\n"+
				"★ ⇒ ⇒ 归档动作只做了【追加】、★ 没做【摘除】⇒ ⇒ 主档被同一批内容顶了两份\n"+
				"★ ⇒ ⇒ 修法：★ 把主档这段**摘掉**、★ 只留一行指针（★ 原文已在归档档、★ 逐字不丢）",
				agentsMdFileForDup, i+1, n, agentsArchiveFile, archiveLineOf(md, arc, i, n))
			i += n - 1
		}
	}
	if hits == 0 {
		t.Logf("★ 主档 %d 行 / 归档档 %d 行 ⇒ **0 段**逐字重复（★ 连续 ≥%d 行的判据）",
			len(md), len(arc), agentsDupMinRun)
	}
}

// archiveLineOf 给出一段在归档档里的起始行号（★ 1-based、★ 找不到时返回 0）。
func archiveLineOf(md, arc []string, i, n int) int {
	pos := map[string][]int{}
	for j, l := range arc {
		pos[l] = append(pos[l], j)
	}
	for _, j0 := range pos[md[i]] {
		if j0+n <= len(arc) {
			ok := true
			for k := 0; k < n; k++ {
				if arc[j0+k] != md[i+k] {
					ok = false
					break
				}
			}
			if ok {
				return j0 + 1
			}
		}
	}
	return 0
}

// TestAgentsMdArchiveDuplicateDetectorBites —— 量具自证：★ 往主档里塞一段与归档档逐字相同的
// 内容 ⇒ 检测器**必须**报出来（★ 证明判据不是恒绿）。
func TestAgentsMdArchiveDuplicateDetectorBites(t *testing.T) {
	root := agentsDupRepoRoot(t)
	arcRaw, err := os.ReadFile(filepath.Join(root, agentsArchiveFile))
	if err != nil {
		t.Fatalf("读 %s：%v", agentsArchiveFile, err)
	}
	arc := strings.Split(string(arcRaw), "\n")
	// 取归档档里一段**连续 ≥4 行、且每行都非空**的正文当样本。
	// ★ 必须【连续】：★ 逐字比较是逐行对齐的、★ 中间跳过空行就构不成一段。
	var sample []string
	for i := 0; i+agentsDupMinRun <= len(arc); i++ {
		ok := true
		for k := 0; k < agentsDupMinRun; k++ {
			if strings.TrimSpace(arc[i+k]) == "" {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		sample = append(sample, arc[i:i+agentsDupMinRun]...)
		break
	}
	if len(sample) < agentsDupMinRun {
		t.Skipf("★ 归档档里找不到 %d 行连续非空样本 ⇒ 跳过自证", agentsDupMinRun)
	}
	// 伪造一份主档：★ 前面放一段无关内容、★ 后面放样本 ⇒ 检测器必须命中样本起点。
	fake := append([]string{"> 无关的第 1 行", "> 无关的第 2 行"}, sample...)
	n := agentsDupLongestRun(fake, arc, 2)
	if n < agentsDupMinRun {
		t.Fatalf("★ 量具自证失败：★ 塞进去的 %d 行样本只识别出 %d 行 ⇒ ⇒ 检测器**恒绿** ⇒ 判据无效",
			len(sample), n)
	}
	t.Logf("★ 量具自证：★ 塞入 %d 行样本 ⇒ 检测器识别 %d 行 ⇒ 判据有牙齿", len(sample), n)
}
