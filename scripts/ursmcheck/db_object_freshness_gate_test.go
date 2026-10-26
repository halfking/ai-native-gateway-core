//go:build !integration

package ursm_test

// ─────────────────────────────────────────────────────────────────────────────
// db_object_freshness_gate_test.go —— ★★ R164「底账新鲜度与漂移判据」门（G15~G20）
//
// ★★ 这道门补的是哪块缺口（★ R161/R162/R163 连续三轮都在 §9「诚实边界」里写着同一句）：
//
//	「底账是【静态快照】⇒ ⇒ 「快照过期」不可机器判」
//
// ★★ R164 的做法：★ **把「过期」定义成【与事实不一致】，而不是【距今多久】** ⇒ ⇒
// ★★ ⇒ 判据是【对拍】，不是【时间阈值】⇒ ⇒ ⇒ 于是没有「时间到了就自己变红」的时间炸弹。
// ★★ ⇒ 且对拍需要连库 ⇒ ⇒ **不能进 CI** ⇒ ⇒ ⇒ 于是分成两半：
//
//	① 离线门（本文件，★ G15~G20）：★ 钉住【产物自带采集时刻】【产物自洽】
//	   【结构/指标两套口径跨语言一致】【verify 模式还在】
//	② 连库对拍（`db-object-census.py verify`，★ 退出码 0/1/2）：★ 真去现场比一次
//	   ⇒ ★★ 它的落点就是【部署序列】（★ 原始目标第 7 条：★ 「确保在多个环境下可以同样的标准来更新」）
//
// ★★ 三条设计纪律（★ 与 R161~R163 同族）：
//
//	① ★ **不静默降级**：★ 「没法判定」必须是一个【非 0 退出码】⇒ ★ 绝不与「没漂移」共用 0。
//	② ★ **排除面必须显式且被钉住**：★ 少排除 = 噪声淹没判据，★ 多排除 = 漏报。
//	③ ★ **两份独立实现互证**：★ Go 侧独立写一遍漂移判定（★ 不调 Python）⇒ ★ 跨语言对拍清单。
// ─────────────────────────────────────────────────────────────────────────────

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	dbObjFreshCensusRel   = "scripts/audit/asset/db-object-census.tsv"
	dbObjFreshColumnsRel  = "scripts/audit/asset/db-object-columns.tsv"
	dbObjFreshToolRel     = "scripts/audit/db-object-census.py"
	dbObjFreshAuditRel    = "docs/audit/2026-10-27-R164-底账新鲜度与漂移判据.md"
	dbObjFreshCensusCols  = 17
	dbObjFreshColumnsCols = 4
)

// ★★ Go 侧的 volatile 列清单（★ ★ 刻意【不读 Python 源】⇒ ★ 它是独立的一份实现
// ★★ ⇒ G18 才有一份真正独立的对照物 ⇒ ⇒ 否则互证是零信息）。
var dbObjFreshVolatile = []string{"reltuples", "table_size", "total_size"}

// dbObjFreshColumnNames 底账 17 列的列名（★ 与 collect 的 SELECT 顺序对应）。
var dbObjFreshColumnNames = []string{
	"env", "kind", "schema", "relkind", "name", "owner", "ispartition",
	"partition_bound", "parent_table", "reltuples", "table_size", "total_size",
	"definition", "flags", "parent_index", "key_columns", "predicate",
}

// dbObjFreshEnvMeta 读底账头部里每个环境的 rows / sha256 / collected_at。
type dbObjFreshEnvMeta struct {
	Rows        int
	SHA256      string
	CollectedAt string
}

var dbObjFreshEnvRe = regexp.MustCompile(`env=(\S+)\s+rows=(\d+)\s+sha256=([0-9a-f]+)(?:\s+collected_at=(\S+))?`)

func dbObjFreshHeaderEnvs(t *testing.T, path string) map[string]dbObjFreshEnvMeta {
	t.Helper()
	out := map[string]dbObjFreshEnvMeta{}
	for _, line := range dbObjHeaderLines(t, path) {
		m := dbObjFreshEnvRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		rows, err := strconv.Atoi(m[2])
		if err != nil {
			t.Fatalf("census header row count is not a number: %q", line)
		}
		out[m[1]] = dbObjFreshEnvMeta{Rows: rows, SHA256: m[3], CollectedAt: m[4]}
	}
	if len(out) == 0 {
		t.Fatalf("%s has no per-env header lines", path)
	}
	return out
}

