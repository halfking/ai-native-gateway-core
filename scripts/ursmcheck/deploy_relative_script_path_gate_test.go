package ursm_test

// deploy_relative_script_path_gate_test.go —— 门：部署脚本**不得**用相对
// `$(dirname "$0")` 解析「同目录下的另一个脚本」的路径；这类写法依赖**求值那一刻
// 的 cwd**，一旦 cwd 被 cd 走、或目标目录正被并发部署替换，就会
// `cd: scripts: No such file or directory` ⇒ 脚本**静默跳过**它要调用的那一腿。
//
// 被测对象：scripts/deploy-seamless-docker.sh
//
// ── 为什么这道门存在（2026-11-23 211 seq 3230 实测）────────────────────────
//
//	缺陷：`deploy-seamless-docker.sh` 里解析同目录脚本 `apply-db-revision-sequence.sh`
//	的那一行写的是
//	    REVISION_SEQ="$(cd "$(dirname "$0")" && pwd)/apply-db-revision-sequence.sh"
//	—— 它是**全脚本唯一**残留的相对写法（其余三处都用 `${BASH_SOURCE[0]}`）。
//
//	远端形态：引擎跑在 $DD_REMOTE_ROOT/scripts/ ⇒ `$0` = scripts/deploy-seamless-docker.sh
//	⇒ `dirname "$0"` = "scripts"（**相对**）⇒ 那行的 `cd scripts` 依赖**当时的 cwd**。
//	⇒ 而它求值时已在 docker build 段之后 ⇒ cwd 可能已被 cd 走；
//	   并发部署把远端树同步到一半时，scripts/ 也可能**短暂不存在**
//	⇒ `cd: scripts: No such file or directory` ⇒ ★ **迁移腿被静默跳过**
//	⇒ 部署在「镜像已推、容器未起」处中断。
//
//	★ 这不是新问题：该脚本自己在 configs 段（≈2979 行）早就写过同一教训
//	  「用 DD_REPO_ROOT（脚本顶部已解析），禁止再 $(cd "$(dirname "$0")/..")——
//	   docker build 段可能已 cd 走 cwd，相对 scripts/.. 会 No such file（2026-10-15 实测）」
//	  ⇒ 本轮只是**漏改了一处**。
//
// ── 两道判据 ────────────────────────────────────────────────────────────
//
//	① 【静态判据】脚本的**可执行位置**（排除纯注释行）不得出现 `dirname "$0"`。
//	   ⇒ 这一条把「漏改」直接钉死。
//	② 【行为判据】（★ 真跑 bash）：把脚本里**真实的**那两行抽出来放进一个探针，
//	   探针在**求值前先 cd 到别处**（复刻 docker build 段 cd 走 cwd 的形态）
//	   ⇒ 必须仍能解析出**存在的** apply-db-revision-sequence.sh。
//	   ★ 同时跑一个**故意写坏的**对照探针（旧写法放在 cd 之后）⇒ 必须失败
//	   ⇒ ⇒ 证明这道判据真的能分辨好坏，不是恒绿。
import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const deployScriptRelPath = "../../scripts/deploy-seamless-docker.sh"

// 探针模板：先「cd 走 cwd」（复刻 docker build 段），再使用**顶部已解析**的路径。
//
// ★ 关键：解析必须发生在 **cd 之前**（脚本顶部）才能活下来；
//
//	把解析放在 cd 之后（旧写法）必然失败 ⇒ 对照臂恒红。
const probeTemplate = `set -eu
__RESOLVE__
cd "__ELSEWHERE__"
__USE__
if [[ -f "$REVISION_SEQ" ]]; then
  echo "OK:$REVISION_SEQ"
else
  echo "MISSING"
  exit 3
fi
`

// 变异对照臂：把解析放到 cd **之后**（= 修复前的形态）⇒ 必须失败。
const probeMutantTemplate = `set -eu
cd "__ELSEWHERE__"
REVISION_SEQ="$(cd "$(dirname "$0")" && pwd)/apply-db-revision-sequence.sh"
if [[ -f "$REVISION_SEQ" ]]; then
  echo "OK:$REVISION_SEQ"
else
  echo "MISSING"
  exit 3
fi
`

func readDeployScriptForRelPath(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(deployScriptRelPath)
	if err != nil {
		t.Fatalf("读不到被测脚本 %s：%v", deployScriptRelPath, err)
	}
	return string(b)
}

// buildRemoteShape 建一个「远端形态」的临时树：
//
//	<tmp>/remote/scripts/deploy-seamless-docker.sh    ← 被测脚本的副本（供相对调用）
//	<tmp>/remote/scripts/apply-db-revision-sequence.sh ← 目标（marker）
//	<tmp>/elsewhere/                                  ← 探针 cd 走的地方
func buildRemoteShape(t *testing.T, scriptBody string) (remoteRoot, elsewhere string) {
	t.Helper()
	base := t.TempDir()
	remoteRoot = filepath.Join(base, "remote")
	elsewhere = filepath.Join(base, "elsewhere")
	scriptsDir := filepath.Join(remoteRoot, "scripts")
	for _, d := range []string{scriptsDir, elsewhere} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("建目录 %s 失败：%v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(scriptsDir, "apply-db-revision-sequence.sh"),
		[]byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("写 marker 失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(scriptsDir, "deploy-seamless-docker.sh"),
		[]byte(scriptBody), 0o755); err != nil {
		t.Fatalf("写被测脚本副本失败：%v", err)
	}
	return remoteRoot, elsewhere
}

// runProbe 在「远端形态」下跑一个探针脚本；返回退出码与输出。
func runProbe(t *testing.T, remoteRoot, elsewhere, probeBody string) (int, string) {
	t.Helper()
	scriptsDir := filepath.Join(remoteRoot, "scripts")
	probePath := filepath.Join(scriptsDir, "probe.sh")
	if err := os.WriteFile(probePath, []byte(probeBody), 0o755); err != nil {
		t.Fatalf("写探针失败：%v", err)
	}
	// ★ 复刻远端调用形态：`cd $DD_REMOTE_ROOT && bash scripts/<x>.sh`
	cmd := exec.Command("bash", "scripts/probe.sh")
	cmd.Dir = remoteRoot
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("跑探针失败：%v（输出：%s）", err, out)
	}
	return code, string(out)
}

