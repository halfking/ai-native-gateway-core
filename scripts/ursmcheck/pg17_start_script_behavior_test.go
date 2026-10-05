package ursm_test

// pg17-start.sh 的分级退出契约门（2026-10-05）
//
// 被测的那条性质只有一句：**「PG 起来了但 pgvector 没恢复」不许退出 0。**
//
// ★ 事故当天它退出的是 0。原写法是
//     curl … && podman cp … && podman exec dpkg -i … && echo restored \
//       || echo "WARN: …"
//   恢复失败 ⇒ 打一行 WARN ⇒ 脚本继续跑完 ⇒ exit 0。
//   而我当时是 `tail -25` 看输出的，WARN 恰好被截掉，
//   于是把「恢复完成」读成了成功。
// ⇒ 这个缺陷有两层：脚本的退出码不诚实，**以及**我读它的方式只看尾部。
//   本门只钉第一层（能在仓库里钉的），第二层属于操作纪律，登记在 runbook。
//
// 为什么这个门值得写：它是本目录里唯一一个「成功」与「部分成功」过去同形的
// 脚本，其余巡检都有 0/1/3 分级。有分级的那些，退出码已经是 SSOT。

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const manualStartScript = "../252-monitor/manual/pg17-start.sh"

// fakePodman 模拟一台机器上 pg-252-pg17 的全部生命周期动作。
// 用 FAKE_* 开关，分别让「pgvector 恢复」成功或失败。
const fakePodman = `#!/bin/sh
case "$1" in
  stop|rm|run) exit 0 ;;                      # 停/删/建，脚本不检查结果
  exec)
    shift
    # 找到子命令
    while [ $# -gt 0 ]; do
      case "$1" in
        pg_isready)
            # ★ 必须真的打印 "accepting"：脚本的判据是
            #   pg_isready 的输出里 grep "accepting"，只退 0 不说话是不够的。
            #   第一版夹具只 exit 0 不输出 ⇒ grep 永远不匹配 ⇒ 脚本走完
            #   真实的生产等待（20×3=60 秒）才往下走，两条用例都挂在 25 秒超时上。
            #   症状看着像「被测脚本死锁」，真因是夹具没说该说的话。
            if [ "${FAKE_ISREADY_RC:-0}" = "0" ]; then
              echo "pg-252-pg17:5432 - accepting connections"
              exit 0
            fi
            exit "${FAKE_ISREADY_RC}" ;;
        test)       # test -f <so>
                    [ "$FAKE_VECTOR_SO" = "present" ] && exit 0 || exit 1 ;;
        dpkg)       exit "${FAKE_DPKG_RC:-0}" ;;
        psql)       break ;;
        *) shift ;;
      esac
    done
    # psql 形态有两种：LOAD 'vector'（功能自检）与调优参数查询
    for a in "$@"; do
      case "$a" in
        "LOAD 'vector'") exit "${FAKE_LOAD_RC:-0}" ;;
      esac
    done
    printf 'max_wal_size|4GB\n'
    exit 0 ;;
  cp) exit "${FAKE_CP_RC:-0}" ;;
esac
exit 0
`

// fakeCurl 返回一个 Packages.gz 的替身。默认给非 gzip 内容 ⇒ zcat 失败 ⇒
// awk 解析不出文件名 —— 这正是事故当天发生的那条路径。
const fakeCurl = `#!/bin/sh
printf 'not-a-gzip-payload'
exit 0
`

type startEnv struct{ bin string }

func newStartEnv(t *testing.T) startEnv {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"podman": fakePodman, "curl": fakeCurl} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return startEnv{bin: bin}
}

func (e startEnv) run(t *testing.T, extra ...string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", manualStartScript, "test-pg-17")
	cmd.Env = append(os.Environ(),
		"PATH="+e.bin+":/usr/bin:/bin:/usr/sbin:/sbin",
		"LC_ALL=C", "LANG=C",
		"FAKE_ISREADY_RC=0",
		"FAKE_VECTOR_SO=present", // 默认 .so 已在 ⇒ 跳过下载路径
		"FAKE_LOAD_RC=0",
		// 等待参数压到 0：生产默认是 20×3=60 秒，门不该真的等一分钟。
		// 轮询循环本身没被改短，只是次数与间隔被注入 ⇒ 测的仍是同一段代码。
		"READY_TRIES=2", "READY_INTERVAL=0",
	)
	cmd.Env = append(cmd.Env, extra...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("崩在解释器层: %v\n%s", err, out)
	}
	return ee.ExitCode(), string(out)
}

// 一切正常 ⇒ 0。
func TestManualStartReportsSuccessOnlyWhenSelfChecksPass(t *testing.T) {
	rc, out := newStartEnv(t).run(t)
	if rc != 0 {
		t.Fatalf("全部自检通过时期望 rc=0，实得 %d：\n%s", rc, out)
	}
}

// ★ 本门存在的理由：.so 缺失 + 下载路径解析不出 + LOAD 失败 ⇒ 必须 rc=1。
// 这一条正是 2026-10-05 静默失败的那条路径。
func TestManualStartReportsDegradedWhenVectorCannotBeRestored(t *testing.T) {
	rc, out := newStartEnv(t).run(t,
		"FAKE_VECTOR_SO=missing", // 触发下载/恢复分支
		"FAKE_LOAD_RC=1",         // 功能自检失败
	)
	if rc == 0 {
		t.Fatalf("pgvector 明确不可用却退出 0 —— 这正是事故当天的形态。\n输出:\n%s",
			"（上面这次执行返回了 0）"+out)
	}
	if rc != 1 {
		t.Errorf("降级期望 rc=1，实得 %d：\n%s", rc, out)
	}
	if !strings.Contains(out, "降级") {
		t.Errorf("降级时输出里必须出现「降级」二字，否则运维会读成正常恢复：\n%s", out)
	}
}

// 「.so 在」不等于「能用」：文件在但 LOAD 失败，同样必须降级。
// 这一条挡的是最容易被当成「已经好了」的情形。
func TestManualStartDoesNotTrustFilePresenceAsProof(t *testing.T) {
	rc, out := newStartEnv(t).run(t,
		"FAKE_VECTOR_SO=present", // 文件确实在
		"FAKE_LOAD_RC=1",         // 但加载不了（ABI 不符 / 依赖缺失）
	)
	if rc != 1 {
		t.Fatalf("文件在但功能自检失败时期望 rc=1，实得 %d：\n%s", rc, out)
	}
	if !strings.Contains(out, "功能自检") {
		t.Errorf("应明说是功能自检失败（而不是「文件缺失」）：\n%s", out)
	}
}

// PG 没起来 ⇒ 与「起来但降级」必须能分开：前者是 2，后者是 1。
func TestManualStartDistinguishesDownFromDegraded(t *testing.T) {
	rc, out := newStartEnv(t).run(t, "FAKE_ISREADY_RC=1")
	if rc != 2 {
		t.Fatalf("PG 没起来期望 rc=2，实得 %d：\n%s", rc, out)
	}
}