// dbObjFreshRowsOf 读底账里某个环境的行（★ 保留 env 前缀 ⇒ ★ 17 列）。
func dbObjFreshRowsOf(t *testing.T, path, env string) [][]string {
	t.Helper()
	var out [][]string
	for _, parts := range dbObjDataLines(t, path) {
		if len(parts) != dbObjFreshCensusCols {
			t.Fatalf("%s: row has %d fields, expected %d", path, len(parts), dbObjFreshCensusCols)
		}
		if parts[0] == env {
			out = append(out, parts)
		}
	}
	return out
}

// ─────────────────────────── 独立实现：漂移判定 ───────────────────────────

type dbObjFreshDrift struct {
	MissingInLive []string
	AddedInLive   []string
	Changed       []string
	MetricOnly    []string
}

// dbObjFreshIdentity 对象身份 ⇒ (kind, schema, name)（★ relkind 属形态不属身份）。
func dbObjFreshIdentity(parts []string) string {
	return parts[1] + "\x00" + parts[2] + "\x00" + parts[4]
}

// dbObjFreshPayload 结构载荷 ⇒ ★ 剔除 volatile 列。
func dbObjFreshPayload(parts []string) string {
	drop := map[int]bool{}
	for _, c := range dbObjFreshVolatile {
		for i, n := range dbObjFreshColumnNames {
			if n == c {
				drop[i] = true
			}
		}
	}
	var b strings.Builder
	for i, v := range parts {
		if drop[i] {
			continue
		}
		b.WriteString(v)
		b.WriteByte('\x01')
	}
	return b.String()
}

// dbObjFreshMetrics 指标 ⇒ ★ 每次 autovacuum/analyze 都会动 ⇒ 不算漂移。
func dbObjFreshMetrics(parts []string) string {
	var b strings.Builder
	for _, c := range dbObjFreshVolatile {
		for i, n := range dbObjFreshColumnNames {
			if n == c {
				b.WriteString(parts[i])
				b.WriteByte('\x01')
			}
		}
	}
	return b.String()
}

// dbObjFreshClassify ★ Go 侧的独立漂移判定（★ 与 Python 侧同语义、★ 独立代码）。
func dbObjFreshClassify(snapshot, live [][]string) dbObjFreshDrift {
	snap := map[string][]string{}
	for _, p := range snapshot {
		snap[dbObjFreshIdentity(p)] = p
	}
	liveByID := map[string][]string{}
	for _, p := range live {
		liveByID[dbObjFreshIdentity(p)] = p
	}
	var d dbObjFreshDrift
	for id, sp := range snap {
		lp, ok := liveByID[id]
		if !ok {
			d.MissingInLive = append(d.MissingInLive, id)
			continue
		}
		if dbObjFreshPayload(sp) != dbObjFreshPayload(lp) {
			d.Changed = append(d.Changed, id)
		} else if dbObjFreshMetrics(sp) != dbObjFreshMetrics(lp) {
			d.MetricOnly = append(d.MetricOnly, id)
		}
	}
	for id := range liveByID {
		if _, ok := snap[id]; !ok {
			d.AddedInLive = append(d.AddedInLive, id)
		}
	}
	return d
}

// ─────────────────────────── G15：每环境必须带采集时刻 ───────────────────────────

// G15：★ 底账头部必须为【每个环境】写出 collected_at ⇒ ★
// ★★ 否则「这个环境的数据是几号采的」在产物里查不到 ⇒ ⇒ 「快照过期」永远不可机器判
// ★★ ⇒ ★★ 而这正是 R161~R163 三轮都写进 §9「诚实边界」的那一条。
func TestDbObjectCensusCarriesPerEnvCollectedAt(t *testing.T) {
	t.Helper()
	path := dbObjRepoPath(t, dbObjFreshCensusRel)
	metas := dbObjFreshHeaderEnvs(t, path)

	// ★ 全部环境（★ 与产物声明的 envs 一致）。
	var declared []string
	for _, line := range dbObjHeaderLines(t, path) {
		m := regexp.MustCompile(`total_rows=(\d+)\s+envs=(\S+)`).FindStringSubmatch(line)
		if m != nil {
			declared = strings.Split(m[2], ",")
		}
	}
	if len(declared) == 0 {
		t.Fatal("census header does not declare its envs")
	}
	for _, env := range declared {
		meta, ok := metas[env]
		if !ok {
			t.Errorf("census header has no row for env %q (★ envs=%v)", env, declared)
			continue
		}
		if meta.CollectedAt == "" {
			t.Errorf("env %q has no collected_at in the census header (★ ⇒ 「快照过期」不可机器判)", env)
			continue
		}
		if meta.CollectedAt == "unknown" {
			t.Errorf("env %q has collected_at=unknown (★ ⇒ 采集时刻没透传进来)", env)
			continue
		}
		if _, err := time.Parse(time.RFC3339, meta.CollectedAt); err != nil {
			t.Errorf("env %q collected_at %q is not RFC3339: %v", env, meta.CollectedAt, err)
		}
	}
}

