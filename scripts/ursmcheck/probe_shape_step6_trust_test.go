package ursm_test

// probe-onetime-db-shape.sh 第 ⑥ 步「形态可信」判据的行为门（2026-10-21，R85 轮）
//
// # 这道门要拦的事故：★ **量具只在「已经坏掉」的时候才说谎**
//
// `scripts/audit/probe-onetime-db-shape.sh` 是本仓的**一次性形态库量具**：
// 它建库 → 跑 prereqs/基线/注册启动链 → 第 ⑥ 步跑一次 ensure 链（＝产品启动一次）
// → 打印形态事实。⇒ 它的产物被大量门当作「装完**且启动过**」的真实形态用。
//
// R85 轮修掉的那个缺陷：**第 ⑥ 步的「✓ ensure 链已跑（库现在 = 产品真实形态）」
// 有四个假绿变体**，任意一个命中都会在坏掉时打印「形态可信」：
//
//	(V0) 取不到容器口令/端口 ⇒ 分支明说「跳过 ensure 链」⇒ 库停在「装完但没启动」，
//	     脚本却继续跑完并以 0 退出；
//	(V1) `go test` 对 **SKIP** 返回 0 ⇒ 跳过被当成「跑过了」
//	     （★ §19.20.28 给那道门加了诚实的 SKIP 分支 ⇒ 这条路径**更近了**）；
//	(V2) `-run` 的名字没匹配到任何测试 ⇒ go test 打印 "no tests to run" 且退出 0；
//	(V3) 第 ④ 步的 startup 链 failed/missing ⇒ 库压根不是产品形态
//	     （第 ④ 步**容忍**链失败并继续往下跑）。
//
// ⇒ ⇒ ★★ 最坏的形态：**平时看着绿，一旦链出问题（正是你最需要它的时候）
// 它反而报「形态可信」** ⇒ 而这个库的产物一旦被当成真实形态，
// **下游所有真库读数都会跟着失真**，且失真被系统性地报成绿。
//
// # 为什么用「注入假 docker + 假 go」而不是子串断言
//
// 子串量的是「源码里写过这句话」，不是「运行时真的这么判」。
// 这里把 `docker` 与 `go` 都换成假的：脚本**真的跑一遍**第 ⑥ 步，
// 再断言它**嘴上说了什么**、**退出码是多少**。
// ⇒ 判据直接对着**行为**（印出「形态可信」/不印、退出 0/3），
// 而不是对着某一行源码。
//
// ★ 正控必须有：FAKE_GO_MODE=pass 时**必须**印出「✓ ensure 链已跑」且退出 0 ——
//   否则这道门可以靠「一律拒绝」变绿，而线上唯一正常的形态恰好也会被拒。

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const probeShapeScript = "../audit/probe-onetime-db-shape.sh"

// probeGoModes 列出被测脚本可能看到的四种 go test 结果。
//
// ★ 形态取自 `go test -v` 的真实输出：
//   - PASS：  "--- PASS: TestFreshInstallRuntimeShape_EnsureChain (5.76s)"
//   - SKIP：  "--- SKIP: TestFreshInstallRuntimeShape_EnsureChain (0.05s)"  ← ★ 退出码仍是 0
//   - 无匹配："testing: warning: no tests to run"                            ← ★ 退出码仍是 0
//   - FAIL：  "--- FAIL: TestFreshInstallRuntimeShape_EnsureChain (1.20s)"   ← 退出码 1
var probeGoModes = []string{"pass", "skip", "nomatch", "fail"}

