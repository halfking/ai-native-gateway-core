package startup

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 跨面 SELECT * 禁令（2026-10-06，审计 §10.48）
//
// 背景：`X_hot` 与其分区父表 `X` 是两张**独立演进的物理表**，`UNION ALL`
// 两侧靠**位置**对齐。§10.46 实测二者的列序确实不同：
//
//	session_turns_hot 106 列 vs session_turns 106 列 → 45 个位置的名字不同
//	request_logs_hot 156 列 vs request_logs 154 列 → 66 个位置的名字不同
//	usage_ledger_hot  25 列 vs usage_ledger  20 列 →  6 个位置的名字不同
//
// 所以一旦有人写出 `SELECT * FROM x_hot UNION ALL SELECT * FROM x`，
// 正确性就完全押在「两侧列序恰好相同」这个从未被任何门记录的巧合上。
//
// 本门把该巧合从「约定」降级为「事实检查」：新建的跨面视图必须逐列枚举。
// 已存在的历史写法进 legacyCrossSurfaceSelectStar 登记表钉死，并由
// TestLegacyCrossSurfaceSelectStarRegistryIsExact 反向守住，防止登记表
// 空转或写错名字后让本门恒绿。
//
// 登记的是**迁移文件**（一次性应用、已被后续显式定义取代），不是活的视图。
// 生产侧 11 个 `*_with_current_month` 视图的现状核验（列序 + 是否仍为 star）
// 记在审计 runbook §10.48，因为 Go 测试连不上生产库。

var legacyCrossSurfaceSelectStar = map[string]string{
	"request_logs_with_current_month":           "341_hot_table_independence.sql（已被 680 显式定义取代）",
	"usage_ledger_with_current_month":           "344_usage_ledger_hot_independence.sql（已被后续显式定义取代）",
	"request_wal_with_current_month":            "345_request_wal_hot_independence.sql（已被 213 fix 显式定义取代）",
	"routing_decision_log_with_current_month":   "346_routing_decision_log_hot_independence.sql",
	"credential_model_index_with_current_month": "347/354_credential_model_index_hot_independence.sql",
	"tool_usage_stats_with_current_month":       "348_tool_usage_stats_hot_independence.sql",
	"credit_ledger_with_current_month":          "349_credit_ledger_hot_independence.sql",
	"request_logs_bodies_with_current_month":    "353_request_logs_bodies_hot_independence.sql",
	"model_probe_runs_with_current_month":       "386_model_probe_runs_hot_independence.sql",
	"candidate_failure_logs_with_current_month": "392_candidate_failure_logs_monthly_partition.sql",
	"session_bodies_unified":                    "625_session_bodies_unified_explicit.down.sql（回滚路径，故登记）",
}

var (
	reSelectStar  = regexp.MustCompile(`(?i)select\s+\*`)
	reUnionAll    = regexp.MustCompile(`(?i)\bunion\s+all\b`)
	reHotRelation = regexp.MustCompile(`(?i)\b([a-z_][a-z0-9_]*_hot)\b`)
	reCreateView  = regexp.MustCompile(`(?i)create\s+(or\s+replace\s+)?view\s+([a-z0-9_.]+)`)
	reLineComment = regexp.MustCompile(`--[^\n]*`)
	reBlockCmt    = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

// sqlWithoutComments 把注释剥掉，否则注释里的 `SELECT *` 会把门变成恒假
// （§10.46 的 625 与其 down 文件都因为注释命中而被误判过）。
func sqlWithoutComments(src string) string {
	src = reBlockCmt.ReplaceAllString(src, " ")
	return reLineComment.ReplaceAllString(src, " ")
}

// crossSurfaceSelectStar 返回「语句文本 → 视图名」的违规集合。
func crossSurfaceSelectStar(t *testing.T) map[string]string {
	t.Helper()
	found := map[string]string{}
	for _, root := range []string{".", filepath.Join("..", "..", "schema")} {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".sql") {
				return nil
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("read %s: %v", path, readErr)
			}
			for _, stmt := range strings.Split(sqlWithoutComments(string(raw)), ";") {
				if !reSelectStar.MatchString(stmt) || !reUnionAll.MatchString(stmt) {
					continue
				}
				// 命中任一 `_hot` 且同语句内出现其父表名 ⇒ 跨面 UNION ALL。
				for _, hot := range reHotRelation.FindAllStringSubmatch(stmt, -1) {
					if !regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(hot[1][:len(hot[1])-4]) + `\b`).MatchString(stmt) {
						continue
					}
					name := "(anonymous)"
					if m := reCreateView.FindStringSubmatch(stmt); m != nil {
						name = m[2]
					}
					// 去掉 schema 前缀，否则 `public.x` 与登记表里的 `x` 对不上
					// （§10.48 首次运行即因此误报，且反向门同时报「登记条目失配」）。
					if i := strings.LastIndex(name, "."); i >= 0 {
						name = name[i+1:]
					}
					found[strings.TrimSpace(stmt)+" @@ "+path] = name
					break
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	return found
}

func TestHotSurfaceViewsMustEnumerateColumns(t *testing.T) {
	for stmt, view := range crossSurfaceSelectStar(t) {
		if why, ok := legacyCrossSurfaceSelectStar[view]; ok {
			_ = why
			continue
		}
		t.Errorf("跨面视图 %s 使用了 SELECT * UNION ALL —— %s 与其分区父表按位置对齐，"+
			"两侧列序实测不同（§10.46）。请逐列枚举，勿依赖列序巧合。\n语句：%s",
			view, strings.TrimSuffix(view, "_with_current_month"), stmt)
	}
}

// 反向门：登记表里每一条都必须真的命中，否则登记表会因改名/删除而空转，
// 让上面的门在「实际仍有违规」时也报绿。
func TestLegacyCrossSurfaceSelectStarRegistryIsExact(t *testing.T) {
	matched := map[string]bool{}
	for _, view := range crossSurfaceSelectStar(t) {
		matched[view] = true
	}
	for view := range legacyCrossSurfaceSelectStar {
		if !matched[view] {
			t.Errorf("登记表条目 %q 在 sql/ 下已无对应写法（迁移被删或已改写）—— "+
				"请确认后删除该条目，否则本门失去意义", view)
		}
	}
}