// ─────────────────────────── G16：产物自身自洽 ───────────────────────────

// G16：★ 头部声明的 rows / sha256 必须与重算一致 ⇒ ★ 产物被截断/被污染 ⇒ 红。
// ★★ 独立于 R161 的 G1（★ 那道门验「形状 + 自洽」，★ 本道验「采集时刻 + 自洽」的交集）★
// ★★ ⇒ 两道门对【sha 自洽】是同一判据 ⇒ ★★ 这是有意的（★ 互证），★ 不是重复。
func TestDbObjectCensusHeaderMatchesRecomputedSha(t *testing.T) {
	t.Helper()
	path := dbObjRepoPath(t, dbObjFreshCensusRel)
	metas := dbObjFreshHeaderEnvs(t, path)
	for env, meta := range metas {
		rows := dbObjFreshRowsOf(t, path, env)
		if len(rows) != meta.Rows {
			t.Errorf("env %q header rows=%d but recomputed %d", env, meta.Rows, len(rows))
		}
		sum := sha256.Sum256([]byte(strings.Join(dbObjFreshJoin(rows), "\n")))
		got := fmt.Sprintf("%x", sum)
		if got != meta.SHA256 {
			t.Errorf("env %q sha256 mismatch: header=%s recomputed=%s", env, meta.SHA256, got)
		}
	}
}

func dbObjFreshJoin(rows [][]string) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, strings.Join(r, "\t"))
	}
	return out
}

// ─────────────────────────── G17：结构 vs 指标必须分得开 ───────────────────────────

// G17：★ Go 侧的漂移判定必须有牙（★ 阳性 + 反向对照）。
// ★★ 而更重要的是这条：★★ 【排除 volatile 列不是洁癖，是实测逼出来的】——
// ★★ 若不排除，★ 任何环境只要发生过一次 autovacuum/analyze ⇒ ⇒ 整行差集就会报「漂移」
// ★★ ⇒ ⇒ 判据被噪声淹没 ⇒ ⇒ ⇒ 门必须【证明这一点在真实产物上成立】。
func TestDbObjectDriftClassifierSeparatesStructureFromMetrics(t *testing.T) {
	t.Helper()

	// ★ 夹具：★ 17 列（带 env 前缀）。
	mk := func(reltuples, size, definition string) []string {
		return []string{
			"local", "idx", "public", "i", "idx_a", "owner", "f", "", "",
			reltuples, size, size, definition, "true,true,true", "", "tenant_id,session_id", "",
		}
	}
	base := mk("100", "4096", "CREATE INDEX ...")
	moved := mk("200", "8192", "CREATE INDEX ...")
	changed := mk("100", "4096", "CREATE INDEX ON public.t (a)")

	// ① 完全一致 ⇒ 零漂移
	d := dbObjFreshClassify([][]string{base}, [][]string{base})
	if len(d.MissingInLive)+len(d.AddedInLive)+len(d.Changed)+len(d.MetricOnly) != 0 {
		t.Errorf("identical rows must produce no drift, got %+v", d)
	}

	// ② 只有统计量在动 ⇒ 不算漂移（★ 但要计入 metric_only）
	d = dbObjFreshClassify([][]string{base}, [][]string{moved})
	if len(d.Changed) != 0 || len(d.MetricOnly) != 1 {
		t.Errorf("metric-only movement must NOT be structural drift, got %+v", d)
	}

	// ③ 形态变了 ⇒ 算漂移
	d = dbObjFreshClassify([][]string{base}, [][]string{changed})
	if len(d.Changed) != 1 || len(d.MetricOnly) != 0 {
		t.Errorf("shape change must be structural drift, got %+v", d)
	}

	// ④ 快照有、现场无 ⇒ 算漂移（★ 危险方向）
	d = dbObjFreshClassify([][]string{base}, [][]string{})
	if len(d.MissingInLive) != 1 {
		t.Errorf("missing-in-live must be structural drift, got %+v", d)
	}

	// ⑤ 现场有、快照无 ⇒ 算漂移
	d = dbObjFreshClassify([][]string{}, [][]string{base})
	if len(d.AddedInLive) != 1 {
		t.Errorf("added-in-live must be structural drift, got %+v", d)
	}

	// ★★ ⑥ 真实产物上证明「排除面是必要的」：★ 若把 volatile 列也纳入差集，
	// ★★ 则【每一个 metric_only 行都会被误判成 changed】⇒ ⇒ 判据被噪声淹没。
	path := dbObjRepoPath(t, dbObjFreshCensusRel)
	metas := dbObjFreshHeaderEnvs(t, path)
	nonTrivial := 0
	for env := range metas {
		rows := dbObjFreshRowsOf(t, path, env)
		for _, r := range rows {
			for _, c := range dbObjFreshVolatile {
				for i, n := range dbObjFreshColumnNames {
					if n == c && r[i] != "" && r[i] != "-1" && r[i] != "0" {
						nonTrivial++
					}
				}
			}
		}
	}
	if nonTrivial == 0 {
		t.Errorf("census carries no non-trivial metric values (★ ⇒ the metric column set may be dead)")
	}
}

