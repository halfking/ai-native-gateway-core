package ursm_test

// hollowout_shared_helpers_test.go —— 掏空门家族的**共享纯助手**（2026-11-13
// 拆通道轮从 tsv_hollowout_gate_test.go 原样搬出，逐字未改）。★ 本文件**不带**
// `ursmhollowout` build tag：这些助手被默认套件的多个门（gocache_script_family_
// guard / gocache_isolation_guard / live_write_shape / hollowout_gocache 的
// TestMain 自愈预检 / safety 文件）共用，必须留在默认构建里；带 tag 的只有
// **会真掏空工作树、逐条目现编译读方二进制**的行为门（tsv_hollowout_gate /
// structured_registry_hollowout / structured_registry_negative_control /
// tsv_hollowout_negative_control —— 它们只应在 `make guards-hollowout` 的
// 专用预算通道上跑，见 Makefile 该 target 的注释）。

import (
	"crypto/md5"
	"encoding/hex"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
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

// md5Of 算 md5（十六进制）。
func md5Of(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

// tailLines 取输出末尾 n 行。
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
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

// structuredReaderKinds 是【读方形态】的封闭词表。
//
// ★★★ r1039 第三轮新增 `shell`（★ 2026-10-28 实测）：
//
//	★ 本词表原本**比共享 harness 窄**：★ `runHollowReaderOverlay`（★
//	  tsv_hollowout_gate_test.go）**早就支持 `shell`**（★ `bash -c <spec>` ⇒
//	  cwd = 仓根）⇒ ⇒ TSV 侧一直在用 shell 读方 ⇒ ★ 而本表（结构化侧）把
//	  shell 排除在外 ⇒ ⇒ **同一套 harness、两套词表** ⇒ ⇒ 一条「读方是 shell
//	  脚本」的结构化登记册**没法在本表里登记**（★ 只能被普查门当成「没归属」）。
//	★ ⇒ 那不是「读方没有牙」，而是**本表没有那条腿** ⇒ ⇒ 把词表补齐到
//	  harness 的真实能力（★ 只**加**不**改**：★ 既有 54 条的行为不变）。
var structuredReaderKinds = map[string]bool{
	"node":   true,
	"vitest": true,
	"gotest": true,
	"shell":  true,
}

// structuredHollowModeList 返回词表的稳定排序列表（★ 只用于报错信息）。
func structuredHollowModeList() []string {
	out := []string{string(structuredHollowEntries), string(structuredHollowUnusable)}
	sort.Strings(out)
	return out
}

// structuredHollowMode 是【掏空模式】的封闭词表。
type structuredHollowMode string

const (
	// structuredHollowEntries：★ 清单型。容器清空 ＋ 数值归零；键集合与类型骨架保留。
	structuredHollowEntries structuredHollowMode = "entries"
	// structuredHollowUnusable：★ 上限型。整册替换成 `{}` / `[]`（读方拿不到真值）。
	structuredHollowUnusable structuredHollowMode = "unusable"
)

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