// fakeProbeDocker 写一个假 docker：
//   - `docker port <c> 5432/tcp` ⇒ 印 127.0.0.1:5432（脚本靠它拼 DSN）
//   - `docker exec <c> printenv POSTGRES_PASSWORD` ⇒ 印一个假口令
//   - 其余（建库 / 跑迁移 / 读形态）⇒ 空输出、退出 0（脚本的链因此「applied=N failed=0」）
//
// ★ 所有调用都记进 $TRACE_FILE：门要能证明「脚本真的走到第 ⑥ 步」，
//   而不是「脚本提前退出所以没有假绿」。
func fakeProbeDocker(t *testing.T, dir string) string {
	t.Helper()
	script := `#!/bin/sh
all="$*"
{
  echo "--- call ---"
  for a in "$@"; do echo "$a"; done
} >> "$TRACE_FILE"

case "$1" in
  port)
    printf '%s' "127.0.0.1:5432"
    exit 0 ;;
  exec)
    # ★★ 必须先把 stdin 读完再退出：脚本是「cat 某个.sql | docker exec -i ... psql」这种形态，
    #   而基线文件很大 ⇒ 管道缓冲区会被填满 ⇒ 假 docker 若直接退出，
    #   cat 收到 EPIPE ⇒ **pipefail 让整条管道判失败** ⇒ 脚本在「01-schema.sql 失败」处提前退出
    #   ⇒ ⇒ 门读到的是「脚本没走到第 ⑥ 步」，而不是被测行为。
    #   （这是本轮第一版假 docker 的真实翻车：红在「01-schema.sql 失败」，看着像产品坏了。）
    cat >/dev/null 2>&1 || true
    case "$all" in
      *printenv*) printf '%s' "fakepassword"; exit 0 ;;
    esac
    exit 0 ;;
esac
exit 0
`
	p := filepath.Join(dir, "docker")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("写假 docker: %v", err)
	}
	return p
}

// probeGoStdout 是假 go 在各模式下打印的**唯一事实源**。
//
// ★ 为什么单独抽出来：门要断言「脚本说了某个**诊断措辞**」，
//   而第 ⑥ 步会把 go test 的输出**原样 tail -12 转述**出来 ⇒
//   ⇒ 若诊断措辞恰好也出现在 go 输出里（第一版的 "no tests to run" 就是），
//   ⇒ 判据就**自证**（无论脚本有没有真的检测，都会绿）。
//   ⇒ 所以门的标记词必须满足：**不在 go 输出里**（下面有断言钉住这条）。
var probeGoStdout = map[string]string{
	"pass": "=== RUN   TestFreshInstallRuntimeShape_EnsureChain\n" +
		"--- PASS: TestFreshInstallRuntimeShape_EnsureChain (5.76s)\n" +
		"PASS\n" +
		"ok  \tgithub.com/kaixuan/llm-gateway-go/db\t6.409s\n",
	"skip": "=== RUN   TestFreshInstallRuntimeShape_EnsureChain\n" +
		"--- SKIP: TestFreshInstallRuntimeShape_EnsureChain (0.05s)\n" +
		"PASS\n" +
		"ok  \tgithub.com/kaixuan/llm-gateway-go/db\t0.061s\n",
	"nomatch": "testing: warning: no tests to run\n" +
		"PASS\n" +
		"ok  \tgithub.com/kaixuan/llm-gateway-go/db\t0.002s\n",
	"fail": "=== RUN   TestFreshInstallRuntimeShape_EnsureChain\n" +
		"--- FAIL: TestFreshInstallRuntimeShape_EnsureChain (1.20s)\n" +
		"FAIL\n",
}

// probeGoExitCode 是假 go 在各模式下的**真实**退出码。
//
// ★★ 这里是最容易骗过自己的地方：**SKIP 与「无匹配」的退出码都是 0**，
//   只有 FAIL 是 1 ⇒ ⇒ 「只看退出码」必然把跳过当成跑过了。
//   ⇒ 必须同时看「PASS/SKIP/FAIL 标记」**和**「退出码」。
var probeGoExitCode = map[string]int{"pass": 0, "skip": 0, "nomatch": 0, "fail": 1}

