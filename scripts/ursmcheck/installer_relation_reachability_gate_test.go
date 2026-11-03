package ursm_test

// installer_relation_reachability_gate_test.go
//
// ★★★ 关系级（新装）可达性门 —— ★★ 补 R163 索引级台账的【单位盲区】。
//
// # 这道门回答的问题
//
// 「**一张表 / 一个视图 / 一个物化视图**，★ 新装环境为什么会有它？」
//
// ★★ R163 立的可达性台账（scripts/audit/asset/installer-reachability.tsv）
// ★★ 回答的是同一个问题，★ 但它的【单位是索引】⇒ ⇒ **关系层没有任何人管**。
//
// # ★★★ 为什么这是个洞（★ 不是「理论上可能」，★ 是实测出来的）
//
// ① ★★ 台账的判定函数 classify()（scripts/audit/db-object-census.py:918-923）写着：
//
//	kind := env_state["rels"].get(table)
//	if kind == nil { return "retired" }
//	if kind not in ("r", "p") { return "retired" }
//
//	★★ 「**这张表在本环境里不存在**」⇒ ⇒ 索引被判成 `retired`
//	★★ ⇒ 上层 verdict 走 `reachable-and-legitimately-retired` / severity=`none`
//	★★ ⇒ ★★ 「新装环境里这张表压根没建」被洗成了「索引合法退役」。
//
// ② ★★ 但「表不存在」有两种完全相反的含义，★ 台账分不出来：
//
//	(a) 该表在 canonical 链里被 DROP 了 ⇒ 索引合法退役 ⇒ ✔ 不是缺口
//	(b) ★★ 该表【本该】被装出来（★ prod 上还在、★ 还有数据）★ 而新装链没装
//	    ⇒ ★★ 这就是新装差异
//
//	★★ 台账只问「**索引**在不在」，★ 从不问「**prod 里那张表还在不在**」
//	★★ ⇒ (b) 这一类在仓内**完全不可见**。
//
// # ★★★ 实测（★ 本门自证，★ 数字来自 census 的四环境事实底账）
//
//	关系集合取自 db-object-expect.tsv 的 table 列（★ 256 张，★ 由已被信任的
//	★ expect 解析器产出 ⇒ ★ 不引入第二套 SQL 解析器）
//	⇒ 「prod(252∪211) 有」且「基线 ∪ 当前 334 条登记文件都不创建」⇒ ★ 4 个候选：
//
//	session_owners        （表，★ 358_session_ownership.sql 建；★ 211 有 2.36M 行）
//	session_owner_stats   （物化视图，★ 358_session_ownership.sql 建；★ 211 有）
//	request_abandoned     （表，★ 819 建；★ 252 ＋ 211 有）
//	free_resource_catalog （表，★ ★ 无任何迁移创建它；★ 252 ＋ 211 ＋ local 有）
//
//	★★★ ★★ 而这 4 个候选的【定性翻了两次】（★ ★★ 全部见审计档 §28.32.5）：
//	★★★
//	★★★ · 版本一：「★ `session_owners` 与 `session_owner_stats` 是【真缺口】、
//	★★★   ★ 根因是迁移 358 未登记进安装器链」
//	★★★   ⇒ ★★ 被下面三处【独立记载】推翻（★ ★★ 都不是本门写的）：
//	★★★     ★ admin/user_profile.go:327-329：「生产共享库缺 session_owners
//	★★★       （★ 迁移 358 的版本号低于部署通道的账本对账下限 412
//	★★★       ⇒ ★★ 存量库永远拿不到它）⇒ 基础聚合直接 42P01」
//	★★★     ★ scripts/deploy-lib/db-changelog.sh:285：投递判据是
//	★★★       `if (( 10#$ver >= ledger_reconcile_from ))` ⇒ ★★ 358 < 412 ⇒ 不投递
//	★★★     ★ 563_session_summary_trigger_on_hot.sql 的头注释：「session_owners
//	★★★       在 154 不存在，★ 跳过以免回滚热写入」
//	★★★ · 版本二：「★ 4 个候选【全部】是【有意的豁免】⇒ ⇒ 真·缺口 = 0」
//	★★★   ⇒ ★★★ 【★ 这句话也错，★ 而且错得更隐蔽 ★】★★★ ⇒ ★★★ 被【台账快照】
//	★★★   ★★★ 推翻：★ db-changelog-ledger-snapshot.tsv 里
//	★★★   ★★★ `211 358 358_session_ownership.sql 2026-10-12 01:27:02`
//	★★★   ★★★ ⇒ ★★★ 【358 在 211 上【确实被应用过】】★ ★★ 而下限 412 引入于
//	★★★   ★★★ 2026-07-17（★ 5ce49a708）⇒ ★★ ⇒ ★★ 「prod 上没有它」是假的
//	★★★   ★★★ ⇒ ★★★ 精确归因是 `below-floor-but-applied`（★ 机器可判）
//	★★★ · ⇒ ⇒ ★★★ **最终**：★ ★★ 真·【对象终态】差异 = 2（★ `session_owners` 族）
//	★★★   ★★★ ⇒ ★★ 真·【能力】缺口 = 0（★ admin 侧有降级路径、★ 物化视图无人引用）
//
//	★★ 而 admin/user_profile.go:193 的注释称「session_owners 属于可选聚合表
//	★★（★ 安装器迁移 563/683 建）」⇒ ★★ 563/683 【都不建它】
//	★★ ⇒ ★★ 那句出处是假的 ⇒ ★★ 这解释了为什么它长期没被当成差异。
//
// # ★★ 这道门守的是「差异必须【登记】」⇒ 而不是「差异必须为零」
//
//	★★ 这 4 条候选【全部】是【已归因的登记项】（★ 2 条真差异 ＋ 2 条有意豁免）
//	★★ ⇒ ★★ 而「★ 哪些对象是故意不给新装环境的、★ 哪些是【两条通道边界不一致】
//	★★ 造成的」从此【机器可判】⇒ ★★ 这才是本轮的产出。
//	★★ ⇒ ★★ 本门要求：★★ 每一个关系层差异都必须【显式登记在豁免清单里】，
//	★★ 且【豁免数只许减少】（★ 棘轮）⇒ ★ 差异被修掉后，★ 清单必须跟着收缩。
//
// # ★★ 判据（★ 每一步都可机器判）
//
//	① 关系集合       ← db-object-expect.tsv 的 table 列（★ 256 张）
//	② prod 事实      ← db-object-census.tsv（★ env=252 / env=211 的 rel 行）
//	③ 新装覆盖面     ← 基线 01-schema.sql ∪ 当前 334 条登记文件
//	                   （★ 两者都按【内容】判定「谁创建了它」⇒ ★ 不靠迁移号代理）
//	④ 差异 = ② 有 ∧ ③ 没有 ⇒ ★ 必须落在豁免清单里
//	⑤ 豁免清单里每一条必须【真的是差异】⇒ ★ 清单不许腐化成摆设
//	⑥ 豁免的【分类】必须被机器核对（★ 理由是人写的、★ 分类必须机器验）
//
//	★★★ ⚠ 判据 ② 只用 prod 侧（252 / 211）⇒ ★★ 【不依赖 census 的 fresh 快照】
//	★★★ ⇒ ★★ 「快照滞后」（★ 本轮实测：outbox_events 在 fresh 快照里缺、★ 而
//	★★★ ★★ 当前链已由 932 补上）⇒ ★★ 只会让门【少报】、★★ 不会让门【误报】。
//
// # ★★ 为什么不靠「创建迁移有没有登记」来判
//
//	★ 实测踩过：★ 用「创建迁移 ∈ 登记清单」当代理判据，★ 会同时产生
//	★★ 假阳性与假阴性 ——
//	  ★★ 假阴性：session_turns_hot（★ 创建迁移 526 未登记，★ 但基线侧的
//	    ★ bootstrap 文件供它）★ ⇒ 按代理判据会误报缺口
//	  ★★ 假阳性：goal_runs / goal_run_actions / goal_run_steps
//	    （★ 创建迁移 549 未登记、★ 但 554 另建了它们）★ ⇒ 同上
//	⇒ ★⇒ 代理判据两头都错 ⇒ ★⇒ 只能用【内容口径】。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	// dbObjRelBaselineRel 新装基线 dump（★ 安装器 00-prereqs → 01-schema → 02-seed 里的 01-schema）。
	dbObjRelBaselineRel = "installer/cmd/llm-gw-installer/embeddata/01-schema.sql"

	// dbObjRelLedgerRel 迁移台账快照（★ 「哪条迁移在哪个环境被应用过」的证据源）。
	//
	// ★★ 为什么必须有它：★★ 「创建迁移的版本号低于部署通道下限」**不足以**推出
	// ★★ 「prod 上也没有它」——★ 实测台账里有 **12 条**低于 412 的迁移
	// ★★ 在 211 上被应用过（★ 032/044/351/357/358/361/375/376/387/392/394/407）
	// ★★ ⇒ ⇒ ★★ 「低于下限」≠「没被应用」⇒ ⇒ ★★ 归因必须多问一句：
	// ★★ 【它到底有没有被应用过？】
	//
	// ★★ ⚠ 按【迁移号】匹配，★ 不按文件名（★ 实测同号不同名：★ 台账记的是
	// ★★ `032_session_tenant_binding.sql`、★ 而 canonical 里是 `032_request_wal.sql`）。
	dbObjRelLedgerRel = "scripts/audit/asset/db-changelog-ledger-snapshot.tsv"
)

