//go:build !integration

package main

// R75 轮：迁移台账对账闸门（deploy channel checksum reconcile）失效的守卫。
//
// ★★ 这道闸门在生产上**从未真正跑过**——不是「跑绿了」，是**只校验了
// 443 个已应用迁移里的第 1 个（412 号）**。机制：
//
//	_deploy_migration_checksum_reconcile()
//	  while IFS='|' read -r ver description; do ... done < <(ssh ... psql ...)
//	                                  ↑ 进程替换 = 管道
//	  循环体内：output=$(ssh ... "$remote_psql")      ← scripts/deploy-lib.legacy/ssh-retry.sh
//	                     ↑ ssh 继承了循环的 stdin，把管道读空
//	  ⇒ 第一次迭代后 read 命中 EOF ⇒ 循环只跑了 1 轮
//
// ⇒ 所以「台账有没有漂移」必须有一个**独立于部署通道**的读数手段：
// `scripts/audit/migration_ledger_drift_report.py`（只读、离线、不碰凭据）。
//
// 本门守三件事：
//  ① 漂移报告器存在、能跑、且自检绿；
//  ② 漂移报告器**只读**（剥注释后不得出现写库动词）；
//  ③ 官方 repair 脚本的 canonical 解析与部署侧**同款单候选兜底**
//     （缺了它，生产上 description 为空的 837/838/839 根本修不了）。

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	r75DriftTool    = "scripts/audit/migration_ledger_drift_report.py"
	r75RepairScript = "scripts/repair-252-migration-ledger.sh"
	r75DeployLib    = "scripts/deploy-lib.legacy/db-changelog.sh"
	r75AuditDoc     = "docs/audit/2026-10-24-R75-台账对账闸门失效与D4后置基线.md"
)

func r75RepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func r75Read(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(r75RepoRoot(t), rel))
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