func TestDeployScriptHasNoRelativeScriptPathResolution(t *testing.T) {
	src := readDeployScriptForRelPath(t)

	// ① 静态判据：可执行位置不得出现相对 dirname "$0"
	offending := []string{}
	for i, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue // 注释里可以引用这个写法（当反例说明）
		}
		if strings.Contains(line, `dirname "$0"`) {
			offending = append(offending, strconv.Itoa(i+1)+": "+trimmed)
		}
	}
	if len(offending) > 0 {
		t.Fatalf("部署脚本的可执行位置仍用相对 `$(dirname \"$0\")` 解析脚本路径（%d 处）：\n%s\n"+
			"★ 相对写法依赖**求值那一刻的 cwd**；docker build 段 cd 走 cwd、或并发部署"+
			"替换远端树时 ⇒ `cd: scripts: No such file or directory` ⇒ 被调用的一腿被静默跳过"+
			"（211 seq 3230 实测）⇒ 解析必须**在脚本顶部**完成（绝对路径）。",
			len(offending), strings.Join(offending, "\n"))
	}

	// ② 顶部解析 + 使用处引用同一个变量
	if !strings.Contains(src, `DD_REVISION_SEQ="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/apply-db-revision-sequence.sh"`) {
		t.Fatal("被测脚本缺少顶部的 DD_REVISION_SEQ 绝对路径解析（DB 修订序列脚本）")
	}
	if !strings.Contains(src, `REVISION_SEQ="$DD_REVISION_SEQ"`) {
		t.Fatal("被测脚本的 REVISION_SEQ 使用处必须引用顶部的 DD_REVISION_SEQ（不得就地再解析）")
	}
}

func TestDeployScriptResolvesRevisionSeqAfterCwdChange(t *testing.T) {
	src := readDeployScriptForRelPath(t)

	// 从被测脚本里**抽出真实的那两行**（不是在本测试里重抄一遍 ⇒ 测的是产品代码）。
	resolveRe := regexp.MustCompile(`(?m)^(DD_REVISION_SEQ=.*)$`)
	useRe := regexp.MustCompile(`(?m)^(REVISION_SEQ="\$DD_REVISION_SEQ")$`)
	resolveLine := resolveRe.FindString(src)
	useLine := useRe.FindString(src)
	if resolveLine == "" || useLine == "" {
		t.Fatalf("被测脚本里抽不到那两行（resolve=%q use=%q）⇒ 门失去被测对象", resolveLine, useLine)
	}

	remoteRoot, elsewhere := buildRemoteShape(t, src)

	// ★ 行为判据：解析在 cd 之前 ⇒ 仍然有效
	code, out := runProbe(t, remoteRoot, elsewhere,
		fmtProbe(probeTemplate, resolveLine, elsewhere, useLine))
	if code != 0 || !strings.Contains(out, "OK:") {
		t.Fatalf("行为判据红：cwd 被 cd 走后，DB 修订序列脚本的路径应当仍能解析出**存在的**文件；"+
			"退出码=%d 输出=%s", code, out)
	}
	if !strings.Contains(out, "apply-db-revision-sequence.sh") {
		t.Fatalf("解析结果不含目标脚本名：%s", out)
	}

	// ★ 变异自证：把解析挪到 cd 之后（旧写法）⇒ **必须失败**
	//   ⇒ 证明这道判据真的能分辨好坏，不是恒绿。
	//   ★ 失败有两种形态，都算数：
	//     ① `set -e` 下 `cd scripts` 直接失败 ⇒ 退出码非 0 + "No such file or directory"；
	//     ② 若 errexit 未触发 ⇒ 走到 [[ -f ]] 分支 ⇒ 输出 "MISSING"。
	code, out = runProbe(t, remoteRoot, elsewhere, fmtProbeMutant(probeMutantTemplate, elsewhere))
	failedByNoSuchDir := strings.Contains(out, "No such file or directory")
	failedByMissing := strings.Contains(out, "MISSING")
	if code == 0 || (!failedByNoSuchDir && !failedByMissing) {
		t.Fatalf("变异自证失败：对照臂（相对 dirname \"$0\"、解析在 cd 之后）本应失败，"+
			"却退出码=%d 输出=%s ⇒ 这道判据分辨不出好坏（恒绿）", code, out)
	}
}

// fmtProbe 把探针模板里的三处占位填上。
func fmtProbe(tpl, resolveLine, elsewhere, useLine string) string {
	out := strings.ReplaceAll(tpl, "__RESOLVE__", resolveLine)
	out = strings.ReplaceAll(out, "__ELSEWHERE__", elsewhere)
	return strings.ReplaceAll(out, "__USE__", useLine)
}

// fmtProbeMutant 只填 elsewhere（对照臂的解析写死在模板里）。
func fmtProbeMutant(tpl, elsewhere string) string {
	return strings.ReplaceAll(tpl, "__ELSEWHERE__", elsewhere)
}