// dbObjRelCreateRe 从一段 SQL 里抓「创建了哪个关系」。
//
// ★ 口径与 db-object-census.py 的 expect 侧保持一致（★ 行注释先剥掉），
// ★ 但★ 【对象类型不同】⇒ ⇒ 与 expect 的索引解析器不是同一套东西。
// ★ 已知边界（★ 注释里写清楚，★ 免得后来人以为它是完备解析器）：
//   - ★ 「CREATE TABLE x PARTITION OF y」这类没有列清单的形态匹配不到（★ 少报）
//   - ★ 动态 SQL（DO $$ … EXECUTE format('CREATE TABLE %I …')）匹配不到（★ 少报）
//     ★★ 少报方向是安全的：★ 本门要抓的是「★ 少创建」⇒ 少报只会让豁免清单
//     ★★ 多几行，★ 不会漏掉真正的缺口（★ 而漏掉才是危险方向）。
var dbObjRelCreateRe = regexp.MustCompile(`(?i)CREATE\s+(?:OR\s+REPLACE\s+)?(?:UNLOGGED\s+)?(?:TEMP(?:ORARY)?\s+)?(?:TABLE|VIEW|MATERIALIZED\s+VIEW)\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:[a-zA-Z_][\w$]*\.)?"?([a-zA-Z_]\w*)"?\s*(?:\(|AS\b)`)

// dbObjRelLineCommentRe 行注释（★ 与 db-object-census.py 的 RE_LINE_COMMENT 同口径）。
var dbObjRelLineCommentRe = regexp.MustCompile(`--[^\n]*`)

// dbObjRelCreatedIn 报告这段 SQL 是不是创建了 name 这个关系。
func dbObjRelCreatedIn(sqlText, name string) bool {
	for _, got := range dbObjRelCreatedNames(sqlText) {
		if got == name {
			return true
		}
	}
	return false
}

