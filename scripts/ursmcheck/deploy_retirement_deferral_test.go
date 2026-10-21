package ursm_test

// v1 request_logs 族的「显式退役」守卫（2026-10-21，§143.53 轮新增）
//
// # 背景：退役类迁移不能躺在「每次部署都跑」的自动列表里
//
// 迁移 890（sql/migrations/startup/890_drop_request_logs_v1_family.sql）
// 自带**非空闸**：v1 族里有行就 RAISE EXCEPTION，把「要不要丢历史」交回人。
// ★ 这个闸本身是**对的**（删表 = 删历史，不可回滚）。
//
// ★ 但 890 同时躺在每次部署的自动重放列表里
// （scripts/apply-db-revision-sequence.sh 的 files=(...)）⇒
// ⇒ 自动通道每次部署都真的执行它 ⇒ ⇒ 撞闸 ⇒ ⇒ `set -euo pipefail`
//   + `psql -v ON_ERROR_STOP=1` ⇒ **迁移腿立即中止** ⇒ ⇒ 部署中止，
//   且 **它后面的 891/892 永远上不去**。
//
// ★★ 实测（2026-10-21，生产只读）：
//
//	211：_request_logs est 35,506 行 / _request_logs_bodies est 35,506 行
//	252：_request_logs est 38,193 行 / _request_logs_bodies est 23,830 行
//	     / _request_logs_archive est 22,656 行
//
// ⇒ 两实例的 v1 族都非空 ⇒ ⇒ 890 的闸在两边都会 RAISE。
//
// # 本门守的是什么
//
// ★ 结构性结论：**退役类迁移（自带非空闸 ⇒ 天然不可自动重放）不能放在
//   每次部署的自动列表里**，必须拆成显式的、属主授权的退役步骤。
//
// ⇒ `scripts/retire-request-logs-v1-family.sh` 就是那个「显式退役步骤」，
//   它提供两个写模式：
//
//	--defer    在台账里登记「890 已刻意不由自动通道执行」
//	           ⇒ 自动通道的跳过规则是「台账行 content_sha256 == 文件当前
//	             sha256 ⇒ 跳过」⇒ ⇒ 写一行 sha 相同的台账即可让自动通道
//	             跳过 ⇒ ⇒ **部署不再被非空闸挡住**，且 v1 历史原样保留。
//	--execute  属主授权的真正退役（前置逐表计数 → 应用 890 → 记账）。
//
// ★★ 关键设计：defer **不需要修改 `apply-db-revision-sequence.sh`** ⇒
//   那个文件此刻正被并行会话编辑（同文件不要两个写者）⇒ ⇒ 改用
//   「台账跳过规则」这个**已存在的机制** ⇒ 零冲突。
//
// # 本门的形状（★ 每条都要问「它真的在守那个形态吗」）
//
//	1. 静态：退役脚本的不变量（授权闸在最前、sha 取自文件、defer 写
//	    NULL 的 execution_sha256、undo 只删带标记的行、execute 先删 defer 行）。
//	2. 静态：★ 自动通道的跳过规则**必须还在**（`stored_sha == file_sha`）
//	    ⇒ ★★ 上游一旦改了跳过规则，defer 机制会**静默失效** ⇒ 必须变红。
//	3. 行为：★ 从**真实脚本**里按内容抽出判据块，放进**真实 for 循环**
//	    执行（`continue` 语义与生产一致），用 stub 替掉 I/O ⇒
//	    判「defer 之后自动通道到底跳不跳」。
//	    ⚠ 不复制判据逻辑 ⇒ 避免「副本漂移」这一经典假绿。
//	4. 反例：sha 不匹配时必须走 apply 路径 ⇒ 证明尺子分得清两种形态。

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	retirementScriptPath = "../../scripts/retire-request-logs-v1-family.sh"
	applySeqScriptPath   = "../../scripts/apply-db-revision-sequence.sh"
	retirementSQLPath    = "../../sql/migrations/startup/890_drop_request_logs_v1_family.sql"
)

