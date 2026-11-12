package startup

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/bg"
)

// migration_963_promote_default_realigned_test.go —— 963
// （promote_session_turns_hot_to_partition 签名默认保留期 '7 days' → '8 hours'）
// 的契约门。全面审计v3 第 53 增量移交清单 F-43 收口的最后一步。
//
// # 它防的缺陷（★ 本轮亲验撞出的完整链）
//
//	SQL 签名默认与 Go 调度器默认（DefaultRetentionWindow = 8h，
//	lifecycle.hot_retention_hours 默认 8）漂移 21 倍：
//	  · 688（2026-09-08）曾把本函数对齐到 '8 hours'；
//	  · 707 重写时保持 8h；
//	  · 640 被登记进投递通道且排在 707 之后（869 头注释记录的投递序事故）
//	    ⇒ 640 的旧形态函数体（含 '7 days' 签名）把对齐覆盖掉；
//	  · 869 重写时**有意保守**：把「在役值」'7 days' 原样保留
//	    （migration_869_test.go:96 钉死 must preserve the deployed default，
//	    理由 = 数据修复不混行为变更）—— ★ 但在役值本身就是被事故覆盖出来的
//	    错值 ⇒ 钉「当前值」的钉测把错值固化成了不变量；
//	  · 897（基线 = 869）沿袭。
//	后果（F-43/F-58 台账）：运维裸调 `SELECT promote_xxx();` 不传参时多保留
//	21 倍 hot 数据；更贵的是 admin/logs.go 的注释曾引用这个 '7 days' 签名默认
//	当论据（hotLegMaxWindow=7d 的误信源头之一，LESSONS §702①）。
//
// # 三道牙
//
//	§A 动态最新载体门：目录里【编号最大】的函数体载体，其签名默认必须是
//	   '8 hours' —— 未来任何人重写本函数而带回旧默认，门当场红
//	   （869/897 形态的回归不会静默溜过）。
//	§B 折叠 delta 逐字等价门：963 的函数体在把唯一的已钉差异
//	   （DEFAULT '8 hours' → '7 days'）折叠回去之后，必须与 897 的函数体
//	   逐字节相等 ⇒ 除那一行外的任何漂移都红；并补正向断言
//	   （demote 步 / 目录派生清单 / 963 后置牙 / 自登记必须在位）。
//	§C 跨包不变式：SQL 签名默认 == bg.DefaultRetentionWindow（真常量，
//	   非手写字符串钉）。
//	§D 六点登记钉：211 通道 files=(...) / intentional_function_chains 链尾 /
//	   runner.go StartupFiles / embeddata 双副本逐字节 / db-changelog 双行 sha
//	   / bootstrap 不再携带旧默认 —— 962 的六面缺一教训（§701）照抄。
func TestMigration963_LatestCarrierCarriesTheAlignedDefault(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读迁移目录失败: %v", err)
	}
	type carrier struct {
		name string
		num  int
	}
	var carriers []carrier
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".down.sql") {
			continue
		}
		n, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读 %s: %v", name, err)
		}
		if strings.Contains(string(b), "CREATE OR REPLACE FUNCTION public.promote_session_turns_hot_to_partition(") {
			carriers = append(carriers, carrier{name, n})
		}
	}
	if len(carriers) == 0 {
		t.Fatal("目录里找不到任何定义 promote_session_turns_hot_to_partition 的迁移 —— 判据失效")
	}
	sort.Slice(carriers, func(i, j int) bool { return carriers[i].num < carriers[j].num })
	latest := carriers[len(carriers)-1]
	if latest.name != "963_promote_session_turns_default_retention_realign.sql" {
		t.Fatalf("★ 本函数出现了编号更大的新载体 %s —— ★ 请把本门的钉点迁移过去，并保持「最新载体默认 = 调度器默认」不变量",
			latest.name)
	}
	b, err := os.ReadFile(latest.name)
	if err != nil {
		t.Fatalf("读 %s: %v", latest.name, err)
	}
	raw := string(b)
	if !strings.Contains(raw, "p_retention INTERVAL DEFAULT '8 hours',") {
		t.Errorf("★ 最新载体 %s 的签名默认不是 '8 hours' —— ★ 688 对齐被重写吃掉的回归又发生了（869/897 形态）", latest.name)
	}
	exec := stripCommentLines963(raw)
	// ⚠ 口径 = 「DEFAULT 形态」而不是任意 '7 days' 字面量：963 自己的后置牙
	//   （IF position('7 days' in v_fndef) > 0）是**拒绝旧形态的哨兵**，
	//   它的可执行面合法携带 '7 days' 字面量 —— 不能把自己的牙当成回流。
	if regexp.MustCompile(`DEFAULT\s+'7 days'`).MatchString(exec) {
		t.Errorf("★ 最新载体 %s 的可执行面仍携带 DEFAULT '7 days' —— 旧默认不得回流", latest.name)
	}
}

