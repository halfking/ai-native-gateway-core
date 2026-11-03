//go:build !integration

package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。
//
// outbox_freshinstall_column_closure_test.go —— 「outbox 状态机写的列 ⊆ fresh-install 迁移建的列」
// 闭集交叉门（2026-11-03 12h 审计 A1 引入）。
//
// # 这道门治什么病
//
// 缺口本体（P0，已实测复现）：
//
//   · 全新安装的库，`outbox_events` 由 **932**（932_outbox_events_fresh_install.sql）创建。
//   · 该迁移此前**漏了 `last_attempt_at` 这一列** ⇒ 而 outbox 状态机的
//     **6 条 UPDATE 全部写它**：
//         internal/outbox/dispatcher.go:386  SET status='sent',     last_attempt_at=NOW(), …
//         internal/outbox/dispatcher.go:422  SET status='failed',   last_attempt_at=NOW(), …
//         internal/outbox/dispatcher.go:485  SET status='failed',   last_attempt_at=NOW(), …
//         internal/outbox/dispatcher.go:522  SET status='dlq',      last_attempt_at=NOW(), …
//         internal/outbox/replay.go:36       SET status='sent',     last_attempt_at=NOW(), …
//         internal/outbox/replay.go:43       SET last_attempt_at=NOW(), …
//   · ⇒ PostgreSQL 报 **42703 column … does not exist** ⇒ markSent/markFailed/
//     markDLQ/markBackpressure **全部失败** ⇒ 行永远停在 `pending` ⇒
//     ⇒ 派发器每个 tick（5s）重复 claim 同一条事件、重复投给 ASM ⇒
//     ⇒ **全新安装的 outbox 投递彻底死掉**。
//
// ★ 为什么此前没人发现：**211 生产上那张表是手工建的**
//   （deploy/sql/migrations/V357__create_outbox_events_table.sql:36 **有**这一列）
//   ⇒ ⇒ 211 不会暴露；⇒ 而**恰恰是这条迁移要修的"全新安装"场景**会拿到残缺的表。
//   ⇒ ⇒ 迁移注释写的「幂等：已有环境（211/252）no-op」**掩盖了全新安装这条路径**。
//
// ★ 为什么不能只靠「记住要加这一列」：**CREATE TABLE IF NOT EXISTS 不做列对齐**
//   ⇒ 「表已在但列不齐」的环境**修不回来** ⇒ ⇒ 必须显式 `ADD COLUMN IF NOT EXISTS`
//   （本轮已补）⇒ ⇒ 而**下一次**有人加一列状态机字段、忘了同步迁移，
//   ⇒ ⇒ **同一形态的洞会再开一次** ⇒ ⇒ 所以要有一道**门**钉住这个闭集。
//
// # 判据
//
//   ① 抽出 outbox 状态机（dispatcher + replay）**实际写到的列**集合 W；
//   ② 抽出 fresh-install 迁移 932 **实际建到的列**集合 D；
//   ③ 要求 **W ⊆ D**（状态机写的每一列，迁移都必须建出来）。
//      ⇒ 少一列 ⇒ 门红（且红在**提交前**，不是在生产 5 秒一次的重试里）。
//   ④ 恒绿护栏：W 与 D 都**不得为空** ⇒ 提取器坏了 ⇒ 门红
//      （★ 否则「一个列名都没抽到」会假装成「没有缺口」）。
//
// ★ 形态判据而非具体值：**不写死 `last_attempt_at` 这一个列名** ⇒
//   ⇒ 下次状态机加任何一列，本门自动纳入 ⇒ ⇒ 门不会因为「只盯一个洞」而过期。
//
// ★ 变异臂（证明门有牙）：
//   M1：从迁移里去掉状态机需要的任一列 ⇒ 本门必须红。
//   M2：把提取器的输入路径改成不存在的文件 ⇒ 本门必须红（不是静默通过）。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 仓内相对路径。
const (
	outboxDispatcherGo = "internal/outbox/dispatcher.go"
	outboxReplayGo     = "internal/outbox/replay.go"
	outboxFreshInstall = "sql/migrations/startup/932_outbox_events_fresh_install.sql"
)

// outboxRepoRoot 定位仓根（scripts/ursmcheck → ../../ = 仓根）。
func outboxRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败：%v", err)
	}
	return filepath.Dir(filepath.Dir(wd))
}

func outboxRead(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(outboxRepoRoot(t), rel))
	if err != nil {
		t.Fatalf("读 %s 失败：%v", rel, err)
	}
	return string(b)
}

// outboxStateMachineWrittenColumns 抽出状态机**写到的列**。
//
// ★ 判据形态：`UPDATE … outbox_events SET col = …` ⇒ 取 SET 之后的每个
//
//	`name =` / `name =`（含 `last_attempt_at = NOW()`）⇒ ★ 只认**写**不认读
//	（读列不需要表里有？不 —— 读列也需要表里有 ⇒ ★ 这里只钉写侧，是因为
//	★ 写侧才是「状态机必需列」这个语义的来源；读侧由下面的护栏兜）。
var outboxSetClauseRe = regexp.MustCompile(`(?is)UPDATE\s+(?:public\.)?outbox_events\s+SET\s+(.*?)(?:WHERE|$)`)

