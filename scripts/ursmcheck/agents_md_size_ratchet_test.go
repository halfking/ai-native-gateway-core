//go:build !integration

package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。
//
// agents_md_size_ratchet_test.go —— AGENTS.md 体积棘轮（2026-10-31 分档归档轮）。
//
// 背景（本轮实测的根因链）：
//
//	2026-10-31 凌晨，AGENTS.md 涨到 **2.5MB（约 150 万 token）**，
//	而 AGENTS.md 会**整体注入每一次请求的上下文** ⇒ 超过模型 1M token 窗口
//	⇒ 本仓内**所有 agent 请求**报 `prompt is too long: 1547368 tokens > 1000000 maximum`
//	⇒ ⇒ **一次文档膨胀 ⇒ 整个仓库的 agent 能力全瘫**。
//
//	当轮采取的止血是**分档归档**：把 2026-10-11 起的历史轮次原样移进
//	`AGENTS-archive-20261031.md`，AGENTS.md 只留维护者交接的当前状态。
//
// ★★★ **本门存在的理由：★ 止血不等于修复。★★★
//
//	★ **没有棘轮 ⇒ AGENTS.md 会静默长回去** ⇒ ⇒ 下一次长到 2.5MB 时，
//	★ 仓库又会**全瘫**，★ 而**没有任何门会提前报警** ⇒ ⇒ ⇒
//	★ **「一次事故修好了」不等于「同型事故不会再犯」** ⇒ ⇒ ⇒ 所以要留一道**只会变严**的门。
//
// 判据（棘轮，只降不升）：
//
//	R1 AGENTS.md 的**字节数** ≤ 本门登记的上限 ⇒ 超了 ⇒ 失败。
//	   （上限按 token 预算反推：1M token 窗口里，AGENTS.md 单独不该吃掉太多。
//	     当轮实测归档后为 **404,463 字节（约 10 万 token）**。）
//
//	R2 **归档文件必须存在且非空** ⇒ ⇒ 否则「历史轮次」就**真的丢了**
//	   ⇒ ⇒ （分档归档的另一半是「不丢内容」，★ 只测 AGENTS.md 小是不够的）。
//
//	R3 **每个 R 标记索引行**必须在**归档文件里**真的能找到对应轮次
//	   ⇒ ⇒ 否则索引会指向不存在的轮次 ⇒ ⇒ 索引比没有索引更坏
//	   ⇒ ⇒ （★ 这条是本轮 **18 道交接门全红** 那次事故的直接教训：
//	   ★ R 标记随历史轮次一起搬走了 ⇒ 交接门读 AGENTS.md 读不到 ⇒ 全红。
//	   ★ 现在的形态是：**AGENTS.md 留一份逐轮 R 标记索引**（指针），
//	   ★ **正文在归档文件** ⇒ ⇒ 两边都断不得。）
//
// R4 **归档文件不得被整体注回 AGENTS.md**：AGENTS.md 必须仍显著小于归档文件
//	⇒ ⇒ 否则「分档」名存实亡 ⇒ ⇒ 窗口炸弹会重新装填。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	// agentsMdFile 是「每次请求都被整体注入」的那个文件 ⇒ 它的体积是**可用性**问题。
	agentsMdFile = "AGENTS.md"
	// agentsMdArchiveFile 是历史轮次的归档（★ 不进请求上下文 ⇒ 体积不敏感）。
	agentsMdArchiveFile = "AGENTS-archive-20261031.md"

	// agentsMdMaxBytes 是 AGENTS.md 的体积上限（字节）。
	//
	// ★★★ **为什么是「字节」而不是「行数」**：★★★ ★ 行数与上下文成本不成正比
	// ★（一行可以是 20 个字，也可以是一整段 2KB 的表格）⇒ ⇒ ★ 上下文成本由**字节**决定
	// ★ ⇒ ⇒ 所以量具必须量**字节** ⇒ ⇒ ⇒ （★ 这就是「量具要量对东西」的同型题。）
	//
	// ★★★ **为什么上限取 512 KiB 而不是实测的 404,463 字节**：★★★ ★ 门要留**余量**
	// ★（否则任何一行正常新增都会把门顶红 ⇒ ⇒ 门会被习惯性绕过 ⇒ ⇒ 门就废了）
	// ★ ⇒ 取「实测值 × 1.25 ≈ 505,579」向上取整到 512 KiB ⇒ ⇒
	// ★ **既拦得住失控膨胀（2.5MB ⇒ 超 4.8 倍 ⇒ 必红），又不会误伤正常新增**。
	agentsMdMaxBytes = 512 * 1024

	// agentsMdMinBytes 是 AGENTS.md 的体积下限（字节）⇒ ⇒ 防止「为了过门把内容删光」。
	//
	// ★ 交接面被清空 ⇒ 冷启动助手拿不到任何当前状态 ⇒ ⇒ 那也是坏状态
	// ★ ⇒ ⇒ 所以**双向**：不能太大（炸窗口），也不能太小（丢交接面）。
	agentsMdMinBytes = 128 * 1024
)

// agentsRMarkerIndexRE 匹配 AGENTS.md 里的「逐轮 R 标记索引」行。
//
// ★ 形如 `> - **R143**（§143.98）…… 全文见 `AGENTS-archive-20261031.md`
var agentsRMarkerIndexRE = regexp.MustCompile(`(?m)^> - \*\*(R\d+)(?: 轮)?\*\*`)

func agentsRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func agentsReadRepoFile(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(agentsRepoRoot(t), rel))
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

