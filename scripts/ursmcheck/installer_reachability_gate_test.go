package ursm_test

// installer_reachability_gate_test.go
//
// 守的是 R163 轮立起来的那张台账：**新装环境（新装侧）的【多环境同一标准】**。
//
// # 这道门回答的问题（★ 原始目标第 7 条：「多个环境同样的标准来更新」）
//
// ★★ 一条索引，**新装环境为什么会有它**？★ 四种答案、每一种都可机器判：
//
//	reachable-and-present          —— 创建它的迁移在【安装器登记清单 ∩ 分发包】里 ⇒ 新装会跑
//	baseline-dependent             —— 创建它的迁移【不在】安装器清单里
//	                                 ⇒ 新装能拥有它**只因为 01-schema.sql 是一份 dump**
//	unreachable-and-missing        —— 创建它的迁移不可达、基线也没有 ⇒ ★★ 任何新装环境【必然缺】
//	reachable-but-missing-in-fresh —— 创建它的迁移【可达】、新装却没有 ⇒ ★★ 安装器自己有 bug
//
// ★★ 这道门把上面每一种都【独立重算一遍】（★ Go 侧自己读清单 / 自己列目录 / 自己分类），
// ★★ 再与台账逐条对拍 ⇒ ⇒ 台账与事实不符 ⇒ 门红。
//
// # R163 轮的三条硬发现（★ 本门守着它们不被悄悄改回去）
//
// ① ★★ **「ON ONLY <table>」被吃成表名 ONLY** ⇒ ⇒ 17 条索引被判成 `retired`
//    ⇒ ⇒ ★★ **7 条真缺口被彻底掩盖**（★ 危害是「掩盖」，★ 与 R162 的「假缺口」相反）。
//
// ② ★★ **删列 ⇒ PG 级联删索引** ⇒ ⇒ 索引不在了，但它【从来没被 DROP 过】
//    ⇒ ⇒ 「建了 − 被 DROP」这种差集**永远看不到它** ⇒ ⇒ ★★ 假缺口的第三种来源。
//
// ③ ★★ **同一套迁移，顺序不同 ⇒ 终态不同**：★ 211 上 708（删死列）先跑、456（加回死列）后跑
//    ⇒ ⇒ 死列复活、索引复活 ⇒ ⇒ 「迁移跑过」≠「迁移效果留存」。
//
// # ★★ 为什么这道门必须是【第二份独立实现】
//
// ★★ R162 的教训：**两份实现也可能同源同错** ⇒ ⇒ 互证对那批零信息。
// ★★ R163 的「ON ONLY」缺陷就是两侧【一模一样】⇒ ⇒ 这次两侧都改了，
// ★★ 而且两侧的**解析路径不同**（★ Python 用正则 + 括号配对扫描器，★ Go 用手写扫描器）
// ★★ ⇒ ⇒ 这次的互证才有信息。

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	dbObjReachAuditRel = "docs/audit/2026-10-27-R163-新装环境多环境同一标准与执行顺序.md"
)

// ★★ 可达性台账的棘轮（★ R163 轮实测值，★ 2026-10-27）。
// ★★ 只许下降（★ 属主把迁移补进安装器清单之后 ⇒ 数字变小 ⇒ 仍绿）。
// ★★ ★ installer-bug 的基线是 0 ⇒ ★★ 【任何一条安装器自身的 bug 都直接红】⇒ 不许有基线余量。
var dbObjReachRatchet = map[string]int{
	"new-install-gap": 13,
	"installer-bug":   0,
}

// dbObjReadManifest 读安装器登记清单 ⇒ {文件名: 0-based apply-order}。
func dbObjReadManifest(t *testing.T, rel string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, r := range dbObjDataLines(t, dbObjRepoPath(t, rel)) {
		if len(r) < 2 {
			t.Fatalf("manifest row malformed: %v", r)
		}
		out[r[1]] = true
	}
	if len(out) == 0 {
		t.Fatal("installer manifest is empty ⇒ the reachability verdict asserts nothing")
	}
	return out
}