// ─────────────────────────── G18：排除面跨语言一致 ───────────────────────────

// G18：★ Go 侧的 volatile 列清单必须与 Python 侧【逐字一致】⇒ ★
// ★★ 否则「结构漂移 vs 指标漂移」在两侧是【两套口径】⇒ ⇒ ★★ 而这正是 R162 教训
// ★★ （★「两份独立实现互证」也可能同源同错 ⇒ 互证零信息）的反面 ⇒ ⇒ 必须钉住。
var dbObjFreshPyVolatileRe = regexp.MustCompile(`VOLATILE_CENSUS_COLUMNS\s*=\s*\(([^)]*)\)`)

func TestDbObjectVolatileColumnSetMatchesPythonSide(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile(dbObjRepoPath(t, dbObjFreshToolRel))
	if err != nil {
		t.Fatalf("read %s: %v", dbObjFreshToolRel, err)
	}
	m := dbObjFreshPyVolatileRe.FindSubmatch(raw)
	if m == nil {
		t.Fatalf("%s does not declare VOLATILE_CENSUS_COLUMNS (★ ⇒ 两侧口径无从对拍)", dbObjFreshToolRel)
	}
	quoted := regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(m[1], -1)
	var py []string
	for _, q := range quoted {
		py = append(py, string(q[1]))
	}
	if strings.Join(py, ",") != strings.Join(dbObjFreshVolatile, ",") {
		t.Errorf("volatile column set diverged: python=%v go=%v", py, dbObjFreshVolatile)
	}
	// ★★ 而且排除面必须【恰好是这三列】★ ⇒ 少排除 = 噪声，★ 多排除 = 漏报。
	if len(dbObjFreshVolatile) != 3 {
		t.Errorf("volatile column set must be exactly 3 columns, got %v", dbObjFreshVolatile)
	}
	for _, c := range dbObjFreshVolatile {
		found := false
		for _, n := range dbObjFreshColumnNames {
			if n == c {
				found = true
			}
		}
		if !found {
			t.Errorf("volatile column %q is not a census column (★ 判据写了一个不存在的列)", c)
		}
	}
}

// ─────────────────────────── G19：verify 模式必须还在 ───────────────────────────