// TestAgentsMdSizeRatchet —— R1：AGENTS.md 体积在登记区间内（只降不升）。
func TestAgentsMdSizeRatchet(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(agentsRepoRoot(t), agentsMdFile))
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", agentsMdFile, err)
	}
	n := len(raw)
	// ★ 顺带报出 token 估算（★ 粗算：中文/UTF-8 约 3~4 字节 1 token ⇒ 取 4 最保守）。
	t.Logf("★ %s = %d 字节（上限 %d / 下限 %d）≈ %d token（按 4B/token 粗算）",
		agentsMdFile, n, agentsMdMaxBytes, agentsMdMinBytes, n/4)
	if n > agentsMdMaxBytes {
		t.Errorf("★ %s = %d 字节 > 上限 %d 字节 ⇒ ⇒ ⇒ 【上下文窗口炸弹风险】⇒ ⇒ "+
			"★ 请把历史轮次移进 %s（★ 归档不注入请求上下文）⇒ ⇒ "+
			"★ 而不是继续往 AGENTS.md 里追加（★ 2026-10-31 就是 2.5MB ⇒ 全仓 agent 请求全瘫）",
			agentsMdFile, n, agentsMdMaxBytes, agentsMdArchiveFile)
	}
	if n < agentsMdMinBytes {
		t.Errorf("★ %s = %d 字节 < 下限 %d 字节 ⇒ ⇒ ⇒ 【交接面被清空】⇒ ⇒ "+
			"★ 冷启动助手拿不到任何当前状态 ⇒ ⇒ 请确认不是误删",
			agentsMdFile, n, agentsMdMinBytes)
	}
}

// TestAgentsMdArchivePresent —— R2：归档文件存在且非空（历史轮次没丢）。
func TestAgentsMdArchivePresent(t *testing.T) {
	raw := agentsReadRepoFile(t, agentsMdArchiveFile)
	if len(raw) == 0 {
		t.Fatalf("★ %s 是空的 ⇒ ⇒ ⇒ 【历史轮次全丢了】⇒ ⇒ 分档归档的另一半没做到",
			agentsMdArchiveFile)
	}
	if !strings.Contains(raw, "# 追加：") {
		t.Errorf("★ %s 里没有 `# 追加：` 段头 ⇒ ⇒ ⇒ 归档内容形态不对（★ 期望是原样搬出的历史轮次）",
			agentsMdArchiveFile)
	}
	t.Logf("★ %s = %d 字节（★ 归档不进请求上下文 ⇒ 体积不敏感）",
		agentsMdArchiveFile, len(raw))
}

// TestAgentsMdRMarkerIndexPointsAtArchive —— R3：索引里的每个 R 标记在归档里真的存在。
//
// ★★★ 这条是本轮 **18 道 §143.xx 交接门全红** 的直接教训门：★★★
// ★ 分档归档把 R 标记随历史轮次一起搬走了 ⇒ 交接门读 AGENTS.md 读不到 ⇒ 全红
// ⇒ ⇒ 现在的形态是「AGENTS.md 留索引 + 归档留正文」⇒ ⇒ **两边都断不得**
// ⇒ ⇒ ⇒ 本门守住「索引不许指向不存在的轮次」。
func TestAgentsMdRMarkerIndexPointsAtArchive(t *testing.T) {
	agents := agentsReadRepoFile(t, agentsMdFile)
	archive := agentsReadRepoFile(t, agentsMdArchiveFile)

	marks := agentsRMarkerIndexRE.FindAllStringSubmatch(agents, -1)
	if len(marks) == 0 {
		t.Fatalf("★ %s 里找不到「逐轮 R 标记索引」行（形如 `> - **R143**（§143.98）……`）⇒ ⇒ "+
			"★ ★★ 分档归档后【交接面必须仍可查】★★ ⇒ ⇒ 否则 §143.8x~§143.10x 系列的交接门"+
			"（它们判「本轮结论是否落在交接面」）⇒ ⇒ 会【全红】⇒ ⇒ "+
			"★ 2026-10-31 那次分档归档就踩过这个坑（18 道门同时变红）",
			agentsMdFile)
	}
	t.Logf("★ 索引里有 %d 条 R 标记", len(marks))
	for _, m := range marks {
		marker := m[1]
		if !strings.Contains(archive, marker) {
			t.Errorf("★ 索引里的 %s 在 %s 里**不存在** ⇒ ⇒ ⇒ 索引指向了不存在的轮次 ⇒ ⇒ "+
				"★ 索引比没有索引更坏（★ 会把人引向不存在的内容）", marker, agentsMdArchiveFile)
		}
	}
}

// TestAgentsMdSplitIsReal —— R4：分档是真的分档（AGENTS.md 显著小于归档文件）。
//
// ★ 如果 AGENTS.md 反而比归档大 ⇒ ⇒ 「分档」名存实亡 ⇒ ⇒ 窗口炸弹在重新装填。
func TestAgentsMdSplitIsReal(t *testing.T) {
	agentsRaw, err := os.ReadFile(filepath.Join(agentsRepoRoot(t), agentsMdFile))
	if err != nil {
		t.Fatal(err)
	}
	archiveRaw, err := os.ReadFile(filepath.Join(agentsRepoRoot(t), agentsMdArchiveFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(agentsRaw) >= len(archiveRaw) {
		t.Errorf("★ %s = %d 字节 ≥ %s = %d 字节 ⇒ ⇒ ⇒ 「分档」名存实亡 ⇒ ⇒ "+
			"★ 请把历史轮次真正搬进归档文件",
			agentsMdFile, len(agentsRaw), agentsMdArchiveFile, len(archiveRaw))
	}
}