// dbObjReadPackageUp 读安装器分发包里的 up 迁移文件名集合（★ .down.sql 不算）。
func dbObjReadPackageUp(t *testing.T, rel string) map[string]bool {
	t.Helper()
	dir := dbObjRepoPath(t, rel)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".sql") && !strings.HasSuffix(n, ".down.sql") {
			out[n] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("installer package has no up migrations ⇒ the reachability verdict asserts nothing")
	}
	return out
}

// dbObjReachVerdict 独立重算 verdict（★ 与 Python 侧同一口径、★ 但独立实现）。
func dbObjReachVerdict(reachable bool, freshCell string) string {
	switch {
	case strings.HasPrefix(freshCell, "present") && reachable:
		return "reachable-and-present"
	case strings.HasPrefix(freshCell, "present"):
		return "baseline-dependent"
	case freshCell == "retired" || freshCell == "retired-by-column-drop":
		return "reachable-and-legitimately-retired"
	case reachable:
		return "reachable-but-missing-in-fresh"
	default:
		return "unreachable-and-missing"
	}
}

// dbObjReachSeverity 独立重算 severity。
func dbObjReachSeverity(verdict string, prodMissing bool) string {
	switch verdict {
	case "reachable-and-present", "reachable-and-legitimately-retired":
		return "none"
	case "baseline-dependent":
		return "baseline-dependent"
	case "reachable-but-missing-in-fresh":
		return "installer-bug"
	default:
		if prodMissing {
			return "prod-gap"
		}
		return "new-install-gap"
	}
}

// ─────────────────────── G9：可达性台账 ≡ 独立重算（★ 全量） ───────────────────────

func TestInstallerReachabilityLedgerIsRecomputable(t *testing.T) {
	states := dbObjParseCensus(t, dbObjRepoPath(t, dbObjCensusRel))
	colStates := dbObjParseColumns(t, dbObjRepoPath(t, dbObjColumnsRel))
	goExpected, _ := dbObjDeriveExpected(t)
	manifest := dbObjReadManifest(t, dbObjManifestRel)
	pkg := dbObjReadPackageUp(t, dbObjPackageRel)

	envs := []string{"local", "fresh", "252", "211"}
	prodEnvs := []string{"252", "211"}
	fresh := "fresh"

	rows := dbObjDataLines(t, dbObjRepoPath(t, dbObjReachRel))
	if len(rows) != len(goExpected) {
		t.Fatalf("reachability ledger has %d rows, expected set has %d", len(rows), len(goExpected))
	}

	seen := map[string]bool{}
	mismatches := 0
	for _, r := range rows {
		if len(r) != dbObjReachCols {
			t.Fatalf("reachability row has %d fields, expected %d: %v", len(r), dbObjReachCols, r)
		}
		name := r[0]
		if seen[name] {
			t.Errorf("reachability ledger has duplicate index %q", name)
			mismatches++
		}
		seen[name] = true
		want, ok := goExpected[name]
		if !ok {
			t.Errorf("reachability ledger has index %q that is not in the expected set", name)
			mismatches++
			continue
		}
		if r[1] != want.table {
			t.Errorf("index %q: ledger table=%s expected=%s", name, r[1], want.table)
			mismatches++
		}
		if r[2] != want.no {
			t.Errorf("index %q: ledger migration=%s expected=%s", name, r[2], want.no)
			mismatches++
		}

		// ★★ creator_reachable 独立重算：★ 清单 ∩ 分发包
		creator := filepath.Base(r[3])
		wantReachable := (manifest[creator] && pkg[creator])
		gotReachable := r[4] == "yes"
		if gotReachable != wantReachable {
			t.Errorf("index %q creator %q: ledger reachable=%s recomputed=%v (★ 清单∩分发包)",
				name, creator, r[4], wantReachable)
			mismatches++
		}

		// ★★ present_envs / missing_envs 独立重算（★ 五态分类）
		var wantPresent, wantMissing []string
		freshCell := ""
		prodMissing := false
		for _, env := range envs {
			cell := dbObjClassify(states[env], name, want.table, want.cols, colStates[env])
			if strings.HasPrefix(cell, "present") {
				wantPresent = append(wantPresent, env)
			}
			if cell == "missing" {
				wantMissing = append(wantMissing, env)
				for _, p := range prodEnvs {
					if p == env {
						prodMissing = true
					}
				}
			}
			if env == fresh {
				freshCell = cell
			}
		}
		if r[5] != strings.Join(wantPresent, ",") {
			t.Errorf("index %q: present_envs=%q recomputed=%q", name, r[5], strings.Join(wantPresent, ","))
			mismatches++
		}
		if r[6] != strings.Join(wantMissing, ",") {
			t.Errorf("index %q: missing_envs=%q recomputed=%q", name, r[6], strings.Join(wantMissing, ","))
			mismatches++
		}

		// ★★ verdict / severity 独立重算
		wantVerdict := dbObjReachVerdict(wantReachable, freshCell)
		if r[7] != wantVerdict {
			t.Errorf("index %q: verdict=%s recomputed=%s (★ reachable=%v fresh=%s)",
				name, r[7], wantVerdict, wantReachable, freshCell)
			mismatches++
		}
		wantSeverity := dbObjReachSeverity(wantVerdict, prodMissing)
		if r[8] != wantSeverity {
			t.Errorf("index %q: severity=%s recomputed=%s", name, r[8], wantSeverity)
			mismatches++
		}
	}
	if mismatches > 0 {
		t.Errorf("%d reachability ledger cells disagree with the independent recomputation", mismatches)
	}
	if len(seen) != len(goExpected) {
		t.Errorf("reachability ledger covers %d indexes, expected set has %d", len(seen), len(goExpected))
	}
}