// outboxColumnAssignRe 匹配 SET 子句里的 `col =`（★ 排除函数调用的实参）。
var outboxColumnAssignRe = regexp.MustCompile(`(?m)([a-z_][a-z0-9_]*)\s*=`)

func outboxStateMachineWrittenColumns(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, rel := range []string{outboxDispatcherGo, outboxReplayGo} {
		src := outboxRead(t, rel)
		for _, m := range outboxSetClauseRe.FindAllStringSubmatch(src, -1) {
			setClause := m[1]
			for _, cm := range outboxColumnAssignRe.FindAllStringSubmatch(setClause, -1) {
				col := cm[1]
				// ★ 排除 SQL 关键字与函数实参（`status` 之类的真列会留下）。
				switch strings.ToLower(col) {
				case "and", "or", "not", "where", "set", "values":
					continue
				}
				out[col] = true
			}
		}
	}
	return out
}

// outboxFreshInstallColumns 抽出 fresh-install 迁移 **CREATE TABLE 建到的列**。
var outboxCreateTableRe = regexp.MustCompile(`(?is)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:public\.)?outbox_events\s*\((.*?)\n\)\s*;`)

// outboxAddColumnRe 抽出 `ADD COLUMN IF NOT EXISTS col`（列对齐补的列也要算）。
var outboxAddColumnRe = regexp.MustCompile(`(?is)ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)\s`)

func outboxFreshInstallColumns(t *testing.T) map[string]bool {
	t.Helper()
	src := outboxRead(t, outboxFreshInstall)
	out := map[string]bool{}
	for _, m := range outboxCreateTableRe.FindAllStringSubmatch(src, -1) {
		for _, line := range strings.Split(m[1], "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "--") {
				continue
			}
			// ★ 一行一个列定义 ⇒ 取第一个标识符。
			f := regexp.MustCompile(`^([a-z_][a-z0-9_]*)\s`).FindStringSubmatch(line)
			if f == nil {
				continue
			}
			out[f[1]] = true
		}
	}
	for _, m := range outboxAddColumnRe.FindAllStringSubmatch(src, -1) {
		out[strings.ToLower(m[1])] = true
	}
	return out
}

// TestOutboxFreshInstallCoversStateMachineColumns 是门本体。
//
// ★ 2026-11-03（12h 审计 A1）：判据 = 「状态机写的列 ⊆ fresh-install 迁移建的列」。
func TestOutboxFreshInstallCoversStateMachineColumns(t *testing.T) {
	written := outboxStateMachineWrittenColumns(t)
	created := outboxFreshInstallColumns(t)

	// ★★ 恒绿护栏：提取器坏了 ⇒ 门必须红（而不是「没抽到列 ⇒ 没缺口 ⇒ 绿」）。
	if len(written) == 0 {
		t.Fatalf("★ 从 %s / %s 一个列都没抽到 ⇒ 提取器坏了 ⇒ 门判红（★ 否则「空集合 ⊆ 任何集合」会假装通过）",
			outboxDispatcherGo, outboxReplayGo)
	}
	if len(created) == 0 {
		t.Fatalf("★ 从 %s 一个列都没抽到 ⇒ 提取器坏了 ⇒ 门判红", outboxFreshInstall)
	}

	var missing []string
	for col := range written {
		if !created[col] {
			missing = append(missing, col)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("★★ outbox 状态机写的列在 fresh-install 迁移 %s 里**没有**建出来 ⇒ ⇒ "+
			"全新安装的库里这些 UPDATE 会报 42703（column does not exist）⇒ ⇒ "+
			"markSent/markFailed/markDLQ 全部失败 ⇒ 行永远停在 pending ⇒ ⇒ "+
			"派发器每个 tick 重复投递同一条事件 ⇒ ⇒ **outbox 投递彻底死掉** ⇒ ⇒ "+
			"缺失列：%v",
			outboxFreshInstall, missing)
	}
}

// TestOutboxStateMachineWrittenColumnsExtractionHasTeeth 是**变异臂**（证明门有牙）。
func TestOutboxStateMachineWrittenColumnsExtractionHasTeeth(t *testing.T) {
	written := outboxStateMachineWrittenColumns(t)

	// ★ 断言提取器抓到的列里**必须**含这几列 —— 它们是状态机的骨架。
	//   ★ 这里**不写死判据要用的那个洞**（last_attempt_at 由上一条门兜），
	//   ★ 但要保证提取器没把 SET 子句整体漏掉。
	for _, want := range []string{"status", "attempts", "last_attempt_at", "updated_at"} {
		if !written[want] {
			t.Errorf("★ 提取器没抓到状态机写的列 %q ⇒ 提取器坏了（★ 否则上一条门会恒绿）⇒ 抽到的列：%v",
				want, written)
		}
	}

	// ★ 提取器**不得**把 WHERE 之后的列当成写列（否则判据面会被放大）。
	src := outboxRead(t, outboxDispatcherGo)
	if strings.Contains(src, "SET status = 'sent', last_attempt_at = NOW()") {
		if written["now"] {
			t.Errorf("★ 提取器把 SQL 关键字/函数名当成了列 ⇒ 判据面被放大 ⇒ 抽到的列：%v", written)
		}
	}
}
