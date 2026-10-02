// Package data - D07 数据测试：保留期删除的谓词列必须有首列匹配的索引。
//
// ## 为什么这条规则独立于 any_batch_cursor_index_test.go
//
// 上一道门普查的是**函数体内的批游标**（ORDER BY ... LIMIT）。后台保留期
// worker 是另一类批删除：它们在 Go 代码里，手写 DELETE ... WHERE id IN
// (SELECT id ... WHERE <时间列> < ... LIMIT 5000)。这道门按同一判据扫它们，
// 但判据对象不同：不是"函数体里的 ORDER BY"，而是"Go 源码里的 DELETE"。
//
// ## 关键判据：ts 出现在索引里 ≠ 索引可用
//
// candidate_failure_logs 的每个分区有 6 条索引，**ts 是其中 4 条的第二列**
// （credential_id, ts DESC / provider_id, ts DESC / raw_model_name, ts DESC /
// session_id, ts DESC）。裸 `ts < cutoff` 谓词用不上任何一条，于是规划器对
// 105MB / 83,289 行的活跃分区 2026_09 走 Seq Scan，cost 10,646：
// 每轮（24h tick、每批 LIMIT 5000）扫 67,826 行只为了删 5,000 行——13.6 倍
// 扫描放大，且表里 67,580 行（81%）早已过期。
//
// 这正是记忆里那条纪律的镜像："索引首列匹配 ≠ 索引可用"的反面是
// "索引里有这一列 ≠ 索引可用"。本门只认**首列**。
//
// ## 边界：Seq Scan ≠ 索引缺失
//
// 小表上规划器主动选 Seq Scan 是**正确**的，判红就是规则用错了对象。本门
// 把这些表按实测 reltuples 显式登记豁免，登记必须带行数与 EXPLAIN 成本证据，
// 不登记就判红——这样新增一张小表必须显式说明，门不会默默放过。
//
// ## 本轮真库实测（PG 17.10，2026-09-29）
//
// 首列匹配、走索引（绿）：
//
//	request_state_transitions  idx_state_transitions_created (created_at)
//	request_stage_events      idx_stage_events_created_at  (created_at)
//	session_aggregate_outbox  idx_..._done_completed_at    (completed_at)
//	session_summaries         idx_session_summaries_archived (archived_at)
//	credential_probe_model_log idx_credential_probe_model_log_created_at
//	ursm_node_snapshot_min     _pkey (snapshot_ts，Index Only Scan)
//
// 首列不匹配且表不小（红，本轮新发现两项）：
//
//	journal_snapshot_receipts  94,657 行 / 57MB —— 三条索引首列分别是
//	                           claim_until(partial)、tenant_id、identity 复合，
//	                           没有任何一条以 updated_at 开头。Seq Scan cost 5,020。
//	candidate_failure_logs     83,289 行 / 105MB 活跃分区 —— 见上。
//
// ## 一条证伪：request_state_transitions 的"无 LIMIT 全量 DELETE"不是缺陷
//
// domains/requestjourney/retention.go:134 是全仓唯一没有 LIMIT、没有分批的
// 保留期 DELETE，形如 `DELETE FROM request_state_transitions WHERE created_at
// < ...`。它看起来是本该红的那一类。实测否掉了：
//
//	时间跨度  min=2026-09-22 01:20  max=2026-09-29 01:34  = 7.01 天
//	过期行(>7d) 2,648      每小时新增 1,103 行
//
// 保留窗口正好等于 7 天保留期，说明**保留期在生效**；每次 tick 删 1.1k 行，
// 30s ctx 超时绰绰有余，索引也在用。写此注释是为了记下"看起来该红但实测
// 不该红"的位置，免得下一轮重新怀疑一次。
//
// ## 这道门不覆盖的东西：DELETE 形态（比索引更贵，且收益逐表不同）
//
// 本门只判「谓词列是不是索引首列」。但续十一的实测显示**真正的成本常常在
// DELETE 形态上，而不在索引上**，所以本门绿 ≠ 这条 DELETE 写得对。
//
// 同一批实测，两个表给出**相反**的结论：
//
//	armor_judgments（804,253 行，单条件 created_at，无 created_at 首列索引）
//	  id IN + ORDER BY + LIMIT 5000   870.7ms
//	  ctid IN + 去掉 ORDER BY            7.0ms   ← 124 倍
//
//	session_aggregate_outbox（1,157,822 行，双条件 status+completed_at，
//	                             completed_at **就是**首列索引 ⇒ 本门判它绿）
//	  id IN + ORDER BY + LIMIT 5000  6,216ms
//	  ctid IN + 去掉 ORDER BY        6,278ms   ← **无差别**
//
// sao 的内层本来就走索引，外层回表是瓶颈（1.5GB 宽表上 5,000 次随机 Tid Scan），
// 改形态救不了；armor 的内层是 Seq Scan，改形态一次消掉外层全表 Hash Join。
// **「ctid 形态 124 倍」不能整类推广，必须逐表实测。**
//
// 判据的分工：本门管「有没有可用索引」，形态与吞吐属于 worker 侧改造，
// 两者不可互相替代。拿本门判绿当「这条 DELETE 没问题」是误用。
//
// 跑测（无库自动 skip；只需能读 pg_catalog 的角色）：
//
//	D07_S01_PG_URL=postgres://reader@127.0.0.1:5432/llm_gateway?sslmode=disable \
//	  go test -timeout 180s -run RetentionTrim ./tests/48h-audit/D07-hot-columnar/data/...
package data

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// retentionSmallTables 登记"小到 Seq Scan 是正确选择"的保留期表。
//
// 每一行都必须带实测 reltuples；新增登记时请在 reason 里写 EXPLAIN 成本，
// 不要只写"表小"。这张表只增不减的例外是：某天实测行数跨过百万级，
// 那时本门会开始红，届时应补索引而不是调大豁免。
var retentionSmallTables = map[string]struct {
	rows   int64
	reason string
}{
	"routing_audit_log": {
		rows: 1077, reason: "856 kB / EXPLAIN Seq Scan cost 52.62 rows=1162，远低于索引扫描",
	},
	"routing_overrides_audit": {
		rows: 66, reason: "96 kB / EXPLAIN Seq Scan cost 2.66 rows=66",
	},
	"sticky_sessions": {
		rows: 5, reason: "656 kB 但 5 行 / EXPLAIN Seq Scan cost 2.08 rows=4",
	},
	"handoff_logs_hot": {
		rows: 0, reason: "56 kB / EXPLAIN Seq Scan cost 0.00 rows=1（hot 表常态空）",
	},
	"handoff_pending_confirmations": {
		rows: 0, reason: "56 kB / EXPLAIN Seq Scan cost 10.20 rows=20（reltuples 未统计，按 0 记）",
	},
	"request_envelope": {
		rows: 0, reason: "16 kB / EXPLAIN Seq Scan cost 0.00 rows=100（reltuples 未统计，按 0 记）",
	},
	"free_quota_tracker": {
		rows: 0, reason: "80 kB，按 tenant 分组循环删除（bg/freequotacleanup/worker.go）",
	},
	"settings_audit": {
		rows: 0, reason: "80 kB / EXPLAIN Seq Scan cost 11.22 rows=23（reltuples 未统计，按 0 记）",
	},
	"session_module_executions_hot": {
		rows: 0, reason: "64 kB / 0 行（hot 表常态空）/ cleanup 由 admin 手动触发，非周期 worker",
	},
}

