//go:build !integration

package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。
//
// deploy_seq_probe_orphan_lines_test.go —— 部署修订序列脚本的「孤儿行」检测。
//
// ── 起因（★ 一次真实的生产部署失败）─────────────────────────────────────
//
// 2026-11-24 看板真源统一轮部署 211 时，脚本以 FATAL 收场：
//
//	/root/llm-gateway-go-211/scripts/apply-db-revision-sequence.sh: line 3748:
//	  to_regclass('public.teams') IS NOT NULL AND (SELECT count(*) FROM pg_indexes
//	  WHERE …) = 2 AND … : command not found
//	[211] FATAL: DB 修订序列失败——直接启动会降级 lite，先修库再部署
//
// ★★ 后果：★ **任何部署都被卡死**（不止这一轮；脚本在主干上，谁部署谁撞）。
//
// ── 根因 ────────────────────────────────────────────────────────────────
//
// 那四行是 `ledger_artifact_probe "<file>" \` 的**续行**，但**函数名与文件名丢了**，
// 只剩判据字符串。shell 于是把它当成**一条命令**去执行 ⇒ command not found。
//
// ★ 成因是 teams 族反复改号（055-058 → 982-985 → 918/925/973/986 → 1000-1003
// ★ → 回到 055-058）时的合并冲突：**SQL 判据被保留，调用壳被弄丢**。
//
// ★★ 为什么 `bash -n` 抓不到：★★ 丢掉函数名之后，那几行**在语法上完全合法**
// ★★ （就是一条「命令 + 参数」）⇒ ⇒ 语法检查必须另设判据。
//
// ── 本门判据 ────────────────────────────────────────────────────────────
//
// R1 修订序列脚本里**不得存在「孤儿行」**：★ 行首是一个双引号字符串、
//    且**上一行没有以续行反斜杠结尾** ⇒ ⇒ 它就是一个**没有调用壳的参数**。
//    ★ 例外：`files=( … )` 数组里的 `"$ROOT_DIR/…"` 元素是**合法**的
//    （数组字面量不要求续行）⇒ 按前缀排除。
//
// R2 每条 `ledger_artifact_probe` 的**文件名参数**必须真实存在（仓内可找到），
//    且该 SQL 文件必须**在 `files=()` 数组里**出现 ⇒ 否则判据永远不会被投递到。
//
// ── 跑法 ────────────────────────────────────────────────────────────────
//
//	go test ./scripts/ursmcheck/ -run TestDeploySequenceScript -count=1

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

const deploySeqScript = "../../scripts/apply-db-revision-sequence.sh"

// TestDeploySequenceScriptHasNoOrphanLines：R1 —— 不得有孤儿行。
func TestDeploySequenceScriptHasNoOrphanLines(t *testing.T) {
	raw, err := os.ReadFile(deploySeqScript)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", deploySeqScript, err)
	}
	lines := strings.Split(string(raw), "\n")

	var orphans []string
	for i, l := range lines {
		s := strings.TrimSpace(l)
		if !strings.HasPrefix(s, `"`) {
			continue
		}
		// files=( … ) 数组元素是合法的（数组字面量不要求续行）。
		if strings.HasPrefix(s, `"$ROOT_DIR/`) {
			continue
		}
		// 上一行以续行反斜杠结尾 ⇒ 它是某条命令的续行（合法）。
		if i > 0 && strings.HasSuffix(strings.TrimSpace(lines[i-1]), `\`) {
			continue
		}
		orphans = append(orphans, "L"+strconv.Itoa(i+1)+": "+truncate(s, 120))
	}
	if len(orphans) > 0 {
		t.Errorf("★ %s 里有 %d 处**孤儿行**（行首是字符串、但上一行没有续行反斜杠）⇒\n"+
			"★ ★★ 这几乎总是「调用壳（函数名 + 文件名）在改号/合并冲突中被弄丢」⇒\n"+
			"★ ★★ shell 会把这段 SQL 当成**命令**执行 ⇒ `command not found` ⇒\n"+
			"★ ★★ 部署在 DB 修订序列阶段 FATAL ⇒ **所有部署被卡死**。\n"+
			"★ ★★ 真实事故（2026-11-24，211）：\n"+
			"★ ★★   line 3748: to_regclass('public.teams') …: command not found\n"+
			"★ ★★   [211] FATAL: DB 修订序列失败——直接启动会降级 lite，先修库再部署\n"+
			"★ ★★ 修法：把丢失的 `ledger_artifact_probe \"<file>\" \\` 前缀补回（或删掉\n"+
			"★ ★★ 这段重复的判据，保留文件里另一处真正在用的那一份）。\n"+
			"★ 孤儿行：\n%s", deploySeqScript, len(orphans), strings.Join(orphans, "\n"))
	}
}

// TestDeploySequenceProbesAreDeliverable：R2 —— 每条探针的文件名要真实存在、且被投递。
func TestDeploySequenceProbesAreDeliverable(t *testing.T) {
	raw, err := os.ReadFile(deploySeqScript)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", deploySeqScript, err)
	}
	s := string(raw)

	// files=() 数组里投递的文件名集合。
	inFiles := false
	files := map[string]bool{}
	for _, l := range strings.Split(s, "\n") {
		t2 := strings.TrimSpace(l)
		if strings.HasPrefix(t2, "files=(") {
			inFiles = true
			continue
		}
		if inFiles {
			if t2 == ")" {
				inFiles = false
				continue
			}
			if strings.Contains(t2, "/sql/migrations/startup/") {
				rest := t2[strings.LastIndex(t2, "/")+1:]
				end := strings.Index(rest, `"`)
				if end >= 0 {
					files[rest[:end]] = true
				}
			}
		}
	}

	// 探针调用：`ledger_artifact_probe "<file>" \`
	for _, l := range strings.Split(s, "\n") {
		t2 := strings.TrimSpace(l)
		if !strings.HasPrefix(t2, `ledger_artifact_probe "`) {
			continue
		}
		rest := strings.TrimPrefix(t2, `ledger_artifact_probe "`)
		end := strings.Index(rest, `"`)
		name := ""
		if end >= 0 {
			name = rest[:end]
		}
		if name == "" {
			t.Errorf("★ %s 里有一条 ledger_artifact_probe **没有文件名参数** ⇒ 判据挂不到任何迁移文件上。", deploySeqScript)
			continue
		}
		if !files[name] {
			t.Errorf("★ 探针 %q 指向的文件**不在 files=() 投递数组里** ⇒\n"+
				"★ ★★ 该迁移永远不会被投递 ⇒ 这条判据**永远不会变绿**（要么永远红，要么是死门）。", name)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