// fakeProbeGo 写一个假 go：按 $FAKE_GO_MODE 印出对应的 `go test -v` 形态。
//
// ★ 关键：**SKIP 与「无匹配」两种形态的退出码都是 0** ——
//   这正是旧判据（只看退出码）会把它们当成「跑过了」的根因。
func fakeProbeGo(t *testing.T, dir string) string {
	t.Helper()
	script := `#!/bin/sh
case "${FAKE_GO_MODE:-pass}" in
`
	for _, mode := range probeGoModes {
		script += "  " + mode + ")\n"
		for _, line := range strings.Split(strings.TrimRight(probeGoStdout[mode], "\n"), "\n") {
			script += "    printf '%s\\n' " + strconv.Quote(line) + "\n"
		}
		script += "    exit " + strconv.Itoa(probeGoExitCode[mode]) + " ;;\n"
	}
	script += `esac
exit 0
`
	p := filepath.Join(dir, "go")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("写假 go: %v", err)
	}
	return p
}

// runProbeShape 跑一遍探针脚本，返回（stdout, 退出码）。
func runProbeShape(t *testing.T, mode string) (string, int) {
	t.Helper()
	dir := t.TempDir()
	fakeDocker := fakeProbeDocker(t, dir)
	fakeGo := fakeProbeGo(t, dir)
	// ★ 假件必须真的落盘（PATH 靠文件名找它们 ⇒ 落盘失败 = 脚本跑的是真 docker/go
	//   ⇒ 门会读到「真实环境」的结果而不是被测形态）。
	for _, p := range []string{fakeDocker, fakeGo} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("假件没落盘（%s）: %v", p, err)
		}
	}
	trace := filepath.Join(dir, "trace.txt")
	logPath := filepath.Join(dir, "probe.log")

	cmd := exec.Command(bashForScriptFor(t, probeShapeScript), probeShapeScript)
	cmd.Env = append(os.Environ(),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_GO_MODE="+mode,
		"TRACE_FILE="+trace,
		"LOG="+logPath,
		"PG_CONTAINER=fakepg",
		"PG_USER=llm_gateway",
		"DB_NAME=itgate_r85_gate",
		"KEEP_DB=1",
	)
	out, err := cmd.CombinedOutput()
	rc := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			rc = ee.ExitCode()
		} else {
			t.Fatalf("跑探针脚本失败: %v", err)
		}
	}
	// ★ 假 docker 必须真的被调用过（否则「没有假绿」只是「脚本没走到」）。
	if body, rerr := os.ReadFile(trace); rerr != nil || !strings.Contains(string(body), "--- call ---") {
		t.Fatalf("假 docker 一次都没被调用 —— 脚本没走到第 ⑥ 步，门禁空转\nstdout:\n%s", out)
	}
	return string(out), rc
}