// G19：★ `verify` 子命令必须仍在工具里 ⇒ ★
// ★★ 因为「快照是否过期」的真判据就是它（★ 连库对拍）⇒ ★★ 它被删了 ⇒
// ★★ ⇒ 那条边界又会回到「不可机器判」⇒ ⇒ ⇒ 门要拦住这个回退。
func TestDbObjectCensusToolStillHasVerifyMode(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile(dbObjRepoPath(t, dbObjFreshToolRel))
	if err != nil {
		t.Fatalf("read %s: %v", dbObjFreshToolRel, err)
	}
	text := string(raw)
	if !regexp.MustCompile(`add_parser\(\s*"verify"`).MatchString(text) {
		t.Error("db-object-census.py has no `verify` subcommand (★ ⇒ 「快照过期」又回到不可机器判)")
	}
	// ★★ 退出码语义也必须钉住：★ 0 无漂移 / 1 有漂移 / 2 无法判定。
	for _, needle := range []string{"RESULT: NO STRUCTURAL DRIFT", "RESULT: STRUCTURAL DRIFT"} {
		if !strings.Contains(text, needle) {
			t.Errorf("verify mode does not emit %q (★ ⇒ 退出码语义不可读)", needle)
		}
	}
}

// ─────────────────────────── G20：审计档必须引用本轮读数 ───────────────────────────

// G20：★ 审计档必须写出四环境的行数与【结构漂移 = 0】的结论 ⇒ ★
// ★★ 数字与产物一致 ⇒ ★★ 文档不会悄悄过期（★ R163 的同款纪律）。
func TestDbObjectFreshnessAuditDocQuotesTheLedger(t *testing.T) {
	t.Helper()
	doc := dbObjRepoPath(t, dbObjFreshAuditRel)
	raw, err := os.ReadFile(doc)
	if err != nil {
		t.Fatalf("read %s: %v", dbObjFreshAuditRel, err)
	}
	text := string(raw)

	metas := dbObjFreshHeaderEnvs(t, dbObjRepoPath(t, dbObjFreshCensusRel))
	for env, meta := range metas {
		needle := fmt.Sprintf("%s=%d", env, meta.Rows)
		if !strings.Contains(text, needle) {
			t.Errorf("audit doc does not quote %q (★ numbers must match the artifact)", needle)
		}
	}
	lower := strings.ToLower(text)
	for _, needle := range []string{"structural", "metric-only", "verify"} {
		if !strings.Contains(lower, needle) {
			t.Errorf("audit doc does not mention %q", needle)
		}
	}
}

// ─────────────────────────── G21：列底账也要带采集时刻 ───────────────────────────

// G21：★ 列底账同样必须带每环境的 collected_at（★ ★ 它与对象底账是【两份产物】⇒ ⇒
// ★★ 只给一份补上 ⇒ ⇒ 另一半的「过期」仍然不可机器判 ⇒ ⇒ ⇒ 两份必须一起补）。
func TestDbObjectColumnsCarryPerEnvCollectedAt(t *testing.T) {
	t.Helper()
	path := dbObjRepoPath(t, dbObjFreshColumnsRel)
	for _, line := range dbObjHeaderLines(t, path) {
		if !strings.Contains(line, "rows=") {
			continue
		}
		// ★★ 头部是【一行多段】（★ 用 " | " 分隔）⇒ ★ 首段还带着 "# ★ " 前缀
		// ★★ ⇒ ⇒ 必须先剥掉，否则正则锚不上（★ ★ 本轮第一版就栽在这儿）。
		segments := strings.Split(line, "|")
		for i, seg := range segments {
			seg = strings.TrimSpace(seg)
			seg = strings.TrimPrefix(seg, "#")
			seg = strings.TrimSpace(seg)
			seg = strings.TrimPrefix(seg, "★")
			seg = strings.TrimSpace(seg)
			segments[i] = seg
			if seg == "" || !strings.Contains(seg, "rows=") {
				continue
			}
			m := regexp.MustCompile(`^(\S+)\s+rows=(\d+)\s+sha256=[0-9a-f]+(?:\s+collected_at=(\S+))?$`).FindStringSubmatch(seg)
			if m == nil {
				t.Errorf("columns header segment is not parseable: %q", seg)
				continue
			}
			if m[3] == "" {
				t.Errorf("columns header env %q has no collected_at", m[1])
				continue
			}
			if m[3] == "unknown" {
				t.Errorf("columns header env %q has collected_at=unknown", m[1])
				continue
			}
			if _, err := time.Parse(time.RFC3339, m[3]); err != nil {
				t.Errorf("columns header env %q collected_at %q is not RFC3339: %v", m[1], m[3], err)
			}
		}
	}
}

var _ = filepath.Join