// shellCaseBlock 抽出 shell `case` 里某个分支的块文本（`--name)` 到 `;;`）。
//
// ★★ 为什么必须【按块】断言而不是全局子串：
//
//	本轮第一版门用「全局子串存在」做判据 ⇒ 变异 M3（把 undo-defer 里的
//	`AND runtime_adaptation = '${DEFER_MARKER}'` 改成 `AND 1=1`）**没被抓到**
//	⇒ 原因：这个 needle 在文件里**出现两次**（undo-defer 分支一次、
//	execute 分支一次）⇒ 改掉一处，另一处还在 ⇒ ⇒ **门照样绿**。
//
// ⇒ ⇒ 判据必须落在【它该在的那个块】里 ⇒ ⇒ 同一个文件的两个分支不能互相
//     顶替对方的证据。
func shellCaseBlock(stripped, name string) (string, bool) {
	lines := strings.Split(stripped, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "--"+name+")" {
			start = i
			break
		}
	}
	if start < 0 {
		return "", false
	}
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == ";;" {
			return strings.Join(lines[start:i], "\n"), true
		}
	}
	return strings.Join(lines[start:], "\n"), true
}

// retireGaps 返回「缺哪些不变量」的清单（空 = 满足）。
// ★ 抽成函数 ⇒ 正例与合成反例共用**同一把尺**（否则反例证明不了尺的有效性）。
//
// ★ 分两级：
//   - 全局级：授权闸 / sha 取源 / 目标文件路径 / 分支存在性；
//   - 分支级：defer / undo-defer / execute 各自的判据（★ 互相不能顶替）。
func retireGaps(stripped string) []string {
	need := []struct {
		what string
		want string
	}{
		{"必须有显式授权开关", "LLM_GATEWAY_RETIREMENT_ACK"},
		{"无授权必须以非 0 退出", "exit 3"},
		{"授权闸必须排在建通道之前", `if [[ -n "${DATABASE_URL:-}" ]]; then`},
		{"sha 必须取自 890 迁移文件本身（不能硬编码）", `FILE_SHA="$(sha256_of "$RETIREMENT_SQL")"`},
		{"必须指向 890 迁移文件", "890_drop_request_logs_v1_family.sql"},
		{"必须有可撤销的 undo 模式", "--undo-defer"},
		{"必须有真正退役的模式", "--execute"},
		{"defer 必须写自述标记（常量名）", "DEFER_MARKER="},
		// ★★★ 台账 marker 按【basename】派生 ⇒ 文件改名 ⇒ marker 变 ⇒ 静默失效
		//   （2026-10-21 实踩：在 211 上把文件传成 890.sql ⇒ marker 变成 ...:890.sql
		//     ⇒ 自动通道查 ...:890_drop_request_logs_v1_family.sql 查不到 ⇒ 照样执行 890）
		{"必须钉住 canonical basename（台账 marker 按它派生）", `CANONICAL_BASE="890_drop_request_logs_v1_family.sql"`},
		{"basename 不符时必须响亮失败（不能静默写没用的台账）", `ACTUAL_BASE" != "$CANONICAL_BASE`},
		{"defer 标记的字面量不得被改", "deferred-to-explicit-retirement"},
	}
	var gaps []string
	for _, n := range need {
		if !strings.Contains(stripped, n.want) {
			gaps = append(gaps, n.what+"（缺："+n.want+"）")
		}
	}

	// ── 分支级判据（★ 同一个文件的两个分支不能互相顶替对方的证据）──
	blocks := []struct {
		branch string
		need   []string
	}{
		{"defer", []string{
			"content_sha256",
			"execution_sha256 = NULL",
			"${DEFER_MARKER}",
			// ★ needle 必须能区分**正确形态**与**貌似合理的错误形态**：
			//   "ON CONFLICT" 太松（DO NOTHING 也含它）⇒ 变异 M9 漏网过。
			"ON CONFLICT (sequence_name) DO UPDATE",
		}},
		{"undo-defer", []string{
			"DELETE FROM ${LEDGER_TABLE}",
			"runtime_adaptation = '${DEFER_MARKER}'",
		}},
		{"execute", []string{
			"DELETE FROM ${LEDGER_TABLE}",
			"runtime_adaptation = '${DEFER_MARKER}'",
			`psql_f < "$RETIREMENT_SQL"`,
		}},
	}
	for _, b := range blocks {
		block, ok := shellCaseBlock(stripped, b.branch)
		if !ok {
			gaps = append(gaps, "缺少 --"+b.branch+" 分支")
			continue
		}
		for _, want := range b.need {
			if !strings.Contains(block, want) {
				gaps = append(gaps, "--"+b.branch+" 分支缺少："+want)
			}
		}
	}
	return gaps
}