// TestProbeShapeStep6TrustsOnlyRealPass 钉住「只有真 PASS 才认形态可信」。
//
// 四个形态各自断言「嘴上说了什么 + 退出码是多少」：
//   pass    ⇒ 必须印「✓ ensure 链已跑（库现在 = 产品真实形态）」且退出 0；
//   skip    ⇒ **不得**印那句，且退出 3（量具坏了）；
//   nomatch ⇒ **不得**印那句，且退出 3；
//   fail    ⇒ **不得**印那句，且退出 3。
//
// ★ 这里刻意**不用**子串断言「脚本里写了 SKIP 检测」——
//   那样只要有人在别处也写了 "SKIP" 字样就可能糊过去。
//   断的是**行为**：脚本在四种结局下各自说什么、退出什么。
func TestProbeShapeStep6TrustsOnlyRealPass(t *testing.T) {
	skipWindows(t)

	const trusted = "ensure 链已跑（库现在 = 产品真实形态）"

	for _, tc := range []struct {
		mode       string
		wantTrust  bool
		wantMarker string
	}{
		{mode: "pass", wantTrust: true, wantMarker: trusted},
		{mode: "skip", wantTrust: false, wantMarker: "未真正执行"},
		// ★★★ 标记词必须是「**脚本自己的诊断措辞**」，不能是「go test 输出里也有的词」。
		//   第一版这里用了 "no tests to run" ⇒ ★★ 变异 M2（删掉脚本的 no-tests-to-run
		//   检测分支）**没有把门打红** ⇒ 因为脚本第 ⑥ 步会把 go test 的输出
		//   原样 `tail -12` 转述出来 ⇒ 标记词**照样出现在 stdout 里** ⇒
		//   判据自证 ⇒ 门在这一格**没有牙**。
		//   ⇒ 改用脚本独有的措辞（"目标测试名没匹配到任何测试"）后，M2 才红。
		{mode: "nomatch", wantTrust: false, wantMarker: "目标测试名没匹配到任何测试"},
		{mode: "fail", wantTrust: false, wantMarker: "未通过"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			// ★★ 自检：标记词必须**不在** go test 的输出里 ——
			//   否则「脚本有没有真的检测到」无法与「脚本原样转述了 go 输出」区分
			//   ⇒ 判据自证 ⇒ 门没有牙（第一版用 "no tests to run" 就踩了这个坑）。
			if raw, ok := probeGoStdout[tc.mode]; ok && tc.wantMarker != "" &&
				strings.Contains(raw, tc.wantMarker) {
				t.Fatalf("★ 标记词 %q 同时出现在假 go 的输出里 ⇒ 判据自证 ⇒ 门没有牙；"+
					"请改用**脚本独有的诊断措辞**", tc.wantMarker)
			}
			out, rc := runProbeShape(t, tc.mode)

			gotTrust := strings.Contains(out, trusted)
			if gotTrust != tc.wantTrust {
				t.Fatalf("形态 %q 下「%s」的印出与否 = %v，期望 %v\n"+
					"★ 印出「形态可信」而实际没跑 ⇒ 就是量具假绿（量具只在坏的时候才说谎）\nstdout:\n%s",
					tc.mode, trusted, gotTrust, tc.wantTrust, out)
			}
			if tc.wantTrust {
				if rc != 0 {
					t.Fatalf("形态 %q 下脚本应以 0 退出（形态可信），实际 rc=%d\nstdout:\n%s", tc.mode, rc, out)
				}
				return
			}
			// 不可信 ⇒ 必须显式说清「不可信」，且以 3 退出（3 = 量具坏了）。
			if !strings.Contains(out, "不可信") {
				t.Fatalf("形态 %q 下必须显式说「读数不可信」（否则人只会看到一段输出）\nstdout:\n%s", tc.mode, out)
			}
			if !strings.Contains(out, tc.wantMarker) {
				t.Fatalf("形态 %q 下必须给出具体成因标记 %q（不能只说「不可信」）\nstdout:\n%s",
					tc.mode, tc.wantMarker, out)
			}
			if rc != 3 {
				t.Fatalf("形态 %q 下脚本应以 3 退出（3 = 量具坏了，与 db-package-isolation-check.sh 同契约），实际 rc=%d\nstdout:\n%s",
					tc.mode, rc, out)
			}
		})
	}
}

