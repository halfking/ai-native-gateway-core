package ursm_test

// pg17-onconflict-constraint-check.sh 的行为门（2026-10-06，审计 §10.59）
//
// 这道门要钉的是**跨文件的同步**：脚本里写死的 `ONCONFLICT_EXPECT_COLS`
// 必须与 domains/ursm/v2/persist/writer.go 的 ON CONFLICT 推断列**完全一致**。
//
// 为什么必须有：脚本把期望列写死（252 上没有仓库、跑不了 Go），
// 这是「让真机能自己判断」的必要代价；但写死就会漂 ——
// 改了 writer 的 ON CONFLICT 而忘了改脚本，检查就会拿旧列去比，
// 报出「健康」或报出**错误的违规**，两种都等于没有检查。
// 这与本项目已踩过的「夹具在、没接线 ⇒ 门绿」是同一族。

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const onconflictScript = "252-monitor/pg17-onconflict-constraint-check.sh"

// writer 的 ON CONFLICT 子句（必须从真实源码提取，不能在这里再写一份）
const (
	writerPath     = "../../domains/ursm/v2/persist/writer.go"
	reONConflictFn = `(?is)ON\s+CONFLICT\s*\(([^)]*)\)`
	reExpectCols   = `(?m)^EXPECT_COLS=\$\{ONCONFLICT_EXPECT_COLS:-([^}]*)\}`
)

func onconflictScriptPath(t *testing.T) string {
	t.Helper()
	p := filepath.Join("..", onconflictScript)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("脚本不存在: %v", err)
	}
	return p
}

func normCols(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func runOnconflict(t *testing.T, psqlRC int, stub string, env ...string) (int, string) {
	t.Helper()
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-psql")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"+stub+"\nexit "+itoa(psqlRC)+"\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	e := append([]string{"PSQL_CMD=" + fake}, env...)
	cmd := exec.Command("bash", onconflictScriptPath(t))
	cmd.Env = append(os.Environ(), e...)
	out, _ := cmd.CombinedOutput()
	return cmd.ProcessState.ExitCode(), string(out)
}

// TestOnconflictExpectColsMatchesWriter 是本文件的正身。
// 脚本的期望列与 writer 的 ON CONFLICT 不一致 ⇒ 红。
func TestOnconflictExpectColsMatchesWriter(t *testing.T) {
	wb, err := os.ReadFile(filepath.FromSlash(writerPath))
	if err != nil {
		t.Fatalf("read writer.go: %v", err)
	}
	m := regexp.MustCompile(reONConflictFn).FindSubmatch(wb)
	if m == nil {
		t.Fatal("writer.go 里没有解析到 ON CONFLICT —— writer 形态变了，请同步本判据")
	}
	want := normCols(string(m[1]))

	sb, err := os.ReadFile(onconflictScriptPath(t))
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	sm := regexp.MustCompile(reExpectCols).FindSubmatch(sb)
	if sm == nil {
		t.Fatal("脚本里没有解析到 EXPECT_COLS 默认值 —— 请保持该行可被本判据读取")
	}
	got := normCols(string(sm[1]))

	if strings.Join(want, ",") != strings.Join(got, ",") {
		t.Errorf("脚本的 EXPECT_COLS 与 writer 的 ON CONFLICT 不一致：\n"+
			"  writer.go : [%s]\n  脚本默认值: [%s]\n"+
			"★ 不一致的后果不是「报红」而是**报错的东西**：脚本会拿旧列去比对，"+
			"可能对一个健康的库报违规，或对真正失配的库报健康。",
			strings.Join(want, ", "), strings.Join(got, ", "))
	}
}

// 命中：存在列集合完全相等的唯一索引 ⇒ exit 0
//
// ★ 夹具里 indisunique 用 `true` 而不是 `t`：2026-10-06 在 252 真库实测
//   psql -A -t 对 boolean::text 返回的就是 `true`。
//   夹具要照抄真实源的形状，否则判据可能测的是「我想象的 psql」。
//   （本门只数字段个数，所以两者都能过 —— 但那属于这道门的运气，不是设计。）
func TestOnconflictMatchExitsZero(t *testing.T) {
	stub := "echo 'ursm_node_snapshot_min_pkey|credential_id,raw_model_name,snapshot_ts,tenant_id|true|1'"
	code, out := runOnconflict(t, 0, stub)
	if code != 0 {
		t.Fatalf("命中时应 exit 0，实得 %d\n%s", code, out)
	}
	// ★ 反向自证：exit 0 必须**带输出**。
	// 2026-10-06 真机验证踩到过一次「exit 0 且 0 字节输出」——
	// 根因是 docker exec -i 把脚本剩余的 stdin 读走、脚本静默截断，
	// 而「exit 0」按本脚本的契约恰恰读作**健康**。
	// ⇒ 只断言退出码，会把「量具没跑完」记成「巡检结论是好的」。
	//   这与同族那条「报 3 但一个字都没输出」是同一条纪律的两面：
	//   **退出码必须配上可核对的输出**。
	if strings.TrimSpace(out) == "" {
		t.Fatalf("exit 0 却没有任何输出 —— 脚本多半没跑完（被静默截断），"+
			"而 exit 0 按契约读作「健康」。不能把「没跑完」记成「没问题」。")
	}
	if !strings.Contains(out, "恰好覆盖") {
		t.Errorf("exit 0 的输出应说明是哪个索引命中了，便于人工核对：\n%s", out)
	}
}

// ★ 线上真实形态：PK 是 3 列，缺 tenant_id ⇒ 必须 exit 1。
func TestOnconflictMissingColumnExitsOne(t *testing.T) {
	stub := "echo 'ursm_node_snapshot_min_pkey|credential_id,raw_model_name,snapshot_ts|true|1'"
	code, out := runOnconflict(t, 0, stub)
	if code != 1 {
		t.Fatalf("缺列时应 exit 1（写入会 100%% 失败），实得 %d\n%s", code, out)
	}
	for _, want := range []string{"42P10", "成功率 0", "不要改 writer"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出应包含 %q：\n%s", want, out)
		}
	}
}

// psql 失败 ⇒ exit 3，绝不退化成 exit 0 报健康。
func TestOnconflictPsqlFailureIsNoConclusion(t *testing.T) {
	if code, out := runOnconflict(t, 1, "echo 'FATAL: conn refused' >&2"); code != 3 {
		t.Fatalf("psql 失败应 exit 3，实得 %d —— 若为 0 就是「查不到却报健康」\n%s", code, out)
	}
}

// 表不存在 / 零唯一索引 ⇒ 没有结论，不是「违规」也不是「健康」。
func TestOnconflictNoUniqueIndexIsNoConclusion(t *testing.T) {
	if code, out := runOnconflict(t, 0, "echo ''"); code != 3 {
		t.Fatalf("零唯一索引应 exit 3（表多半不存在），实得 %d\n%s", code, out)
	}
}

// 字段数不合契约 ⇒ exit 3（psql 可能 rc=0 却吐出提示行）。
func TestOnconflictWrongFieldCountAborts(t *testing.T) {
	if code, out := runOnconflict(t, 0, "echo 'a|b|c'"); code != 3 {
		t.Fatalf("字段数不符应 exit 3，实得 %d\n%s", code, out)
	}
}

// itoa 是本包内 runDupIndex 已提供的助手；若编译报未定义则此处补上。