// readRetireFile 读仓内文件（★ 独立命名，避免与同包其他测试的 helper 撞名）。
func readRetireFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// retireFileSha256 算文件 sha256（与生产通道的 sha256_file 同款算法）。
func retireFileSha256(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// authorizationGuardShape 判「授权闸**真的在守**」而不是「授权字样还在」。
//
// ★★ 为什么不能只查字符串：本轮第一次写这版门时，它只要求
// `LLM_GATEWAY_RETIREMENT_ACK` 与 `exit 3` 这两个**字符串存在** ⇒
// ⇒ 变异 M1 把整个授权条件换成 `if false; then`（**授权闸彻底失效**）
// ⇒ ⇒ 而这两个字符串**仍在文件里**（一个在错误消息里，一个在死代码块里）
// ⇒ ⇒ **门照样绿** ⇒ ★ 门无牙。
//
// ⇒ 所以这里查的是**结构**：从授权字样到 `exit 3` 之间的那一段里，
// 必须出现「与 "1" 比较」这个动作（`!=` + `"1"`）。
// ⇒ 授权条件一旦被换成常量真值，这一段里就没有比较了 ⇒ ⇒ 门变红。
func authorizationGuardShape(stripped string) bool {
	idx := strings.Index(stripped, "LLM_GATEWAY_RETIREMENT_ACK")
	if idx < 0 {
		return false
	}
	rest := stripped[idx:]
	end := strings.Index(rest, "exit 3")
	if end < 0 {
		return false
	}
	block := rest[:end]
	return strings.Contains(block, "!=") && strings.Contains(block, `"1"`)
}

// TestRetirementDeferralScriptInvariants —— 退役脚本的不变量。
func TestRetirementDeferralScriptInvariants(t *testing.T) {
	stripped := stripShellCommentsAndInline(readRetireFile(t, retirementScriptPath))
	if gaps := retireGaps(stripped); len(gaps) > 0 {
		for _, g := range gaps {
			t.Errorf("退役脚本缺少不变量：%s", g)
		}
		t.Fatalf("共 %d 处缺口 ⇒ ★ 890 仍会躺在自动重放列表里挡住每次部署", len(gaps))
	}

	// ★ 授权闸的**位置**必须早于建通道 ⇒ 否则「没配 DSN」会掩盖「没授权」。
	ackIdx := strings.Index(stripped, "LLM_GATEWAY_RETIREMENT_ACK")
	chanIdx := strings.Index(stripped, `if [[ -n "${DATABASE_URL:-}" ]]; then`)
	if ackIdx < 0 || chanIdx < 0 {
		t.Fatal("定位失败：授权闸或建通道的锚点不见了")
	}
	if ackIdx > chanIdx {
		t.Errorf("授权闸排在了建通道之后（ack@%d > channel@%d）⇒ 无授权时也会先尝试连库", ackIdx, chanIdx)
	}

	// ★ 结构判定：授权闸必须**真的在比较**，而不是常量真值。
	//   （本轮第一次写这版门时只查字符串存在 ⇒ 变异 M1 把它换成 `if false`
	//     门照样绿 ⇒ ★ 门无牙 ⇒ 这里补上结构判定。）
	if !authorizationGuardShape(stripped) {
		t.Error("★ 授权闸不是「比较」形态（授权字样在，但到 exit 3 之间没有与 \"1\" 的比较）\n" +
			"   ⇒ 授权闸可能已被换成常量真值 ⇒ 生产写会在无授权下发生")
	}
}

// TestAutoChannelSkipRuleStillPresent —— ★ 自动通道的跳过规则必须还在。
//
// ★★ 这是本门最容易被忽略、但最要命的一条：
// defer 机制**完全依赖**「台账行 content_sha256 == 文件 sha ⇒ 跳过」这条
// 规则（scripts/apply-db-revision-sequence.sh 的执行循环）。
// ⇒ ★ 上游一旦把这条规则换掉（例如改成只比对 execution_sha256），
//   defer 会**静默失效**：台账里明明有 sha 相同的行，自动通道却照样执行
//   890 ⇒ ⇒ 部署又被非空闸挡住，而**没有任何门会红**。
func TestAutoChannelSkipRuleStillPresent(t *testing.T) {
	src := readRetireFile(t, applySeqScriptPath)
	if !strings.Contains(src, `[[ "$stored_sha" == "$file_sha" ]]`) {
		t.Fatal("★ 自动通道的跳过判据 `[[ \"$stored_sha\" == \"$file_sha\" ]]` 不见了\n" +
			"   ⇒ defer 机制已经静默失效（台账里有 sha 相同的行也会被照样执行）\n" +
			"   ⇒ 请重新设计 defer 的落点，或更新本门与退役脚本")
	}
	// ★ 跳过判据必须读的是 content_sha256（不是别的列）。
	if !strings.Contains(src, "SELECT content_sha256 FROM public.gateway_db_revision_sequences") {
		t.Fatal("自动通道读台账指纹的那句变了 ⇒ defer 写的列可能已经不是它读的那一列")
	}
}

// extractSkipBlock 从真实脚本里**按内容**抽出「跳过判据块」。
//
// ⚠ 为什么不写死行号：那个文件正被并行会话编辑 ⇒ 行号会漂 ⇒
//   写死行号会让门在别人改文件后**指向别的代码**（静默失准）。
// ⇒ 定位规则：从含 `stored_sha="$` 的那行开始，到**第一个** 2 空格缩进的
//   `fi` 为止（那是外层 if 的收尾）。
func extractSkipBlock(t *testing.T) string {
	t.Helper()
	lines := strings.Split(readRetireFile(t, applySeqScriptPath), "\n")
	start := -1
	for i, l := range lines {
		if strings.Contains(l, `stored_sha="$`) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("定位失败：找不到 `stored_sha=\"$(` 这一行")
	}
	end := -1
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "  fi" {
			end = i
			break
		}
	}
	if end < 0 {
		t.Fatal("定位失败：找不到跳过判据块的收尾 `fi`")
	}
	return strings.Join(lines[start:end+1], "\n")
}