// knownRetentionDefects 是**已确认但未修**的缺陷登记。
//
// 为什么不让门为它们一直红：长期红的门会被人习惯性忽略，久了和没有门一样。
// 所以它们登记在这里，门只在出现**登记表以外**的新表时红；同时用
// expectedKnownRetentionDefects 棘轮钉住条目数——修好一条就把登记删掉并调小
// 棘轮，门不会因为有人忘了更新登记而静默放过。
//
// 登记必须带实测数据。定级前量的都是分布而不是峰值。
var knownRetentionDefects = map[string]string{
	// 注意：这条门报的是「created_at 不是索引首列」，但**实测该缺失在本表上不构成
	// 成本**——本表真正的成本在 **DELETE 形态**，不在索引。
	//
	// 生产规模（804,253 行）TEMP TABLE 对照实测：
	//   A 现状 id IN + ORDER BY + LIMIT 5000   870.7ms   ← 生产查询形态
	//   B id IN 去掉 ORDER BY                    695.6ms
	//   C **ctid IN + 去掉 ORDER BY**              **7.0ms**   ← 124 倍
	//   D ctid IN + 补 (created_at) 索引            6.8ms（与 C 无差别，补索引无用）
	//
	// `DELETE ... WHERE id IN (SELECT id ...)` 迫使 DELETE 主体**全表扫**做 Hash Join
	// （EXPLAIN: `Seq Scan ... actual rows=804306`）；`ctid IN (...)` 让规划器用
	// **Tid Scan** 按子查询输出的物理地址直接定位行，无需扫外层。
	// 本仓已有两个生产用例是这个形态：bg/session_summaries_trimmer.go:122、
	// domains/attachments/repository.go:294 —— 改法不需要任何迁移。
	//
	// **2026-09-29 二十轮已修**：bg/audit_trimmer.go armor 段已改 ctid 形态 +
	// 去 ORDER BY + 按批循环（20 批 × 5000/天，写 6,318/天的吞吐倒挂闭合）。
	// 本条登记**继续保留**：created_at 仍不是任何索引的首列（ctid 形态下内层
	// Seq Scan 仍在，只是 7ms/批可忽略），门仍会扫到本表。若将来有人改回
	// id 形态或加 ORDER BY，本登记的实测数据就是回滚依据。
	"armor_judgments": "已修注记 armor_judgments：DELETE 形态已改 ctid + 去序 + 批循环（2026-09-29 二十轮，" +
		"bg/audit_trimmer.go），吞吐倒挂（写 6,318/天 > 删 5,000/天）已闭合。" +
		"登记保留原因：created_at 仍非任何索引首列——对 ctid 形态无成本影响（Tid Scan 7ms/批），" +
		"但门按首列判据仍会扫到。改回 id IN / ORDER BY 形态前先读本注记顶部的 124 倍实测。",

	// P3（原 P2，本轮实测后降级）：三条索引首列分别是 claim_until(partial)、
	// tenant_id、identity 复合，**没有一条以 updated_at 开头**。形态上最可疑的一条
	// ——它**无 LIMIT 无分批**，看起来正该吃全表 Seq Scan。实测否掉了（92,862 行，
	// 匹配仅 ~556 行，重复 4 次取中位）：
	//   现状（无 LIMIT，无索引）        98 ms（中位 94/95/102/127）
	//   补 (updated_at) 索引           285 ms —— **无收益**
	//   ctid IN + LIMIT 500             30 ms（3.2×，但绝对值只省 1.6 秒/天）
	//
	// **降级理由是绝对成本，不是相对倍数**：tick=1h ⇒ 98ms/小时 ≈ 2.4 秒/天。
	// 顺带一个单位时间对照：145ms/小时 ≈ 0.040 ms/s，而 armor_judgments 是
	// 870ms/24h ≈ 0.036 ms/s —— **两者单位时间成本几乎相同**，但本表的绝对值小两个量级。
	//
	// **该表没有 id 列也没有主键**（唯一键是 (tenant_id, request_id, snapshot_version)），
	// 所以 `id IN` 形态对它根本不适用——续十二的 ctid 修法要配合 `updated_at` 一起用。
	// 写成 id 形态会直接 42703。留此注记是为了防止将来有人「顺手优化」成 id 形态。
	"journal_snapshot_receipts": "P3 journal_snapshot_receipts（实测降级）：92,862 行，匹配仅 556 行；" +
		"补 (updated_at) 索引实测无收益（98ms→285ms），ctid+LIMIT 500 为 30ms。" +
		"**降级理由是绝对成本 2.4 秒/天，不是相对倍数。** 该表无 id 列无主键，id IN 形态不适用" +
		"（写了会 42703）。形态不理想（无 LIMIT 无分批）只在积压时才是风险",

	// P2：分区表，每个分区 6 条索引，ts 是其中 4 条的第二列
	// （credential_id/provider_id/raw_model_name/session_id, ts DESC）。
	// 活跃分区 2026_09 是 heap 105MB / 83,289 行，EXPLAIN cost 10,646，81%（67,580 行）已过期。
	// 同 armor_judgments：**cost 10,646 与「13.6x 扫描放大」都是估算，未实测执行时间**
	//（LIMIT 会提前终止），不能当已证实的成本上报。
	// 仍定 P2 的理由是**速率**而非扫描成本：写入仅 318 行/24h ≪ 删除能力 5,000/天，
	// 积压 13.5 天自行收敛。
	//
	// **2026-09-29 二十轮已修形态**：bg/opslog_trimmer.go cfl 段已改 ctid + 去 ORDER BY
	//（411ms→19ms，22 倍）。登记保留原因同 armor_judgments：ts 仍非首列，门按首列
	// 判据仍会扫到；本条降级为「已修注记」防止下轮误当未修缺陷重复排查。
	"candidate_failure_logs": "已修注记 candidate_failure_logs：DELETE 形态已改 ctid + 去序（2026-09-29 二十轮，" +
		"bg/opslog_trimmer.go，411ms→19ms）。写入 318/24h ≪ 删除能力，无吞吐风险。" +
		"登记保留原因：ts 仅作 4 条复合索引第二列，门按首列判据仍会扫到（对 ctid 形态无成本影响）。" +
		"cost 10,646 与「13.6x 放大」是估算，未实测执行时间（LIMIT 提前终止）",

	// P3：0 行，365 天保留期，尚未到达触发规模。tested_at 是 3 条索引的第二列。
	// 登记而不是豁免，是为了等它长起来时这条信息还在，而不是重新普查一遍。
	"model_iq_runs": "P3 model_iq_runs：0 行 / 40kB；tested_at 仅作 3 条索引第二列；365 天保留期未到触发规模。修法（触发前）：补 (tested_at) 索引",
}

