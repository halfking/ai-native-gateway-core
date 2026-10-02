//go:build !integration

package admin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// S4 v1 退役的读面门（2026-10-02，见审计文档 §9.21）。
//
// §9.21 扫描出 66 个「绕过视图直读 v1 宽族」的文件，其中 39 个读了
// session 臂恒 NULL 的 30 列补位集。那 39 个今天都能正常工作（读物理 v1，列齐全），
// **危险不在今天，在改指视图的那一天**：session 分臂的行会从这些列拿到 NULL，
// 而接口照样返回 200 —— 即 §9.18 修掉的「200 但全盲」那一类。
//
// 那 66/39 两个数字来自一次性扫描器（/tmp/v1scan、/tmp/padscan，不入仓库、无回归保护）。
// 这道门把那批文件变成**显式登记表**：新增一个「直读 v1 且读补位列」的读方必须登记
// 并写明理由，而登记项失效（文件改完或被删）时门会反过来报红。
//
// 为什么不是「禁止」：这些读方在 v1 还在的时候是**正确**的代码，全面禁止会把
// S4 之前的正常迭代也堵死。正确的形状是「默认未登记 = 需要有人拍板」，而不是
// 「一律不许」——与 request_logs_stop_write_classification_test.go 的具名论证同一范式。
//
// 口径精度（必须随登记表一起读）：本门只看 SQL 字面量的 FROM/JOIN 关系集合，
// 分不出 SELECT 与 UPDATE...FROM / ON CONFLICT，所以写路径也会被计入；
// 登记表里的多数条目是读方，但**不应**把 39 读成 39 条 SELECT 语句。

// v1DirectPaddedColumnReaders 登记「绕过视图直读 v1 且读了补位列」的读方。
// 键是仓库根相对路径，值是必须非空的理由——理由会随登记一起出现在失败输出里，
// 所以它不能是一句空话。统一指向审计文档 §9.21 的工作项清单。
// v1DirectPaddedColumnReader 是登记表的条目：cols 是该读方今天读到的补位列集合。
type v1DirectPaddedColumnReader struct {
	cols []string
	why  string
}