// dbObjRelCreatedNames 抽出这段 SQL 创建的所有关系名（★ 不带 schema 前缀）。
func dbObjRelCreatedNames(sqlText string) []string {
	body := dbObjRelLineCommentRe.ReplaceAllString(sqlText, "")
	out := []string{}
	for _, m := range dbObjRelCreateRe.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

// dbObjRelExpectTables 读 db-object-expect.tsv ⇒ 关系名 → 创建它的迁移文件。
//
// ★ 为什么用 expect 的 table 列当「关系集合」：
// ★ expect 是从 canonical 迁移反解出来的、★ 且它自己的门（db_object_census_gate_test.go）
// ★ 已经在守 ⇒ ★ 它是仓内【已被信任】的关系/索引清单 ⇒ ★ 本门不必再写一套 SQL 解析器。
// ★★ 代价：★ 只覆盖「★ 有索引的关系」⇒ ★ 没有任何索引的关系不在集合里
// ★★ （★ 已知边界，★ 写在注释里，★ 不假装是全集）。
func dbObjRelExpectTables(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, r := range dbObjDataLines(t, dbObjRepoPath(t, dbObjExpectRel)) {
		if len(r) < 4 {
			t.Fatalf("expect row malformed: %v", r)
		}
		out[r[2]] = r[3]
	}
	if len(out) == 0 {
		t.Fatal("expect table set is empty ⇒ the relation verdict asserts nothing")
	}
	return out
}

// dbObjRelManifest 读安装器登记清单 ⇒ 文件名集合（★ 0-based apply-order）。
func dbObjRelManifest(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, r := range dbObjDataLines(t, dbObjRepoPath(t, dbObjManifestRel)) {
		if len(r) < 2 {
			t.Fatalf("manifest row malformed: %v", r)
		}
		out[r[1]] = true
	}
	if len(out) == 0 {
		t.Fatal("installer manifest is empty ⇒ the relation verdict asserts nothing")
	}
	return out
}

// dbObjRelBaselineText 读新装基线 dump（★ 只读一次 ⇒ ★ 256 张表不该读 256 遍）。
func dbObjRelBaselineText(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(dbObjRepoPath(t, dbObjRelBaselineRel))
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	return string(raw)
}

// dbObjRelCoveredSet 一次扫完「新装通道」的每一个文件 ⇒ 关系名 → 创建它的来源。
//
// ★★ 口径 = ★ 基线 dump ∪ ★ 当前登记清单里那 334 个文件（★ 逐条按内容判）。
// ★★ 注意：★ 登记清单里【每一条都要真的跑一遍内容】⇒ ⇒ 判据与安装器的
// ★★ 真实行为一致（★ 安装器只跑 StartupFiles 这 334 个文件）。
//
// ★★ 性能：★ 早期版本按「每个关系 × 每个文件」找 ⇒ 256×334 次读盘 ⇒ 11.9s
// ★★ ⇒ 改成【一次扫完全部文件、建成集合】⇒ ⇒ 同一份结果、几十毫秒。
func dbObjRelCoveredSet(t *testing.T, manifest map[string]bool) map[string]string {
	t.Helper()
	out := map[string]string{}

	baselineText := dbObjRelBaselineText(t)
	for _, n := range dbObjRelCreatedNames(baselineText) {
		if _, ok := out[n]; !ok {
			out[n] = "baseline:" + dbObjRelBaselineRel
		}
	}

	dir := dbObjRepoPath(t, dbObjPackageRel)
	names := make([]string, 0, len(manifest))
	for f := range manifest {
		names = append(names, f)
	}
	sort.Strings(names) // ★ 稳定顺序 ⇒ 报错时指名的文件是确定的
	for _, f := range names {
		raw, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("read registered file %s: %v", f, err)
		}
		for _, n := range dbObjRelCreatedNames(string(raw)) {
			if _, ok := out[n]; !ok {
				out[n] = "registered:" + f
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("the fresh-install channel creates nothing ⇒ the relation verdict asserts nothing")
	}
	return out
}

// dbObjRelDeployFloor 部署通道的【投递下限】。
//
// ★★ 这不是猜的：★ `scripts/deploy-lib/db-changelog.sh:253` 写着
// ★   `local ledger_reconcile_from=${DB_LEDGER_RECONCILE_FROM:-412}`
// ★ ⇒ ★★ 部署通道【只投递版本号 ≥ 该下限】的迁移 ⇒ ★★ 「低于下限」是一条
// ★★ 【机器可判】的归因：★ 部署链也不会建它 ⇒ 新装链不建它 = 与部署链一致。
//
// ★★ ⚠ 该值是【可配环境变量】（★ 见 docs/全面审计v3/2026-10-01/184 号报告
// ★★ 「阈值来自被审计对象自身配置」）⇒ ★ 若某台机器把它调高 ⇒ 这条归因的
// ★★ 前提就变了 ⇒ ★ 本门读同一个环境变量 ⇒ ★ 判定与被审计对象同源。
func dbObjRelDeployFloor(t *testing.T) int {
	t.Helper()
	if v := strings.TrimSpace(os.Getenv("DB_LEDGER_RECONCILE_FROM")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 412
}

// dbObjRelAppliedMigrations 读迁移台账快照 ⇒ 迁移号 ⇒ 是否被应用过。
//
// ★★ 口径：★ 只看「★ 有没有应用记录」，★ 不看「★ 在哪个环境」
// ★★ （★ 判据是「★ prod 上存在的东西，★ 它到底有没有被应用过」）。
// ★★ ★★ 快照本身会过期（★ 本文件头部自己写着「★ 快照会过期 ⇒ ★ 用 collect 重采」）
// ★★ ⇒ ★★ 本门因此只把它当【证据源】、★ 不当【真值】⇒ ★★ 它的作用是
// ★★ 「★ 区分『低于下限却从没被应用』与『低于下限但被应用过』」。
func dbObjRelAppliedMigrations(t *testing.T) map[int]bool {
	t.Helper()
	out := map[int]bool{}
	for _, r := range dbObjDataLines(t, dbObjRepoPath(t, dbObjRelLedgerRel)) {
		if len(r) < 2 {
			t.Fatalf("ledger row malformed: %v", r)
		}
		v, err := strconv.Atoi(strings.TrimSpace(r[1]))
		if err != nil {
			continue // ★ 非数字的行（★ 台账里有 V357 这类手工条目）⇒ 跳过
		}
		out[v] = true
	}
	if len(out) == 0 {
		t.Fatal("the migration ledger snapshot is empty ⇒ the attribution check asserts nothing")
	}
	return out
}

// dbObjRelVersion 取迁移文件名的版本号（★ 「358_session_ownership.sql」⇒ 358）。
// ★ 取不到数字（★ 非数字前缀）⇒ 返回 -1。
func dbObjRelVersion(file string) int {
	base := filepath.Base(file)
	n := 0
	for i := 0; i < len(base) && base[i] >= '0' && base[i] <= '9'; i++ {
		n = n*10 + int(base[i]-'0')
	}
	if n == 0 {
		return -1
	}
	return n
}

// dbObjRelCanonicalCreators 扫 canonical 迁移 ⇒ 关系名 → 创建它的文件列表。
//
// ★★ 为什么不用 expect.tsv 的 source_file：★★ 那是【创建该表上【索引】的迁移】
// ★★ （★ 例：goal_runs 的索引创建者是 549、★ 而建表的是 554）⇒ ★★ 用它判
// ★★ 「这张表属于哪条迁移」会判错 ⇒ ⇒ ★ 必须自己按【内容】找【建表者】。
func dbObjRelCanonicalCreators(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	dir := dbObjRepoPath(t, dbObjMigrationsRel)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".sql") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		for _, name := range dbObjRelCreatedNames(string(raw)) {
			out[name] = append(out[name], filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk canonical migrations: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("canonical migrations create nothing ⇒ the attribution check asserts nothing")
	}
	return out
}

// dbObjRelProdPresent 报告这个关系在 prod（252 / 211）里在不在。
func dbObjRelProdPresent(t *testing.T, name string) bool {
	t.Helper()
	for env, st := range dbObjParseCensus(t, dbObjRepoPath(t, dbObjCensusRel)) {
		if env != "252" && env != "211" {
			continue
		}
		if _, ok := st.rels[name]; ok {
			return true
		}
	}
	return false
}

// ★★★ 关系层缺口的【显式登记清单】。
//
// ★★ 每一条都必须写清「为什么它不算安装器的缺口」——★ 空理由一律不接受。
// ★★ 棘轮见 dbObjRelRatchet：★ 只许减少。
// dbObjRelExempt 一条关系层豁免：★ 分类 ＋ 人工理由。
//
// ★★ 「分类」是【机器可判】的（★ 见 TestInstallerRelationExemptionKindsAreVerifiable）
// ★★ ⇒ ⇒ ★ 一条豁免不能只写一句人话，★ 必须能被机器核对它**为什么**成立。
// ★★ ⇒ ⇒ ★★ 这一条把「★ prod 有、新装没有 ⇒ 为什么？」从注释里搬到了【可判的登记】里。
type dbObjRelExempt struct {
	Kind   string
	Reason string
}

// ★ 三种归因分类（★ 每一类都有机器判据）。
const (
	// dbObjRelKindBelowFloor：★ 创建迁移的版本号【低于部署通道的投递下限】
	// ★（★ 默认 412，★ 由 DB_LEDGER_RECONCILE_FROM 决定）⇒ ★★ 部署通道【也不投递它】
	// ★ ⇒ ★★ 新装链不建它 = 【与部署链一致】⇒ ⇒ 不是新装缺口。
	//
	// ★★★ ⚠★ 【别把这一类读成「新装链不含 <412 的迁移」】★★★ ★★ 它【含】：
	// ★★★ 登记清单 334 条里有 **8 条**版本号 < 412（★ 351 / 361b / 375 / 376 /
	// ★★★ 387 / 388 / 392 ＋ 无版本号的 `session_turns_hot_bootstrap.sql`）
	// ★★★ ⇒ ★⇒ **两条通道的边界【本来就不同】** ⇒ ★⇒ 这才是这类归因的真意思：
	// ★★★ 【「部署通道也不建它」】，★ 而不是【「新装链不装低版本迁移」】。
	dbObjRelKindBelowFloor = "below-deploy-floor"

	// dbObjRelKindBelowFloorApplied：★ 创建迁移【低于部署通道下限】★ ★★ **但它
	// ★★ 在 prod 上【被应用过】**（★ 迁移台账快照里有它的应用记录）
	// ★★ ⇒ ★★ 两条通道的【边界不一致】已经【实际发生】⇒ ⇒ ★★ 这是【真差异】
	// ★★ ⇒ ⇒ ★★ 不是「一致」、★ 也不是「忘了登记」⇒ ★★ 必须登记。
	//
	// ★★★ ★★ 实测（本轮）：★ 台账里有 **12 条**低于 412 的迁移在 211 上被应用过
	// ★★★ （★ 032/044/351/357/358/361/375/376/387/392/394/407）
	// ★★★ ⇒ ★★ 而这 12 条创建的对象里，★ **只有 358** 建的那两个
	// ★★★ （★ `session_owners` + `session_owner_stats`）★ ★★ 是「prod 有而新装链没有」
	// ★★★ ⇒ ⇒ ★★ **358 是【唯一】一条「边界不一致」的对象族** ⇒ ⇒ ★★ 而这一类
	// ★★★ **只有把 ledger 维度加进判据才看得见** ⇒ ⇒ ★★ 少了它，★ 这道门就会
	// ★★★ 把真差异洗成「与部署链一致」⇒ ⇒ ★★★ **★ 而那正是本轮差点犯的错**
	dbObjRelKindBelowFloorApplied = "below-floor-but-applied"

	// dbObjRelKindSuperseded：★ 创建迁移【已被更晚的迁移取代】⇒ ★ 仓内代码零引用
	// ★ ⇒ ★ 新装链走的是新方案、★ 不需要这个对象 ⇒ ⇒ 不是新装缺口。
	dbObjRelKindSuperseded = "superseded"

	// dbObjRelKindOutOfRepo：★ 【没有任何 canonical 迁移创建它】⇒ ★ 仓外对象
	// ★（★ 手工建 / 别的服务建）⇒ ⇒ 安装器链既不该建、★ 也建不出它。
	dbObjRelKindOutOfRepo = "out-of-repo"
)

// ★★★ 关系层缺口的【显式登记清单】。
//
// ★★ 每一条都必须写清「为什么它不算安装器的缺口」——★ 空理由一律不接受。
// ★★ ★ 分类是【机器可判】的（★ 理由是人写的，★ 分类必须被机器核对）。
var dbObjRelExemptions = map[string]dbObjRelExempt{
	// request_abandoned：★ 创建迁移 819 【已被 820/821 线取代】
	// ★ （819 的头 15 行明写：「★ 历史档案…★ 独立表方案被 820/821 线取代
	// ★   （821 改为在 session_turns 上打 is_abandoned 标记，2026-10-03 用户拍板），
	// ★   ★ 生产 *.go 对本表零引用…★ 仅为 checksum 门完整性保留」）
	// ★ ⇒ ★ prod 上它还在（★ 819 先跑了、821 只取代了【写路径】、★ 没删表）
	// ★ ⇒ ★ 但**没有任何代码用它** ⇒ ★ 新装链不需要它。
	"request_abandoned": {
		Kind:   dbObjRelKindSuperseded,
		Reason: "迁移 819 已被 820/821 线取代（819 头 15 行「历史档案…生产 *.go 对本表零引用」）⇒ 新装链走 821、不需要该表",
	},

	// free_resource_catalog：★ 【没有任何迁移创建它】
	// ★ （900 只在注释里引用，★ 且它自己写着「★ 本表不在任何迁移通道」）⇒ ★ 仓外对象
	// ★ ⇒ 安装器链既不该建它、★ 也建不出它 ⇒ 不算安装器缺口。
	"free_resource_catalog": {
		Kind:   dbObjRelKindOutOfRepo,
		Reason: "无任何迁移创建它（900 只引用并注明「本表不在任何迁移通道」）⇒ 仓外对象 ⇒ 不算安装器缺口",
	},

	// ★★★ session_owners：★★ 【★ 不是「新装漏了」，★ 是「部署链也不建它」】
	// ★ 创建迁移 358_session_ownership.sql；★ ★★ 358 的版本号【低于部署通道的投递下限 412】
	// ★ ⇒ ★★ `scripts/deploy-lib/db-changelog.sh:285` 的投递判据
	// ★   `if (( 10#$ver >= ledger_reconcile_from ))` ⇒ ★★ 358 不会被部署
	// ★ ⇒ ★★ 于是 252 上没有它（★ 与 census 一致）、★ 而 211 上有（★ 2.36M 行）
	// ★ ★★ ⇒ ★★★ **★ 而「211 上那一份是历史产物」这个说法是错的 ★★**：
	// ★ ★★ 台账快照记录 `211 358 … 2026-10-12 01:27:02` ⇒ ★★ 211 是【正常应用过】
	// ★ ★★ 358 的（★ 而下限 412 引入于 2026-07-17、★ 晚了近 3 个月）⇒ ★★ ⇒ ★★
	// ★ ★★ **⇒ 精确归因是 `below-floor-but-applied`（★ 边界不一致 ⇒ 真差异）**
	// ★ ★ admin/user_profile.go:328 早就写着这件事：
	// ★   「生产共享库缺 session_owners（迁移 358 的版本号低于部署通道的账本对账下限 412
	// ★   ⇒ 存量库永远拿不到它）」⇒ ★ 降级路径（200 ＋ 空载荷 ＋ degraded）已经在了。
	"session_owners": {
		Kind:   dbObjRelKindBelowFloorApplied,
		Reason: "★ 真差异：创建迁移 358 的版本号低于部署通道投递下限（默认 412）⇒ deploy 通道【不会】再投递它，★ 而迁移台账快照记录 211 在 2026-10-12 【确实应用过】358 ⇒ ★★ 两条通道边界不一致 ⇒ ⇒ 新装环境必然无此表（admin 侧已有降级路径 ⇒ 能力影响 0）",
	},

	// ★★★ session_owner_stats：★★ 同一个根因（★ 358 建、★ 358 < 412）
	// ★ 物化视图，★ 358_session_ownership.sql:345 建；★ 211 上存在。
	"session_owner_stats": {
		Kind:   dbObjRelKindBelowFloorApplied,
		Reason: "★ 真差异：创建迁移 358（:345 的物化视图）低于部署通道投递下限（默认 412）⇒ deploy 通道【不会】再投递它，★ 而台账快照记录 211 应用过 358 ⇒ ★★ 两条通道边界不一致 ⇒ ⇒ 新装环境必然无此视图（仓内无代码引用它）",
	},
}

// dbObjRelRatchet 关系层缺口的棘轮（★ 2026-11-03 实测）。
//
// ★★ prod-only-relations 的基线是 4 ⇒ ★★ 【任何新增的「prod 有、新装不创建」的关系都直接红】。
// ★★ exempt 的基线是 4 ⇒ ★★ 【豁免清单不许增长】⇒ ★ 缺口被修掉后 ★ 清单必须跟着收缩。
//
// ★★★ ★★ 为什么基线是 4 而不是 0：★★ 这 4 条**全部**是「★ 有意的豁免」
// ★★★ （★ 三种归因各占一部分，★ 见上面的清单）⇒ ★★ 它们**不是缺陷**。
// ★★★ ⇒ ★★ 但「★ 有意」这件事**必须被登记**，★ 不能靠注释散落在各处
// ★★★ ⇒ ★★ ⇒ 所以这 4 条【就是本轮的产出】：★ ★★ 缺口本身是 0，★
// ★★★ ★★ 而「★ 哪些对象是故意不给新装环境的」从此【机器可判】。
var dbObjRelRatchet = map[string]int{
	"prod-only-relations": 4,
	"exempt":              4,
}

// TestInstallerRelationReachabilityCoversEveryProdRelation 是这道门的核心断言：
//
// ★★ 对每一个「★ 仓内迁移创建过、★ 且 prod(252∪211) 上还在」的关系，
// ★★ 它必须被【新装通道】（★ 基线 ∪ 当前 334 条登记文件）创建
// ★★ —— 要么如此，★★ 要么它必须落在豁免清单里（★ 且豁免数不许超棘轮）。
func TestInstallerRelationReachabilityCoversEveryProdRelation(t *testing.T) {
	expect := dbObjRelExpectTables(t)
	covered := dbObjRelCoveredSet(t, dbObjRelManifest(t))

	type gapRow struct {
		name    string
		creator string
	}
	var gaps []gapRow
	prodSeen := 0

	for name, creator := range expect {
		if !dbObjRelProdPresent(t, name) {
			continue
		}
		prodSeen++
		_, ok := covered[name]
		if !ok {
			gaps = append(gaps, gapRow{name: name, creator: creator})
		}
	}

	// ★ 口径自证：★ 这道门必须真的看了 prod 侧的事实（★ 不许因为解析坏了而空转）。
	if prodSeen == 0 {
		t.Errorf("no relation in the expect set was found in prod ⇒ " +
			"the relation verdict would pass vacuously (★ ★ 解析口径坏了 / census 路径错了)")
	}

	sort.Slice(gaps, func(i, j int) bool { return gaps[i].name < gaps[j].name })

	// ① 每一个真缺口都必须登记在豁免清单里。
	for _, g := range gaps {
		if _, ok := dbObjRelExemptions[g.name]; !ok {
			t.Errorf("relation %q is present in prod (252/211) but the fresh-install chain "+
				"never creates it (★ creator=%s) ⇒ ★★ it is a new-install gap that is NOT registered.\n"+
				"    Either register the creating migration in installer/internal/dbinit/runner.go's\n"+
				"    StartupFiles (+ the embeddata copy), or add it to dbObjRelExemptions with the reason.",
				g.name, g.creator)
		}
	}

	// ② 棘轮：★ 豁免数不许超过基线（★ 只许减少）。
	if n := len(dbObjRelExemptions); n > dbObjRelRatchet["exempt"] {
		t.Errorf("relation exemption list has %d entries, ratchet allows %d ⇒ "+
			"★ the list grew (★ only shrinking is allowed) ⇒ "+
			"delete the entries whose objects the fresh-install chain now creates",
			n, dbObjRelRatchet["exempt"])
	}

	// ③ 棘轮：★ 真缺口数不许超过基线（★ 新增缺口直接红）。
	if len(gaps) > dbObjRelRatchet["prod-only-relations"] {
		t.Errorf("relations present in prod but not created by the fresh-install chain = %d, "+
			"ratchet allows %d ⇒ %v", len(gaps), dbObjRelRatchet["prod-only-relations"], gaps)
	}
}

// TestInstallerRelationExemptionsAreExactlyTheGaps 守豁免清单不许腐化：
//
// ★★ 清单里每一条都必须【真的是缺口】⇒ ⇒ 不许挂着早已修好的名字当摆设。
// ★★ 且每一条都必须带理由（★ 空字符串不接受）⇒ ⇒ 不许「先挂上、以后再说」。
func TestInstallerRelationExemptionsAreExactlyTheGaps(t *testing.T) {
	expect := dbObjRelExpectTables(t)
	covered := dbObjRelCoveredSet(t, dbObjRelManifest(t))

	for name, ex := range dbObjRelExemptions {
		if strings.TrimSpace(ex.Reason) == "" {
			t.Errorf("relation exemption %q has no reason ⇒ ★ an unexplained exemption is a silent hole", name)
		}
		if ex.Kind == "" {
			t.Errorf("relation exemption %q has no kind ⇒ ★ an unattributed exemption cannot be machine-checked", name)
		}
		if _, ok := expect[name]; !ok {
			t.Errorf("relation exemption %q is not in the expect set (★ %s) ⇒ "+
				"★ it is not a relation the repo's migrations create ⇒ the exemption is stale",
				name, dbObjExpectRel)
		}
		if src, ok := covered[name]; ok {
			t.Errorf("relation exemption %q is now created by the fresh-install chain (%s) ⇒ "+
				"★ delete it from dbObjRelExemptions (★ the list must shrink)",
				name, src)
		}
		if !dbObjRelProdPresent(t, name) {
			t.Errorf("relation exemption %q is not present in prod (252/211) ⇒ "+
				"★ it is not a prod/fresh divergence ⇒ the exemption is stale", name)
		}
	}
}

// TestInstallerRelationExemptionKindsAreVerifiable 是本门最关键的一条断言：
//
// ★★ 豁免的【分类】必须被机器核对 ⇒ ⇒ ★★ 「★ prod 有、新装没有 ⇒ 为什么？」
// ★★ 不能只写一句人话（★ 人话会被写错、★ 而且写错了没人发现）。
//
// ★★ 三类归因、★ 三条机器判据：
//
//	below-deploy-floor ⇒ ★ 创建迁移的版本号【低于部署通道的投递下限】
//	                   ⇒ ★★ ⇒ 部署链也不建它 ⇒ 新装链不建它 = 与部署链一致
//	superseded         ⇒ ★ 创建迁移【≥ 下限】（★ 否则该归 below-deploy-floor）
//	                   ⇒ ★★ 且它的【头 15 行】写着「取代/历史档案/已废弃」
//	                   ⇒ ★★ 与部署通道自己的判据（head -15 + SUPERSEDED|DEPRECATED）同族
//	out-of-repo        ⇒ ★ 【没有任何 canonical 迁移创建它】
//
// ★★★ ★★ 这条断言**当场判错过本轮自己的结论**：★★ 第一版豁免理由写的是
// ★★★ 「session_owners = ★ 真缺口：358 未登记进安装器链」⇒ ★★ 而机器判据说：
// ★★★ 358 < 412 ⇒ 部署通道【也不投递】⇒ ⇒ 归因必须是 below-deploy-floor
// ★★★ ⇒ ⇒ ★★ 「真缺口」是错的（★ 新装链不建它是对的）⇒ ★★ 本门把它判红。
func TestInstallerRelationExemptionKindsAreVerifiable(t *testing.T) {
	floor := dbObjRelDeployFloor(t)
	manifest := dbObjRelManifest(t)
	creators := dbObjRelCanonicalCreators(t)
	appliedMigs := dbObjRelAppliedMigrations(t)

	for name, ex := range dbObjRelExemptions {
		files := creators[name]
		switch ex.Kind {
		case dbObjRelKindOutOfRepo:
			if len(files) != 0 {
				t.Errorf("relation exemption %q is classified out-of-repo, but canonical migrations "+
					"create it in %v ⇒ ★★ the classification is wrong (★ it must be "+
					"below-deploy-floor or superseded)", name, files)
			}

		case dbObjRelKindBelowFloor, dbObjRelKindBelowFloorApplied:
			if len(files) == 0 {
				t.Errorf("relation exemption %q is classified %s, but no canonical "+
					"migration creates it ⇒ ★★ the classification must be out-of-repo",
					name, ex.Kind)
				continue
			}
			minVer := -1
			for _, f := range files {
				if v := dbObjRelVersion(f); v >= 0 && (minVer < 0 || v < minVer) {
					minVer = v
				}
			}
			if minVer >= floor {
				t.Errorf("relation exemption %q is classified %s, but its creating "+
					"migration is %d (≥ the deploy floor %d) ⇒ ★★ the deploy channel DOES deploy it "+
					"⇒ ★★ so the fresh-install chain missing it IS a gap ⇒ "+
					"★ either fix the chain or re-attribute this exemption",
					name, ex.Kind, minVer, floor)
			}
			for _, f := range files {
				if manifest[f] {
					t.Errorf("relation exemption %q is classified %s, but %s IS "+
						"registered in the installer manifest ⇒ ★★ the two attributions contradict "+
						"★ each other (★ it is both unreachable and reachable)", name, ex.Kind, f)
				}
			}
			// ★★★ 【★ 本轮的关键判据 ★】：★ 低于下限【且在 prod 上被应用过】
			// ★★★ ⇒ ⇒ ★★ 这是【边界不一致】⇒ ⇒ 归因必须是 below-floor-but-applied
			// ★★★ ⇒ ⇒ ★★ 少了这一维，★ 门就会把【真差异】洗成「与部署链一致」。
			applied := appliedMigs[minVer]
			if ex.Kind == dbObjRelKindBelowFloorApplied && !applied {
				t.Errorf("relation exemption %q is classified below-floor-but-applied, but the "+
					"migration ledger snapshot has NO application record for %d ⇒ ★★ then it was "+
					"never applied ⇒ ★★ the deploy channel not delivering it is consistent ⇒ "+
					"★★ the correct attribution is below-deploy-floor", name, minVer)
			}
			if ex.Kind == dbObjRelKindBelowFloor && applied {
				t.Errorf("relation exemption %q is classified below-deploy-floor, but the migration "+
					"ledger snapshot DOES record %d as applied (★ in a prod env) ⇒ ★★ so prod has "+
					"its objects although the deploy channel would not deliver it ⇒ ★★ that is a "+
					"★ boundary mismatch ⇒ ★★ the correct attribution is below-floor-but-applied",
					name, minVer)
			}

		case dbObjRelKindSuperseded:
			if len(files) == 0 {
				t.Errorf("relation exemption %q is classified superseded, but no canonical migration "+
					"creates it ⇒ ★★ the classification must be out-of-repo", name)
				continue
			}
			minVer := -1
			for _, f := range files {
				if v := dbObjRelVersion(f); v >= 0 && (minVer < 0 || v < minVer) {
					minVer = v
				}
			}
			if minVer < floor {
				t.Errorf("relation exemption %q is classified superseded, but its creating migration "+
					"is %d (< the deploy floor %d) ⇒ ★★ the deploy channel does NOT deploy it ⇒ "+
					"★★ the correct attribution is below-deploy-floor", name, minVer, floor)
			}
			// ★ 与部署通道同族的判据：★ head -15 里有「取代/历史档案/已废弃」。
			marked := false
			for _, f := range files {
				raw, err := os.ReadFile(filepath.Join(dbObjRepoPath(t, dbObjMigrationsRel), filepath.FromSlash(f)))
				if err != nil {
					t.Fatalf("read %s: %v", f, err)
				}
				head := strings.SplitN(string(raw), "\n", 16)
				if len(head) > 15 {
					head = head[:15]
				}
				joined := strings.ToLower(strings.Join(head, "\n"))
				if strings.Contains(joined, "supersed") || strings.Contains(joined, "deprecat") ||
					strings.Contains(joined, "取代") || strings.Contains(joined, "历史档案") {
					marked = true
				}
			}
			if !marked {
				t.Errorf("relation exemption %q is classified superseded, but no creating migration "+
					"carries a 「取代 / 历史档案 / superseded / deprecated」 marker in its head 15 lines "+
					"(★ %v) ⇒ ★★ the attribution is not machine-verifiable", name, files)
			}

		default:
			t.Errorf("relation exemption %q has an unknown kind %q ⇒ ★★ allowed kinds: %s / %s / %s / %s",
				name, ex.Kind, dbObjRelKindBelowFloor, dbObjRelKindBelowFloorApplied,
				dbObjRelKindSuperseded, dbObjRelKindOutOfRepo)
		}
	}
}

// TestInstallerRelationReachabilityAgreesWithIndexLedger 是两侧互证：
//
// ★★ R163 的索引级台账对「★ fresh 里这张表压根不存在」的关系一律判
// ★★ `reachable-and-legitimately-retired` / severity=`none`
// ★★ ⇒ ⇒ 它【看不见】关系层缺口。
// ★★ 本门断言的正是这件事：★★ 索引级台账没有把这些关系算成缺口
// ★★ ⇒ ⇒ 它们只能由本门（★ 关系层）来管。
//
// ★★ 若哪天索引级台账开始报出这些关系 ⇒ ★ 本门会红 ⇒ ★ 那正是好消息：
// ★★ 说明台账被修好了 ⇒ ★ 此时应当把结论从本门迁回台账口径。
func TestInstallerRelationReachabilityAgreesWithIndexLedger(t *testing.T) {
	rows := dbObjDataLines(t, dbObjRepoPath(t, dbObjReachRel))
	if len(rows) == 0 {
		t.Fatal("reachability ledger is empty ⇒ the cross-check asserts nothing")
	}
	byTable := map[string][]string{}
	for _, r := range rows {
		if len(r) < dbObjReachCols {
			t.Fatalf("reachability row malformed: %v", r)
		}
		byTable[r[1]] = append(byTable[r[1]], r[7]) // verdict
	}
	for name := range dbObjRelExemptions {
		verdicts := byTable[name]
		if len(verdicts) == 0 {
			// ★ 该关系没有任何索引行 ⇒ 索引层本来就看不见它 ⇒ 与本门不冲突。
			continue
		}
		for _, v := range verdicts {
			if v == "unreachable-and-missing" || v == "reachable-but-missing-in-fresh" {
				t.Errorf("relation %q is reported by the index-level ledger as %q ⇒ "+
					"★ the index ledger now sees it ⇒ ★ the relation-level finding is superseded ⇒ "+
					"★ re-check §28.32 and, if the ledger's verdict is correct, drop the exemption",
					name, v)
			}
		}
	}
}

// TestInstallerRelationReachabilityHasTeeth 是这道门的【牙】：
//
// ★★ 没有牙的门等于没有门。★★ 这里用合成夹具验证判据本身，
// ★★ 而不是验证「当前仓库恰好是绿的」。
func TestInstallerRelationReachabilityHasTeeth(t *testing.T) {
	cases := []struct {
		name  string
		sql   string
		want  []string
		notIn []string
	}{
		{
			name: "table",
			sql:  "CREATE TABLE IF NOT EXISTS public.foo_bar (\n  id bigint\n);",
			want: []string{"foo_bar"},
		},
		{
			name: "materialized-view",
			sql:  "CREATE MATERIALIZED VIEW IF NOT EXISTS mv_x AS SELECT 1;",
			want: []string{"mv_x"},
		},
		{
			name: "plain-view",
			sql:  "CREATE OR REPLACE VIEW v_x AS SELECT 1;",
			want: []string{"v_x"},
		},
		{
			name: "unlogged-table",
			sql:  "CREATE UNLOGGED TABLE ul_x (id int);",
			want: []string{"ul_x"},
		},
		{
			name: "comment-is-not-a-statement",
			sql:  "-- CREATE TABLE commented_out (id int);\nCREATE TABLE real_one (id int);",
			want: []string{"real_one"},
			// ★ 注释里的建表【必须】不被认出来 ⇒ 否则一张已删的表会被当成「有」
			// ★ ⇒ 缺口被吃掉。
			notIn: []string{"commented_out"},
		},
		{
			name:  "name-prefix-is-not-a-match",
			sql:   "CREATE TABLE foo (id int);",
			notIn: []string{"foo_extra"},
		},
		{
			name: "create-table-as-select",
			sql:  "CREATE TABLE ctas_x AS SELECT 1;",
			want: []string{"ctas_x"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := map[string]bool{}
			for _, n := range dbObjRelCreatedNames(c.sql) {
				got[n] = true
			}
			for _, w := range c.want {
				if !got[w] {
					t.Errorf("expected %q to be recognized as created, got %v", w, got)
				}
			}
			for _, n := range c.notIn {
				if got[n] {
					t.Errorf("expected %q NOT to be recognized as created, got %v", n, got)
				}
			}
			if len(c.want) == 0 && len(c.notIn) == 0 {
				t.Fatal("case asserts nothing")
			}
		})
	}
}