// runSkipDecision 把真实判据块放进真实 for 循环执行，I/O 用 stub。
//
//   - psql_query 返回 ledgerSha（模拟台账里那一行）；
//   - psql_file  打印 APPLIER_CALLED（模拟「真的去执行了这个迁移」）。
//
// 返回 (是否走 apply 路径, 原始输出)。
func runSkipDecision(t *testing.T, block, ledgerSha, fileSha string) (bool, string) {
	t.Helper()
	harness := `#!/usr/bin/env bash
set -euo pipefail
LEDGER_SHA="$1"
FILE_SHA="$2"
ROOT_DIR="$3"
TARGET="$4"
psql_query() { printf '%s' "$LEDGER_SHA"; }
# ★ stub 必须像真实的 psql_file 那样设置这两个全局量（真实实现在
#   psql_file() 里给 execution_sha / runtime_adaptation 赋值）⇒
#   否则内容重放分支会因 set -u 报「未绑定的变量」而中止 ⇒
#   抽取的判据块一旦与真实实现不同步，这里就会**当场暴露**。
psql_file() {
  execution_sha="stub-execution-sha"
  runtime_adaptation="none"
  printf 'APPLIER_CALLED\n'
}
sha256_file() { printf '%s' "$FILE_SHA"; }
marker="session-summary-and-integrity-2026-09:890_drop_request_logs_v1_family.sql"
file_sha="$FILE_SHA"
for file in "$TARGET"; do
` + block + `
done
`
	dir := t.TempDir()
	hp := filepath.Join(dir, "harness.sh")
	if err := os.WriteFile(hp, []byte(harness), 0o755); err != nil {
		t.Fatalf("write harness: %v", err)
	}
	cmd := exec.Command("bash", hp, ledgerSha, fileSha, ".", "890_drop_request_logs_v1_family.sql")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("harness 退出 %v：%s\n---\n%s", err, out, harness)
	}
	return strings.Contains(string(out), "APPLIER_CALLED"), string(out)
}

// TestDeferralMakesAutoChannelSkip890 —— ★ 行为证明：defer 之后自动通道跳过 890。
//
// ★ 这里用的是**真实脚本里的判据块**（按内容抽取）⇒ 不复制逻辑 ⇒
//   副本漂移这一类假绿被排除。
func TestDeferralMakesAutoChannelSkip890(t *testing.T) {
	block := extractSkipBlock(t)
	// 真实的文件 sha（defer 写进台账的就是它）
	realSha := retireFileSha256(t, retirementSQLPath)

	// ① 账本里存着同一个 sha（= defer 之后的形态）⇒ 必须跳过。
	applied, out := runSkipDecision(t, block, realSha, realSha)
	if applied {
		t.Fatalf("★ defer 之后自动通道仍然会执行 890 ⇒ 部署仍会被非空闸挡住\n输出：\n%s", out)
	}
	if !strings.Contains(out, "already applied") {
		t.Errorf("没有走到跳过分支（输出：%s）", out)
	}

	// ② 反例：账本里的 sha 与文件不一致 ⇒ 必须走 apply 路径。
	//    ⇒ 这一步证明尺子**分得清两种形态**（否则 ① 的绿没有意义）。
	applied2, out2 := runSkipDecision(t, block, "0000000000000000000000000000000000000000000000000000000000000000", realSha)
	if !applied2 {
		t.Fatalf("★ 尺子失灵：sha 不匹配时也没走 apply 路径 ⇒ ① 的『跳过』结论没有意义\n输出：\n%s", out2)
	}
}