func TestMigration963_FoldsExactlyOneDeltaAgainst897(t *testing.T) {
	up963 := mustRead963(t, "963_promote_session_turns_default_retention_realign.sql")
	base897 := mustRead963(t, "897_session_turns_promote_final_success_self_heal.sql")

	fn963 := extractFunction963(t, up963, "963")
	fn897 := extractFunction963(t, base897, "897")

	folded := strings.Replace(fn963, "p_retention INTERVAL DEFAULT '8 hours',", "p_retention INTERVAL DEFAULT '7 days',", 1)
	if folded != fn897 {
		// 给出第一处差异，帮助定位（不要整段 dump）。
		i := 0
		for i < len(folded) && i < len(fn897) && folded[i] == fn897[i] {
			i++
		}
		lo := i - 80
		if lo < 0 {
			lo = 0
		}
		t.Fatalf("★ 963 的函数体与 897 的差异不止 DEFAULT 一行（首处差异 @%d）：\n963 折叠后: %q\n897      : %q",
			i, clip963(folded, lo, i+120), clip963(fn897, lo, i+120))
	}

	// 正向断言（防「折叠等价」反向变成「允许退回」的门）：963 必须携带
	// 897 的两道牙 + 自己的后置牙 + 自登记。
	for _, needle := range []string{
		"SET is_final_success = FALSE",                           // 897 的 demote 自愈步
		"INSERT INTO public.session_turns (%s)",                  // 869 的目录派生映射
		"''08:00:00''::interval",                                 // 963 自己的后置牙
		"963 post-condition failed: stale 7-day default is back", // 963 后置牙的旧形态拒绝臂
		"VALUES ('963',",                                         // 双账本自登记（≥952 纪律）
	} {
		if !strings.Contains(up963, needle) {
			t.Errorf("★ 963 up 必须携带 %q —— 没有它，本迁移只是把 897 重装一遍而什么都没修", needle)
		}
	}
}

func TestMigration963_DownRestoresThe897FormVerbatim(t *testing.T) {
	down := mustRead963(t, "963_promote_session_turns_default_retention_realign.down.sql")
	base897 := mustRead963(t, "897_session_turns_promote_final_success_self_heal.sql")

	fnDown := extractFunction963(t, down, "963 down")
	fn897 := extractFunction963(t, base897, "897")
	if fnDown != fn897 {
		t.Fatal("★ 963 down 的函数体必须与 897 逐字节相等（回滚目标 = 897 形态）—— 任何漂移都会让回滚落到一个没人验证过的形态")
	}
	if !strings.Contains(down, "963 down post-condition failed") {
		t.Error("★ 963 down 必须自带后置牙（安装的默认值确为 7 天形态），否则 down 成功会掩盖装错形态")
	}
	if !strings.Contains(stripCommentLines963(down), "DEFAULT '7 days'") {
		t.Error("★ 963 down 的可执行面必须携带 '7 days' 默认（回滚语义本体）")
	}
}

// TestMigration963_SQLDefaultEqualsTheGoSchedulerDefault —— 跨包不变式：
// SQL 签名默认 == Go 调度器默认。判据用 bg.DefaultRetentionWindow 真常量
// （编译期对拍），不是手写字符串钉 —— 调度器默认改了，门会跟着要求 SQL 侧改。
func TestMigration963_SQLDefaultEqualsTheGoSchedulerDefault(t *testing.T) {
	if bg.DefaultRetentionWindow != 8*time.Hour {
		t.Fatalf("bg.DefaultRetentionWindow 已变为 %s —— ★ 本门与其 963 的 '8 hours' 钉点需要一起重新对拍（§702①：生产行为只认调度器显式传参链）", bg.DefaultRetentionWindow)
	}
	up963 := mustRead963(t, "963_promote_session_turns_default_retention_realign.sql")
	if !strings.Contains(up963, "p_retention INTERVAL DEFAULT '8 hours',") {
		t.Error("★ 963 签名默认必须与 bg.DefaultRetentionWindow（8h）同值")
	}
}