// r75StripComments 去掉行注释与模块 docstring（只留可执行文本）。
//
// ★ 为什么要剥：漂移报告器的 docstring 里**故意**写了 psql 的导出命令示例
// （那是使用说明，不是执行路径）⇒ 不剥注释就会把「文档里出现 psql」
// 误判成「工具会执行 psql」⇒ 那是【量具坏了】不是事实。
func r75StripComments(src string) string {
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines))
	inDoc := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if i == 0 && strings.HasPrefix(trimmed, "#!") {
			continue
		}
		if !inDoc && strings.HasPrefix(trimmed, `"""`) {
			// 单行 docstring 或多行 docstring 的开头
			if strings.Count(line, `"""`) >= 2 {
				continue
			}
			inDoc = true
			continue
		}
		if inDoc {
			if strings.Contains(line, `"""`) {
				inDoc = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// TestR75LedgerDriftToolSelfTest（★ 判据①）
//
// ★ 工具必须带离线 fixture 自检 ⇒ 因为它要在**没有生产库**的环境里被验证。
func TestR75LedgerDriftToolSelfTest(t *testing.T) {
	root := r75RepoRoot(t)
	script := filepath.Join(root, r75DriftTool)
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("漂移报告器不存在：%s", r75DriftTool)
	}
	// python3 在 Linux 是唯一名；Windows 上有两层坑：
	// ① python3 多为 Git Bash shim 脚本，CreateProcess 执行不了；
	// ② Microsoft Store 的 0 字节 stub 别名（%LOCALAPPDATA%/Microsoft/WindowsApps/python3.exe）
	//    会被 LookPath 命中但一执行就 exit 9009 ⇒ LookPath 探测不可信，
	//    必须**真的跑一次 --version 验明正身**再定解释器。
	r75PythonInterpreter := func() string {
		for _, name := range []string{"python3", "python"} {
			path, err := exec.LookPath(name)
			if err != nil {
				continue
			}
			probe := exec.Command(path, "--version")
			if out, probeErr := probe.CombinedOutput(); probeErr == nil &&
				strings.Contains(string(out), "Python 3") {
				return path
			}
		}
		return ""
	}
	interpreter := r75PythonInterpreter()
	if interpreter == "" {
		t.Skip("未找到可用的 Python 3 解释器（python3/python 都缺位或是 Store stub）——本机未测量，不是通过")
	}
	cmd := exec.Command(interpreter, script, "--self-test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("漂移报告器自检未过：err=%v\n%s", err, out)
	}
	if !strings.Contains(string(out), "SELF-TEST OK") {
		t.Fatalf("自检输出缺少 SELF-TEST OK 标记：\n%s", out)
	}
}

// TestR75LedgerDriftToolIsReadOnly（★ 判据②）
//
// ★ 只读是它的**契约**：它是给属主做决策用的读数手段，
// 一旦它能写库，就等于给了「顺手补一笔」的后门。
func TestR75LedgerDriftToolIsReadOnly(t *testing.T) {
	raw := r75Read(t, r75DriftTool)
	code := r75StripComments(raw)
	forbidden := []string{
		"INSERT INTO", "UPDATE ", "DELETE FROM", "DROP TABLE", "DROP INDEX",
		"TRUNCATE", "ALTER TABLE", "CREATE TABLE", "CREATE INDEX",
	}
	for _, bad := range forbidden {
		if strings.Contains(code, bad) {
			t.Errorf("★ 漂移报告器必须只读，但可执行文本里出现了写库动词 %q", bad)
		}
	}
	// 工具不得自己连库（输入只能是转储 TSV）⇒ 凭据不经过它。
	for _, bad := range []string{"psycopg", "sql.Open", "pgx.Connect"} {
		if strings.Contains(code, bad) {
			t.Errorf("★ 漂移报告器只接受 TSV 输入，不应自行连库（出现 %q）", bad)
		}
	}
}

// r75StripShellComments 去掉 shell 脚本的行注释（只留可执行文本）。
//
// ★★ 这条不是洁癖：本门第一版**恒绿**过——
//   ① 断言 needle `first="$candidate"` 命中的是我自己在**注释**里写的解释；
//   ② needle `printf '%s\\n' "$first"` 多了反斜杠 ⇒ 永不匹配。
//   ⇒ ⇒ 「剥注释后断言」是硬要求（与 SQL 断言同理）。
//
// ⚠ 第二版踩的坑：朴素地「遇到 # 就截断」会把 `${#candidates[@]}` 从中间切开
//   ⇒ 连紧跟其后的 `-eq 1` 一起切掉 ⇒ 断言**假阴性**。
//   ⇒ ⇒ shell 注释的 `#` 必须**行首或前置空白**，才真是注释。
func r75StripShellComments(src string) string {
	out := make([]string, 0)
	for _, line := range strings.Split(src, "\n") {
		cut := -1
		for i := 0; i < len(line); i++ {
			if line[i] != '#' {
				continue
			}
			if i == 0 {
				cut = i
				break
			}
			if line[i-1] == ' ' || line[i-1] == '\t' {
				cut = i
				break
			}
		}
		if cut >= 0 {
			line = line[:cut]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// TestR75LedgerRepairCanonicalParityWithDeploySide（★ 判据③）
//
// ★★ 官方 repair 脚本与部署侧对账函数**必须用同一套 canonical 解析**。
//
// 实测（2026-10-24）：repair 侧原实现要求「单候选时 description 也必须逐字相等」，
// 而生产上 837/838/839 的 description **是空的**（2026-10-07 12:48~12:50 应用）
// ⇒ ⇒ repair 判「无法解析为唯一 canonical 文件」⇒ **官方载具修不了它们**。
// 部署侧早有单候选兜底（`${#candidates[@]} -eq 1`）⇒ 两边不同源 ⇒ 本门钉住。
//
// ★ 另一条同源陷阱：`while IFS= read -r candidate` 循环**结束时 EOF 会把循环
//   变量置空** ⇒ 循环之后读 `$candidate` 只会拿到空串 ⇒ 首候选必须在循环内捕获。
//
// ★ 命名纪律：本门 4 条测试**必须**同前缀 `TestR75Ledger…`
//   ⇒ 否则 `-run R75Ledger` 会把它们过滤掉 ⇒ 门看起来绿、实际没跑
//   （本轮第一版就踩了这个：parity 测试从未执行，变异验证因此「无效绿」）。
func TestR75LedgerRepairCanonicalParityWithDeploySide(t *testing.T) {
	repair := r75StripShellComments(r75Read(t, r75RepairScript))
	deploy := r75StripShellComments(r75Read(t, r75DeployLib))

	// ① repair 侧：单候选兜底
	if !strings.Contains(repair, `count == 1`) {
		t.Error("★ repair 脚本的 canonical 解析里找不到单候选分支（count == 1）")
	}
	if !strings.Contains(repair, `first="$candidate"`) {
		t.Error("★ repair 脚本必须在 while read 循环内捕获首个候选（first=\"$candidate\"）——" +
			"while read 结束时的 EOF 会把循环变量置空")
	}
	if !strings.Contains(repair, `printf '%s\n' "$first"`) {
		t.Error("★ repair 脚本的单候选兜底应当输出 $first（而不是循环后的 $candidate）")
	}

	// ② 部署侧：单候选兜底（本来就该有）
	if !strings.Contains(deploy, `-eq 1`) || !strings.Contains(deploy, `candidates[0]`) {
		t.Error("★ 部署侧对账函数缺单候选兜底（${#candidates[@]} -eq 1 ⇒ candidates[0]）")
	}

	// ③ 两边都必须有 461 的 description 特例（不同源就会各修一半）
	for name, src := range map[string]string{"repair": repair, "deploy": deploy} {
		if !strings.Contains(src, "request_wal_hot_unique_request_id") {
			t.Errorf("★ %s 侧缺 461 的 description 特例 ⇒ 两边不同源", name)
		}
	}
}

// TestR75LedgerAuditDocExists（★ 判据④）
//
// ★ 本轮的两项重磅结论（闸门失效机制 + D4 后置基线对比）必须有归档，
// 否则下一轮冷启动会话会把「这道闸门是绿的」当成事实。
func TestR75LedgerAuditDocExists(t *testing.T) {
	raw := r75Read(t, r75AuditDoc)
	for _, needle := range []string{
		"stdin",     // 闸门失效的机制关键词
		"443",       // 已应用迁移数（边界 ≥412）
		"108",       // 漂移合计
		"891",       // 属主决策项
		"对账",
	} {
		if !strings.Contains(raw, needle) {
			t.Errorf("★ 审计档 %s 里找不到关键结论 %q", r75AuditDoc, needle)
		}
	}
}