// expectedKnownRetentionDefects 是棘轮：修好一条缺陷就调小它。
// 有人往 knownRetentionDefects 里塞新条目而不改棘轮 → 门红。
const expectedKnownRetentionDefects = 4

var (
	// retentionDeleteRe 抓 DELETE 语句本体：从 DELETE FROM 起到语句结束。
	// 第二组捕获可选的 schema 限定名：domains/attachments/repository.go:294 写的是
	// `DELETE FROM public.xxx`，只抓 `(\w+)` 会把表名报成 "public"——门报错时报错的名字
	// 必须是真表名，否则运维按报错去补索引会补到不存在的东西上。
	retentionDeleteRe = regexp.MustCompile(`(?is)DELETE\s+FROM\s+(?:(\w+)\.)?(\w+)(.{0,600}?)(?:LIMIT\s+\$?\d+)?[\x60"]`)
	// retentionPredicateRe 抓谓词里的时间列比较。两种形态都覆盖：
	//   A  DELETE FROM t WHERE created_at < ...
	//   B  DELETE FROM t WHERE id IN (SELECT id FROM t WHERE ts < ... ORDER BY ts LIMIT n)
	retentionPredicateRe = regexp.MustCompile(`(?i)(\w+)\s*<\s*(?:NOW\s*\(\)|now\s*\(\)|\$\d+|time\.Now)`)
	// retentionSrcDir 保留期 worker 所在的源码子目录。
	retentionSrcDirs = []string{"bg", "domains", "internal", "admin", "settings", "center", "licensing", "security"}
)