// TestProbeShapeStep6RefusesWhenStartupChainFailed 钉住第 ④ 步链失败时的拒绝。
//
// ★ 这是**最危险**的变体：startup 链失败 ⇒ 库压根不是产品形态，
//   但旧写法里 `go test` 仍可能 PASS（⇒ 印「形态可信」）。
//   ⇒ 本门让 startup 链**失败**，再断言脚本**不认**形态可信。
//
// 手法：让假 docker 对**启动迁移**这一步失败（其余照常成功）⇒
//   脚本第 ④ 步的 failed 计数 > 0 ⇒ 但第 ⑥ 步的 go test 仍报 PASS
//   ⇒ ⇒ 若判据只看 go test 的结果，这一步就会印出「形态可信」⇒ 门红。
func TestProbeShapeStep6RefusesWhenStartupChainFailed(t *testing.T) {
	skipWindows(t)

	dir := t.TempDir()
	trace := filepath.Join(dir, "trace.txt")
	logPath := filepath.Join(dir, "probe.log")

	// 假 docker：只让**启动迁移**失败，prereqs / 基线必须成功。
	//
	// ★★ 量具必须精准 —— 第一版这里让「所有从 stdin 灌 SQL 的调用」都失败，
	//   结果 prereqs（第 ② 步）就挂了 ⇒ 脚本在第 ② 步 exit 2 ⇒
	//   **压根没走到第 ④ 步** ⇒ 门读到的是「脚本提前退出」，不是被测行为。
	//   （这是本轮第二次自造量具缺陷：**红在错的步骤上 = 门空转**。）
	//
	// ★ 判别手段 = **stdin 调用序号**：
	//   脚本的 docker exec 序列是 —— DROP/CREATE（带 -c）→ prereqs(stdin#1)
	//   → baseline(stdin#2) → 启动迁移 ×N(stdin#3..N+2) → 之后全是 -c 读形态。
	//   ⇒ 「第 3 次起失败」**恰好**等于「只有启动迁移失败」。
	dockerScript := `#!/bin/sh
{
  echo "--- call ---"
  for a in "$@"; do echo "$a"; done
} >> "$TRACE_FILE"
case "$1" in
  port) printf '%s' "127.0.0.1:5432"; exit 0 ;;
  exec)
    case "$*" in
      *printenv*) printf '%s' "fakepassword"; exit 0 ;;
      *"-c "*) exit 0 ;;
    esac
    # ★ 从 stdin 灌 SQL 的调用：计数 + 必须把 stdin 读完（否则 cat 收到 EPIPE ⇒ pipefail 误判）。
    n=$(cat "$TRACE_FILE.count" 2>/dev/null || echo 0)
    n=$((n + 1))
    printf '%s' "$n" > "$TRACE_FILE.count"
    cat >/dev/null 2>&1 || true
    # 第 3 次起 = 启动迁移 ⇒ 失败（制造 startup 链 failed>0）
    if [ "$n" -ge 3 ]; then exit 1; fi
    exit 0 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(dockerScript), 0o755); err != nil {
		t.Fatalf("写假 docker: %v", err)
	}
	goScript := `#!/bin/sh
echo "=== RUN   TestFreshInstallRuntimeShape_EnsureChain"
echo "--- PASS: TestFreshInstallRuntimeShape_EnsureChain (5.76s)"
echo "PASS"
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(goScript), 0o755); err != nil {
		t.Fatalf("写假 go: %v", err)
	}

	cmd := exec.Command(bashForScriptFor(t, probeShapeScript), probeShapeScript)
	cmd.Env = append(os.Environ(),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TRACE_FILE="+trace,
		"LOG="+logPath,
		"PG_CONTAINER=fakepg",
		"PG_USER=llm_gateway",
		"DB_NAME=itgate_r85_gate",
		"KEEP_DB=1",
	)
	out, err := cmd.CombinedOutput()
	rc := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			rc = ee.ExitCode()
		} else {
			t.Fatalf("跑探针脚本失败: %v", err)
		}
	}
	body := string(out)

	if strings.Contains(body, "ensure 链已跑（库现在 = 产品真实形态）") {
		t.Fatalf("★ startup 链失败 ⇒ 库不是产品真实形态，却印出「形态可信」\nstdout:\n%s", body)
	}
	if !strings.Contains(body, "startup 链有失败") {
		t.Fatalf("必须指出是 startup 链失败（否则人不知道该查链还是查测试）\nstdout:\n%s", body)
	}
	// ★ 必须证明是 **failed>0**（而不是 missing>0）：假 docker 让迁移**跑失败**，
	//   不是让迁移文件消失 ⇒ 两条都指「形态不可信」但成因不同。
	if !regexp.MustCompile(`failed=[1-9]\d*`).MatchString(body) {
		t.Fatalf("必须报出 failed>0 的具体计数（否则分不清「跑失败」与「文件缺失」）\nstdout:\n%s", body)
	}
	if rc != 3 {
		t.Fatalf("startup 链失败时脚本应以 3 退出（量具坏了），实际 rc=%d\nstdout:\n%s", rc, body)
	}
}
