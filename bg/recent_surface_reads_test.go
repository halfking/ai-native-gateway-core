package bg

import (
	"os"
	"regexp"
	"testing"
)

// TestRecentWindowReadsUseCurrentMonthSurface extends the
// passive_probe_recent_surface_test.go doctrine (2026-09-10 minimax-prod-v2
// incident) to the remaining bg workers with recent-window _request_logs
// reads. The bare _request_logs parent only holds cold rows — its max(ts) ran
// a full day+ stale on 154 while _request_logs_hot served live traffic — so
// every recent-window read against it is structurally blind.
//
// Covered files (2026-09-11 audit round):
//   - candidate_failure_monitor.go: 5-minute staleness probe + auto-cool
//     attempt window (staleness never fired / auto-cool never fired)
//   - shared_pick.go: 7-day most-used probe-model pick (stale pick)
//   - daily_probe_audit.go: 3-day lookback for daily submissions (under-scan)
//
// Previously NOT covered: integrity_fingerprint_drift.go stayed on the bare
// parent while system_fingerprint was absent from the view (hot column added
// by 603, but the base wrapper's intersection was frozen pre-603). Migration
// 696 appended the column to the view and the scanner switched — the
// exclusion is now an inclusion pinned at the bottom of this test.
//
// 2026-09-12 audit round: same doctrine extended to candidate_failure_logs —
// the writer (domains/streaming/executors/candidate_failure_logger.go)
// inserts into candidate_failure_logs_hot, so the bare parent only holds
// promoted (cold) rows; with the partition promote failing (P5 duplicate key,
// since 09-10) its max(ts) ran 8h+ stale while the hot table was served
// minutes earlier. Covered files:
//   - candidate_failure_monitor.go: staleness max(ts) + 5-minute alert scan
//   - auto-cool cfl CTE (stale alert fired / alerts + auto-cool blind)
//   - daily_probe_audit.go: 3-day candidate branch (under-scan)
//   - model_probe.go: 5-minute passive-boost pick (always empty)
//
// 2026-10-13 252 SQL 日志审计轮：doctrine 再演进——hot 双写表本身成为合法
// 近窗面。2026-09-10 事故的对立面是「裸父表冷读」，而 _request_logs_hot /
// candidate_failure_logs_hot 是写方同步喂数的最新鲜面；canonical 视图对
// 短窗（≤hot promote retention 8h）是纯税（三臂全分区反连接，R32
// slidingWindowHotQuery 定版同一论证）。candidate_failure_monitor.go 的
// auto-cool 改读 hot 三表：252 生产库 EXCEPT 双向差集 0/0 + 真库回归门
// auto_cool_hot_realdb_test.go（计划断言钉「只读 hot、不触碰视图」）。
// 豁免以 _hot 后缀放行 + 读点数钉死（下方案数断言），新读点必须先过
// 对拍门再扩名单——与 opslog_trimmer/integrity_fingerprint 两个先例同款。
//
// ★ 2026-10-20（迁移 890，v1 族退役）：v1 族（_request_logs*）整族被 DROP
// ⇒ 「读 v1 族」不再是另一种读面，而是一个**必须为零**的违规（提及即 42P01）。
// ⇒ 本门从「v1 读面要么是 canonical 视图、要么是 hot 表」**收紧**为
// 「这三个文件里不得再有任何 `FROM _request_logs*`」。
//
// ⚠ 而**同一条纪律**（近窗读不读冷裸父表）在会话族上仍然成立，只是对象换了：
// 会话族的冷面是 `session_turns`（分区父表），热面是 `session_turns_hot`
// ⇒ 近窗读必须落在热腿或 canonical 视图上，不能落在冷父表上。
func TestRecentWindowReadsUseCurrentMonthSurface(t *testing.T) {
	re := regexp.MustCompile(`(?i)FROM\s+_?request_logs(\w*)`)
	for _, file := range []string{
		"candidate_failure_monitor.go",
		"shared_pick.go",
		"daily_probe_audit.go",
	} {
		contents, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		src := string(contents)
		matches := re.FindAllStringSubmatch(src, -1)
		// ★ 890：v1 族已退役 ⇒ 唯一还允许的 v1 名字是 canonical **视图**
		// request_logs_with_current_month（890 之后它是会话两臂体）。
		// 任何其它后缀（裸父表 / _hot / _bodies…）都是已 DROP 的族。
		for _, m := range matches {
			if m[1] == "_with_current_month" {
				continue
			}
			t.Fatalf("%s 仍读已退役的 v1 族 %q —— 迁移 890 把它整族 DROP 了，"+
				"提及即整条语句 42P01（近窗读改读会话族热腿或 canonical 视图）",
				file, m[0])
		}
	}

	// 2026-10-13: hot 快路径读点钉子——candidate_failure_monitor.go 恰好
	// 1 处 _request_logs_hot（auto-cool win 的 v1 臂）+ 1 处
	// candidate_failure_logs_hot（cfl 臂）读点。数字变了=有新近窗读点或
	// 旧读点被改面，必须先过 auto_cool_hot_realdb_test.go 对拍门再改这里。
	//
	// ★ 890：v1 hot 读点归零（那条臂随 v1 族一起退役）；cfl 臂读点数不变；
	// 同时新增**会话族热腿**的读点钉子（近窗纪律在会话族上的落点）。
	monSrc, err := os.ReadFile("candidate_failure_monitor.go")
	if err != nil {
		t.Fatalf("read candidate_failure_monitor.go: %v", err)
	}
	for _, pin := range []struct {
		re   string
		want int
	}{
		{`(?i)FROM\s+_request_logs_hot\b`, 0},          // ★ 890：v1 族已退役 ⇒ 归零
		{`(?i)FROM\s+candidate_failure_logs_hot\b`, 1}, // cfl 族未退役，读点不变
		{`(?i)FROM\s+session_turns_hot\b`, 1},          // ★ 890：auto-cool 腿读会话族热腿
		{`db\.SessionFamilyTurnsHotSourceSQL\(\)`, 1},  // ★ 890：staleness 源读会话族热腿
	} {
		if got := len(regexp.MustCompile(pin.re).FindAllStringSubmatch(string(monSrc), -1)); got != pin.want {
			t.Fatalf("candidate_failure_monitor.go hot 读点数变化 (%s): got=%d want=%d — "+
				"先过对拍门再改 recent_surface_reads_test.go 的钉子", pin.re, got, pin.want)
		}
	}

	// 2026-09-12: same blindness, candidate_failure_logs family. The writer
	// feeds the hot table; the bare parent only holds promoted rows.
	reCfl := regexp.MustCompile(`(?i)FROM\s+candidate_failure_logs(\w*)`)
	for _, file := range []string{
		"candidate_failure_monitor.go",
		"daily_probe_audit.go",
		"model_probe.go",
	} {
		contents, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		matches := reCfl.FindAllStringSubmatch(string(contents), -1)
		if len(matches) == 0 {
			t.Fatalf("%s: no candidate_failure_logs reads found — query surface moved?", file)
		}
		for _, m := range matches {
			// 2026-10-13: hot 双写表是合法近窗面（见头注 doctrine 演进）。
			if m[1] == "_hot" {
				continue
			}
			if m[1] != "_with_current_month" {
				t.Fatalf("%s reads the cold %q surface — recent-window reads must "+
					"use candidate_failure_logs_with_current_month (writer feeds the "+
					"hot table; bare parent only holds promoted rows)", file, m[0])
			}
		}
	}

	// 2026-09-12: the drift scanner's exclusion is LIFTED. Migration 696
	// appended system_fingerprint to request_logs_with_current_month (the
	// base wrapper's hot∩parent intersection had been frozen before 603
	// added the column to the hot table), so the scanner now reads the view
	// like every other recent-window reader. The remaining guard flips
	// accordingly: every _request_logs-family FROM in the drift scanner must
	// stay on the _with_current_month surface — no silent regression to the
	// bare parent, no new bare-parent reader unreviewed.
	drift, err := os.ReadFile("integrity_fingerprint_drift.go")
	if err != nil {
		t.Fatalf("read integrity_fingerprint_drift.go: %v", err)
	}
	driftMatches := re.FindAllStringSubmatch(string(drift), -1)
	if len(driftMatches) == 0 {
		t.Fatalf("integrity_fingerprint_drift.go: no _request_logs reads found — query surface moved?")
	}
	for _, m := range driftMatches {
		if m[1] != "_with_current_month" {
			t.Fatalf("integrity_fingerprint_drift.go reads the cold %q surface — the 7-day "+
				"fingerprint window is a recent-window read and must use "+
				"request_logs_with_current_month (migration 696 added the column to the view)", m[0])
		}
	}

	// Lock the other conscious exclusion: opslog_trimmer deletes aged rows
	// from the bare parent on purpose — retention semantics (the UNION view
	// is not deletable, and hot rows must survive the trim window).
	trimmer, err := os.ReadFile("opslog_trimmer.go")
	if err != nil {
		t.Fatalf("read opslog_trimmer.go: %v", err)
	}
	if n := len(reCfl.FindAllStringSubmatch(string(trimmer), -1)); n != 2 {
		t.Fatalf("opslog_trimmer.go bare-parent candidate_failure_logs site count changed (%d): "+
			"retention deletes must stay on the deletable parent — re-audit before touching", n)
	}
}