var v1DirectPaddedColumnReaders = map[string]v1DirectPaddedColumnReader{
	"admin/analytics.go":                       {cols: []string{"auto_profile", "client_model"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"admin/credential_success_rate.go":         {cols: []string{"client_model"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"admin/data_lifecycle_attachments.go":      {cols: []string{"attachments", "client_model"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"admin/diagnostics_credential.go":          {cols: []string{"client_model", "provider_id"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"admin/logs.go":                            {cols: []string{"client_model", "id"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"admin/memora_handlers.go":                 {cols: []string{"client_model"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"admin/probe_history.go":                   {cols: []string{"id"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"admin/provider_diagnose.go":               {cols: []string{"provider_id"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"admin/providers.go":                       {cols: []string{"id", "provider_id"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"admin/request_trace.go":                   {cols: []string{"client_model", "provider_id"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"admin/routing.go":                         {cols: []string{"client_model", "id"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"admin/session_tenant.go":                  {cols: []string{"gw_task_id"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"admin/swim_lane_init.go":                  {cols: []string{"client_model", "id", "provider_id"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"admin/tenants.go":                         {cols: []string{"client_model"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"admin/unified_detail.go":                  {cols: []string{"client_model", "gw_task_id"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"admin/work_types.go":                      {cols: []string{"client_model"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"autoroute/recommend_v2.go":                {cols: []string{"client_model", "model_chosen"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"bg/auto_index_refresher.go":               {cols: []string{"client_model", "id"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"bg/auto_route_settle_worker.go":           {cols: []string{"id"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"bg/credential_recovery.go":                {cols: []string{"client_model", "id", "provider_id"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"bg/credential_selfcheck.go":               {cols: []string{"client_model", "id"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"bg/model_probe.go":                        {cols: []string{"client_model", "id", "provider_id"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"bg/model_tier.go":                         {cols: []string{"client_model"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"bg/today_success_probe.go":                {cols: []string{"id"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"cmd/compression-bench/main.go":            {cols: []string{"id", "outbound_msg_count", "outbound_token_est"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"cmd/gateway/dual_read_validator.go":       {cols: []string{"request_type"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"cmd/gateway/main_v3_wiring.go":            {cols: []string{"outbound_msg_count", "outbound_msg_hashes", "outbound_token_est"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"cmd/gateway/output_compliance_control.go": {cols: []string{"api_key_owner_user", "owner_user"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"cmd/gateway/waterfall_by_request.go":      {cols: []string{"client_model"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"cmd/gateway/waterfall_db.go":              {cols: []string{"client_model"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"cmd/tools/validate_sessions_v2/loader.go": {cols: []string{"client_model", "provider_id"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"db/db.go":                      {cols: []string{"auto_profile", "client_model", "id", "provider_id"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"domains/analysis/optimizer.go": {cols: []string{"outbound_token_est"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"domains/credentialstate/popularity_tracker.go": {cols: []string{"client_model"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"domains/sessionforensics/export.go":            {cols: []string{"client_model"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"domains/streaming/model_alternatives.go":       {cols: []string{"id"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"internal/quality/minute_aggregator.go":         {cols: []string{"client_model", "provider_id"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"tests/session_audit/cmd/audit-test/main.go":    {cols: []string{"client_model"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
	"tests/test_popularity_tracker.go":              {cols: []string{"client_model"}, why: "S4 退出工作项（审计文档 §9.21 读面清单第 2 条）：绕过 canonical 视图直读 v1 宽族，且读了 session 臂恒 NULL 的补位列。今天行为正确，v1 退役前须改为视图读法（去掉补位列依赖，或改成带 session 侧等价落地的 COALESCE）。"},
}

// v1DirectTables 是「绕过视图直读」判定里的 v1 宽族关系名。
var v1DirectTables = map[string]bool{
	"request_logs": true, "request_logs_hot": true,
	"request_logs_bodies": true, "request_logs_bodies_hot": true,
}

const canonicalView = "request_logs_with_current_month"

func TestNoUnregisteredVPaddedColumnReader(t *testing.T) {
	files, err := goFilesUnder("..")
	if err != nil {
		t.Fatalf("walk repo root: %v", err)
	}
	fired := map[string]map[string]bool{} // file -> 读到的补位列
	for _, f := range files {
		rel := relToRepoRoot(f)
		for col := range paddedColumnsReadFromV1Direct(f) {
			if fired[rel] == nil {
				fired[rel] = map[string]bool{}
			}
			fired[rel][col] = true
		}
	}

	// 未登记 = 需要有人拍板。
	var unreg []string
	for f := range fired {
		if _, ok := v1DirectPaddedColumnReaders[f]; !ok {
			unreg = append(unreg, f)
		}
	}
	sort.Strings(unreg)
	for _, f := range unreg {
		cols := sortedKeys(fired[f])
		t.Errorf("%s: 新增了「绕过 %s 直读 v1 宽族、且读了补位列 %s」的读方。\n"+
			"它在 v1 存活期间是正确代码，但改指视图后 session 分臂会从这些列拿到 NULL 而接口返回 200。\n"+
			"若已评估过，请登记进 v1DirectPaddedColumnReaders 并写明理由（审计文档 §9.21）。",
			f, canonicalView, strings.Join(cols, ", "))
	}

	// 已登记的读方**多读一列**补位列，文件级判定看不见，但重写工作量会变。
	// 只钉「文件在不在表里」的门会漏掉这一类（变异验证实测：给 admin/analytics.go
	// 追加一个 client_model 过滤条件，门不响）。
	for f, e := range v1DirectPaddedColumnReaders {
		recorded := map[string]bool{}
		for _, c := range e.cols {
			recorded[c] = true
		}
		var grown []string
		for c := range fired[f] {
			if !recorded[c] {
				grown = append(grown, c)
			}
		}
		sort.Strings(grown)
		if len(grown) > 0 {
			t.Errorf("%s: 已登记读方新读了补位列 %s（登记集合为 %s）。\n"+
				"该文件不会出现在「新增读方」清单里，但 v1 退役前要重写的列又多了一列。\n"+
				"请同步更新 v1DirectPaddedColumnReaders 里的 cols。",
				f, strings.Join(grown, ", "), strings.Join(e.cols, ", "))
		}
	}

	// 反向：登记项失效也要报红，否则表会变成永不更新的占位。
	//
	// 注意 (路径, 原因) 必须成对收集再排序：早先的写法把原因串收进一个 slice、
	// 排序后拿 registryOrder()[i] 去配对——排的是**原因**不是**路径**，多条失效时
	// 会把原因报到别的文件头上。单条失效时恰好对，所以是靠运气过的。
	type staleEntry struct{ path, why string }
	var stale []staleEntry
	for f, e := range v1DirectPaddedColumnReaders {
		if strings.TrimSpace(e.why) == "" {
			t.Errorf("登记 %q 的理由是空的——空理由的登记等于没有登记", f)
		}
		if _, still := fired[f]; !still {
			stale = append(stale, staleEntry{f, "该位置不再触发本门（读方已改完、被删或不再读补位列）"})
			continue
		}
		var lost []string
		for _, c := range e.cols {
			if !fired[f][c] {
				lost = append(lost, c)
			}
		}
		if len(lost) > 0 {
			sort.Strings(lost)
			stale = append(stale, staleEntry{f, "登记了补位列 " + strings.Join(lost, ", ") + "，但该读方已不再读它们"})
		}
	}
	sort.Slice(stale, func(i, j int) bool { return stale[i].path < stale[j].path })
	for _, e := range stale {
		t.Errorf("登记 %q 已失效：%s。\n"+
			"请同步更新 v1DirectPaddedColumnReaders，别让它继续占位。", e.path, e.why)
	}

	t.Logf("v1 直读 + 读补位列的读方：%d 个（其中已登记 %d，未登记 %d）",
		len(fired), len(v1DirectPaddedColumnReaders), len(unreg))
}

// paddedColumnsReadFromV1Direct 返回该文件里「以 v1 宽族为源、且同一字面量里没有
// canonical 视图」的补位列引用。
func paddedColumnsReadFromV1Direct(path string) map[string]bool {
	out := map[string]bool{}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return out // 构建会报解析错误，不是本门的事
	}
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		v, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		body := stripSQLLineComments(v)

		hasV1, hasView := false, false
		for _, m := range fromJoinRE.FindAllStringSubmatch(body, -1) {
			rel := strings.ToLower(m[1])
			last := rel[strings.LastIndexByte(rel, '.')+1:]
			if v1DirectTables[last] {
				hasV1 = true
			}
			if last == canonicalView {
				hasView = true
			}
		}
		if !hasV1 || hasView {
			return true
		}
		for col := range sessionArmNullPaddedColumns {
			if len(findColumnRefs(body, col)) > 0 {
				out[col] = true
			}
		}
		return true
	})
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