type retentionDelete struct {
	table     string
	predicate string
	source    string // repo-relative file:line
}

// scanRetentionDeletes 扫出全仓 Go 源码里的保留期 DELETE。
//
// 排除 tests/48h-audit：本门自己的注释里会写 DELETE FROM 字面量，把它们
// 当成被审对象就是门在审自己（与 any_batch_cursor_index_test.go 同一条纪律）。
func scanRetentionDeletes(t *testing.T) []retentionDelete {
	t.Helper()
	root := repoRoot(t)

	var out []retentionDelete
	for _, dir := range retentionSrcDirs {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); err != nil {
			t.Fatalf("源码目录不可读 %s: %v —— 门不能因为找不到源码就静默放行", dir, err)
		}
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "tests" || d.Name() == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			rel, _ := filepath.Rel(root, path)
			collectRetentionDeletes(string(b), rel, &out)
			return nil
		})
		if err != nil {
			t.Fatalf("遍历 %s 失败: %v", dir, err)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].source < out[j].source })
	return out
}

func collectRetentionDeletes(src, rel string, out *[]retentionDelete) {
	for _, m := range retentionDeleteRe.FindAllStringSubmatch(src, -1) {
		stmt := m[0]
		pred := retentionPredicateRe.FindStringSubmatch(stmt)
		if pred == nil {
			continue // 非按时间列的 DELETE（业务 CRUD / 按主键删）
		}
		*out = append(*out, retentionDelete{
			table:     m[2],
			predicate: strings.ToLower(pred[1]),
			source:    fmtLine(rel, src, strings.Index(src, stmt)),
		})
	}
}

