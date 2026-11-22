package ursm_test

// 2026-11-23（211 配置优化遗留 L5）· 门：部署前预检脚本必须真的把两条
// 「已经付出过代价」的纪律变成闸门。
//
// 被测对象：scripts/deploy-tree-preflight.sh
//
// ── 两条纪律各自的代价 ───────────────────────────────────────────────────
//
//	① 【祖先闸】2026-11-22 实测：修复发上 211（seq 3198）、推上 origin/main，
//	  之后三个班次用**未同步这次推送的旧树**连发 211（seq 3199/3200/3201）
//	  ⇒ 三棵树都不含修复 ⇒ 大请求体 120 秒超时在生产回来约 30 分钟。
//	  ⇒ 根因不是「推送没成功」，而是「推送只保证修复在 origin，不保证修复在
//	    现役」⇒ 部署前必须做**祖先检查**。
//
//	② 【槽位闸】2026-11-23 实测：手工重建容器用了已被并行部署占用的名字
//	  llm-gateway-211-b ⇒ docker Conflict ⇒ 旧容器已 stop ⇒ 8781 无人监听
//	  ⇒ 服务中断；随后重建的容器又**不在 nginx 路由上**（引擎用 upstream
//	  的端口反推活跃槽）⇒ 游离实例。
//
// ── 三道判据 ────────────────────────────────────────────────────────────
//
//	① 【祖先判据】：必须有 `git merge-base --is-ancestor` 且不通过时退出非 0
//	   （fail-closed：查不到就当不通过，而不是当通过）。
//	② 【槽位判据】：必须盘点目标机的容器占用与 nginx upstream 指向。
//	③ 【只读判据】：脚本里不得出现停/删/重启容器、改 nginx 的写操作 ——
//	   预检一旦有副作用，它就不能在每次部署前放心跑。

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func preflightScriptPath(t *testing.T) string {
	t.Helper()
	root := repoRootDeployGuard(t)
	p := filepath.Join(root, "scripts", "deploy-tree-preflight.sh")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("找不到 %s: %v", p, err)
	}
	return p
}

func readPreflightScript(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(preflightScriptPath(t))
	if err != nil {
		t.Fatalf("读预检脚本失败: %v", err)
	}
	return string(b)
}

// ── 判据 ①：祖先判据 ────────────────────────────────────────────────────
func TestPreflightHasAncestryGate(t *testing.T) {
	src := readPreflightScript(t)

	if !strings.Contains(src, "git merge-base --is-ancestor") {
		t.Error("预检脚本没有 `git merge-base --is-ancestor`。\n" +
			"  ★ 这正是 2026-11-22 生产事故的根因闸：推送只保证修复在 origin，\n" +
			"    不保证修复在现役 —— 部署前必须证明这棵部署树 ⊇ 修复提交。")
	}
	if !strings.Contains(src, "git fetch origin") {
		t.Error("预检脚本没有 `git fetch origin`：不 fetch 就判祖先 ⇒ 用的是过期 ref。")
	}
	// fail-closed：查不到提交 ⇒ 当作不通过（而不是跳过）。
	if !regexp.MustCompile(`git cat-file -e`).MatchString(src) {
		t.Error("预检脚本没有校验提交是否存在：查不到的 sha 必须当「不通过」，不能当「通过」。")
	}
	if !strings.Contains(src, "exit 1") {
		t.Error("预检脚本没有非零退出：闸门失败必须让部署停下来。")
	}
}

// ── 判据 ②：槽位判据 ────────────────────────────────────────────────────
func TestPreflightInspectsSlotsAndUpstream(t *testing.T) {
	src := readPreflightScript(t)

	if !strings.Contains(src, "docker ps") {
		t.Error("预检脚本没有盘点容器占用：手工动容器前必须先看槽位被谁占了。")
	}
	if !strings.Contains(src, "upstream") {
		t.Error("预检脚本没有读 nginx upstream：引擎用 upstream 的端口反推活跃槽，\n" +
			"  ★ 不知道 upstream 指向就动手 ⇒ 容器起来了也不在路由上（游离实例）。")
	}
	if !strings.Contains(src, "--via") {
		t.Error("预检脚本没有 --via：目标机现状必须能在部署机侧远程只读盘点。")
	}
}

// ── 判据 ③：只读判据 ────────────────────────────────────────────────────
func TestPreflightIsReadOnly(t *testing.T) {
	src := readPreflightScript(t)

	forbidden := []string{
		"docker stop", "docker rm ", "docker restart", "docker kill",
		"nginx -s reload", "systemctl restart", "rm -rf /",
	}
	for _, f := range forbidden {
		if strings.Contains(src, f) {
			t.Errorf("预检脚本里出现了写操作 %q。\n"+
				"  ★ 预检必须**只读**：它要在每次部署前放心跑；一旦有副作用，\n"+
				"    就会变成「不敢跑」的门。", f)
		}
	}
}

// ── 判据 ④：语法 ────────────────────────────────────────────────────────
func TestPreflightScriptSyntaxValid(t *testing.T) {
	cmd := exec.Command("bash", "-n", preflightScriptPath(t))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash -n 失败：%v\n%s", err, out)
	}
}