func TestMigration963_RegisteredInEveryDeliveryFace(t *testing.T) {
	const (
		upName   = "963_promote_session_turns_default_retention_realign.sql"
		downName = "963_promote_session_turns_default_retention_realign.down.sql"
	)
	up963 := mustRead963(t, upName)
	down963 := mustRead963(t, downName)

	// ① 211 通道 files=(...) 显式清单。
	seqRaw := string(mustRead963(t, filepath.Join("..", "..", "..", "scripts", "apply-db-revision-sequence.sh")))
	if !strings.Contains(seqRaw, `"$ROOT_DIR/sql/migrations/startup/`+upName+`"`) {
		t.Error("★ 963 未登记进 scripts/apply-db-revision-sequence.sh 的 files=(...) —— 211 seamless 通道将看不到这条迁移（962 的记账断层同款）")
	}
	// ② intentional_function_chains 链尾。
	chainRe := regexp.MustCompile(`'promote_session_turns_hot_to_partition\|[^']*'`)
	chain := chainRe.FindString(seqRaw)
	if chain == "" {
		t.Fatal("★ intentional_function_chains 里找不到 promote_session_turns_hot_to_partition 的链行 —— 判据失效")
	}
	if !strings.HasSuffix(strings.TrimSuffix(strings.TrimSuffix(chain, "'"), "|"), upName) {
		t.Errorf("★ 963 必须是 promote_session_turns_hot_to_partition 链的最后一项（活库最终体）：%s", chain)
	}
	// ③ runner.go StartupFiles。
	runner := string(mustRead963(t, filepath.Join("..", "..", "..", "installer", "internal", "dbinit", "runner.go")))
	if !strings.Contains(runner, `"`+upName+`"`) {
		t.Error("★ 963 未登记进 installer/internal/dbinit/runner.go 的 StartupFiles")
	}
	// ④⑤ embeddata 双副本逐字节一致。
	for _, pair := range []struct{ canonical, embedded string }{
		{up963, string(mustRead963(t, filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup", upName)))},
		{down963, string(mustRead963(t, filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup", downName)))},
	} {
		if pair.canonical != pair.embedded {
			t.Error("★ 963 的 embeddata 副本与 canonical 迁移文件不一致 —— 双副本必须逐字节一致（962 先例）")
		}
	}
	// ⑥ db-changelog 双行 + sha 与文件实测一致。
	changelog := string(mustRead963(t, filepath.Join("..", "..", "..", "docs", "db-changelog.md")))
	for _, name := range []string{upName, downName} {
		if !strings.Contains(changelog, "`"+name+"`") {
			t.Errorf("★ docs/db-changelog.md 缺 %s 登记行", name)
		}
	}
	for _, pair := range []struct{ name, content string }{
		{upName, up963},
		{downName, down963},
	} {
		sum := sha256.Sum256([]byte(pair.content))
		sha := hex.EncodeToString(sum[:])
		if !strings.Contains(changelog, "`"+sha+"`") {
			t.Errorf("★ db-changelog 里 %s 的 sha 不是文件实测值（%s）—— 台账 sha 必须是本文件逐字节实测（TestRegistryShaIsActuallyTheFile 同族）", pair.name, sha[:16]+"…")
		}
	}
	// ⑦ fresh-install bootstrap 不再携带旧默认（瞬态载体也应对齐；869 从
	//    「旧形态」复制签名的教训面）。
	boot := string(mustRead963(t, filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup", "session_turns_hot_bootstrap.sql")))
	if !strings.Contains(boot, "p_retention INTERVAL DEFAULT '8 hours',") {
		t.Error("★ session_turns_hot_bootstrap.sql 的 promote 签名默认仍是旧形态 —— fresh-install 载体也必须与调度器对齐")
	}
}

func mustRead963(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("读 %s: %v", rel, err)
	}
	return string(b)
}

// extractFunction963 从文件里截出 promote 函数的完整文本（签名行到收尾 $$;）。
func extractFunction963(t *testing.T, sql, label string) string {
	t.Helper()
	const head = "CREATE OR REPLACE FUNCTION public.promote_session_turns_hot_to_partition("
	start := strings.Index(sql, head)
	if start < 0 {
		t.Fatalf("★ %s 里没有 %q —— 判据失效", label, head)
	}
	rest := sql[start:]
	end := strings.Index(rest, "\n$$;")
	if end < 0 {
		t.Fatalf("★ %s 的函数体没有收尾 \\n$$; —— 判据失效", label)
	}
	return rest[:end]
}

// stripCommentLines963 剥掉整行注释与行尾注释（可执行面口径）。
func stripCommentLines963(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func clip963(s string, lo, hi int) string {
	if lo < 0 {
		lo = 0
	}
	if hi > len(s) {
		hi = len(s)
	}
	if lo >= hi {
		return ""
	}
	return strings.ReplaceAll(s[lo:hi], "\n", "\\n")
}