// ─────────────────────── G10：与缺口台账逐条一致（★ 两张台账不许各说各话） ───────────────────────

func TestInstallerReachabilityAgreesWithGapLedger(t *testing.T) {
	envs := []string{"local", "fresh", "252", "211"}
	reach := map[string][]string{}
	for _, r := range dbObjDataLines(t, dbObjRepoPath(t, dbObjReachRel)) {
		if len(r) != dbObjReachCols {
			t.Fatalf("reachability row has %d fields, expected %d: %v", len(r), dbObjReachCols, r)
		}
		reach[r[0]] = r
	}
	ledger := map[string][]string{}
	for _, r := range dbObjDataLines(t, dbObjRepoPath(t, dbObjLedgerRel)) {
		if len(r) != dbObjLedgerCols {
			t.Fatalf("ledger row has %d fields, expected %d: %v", len(r), dbObjLedgerCols, r)
		}
		ledger[r[1]] = r
	}
	if len(reach) != len(ledger) {
		t.Fatalf("reachability ledger has %d rows, gap ledger has %d", len(reach), len(ledger))
	}
	for name, rr := range reach {
		gr, ok := ledger[name]
		if !ok {
			t.Errorf("index %q is in the reachability ledger but not in the gap ledger", name)
			continue
		}
		var wantMissing []string
		for i, env := range envs {
			if gr[3+i] == "missing" {
				wantMissing = append(wantMissing, env)
			}
		}
		if rr[6] != strings.Join(wantMissing, ",") {
			t.Errorf("index %q: reachability missing_envs=%q but gap ledger says %v",
				name, rr[6], wantMissing)
		}
	}
}

// ─────────────────────── G11：可达性棘轮（★ 只降不升） ───────────────────────

func TestInstallerReachabilityRatchet(t *testing.T) {
	counts := map[string]int{}
	for _, r := range dbObjDataLines(t, dbObjRepoPath(t, dbObjReachRel)) {
		if len(r) != dbObjReachCols {
			t.Fatalf("reachability row malformed: %v", r)
		}
		counts[r[8]]++
	}
	for sev, baseline := range dbObjReachRatchet {
		if counts[sev] > baseline {
			t.Errorf("★ reachability ratchet broken: %s has %d, baseline %d (★ 只许下降)",
				sev, counts[sev], baseline)
		}
	}
	// ★★ 阳性对照：★ 必须【确实】有「新装必然缺」的行 ⇒ ★ 否则棘轮是空转的
	if counts["new-install-gap"] == 0 {
		t.Error("reachability ledger has zero new-install-gap rows ⇒ the gate asserts nothing")
	}
	// ★★ 阳性对照：★ 「基线依赖」也必须非零 ⇒ ★★ 那 342 条是本轮的核心发现
	if counts["baseline-dependent"] == 0 {
		t.Error("reachability ledger has zero baseline-dependent rows ⇒ the 'baseline is a dump' finding is gone")
	}
	// ★★ 反向对照：★ 「安装器自己有 bug」必须为 0 ⇒ ★★ 有就是红
	if counts["installer-bug"] != 0 {
		t.Errorf("★ %d indexes are reachable but missing from a fresh install ⇒ the installer itself is broken",
			counts["installer-bug"])
	}
	t.Logf("reachability counts: %v (baseline %v)", counts, dbObjReachRatchet)
}

