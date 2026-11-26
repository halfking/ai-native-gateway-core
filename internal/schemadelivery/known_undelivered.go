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
	"bg/systemmonitor/audit.go": {
		"system_probe_runs": {
			Since: "2026-11-26",
			Reason: "存量缺口：探针运行审计的写侧 INSERT 仍写 total_tokens；定义它的 sql/migrations/domain/346_system_probe_run_tokens.sql " +
				"不在两条通道里 ⇒ 由仓内投递建出来的库上每次审计写入都 42703（2026-11-26 空库实测），而写侧是 best-effort" +
				"（bg/systemmonitor/monitor.go 的 writeAudit）⇒ 探针审计行静默丢失。读侧的同一列 2026-10-20 已因 42703 改掉" +
				"（admin/systemmonitor_handlers.go 的注释记着当时真库的列）。出路：把 346 搬进投递通道并登记，或写侧不再写这一列。",
			Cols: []string{"total_tokens"},
		},
	},
	"domains/toolexecution/postgres_store.go": {
		"tool_usage_stats": {
			Since: "2026-11-26",
			Reason: "存量缺口：本文件按 sql/migrations/domain/134_tool_execution.sql 的形状读写（tool_name／date／total_calls…），" +
				"而投递出来的表是新装基线的形状（tool_id／usage_date／call_count…，sql/schema/01-schema.sql）；134 不在两条通道里" +
				"⇒ 空库实测 42703（column \"tool_name\" does not exist）。出路：二选一——把 134 的列变更搬进投递通道并登记，" +
				"或把本文件改回基线形状。",
			Cols: []string{
				"avg_duration_ms", "date", "failed_calls", "p50_duration_ms", "p95_duration_ms", "p99_duration_ms",
				"success_calls", "timeout_calls", "tool_name", "top_users", "total_calls", "unique_sessions", "unique_users",
			},
		},
	},
	"tenantops/handler.go": {
		"licenses": {
			Since: "2026-11-26",
			Reason: "存量缺口：加列件 sql/migrations/startup/up/389_tenant_ops_scope.sql 不在两条通道里，新装基线的 licenses 也没有 tenant_id" +
				"⇒ 空库实测 42703；licenseStatus 已在 cmd/gateway/main.go 接线（GET /api/tenant/license/status）。" +
				"出路：把 389 搬进投递通道并登记，或下线这条查询。",
			Cols: []string{"tenant_id"},
		},
		"releases": {
			Since: "2026-11-26",
			Reason: "存量缺口：同上一条（389_tenant_ops_scope.sql 未投递，新装基线的 releases 没有 tenant_id）⇒ 空库实测 42703；" +
				"updateCheck 已在 cmd/gateway/main.go 接线（GET /api/tenant/autoupdate/check）。出路同上。",
			Cols: []string{"tenant_id"},
		},
	},
	// ── 上游 W4.3 新特性的投递面缺口（2026-11-26 审计轮登记，待 owner）────────
	"admin/channels.go": {
		"channel_users": {
			Since: "2026-11-26",
			Reason: "★ 上游 W4.3（07101e72c3）的投递面缺口：★ 它的启动迁移 " +
				"sql/migrations/startup/492_channels_customers.sql 【不在任何投递通道里】" +
				"（★ 211 通道 scripts/apply-db-revision-sequence.sh 的 files=() 无、★ 安装器清单 " +
				"sql/schema/installed_startup_migrations.tsv 无、★ installer/cmd/llm-gw-installer/embeddata/startup 无、★ " +
				"installer/migrations 无 ★）⇒ ★⇒⇒ 部署与新装都不会建出这张表 ⇒ ★ 本文件对它的引用在真库上恒 42703。" +
				"★ 2026-11-26 只读实测：154 生产库 public.schema_migrations 无 '492' 行、public.channel_users 不存在。" +
				"★★ 而【两道投递通道覆盖门都没拦住它】：★ 它们只断言【最大编号附近的近邻窗口】（★ 当前最大号 999）" +
				"⇒ ★ 492 远在窗口外 ⇒ ★★ 门根本没看它（★ 这就是「拿旧编号当新迁移的编号」会绕过门的原因 ★★）。" +
				"★★ 出路：① 把 492 搬进四条投递通道并登记（★ 属 schema 变更、★ owner 决定 ★★）；" +
				"② 或本文件不再引用这张表（★ 则删掉本条登记 ★★）。" +
				"★ 本轮不代做：★ 迁移动作＝生产语义变更、★ 需 owner 明确 ★★。",
			Cols: []string{"", "channel_id", "user_id"},
		},
		"customers": {
			Since: "2026-11-26",
			Reason: "★ 同上：★ 上游 W4.3 的 492_channels_customers.sql 不在任何投递通道里 ⇒ ★ 部署与新装都不会建出这张表 " +
				"⇒ ★ 本文件对它的引用在真库上恒 42703（★ 2026-11-26 只读实测：154 生产库 public.customers 不存在 ★）。" +
				"★★ 出路与判据同 admin/channels.go → channel_users 那一条：★ ① 搬进投递通道（owner 决定）或 ② 改代码不再引用 ★★。",
			Cols: []string{"", "channel_id", "code", "created_at", "enabled", "id", "name"},
		},
	},
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