func fmtLine(rel, src string, off int) string {
	if off < 0 {
		return rel
	}
	return fmt.Sprintf("%s:%d", rel, 1+strings.Count(src[:off], "\n"))
}

// leadingIndexedCols 查每张表所有 btree/部分 btree 索引的**首列**集合。
// 部分索引（pg_index.indpred 非空）同样计入首列：谓词蕴含时规划器可用。
func leadingIndexedCols(ctx context.Context, conn *pgx.Conn) (map[string]map[string]bool, error) {
	rows, err := conn.Query(ctx, `
		SELECT i.indrelid::regclass::text,
		       a.attname
		FROM pg_index i
		JOIN pg_class t ON t.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = i.indkey[0]
		WHERE n.nspname = 'public' AND t.relkind IN ('r','p')
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]bool{}
	for rows.Next() {
		var tbl, col string
		if err := rows.Scan(&tbl, &col); err != nil {
			return nil, err
		}
		if out[tbl] == nil {
			out[tbl] = map[string]bool{}
		}
		out[tbl][col] = true
	}
	return out, rows.Err()
}

func TestData_RetentionTrim_PredicateColumnHasLeadingIndex(t *testing.T) {
	dsn := retentionDSN()
	if dsn == "" {
		t.Skip("no read-only PostgreSQL DSN (D07_S01_PG_URL / TEST_PG_URL / LLM_GATEWAY_PG_URL / TEST_DATABASE_URL)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(context.Background())

	deletes := scanRetentionDeletes(t)

	// fail-closed：扫描器失效（正则退化 / 目录改名）会让这个门全绿而不是变红。
	// 这里把"扫到的东西太少"当成门自己的故障，而不是当成"没有缺陷"。
	if len(deletes) < 10 {
		t.Fatalf("只扫到 %d 条保留期 DELETE，扫描器很可能已失效（本轮基线 16 条）：\n%v",
			len(deletes), dumpRetention(deletes))
	}

	indexed, err := leadingIndexedCols(ctx, conn)
	if err != nil {
		t.Fatalf("leading indexed cols: %v", err)
	}
	if len(indexed) == 0 {
		t.Fatalf("从 pg_index 读到 0 条索引，查询或权限有问题（门不能因此全绿）")
	}

	// 表 -> 该表被判红的原因（保留首个）
	// known 里同时收录"真缺陷"与"表本身已消失/已改名"的条目，两类都必须对得上
	// 真实扫描结果，否则登记会变成一张和现实脱节的静态贴纸。
	seenTables := map[string]bool{}
	for _, d := range deletes {
		seenTables[d.table] = true
	}
	for table := range knownRetentionDefects {
		if !seenTables[table] {
			t.Errorf("knownRetentionDefects 里的 %q 已不存在于扫描结果（SQL 被删/改名，或谓词列变了）："+
				"要么删掉这条登记并把 expectedKnownRetentionDefects 调小，要么把新的真实缺陷登记进来。", table)
		}
	}
	if n := len(knownRetentionDefects); n != expectedKnownRetentionDefects {
		t.Errorf("knownRetentionDefects 有 %d 条，但棘轮 expectedKnownRetentionDefects = %d。\n"+
			"修好一条缺陷就删掉登记并调小棘轮；新增缺陷登记时必须同时调大棘轮。", n, expectedKnownRetentionDefects)
	}

	unindexed := map[string]string{}
	knownHit := map[string]bool{}
	for _, d := range deletes {
		if d.predicate == "ctid" {
			continue // 系统列无法建索引，见文件头
		}
		if indexed[d.table] != nil && indexed[d.table][d.predicate] {
			// 登记表里的缺陷可能已经被迁移修好了——这类"登记还在但已覆盖"必须
			// 报出来，否则登记会永远挂在文档里骗人。
			if _, isKnown := knownRetentionDefects[d.table]; isKnown {
				t.Errorf("%s 已补上以 %s 为首列的索引（%s），但它仍留在 knownRetentionDefects 里。\n"+
					"请删掉该登记并把 expectedKnownRetentionDefects 调小——过期的登记比没有登记更糟。",
					d.table, d.predicate, d.source)
			}
			continue
		}
		if _, ok := retentionSmallTables[d.table]; ok {
			continue
		}
		if _, isKnown := knownRetentionDefects[d.table]; isKnown {
			knownHit[d.table] = true
			continue
		}
		if _, seen := unindexed[d.table]; !seen {
			unindexed[d.table] = fmt.Sprintf("谓词列 %s 不是任何索引的首列（%s）", d.predicate, d.source)
		}
	}

	// 登记了但这一轮没命中（谓词列改了形态、或走了别的分支）也要说，别让登记悬空。
	for table := range knownRetentionDefects {
		if !knownHit[table] && !unindexedHas(unindexed, table) {
			if !seenTables[table] {
				continue // 上面已报"已不存在"
			}
			t.Logf("注意：登记的 %s 本轮未命中「无首列索引」判定（谓词形态可能已变），请复核", table)
		}
	}

	if len(unindexed) > 0 {
		names := make([]string, 0, len(unindexed))
		for k := range unindexed {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, name := range names {
			t.Errorf("保留期删除走全表扫描: %s —— %s\n"+
				"    修法二选一：给该表补一条以 %s 为首列的 btree 索引迁移；"+
				"或实测证明表小到 Seq Scan 合理，登记进 retentionSmallTables（必须带行数）；"+
				"若确为已知缺陷，登记进 knownRetentionDefects 并同时调大 expectedKnownRetentionDefects。",
				name, unindexed[name], retentionPredicateOf(deletes, name))
		}
	}

	// 已确认未修的缺陷逐条打印：门是绿的，但缺陷必须是可见的，不能因为"登记了所以不报"
	// 就从输出里消失。
	knownNames := make([]string, 0, len(knownHit))
	for k := range knownHit {
		knownNames = append(knownNames, k)
	}
	sort.Strings(knownNames)
	for _, name := range knownNames {
		t.Logf("已知未修：%s", knownRetentionDefects[name])
	}
	t.Logf("扫描 %d 条保留期 DELETE；新判红 %d 张；已知未修 %d 张；小表豁免 %d 张",
		len(deletes), len(unindexed), len(knownHit), len(retentionSmallTables))
}

func unindexedHas(m map[string]string, table string) bool {
	_, ok := m[table]
	return ok
}

func retentionPredicateOf(deletes []retentionDelete, table string) string {
	for _, d := range deletes {
		if d.table == table {
			return d.predicate
		}
	}
	return "?"
}

func dumpRetention(ds []retentionDelete) string {
	var b strings.Builder
	for _, d := range ds {
		b.WriteString("  " + d.source + " -> " + d.table + " / " + d.predicate + "\n")
	}
	return b.String()
}

func retentionDSN() string {
	for _, k := range []string{"D07_S01_PG_URL", "TEST_PG_URL", "LLM_GATEWAY_PG_URL", "TEST_DATABASE_URL"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