// ─────────────────────── G12：★「不可达 ⇒ 新装必然缺」必须真的成立 ───────────────────────

func TestUnreachableCreatorMeansFreshInstallLacksIt(t *testing.T) {
	rows := dbObjDataLines(t, dbObjRepoPath(t, dbObjReachRel))
	checked := 0
	for _, r := range rows {
		if len(r) != dbObjReachCols {
			t.Fatalf("reachability row malformed: %v", r)
		}
		if r[7] != "unreachable-and-missing" {
			continue
		}
		checked++
		// ★★ 不可达 ⇒ ★ 它的创建迁移必然不在【安装器清单】里 ⇒ ★ 复核一次
		if r[4] != "no" {
			t.Errorf("index %q is unreachable-and-missing but creator_reachable=%s", r[0], r[4])
		}
		// ★★ 而它【一定】在新装缺 ⇒ ★ fresh 必须落在 missing_envs 里
		if !strings.Contains(","+r[6]+",", ",fresh,") {
			t.Errorf("index %q is unreachable-and-missing but fresh is not in missing_envs=%q", r[0], r[6])
		}
	}
	if checked == 0 {
		t.Error("no unreachable-and-missing rows ⇒ the 'fresh install necessarily lacks it' claim asserts nothing")
	}
	t.Logf("unreachable-and-missing rows: %d", checked)
}

// ─────────────────────── G13：★「基线依赖」必须真的靠基线 ───────────────────────

func TestBaselineDependentIndexesAreNotReachable(t *testing.T) {
	rows := dbObjDataLines(t, dbObjRepoPath(t, dbObjReachRel))
	baseline := 0
	for _, r := range rows {
		if len(r) != dbObjReachCols {
			t.Fatalf("reachability row malformed: %v", r)
		}
		if r[7] != "baseline-dependent" {
			continue
		}
		baseline++
		if r[4] != "no" {
			t.Errorf("index %q is baseline-dependent but creator_reachable=%s (★ 那它就不是靠基线)", r[0], r[4])
		}
		if !strings.Contains(","+r[5]+",", ",fresh,") {
			t.Errorf("index %q is baseline-dependent but fresh is not in present_envs=%q", r[0], r[5])
		}
	}
	if baseline == 0 {
		t.Error("zero baseline-dependent rows ⇒ the 'the baseline is a dump' finding is gone")
	}
	t.Logf("baseline-dependent rows: %d", baseline)
}

// ─────────────────────── G14：审计档必须引用可达性读数 ───────────────────────

func TestInstallerReachabilityAuditDocQuotesTheLedger(t *testing.T) {
	doc := dbObjRepoPath(t, dbObjReachAuditRel)
	raw, err := os.ReadFile(doc)
	if err != nil {
		t.Fatalf("read %s: %v", doc, err)
	}
	text := string(raw)
	counts := map[string]int{}
	for _, r := range dbObjDataLines(t, dbObjRepoPath(t, dbObjReachRel)) {
		counts[r[8]]++
	}
	// ★ 审计档必须写出每个 severity 的条数（★ 数字与台账一致 ⇒ 文档不会悄悄过期）
	for _, sev := range []string{"baseline-dependent", "new-install-gap", "prod-gap", "installer-bug"} {
		needle := sev + "=" + strconv.Itoa(counts[sev])
		if !strings.Contains(text, needle) {
			t.Errorf("audit doc does not quote %q (★ numbers must match the ledger)", needle)
		}
	}
	// ★★ 而且必须写出「基线依赖」的占比 ⇒ ★ 那是本轮最重的一条结论
	if !strings.Contains(text, "baseline-dependent") {
		t.Error("audit doc does not mention baseline-dependent")
	}
}
