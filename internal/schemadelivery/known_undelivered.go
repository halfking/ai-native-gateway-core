package schemadelivery

import "sort"

// KnownUndelivered 是「生产 SQL 引用了未投递对象」的已知欠账台账：逐条登记，带理由与处置方向。
//
// 门对命中的登记不红，但两头都核账（与仓里其它棘轮台账是同一个纪律）：
//
//   - 没登记的引用 ⇒ 红（Result.References 非空）；
//   - 登记了却不再被命中 ⇒ 红（Result.Stale 非空）——问题修好了就必须回来删登记，防免死金牌。
//
// 登记一条不是「认可它没问题」，而是「已经有人看过、写下了它为什么还在」。每条的出路只有两种：
// 把对象的定义搬进投递通道并登记（这是 schema 变更，owner 决定），或改代码不再引用它。
//
// 键是「文件 ⇒ 表 ⇒ 列」，不含行号（行号会漂；对象与归属文件才是事实）。
// 台账按对象精确到列：同一文件里新引用的别名列照样红。
type KnownEntry struct {
	Since  string   // 登记日期
	Reason string   // 它为什么还在、证据在哪、出路是什么
	Cols   []string // 该文件里允许引用的未投递列（已排序）
}

var KnownUndelivered = map[string]map[string]KnownEntry{
	// ── 死代码 ─────────────────────────────────────────────────────────────────
	"bg/bandit_flusher.go": {
		"credentials": {
			Since: "2026-11-26",
			Reason: "死代码：Bandit 选路 2026-09-19 已删除（cmd/gateway/main.go 同位置注释：自 2026-06-26 起从未在生产接线，" +
				"R28 审计 #20 判死代码后整体移除），本文件是那次移除的漏网件，bg.NewBanditFlusher 没有生产调用方。" +
				"它写的 13 列只在 sql/migrations/domain/033_bandit_scoring.sql 里有定义、任何通道都不建（空库实测 42703）" +
				"⇒ 一旦被重新接线就恒失败。出路：删除本文件及其测试，然后删本条登记。",
			Cols: []string{
				"bandit_alpha", "bandit_beta", "failure_requests", "last_quota_update", "last_rate_limit_hit",
				"last_scored_at", "quota_remaining", "quota_total", "rate_limit_hits", "rate_limit_penalty",
				"success_requests", "total_latency_ms", "total_requests",
			},
		},
	},
	// ── 有意的双形状写侧 ────────────────────────────────────────────────────────
	"domains/promptinjection/detector.go": {
		"prompt_injection_detections": {
			Since: "2026-11-26",
			Reason: "有意的双形状：fleet 里这张表的形状真实分歧（211 是正典、没有这四列；245/154 的活表带 315 的四列），" +
				"写侧在 boot 期探测列集后于正典 INSERT 与 insertDetectionSQLLegacyCols 之间二选一；" +
				"四列在 sql/schema/fresh_install_column_gaps.tsv 另有登记。空库（正典）上走不到这条分支。" +
				"出路：待 fleet 形状收敛后删掉旧形状写侧与本条登记。",
			Cols: []string{"detection_layers", "detection_score", "matched_rules", "matched_rules_count"},
		},
	},
	// ── 存量缺口（待 owner：投递迁移，或改代码）─────────────────────────────────
	"admin/channels.go": {
		"customers": {
			Since: "2026-11-26",
			Reason: "存量缺口（★ 上游 W4.3 渠道商车道引入，★ 非本车道）：本文件的渠道/客户读端 SQL 读 customers 表，" +
				"而定义它的 sql/migrations/startup/492_channels_customers.sql **不在两条投递通道里**（仓内 grep 无任何通道点名它）" +
				"⇒ ★ 实测（2026-11-26，211 生产库 acc-pg17/llm_gateway）：表不存在 ⇒ ⇒ 这些端点在部署后会报 **42P01（表不存在）**。" +
				"★ 出路：把 492 搬进投递通道并登记（这是 schema 交付决策，owner 决定），或读端改读已有形状。" +
				"★ **该修法属 W4.3 车道的产品口径，本车道只登记不代决策**。" +
				"★ 处置：本条登记不阻断部署（scripts/deploy-test-gate.sh 的包清单不含 scripts/ursmcheck ⇒ 交付门不在部署闸门内；" +
				"且 211 本轮部署已实测通过）。",
			// ★ 空串 = **整表**登记：本文件有 4 处是整表引用（SELECT 1 FROM customers /
			//   SELECT id FROM customers / FROM customers），不带列名 ⇒ 逐列登记匹配不上
			//   ⇒ 台账按「文件⇒表⇒列」匹配（knownEntry），整表引用需要空串这一项。
			Cols: []string{"", "channel_id", "code", "created_at", "enabled", "id", "name"},
		},
		"channel_users": {
			Since: "2026-11-26",
			Reason: "存量缺口（★ 上游 W4.3 渠道商车道引入，★ 非本车道）：同 492_channels_customers.sql 建出的渠道-用户关联表，" +
				"同样不在投递通道里 ⇒ 部署后读端 42P01。出路与 customers 同条：把 492 搬进投递通道并登记，或读端改读已有形状。" +
				"★ 本车道只登记不代决策。",
			Cols: []string{"", "channel_id", "user_id"},
		},
		"request_logs": {
			Since: "2026-11-26",
			Reason: "存量缺口（★ 上游 W4.3 渠道商车道引入，★ 非本车道）：本文件的渠道统计与客户会话两个读端 SQL 读 request_logs，" +
				"而定义它的 sql/migrations/startup/000_base_tables.sql **不在两条投递通道里**（仓内 grep 无任何通道点名它）" +
				"⇒ ★ 实测（2026-11-26，211 生产库 acc-pg17/llm_gateway）：information_schema 里 request_logs **表不存在**，" +
				"只有视图 request_logs_with_current_month ⇒ ⇒ 这两个端点在部署后会报 **42P01（表不存在）**。" +
				"★ 注意：请求日志的真实表由 341_hot_table_independence.sql 的架构改造承载（热表 request_logs_hot ＋ 分区表 request_logs 异步承接），" +
				"而 request_logs 本身正是那次改造要淘汰的旧家 ⇒ **出路不是「把 000_base_tables 搬进通道」（那会把已淘汰的形状重新投递）」，" +
				"而是**读端改读 request_logs_hot / 已有视图**（并按 651_provider_quality_hot_rollup.sql 已固化的列映射口径对齐：" +
				"created_at → ts、status_code → upstream_status_code 等）⇒ ★ **该修法属 W4.3 车道的产品口径，本车道只登记不代决策**。" +
				"★ 处置：本条登记不阻断部署（scripts/deploy-test-gate.sh 的包清单不含 scripts/ursmcheck ⇒ 交付门不在部署闸门内；" +
				"且 211 本轮部署已实测通过）。",
			Cols: []string{"created_at"},
		},
	},
	// ── 存量缺口三组（2026-11-26 存量缺口收口轮 · 第 31 轮已修 ⇒ 登记已删）──────
	// ★ 历史（本轮之前的三条登记，★ 本轮修完 ⇒ 按「两头核账」的规则删掉登记）：
	//   ① bg/systemmonitor/audit.go → system_probe_runs.total_tokens
	//      （346 那份件躺在 sql/migrations/domain/ ⇒ 不被任何通道消费 ⇒ 列从没到过库）
	//   ② domains/toolexecution/postgres_store.go → tool_usage_stats 的 13 列
	//      （134 那份件不在任何通道里；仓内权威形状是 335/348 那套 tool_id 口径）
	//   ③ tenantops/handler.go → licenses.tenant_id / releases.tenant_id
	//      （389 那份件躺在 sql/migrations/startup/up/ ⇒ ★ 那个目录整体不被任何通道消费）
	// ★ 本轮的修法（owner 2026-11-26 决定：都走投递迁移）：
	//   ① → 999b_system_probe_run_tokens.sql（列 ＋ 读侧聚合同批恢复）
	//   ② → 999c_tool_usage_stats_columns.sql（13 列 ＋ tool_id/tenant_id/usage_date 去 NOT NULL
	//        ＋ hot 表 UNIQUE (tool_name, date)）
	//   ③ → 999d_tenant_ops_scope.sql（两列 ＋ 两索引）
	// ★ 三条都已进两条投递通道 ⇒ 「已投递对象」里就有这些列 ⇒ 门不再命中
	//   ⇒ **按两头核账的规则删登记**（登记了却不再被命中＝失效登记，门会红）。
	// ★ 保留这段历史注释，是为了让下一个读的人知道「这里曾经有过三条欠账、
	//   怎么修的、为什么删」—— 免得同一个缺口再被当成新发现。
}

// knownEntry 查一处引用是否在台账里（精确到列）。
func knownEntry(file string, o Object) (KnownEntry, bool) {
	e, ok := KnownUndelivered[file][o.Table]
	if !ok {
		return KnownEntry{}, false
	}
	for _, c := range e.Cols {
		if c == o.Column {
			return e, true
		}
	}
	return KnownEntry{}, false
}

// staleKnown 返回台账里已经不被任何引用命中的条目（「文件 表.列」）。
func staleKnown(known []Reference) []string {
	type key struct{ file, table, col string }
	hit := map[key]bool{}
	for _, r := range known {
		hit[key{r.File, r.Object.Table, r.Object.Column}] = true
	}
	var stale []string
	for file, tables := range KnownUndelivered {
		for table, e := range tables {
			for _, c := range e.Cols {
				if !hit[key{file, table, c}] {
					stale = append(stale, file+" "+table+"."+c)
				}
			}
		}
	}
	sort.Strings(stale)
	return stale
}