// TestRetirementDeferralGateHasTeeth —— **门自己有牙**。
//
// 合成一份「把所有保证都写进注释、代码里什么都不做」的脚本 ⇒
// 同一把尺（retireGaps）必须报出缺口 ⇒ ⇒ 证明本门不会被注释喂饱。
func TestRetirementDeferralGateHasTeeth(t *testing.T) {
	commentOnly := strings.Join([]string{
		"#!/usr/bin/env bash",
		"# 这里假装有 LLM_GATEWAY_RETIREMENT_ACK",
		"# exit 3",
		`# if [[ -n "${DATABASE_URL:-}" ]]; then`,
		`# FILE_SHA="$(sha256_of "$RETIREMENT_SQL")"`,
		"# 890_drop_request_logs_v1_family.sql",
		"# DEFER_MARKER=deferred-to-explicit-retirement",
		"# --undo-defer",
		"# --execute",
		"# case $MODE in",
		"  --defer)",
		"    # content_sha256",
		"    # execution_sha256 = NULL",
		"    # ${DEFER_MARKER}",
		"    # ON CONFLICT",
		"    ;;",
		"  --undo-defer)",
		"    # DELETE FROM ${LEDGER_TABLE}",
		"    # runtime_adaptation = '${DEFER_MARKER}'",
		"    ;;",
		"  --execute)",
		"    # DELETE FROM ${LEDGER_TABLE}",
		"    # runtime_adaptation = '${DEFER_MARKER}'",
		`    # psql_f < "$RETIREMENT_SQL"`,
		"    ;;",
		"esac",
		"echo stub",
	}, "\n")
	gaps := retireGaps(stripShellCommentsAndInline(commentOnly))
	if len(gaps) == 0 {
		t.Fatal("门无牙：合成样本把保证全写在注释里，尺子却报『满足』⇒ 剥离器失效")
	}
	t.Logf("尺子按预期报出 %d 处缺口（首条：%s）", len(gaps), gaps[0])
}

// TestRetirementScriptReferencesRealMigration —— 退役脚本指向的文件必须存在。
func TestRetirementScriptReferencesRealMigration(t *testing.T) {
	if _, err := os.Stat(retirementSQLPath); err != nil {
		t.Fatalf("890 迁移文件不在预期路径：%v", err)
	}
	src := stripShellCommentsAndInline(readRetireFile(t, retirementScriptPath))
	if !strings.Contains(src, "890_drop_request_logs_v1_family.sql") {
		t.Fatal("退役脚本没有指向 890 迁移文件")
	}
	// ★ 退役脚本必须**读同一个文件**（否则 sha 对不上 ⇒ defer 无效）。
	// ⚠ 断言【语义】而不是某个字面量：RETIREMENT_SQL 的默认值可以被
	//   LLM_GATEWAY_RETIREMENT_SQL 覆盖（远端主机上没有仓库副本）⇒
	//   ⇒ 判据是「赋值里指向 890 迁移文件」，而不是「赋值长什么样」。
	if !strings.Contains(src, "sql/migrations/startup/890_drop_request_logs_v1_family.sql") {
		t.Error("退役脚本的 RETIREMENT_SQL 没有指向 890 迁移文件")
	}
	if !strings.Contains(src, "RETIREMENT_SQL=") {
		t.Error("退役脚本没有 RETIREMENT_SQL 赋值")
	}
	// ★ 覆盖路径必须能指到 890 迁移文件（远端执行用）⇒ 否则远端没法用。
	if !strings.Contains(src, "LLM_GATEWAY_RETIREMENT_SQL") {
		t.Error("退役脚本不支持 LLM_GATEWAY_RETIREMENT_SQL 覆盖 ⇒ 远端主机（无仓库副本）上无法执行")
	}
}
