package startup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestMigration529RepairContract(t *testing.T) {
	up, err := os.ReadFile("529_repair_shared_pg_sticky_and_bodies_2026_07.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("529_repair_shared_pg_sticky_and_bodies_2026_07.down.sql")
	if err != nil {
		t.Fatal(err)
	}

	upSQL := string(up)
	for _, required := range []string{
		"HAVING count(*) > 1",
		"RAISE EXCEPTION '529: sticky_sessions contains",
		"ADD CONSTRAINT uq_sticky_sessions_sticky_key UNIQUE (sticky_key)",
		"PARTITION OF public.request_logs_bodies",
		"FOR VALUES FROM ('2026-07-01 00:00:00+08')",
		"TO ('2026-08-01 00:00:00+08')",
		"pg_get_expr(c.relpartbound, c.oid)",
		"sticky_key unique conflict arbiter was not created",
	} {
		if !strings.Contains(upSQL, required) {
			t.Errorf("migration 529 missing fail-closed contract %q", required)
		}
	}

	downSQL := string(down)
	if !strings.Contains(downSQL, "intentionally non-destructive") ||
		strings.Contains(downSQL, "DROP TABLE") || strings.Contains(downSQL, "DROP CONSTRAINT") {
		t.Error("migration 529 down must preserve the forward-compatible schema")
	}
	if strings.Contains(upSQL, "DELETE FROM public.sticky_sessions") {
		t.Error("migration 529 must not delete duplicate sticky data")
	}
}

// goStringLiterals 返回 Go 文件里所有**字符串字面量**的文本。
//
// 为什么用 AST 而不是按行剥注释：Go 字符串字面量里可以含 "//"（URL、SQL 里的
// `--` 注释），按行剥会把字面量截断。走 AST 的另一个好处是**注释天然不在字面量
// 里** ⇒ 判据只可能命中真正下发给 PostgreSQL 的语句，不会被源码里的退役说明
// 文本顶成永绿。
//
// 解析失败按 t.Fatal 处理 —— 这道门宁可红在「读不到」，也不要静默放行。
func goStringLiterals(t *testing.T, path string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败 %v —— 这道门不能在没有源码的情况下判定为通过", path, err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		if s, err := strconv.Unquote(bl.Value); err == nil {
			out = append(out, s)
		}
		return true
	})
	return out
}

// TestV1BodyTTLContractIsRetiredWithTheFamily 取代原先的
// TestMigration529HistoricalPartitionIsSubjectToBodyTTL。
//
// ── 退役了什么 ────────────────────────────────────────────────────────────
// v1 正文族（`_request_logs_bodies`）的「7 天 TTL 清理」契约：
//
//	① 正典本体 `sql/objects/functions/drop_old_request_logs_bodies_partitions_integer.sql`
//	   （随族一起被 890 删除）；
//	② `bg/partition_manager.go` 里每 tick 调它的那个 Go 函数与调用点
//	   （旧实现里它**没有**退役闸 ⇒ 是 890 之后唯一一条裸奔的 v1 调用点：
//	   每 tick 一次 42P01）。
//
// ── 为什么退役 ────────────────────────────────────────────────────────────
// 迁移 890 把 v1 族（表 + 分区 + 序列 + 只服务它的分区管理函数）整体 DROP ⇒
// 那个 SQL 函数已不存在，继续调用必然 42P01；而族没了，**也没有对象可清**。
//
// ── 现在由什么承担 ────────────────────────────────────────────────────────
// 会话族正文（`session_bodies`）走两条**不同**的链：
//
//	· hot → 月分区：`promote_session_bodies_hot_to_partition`（614，在
//	  bg/partition_manager.go 的 promoteSpecs 内）；它只搬运、不删除；
//	· 月分区过期 DROP：迁移 841 的**通用**月度分区保留机制
//	  （`llm_gateway_drop_expired_month_partitions()`，配置表
//	  `llm_gateway_partition_retention`）。
//	⚠ 如实记录：841 的配置表**出厂为空**、全仓无 INSERT ⇒ 默认不删任何月分区
//	  ⇒ v1 那条 7 天正文 TTL **没有等价物**（会话族月分区目前只增不减）。
//	  这是**遗留项**，不是本门能替业务决定的事。
//
// ── 判据（每条负向断言配阳性对照）────────────────────────────────────────
//
//	· 正典文件必须**已不存在**（否则说明 890 没删干净）；
//	· partition_manager.go 的**字符串字面量**里不得再出现该函数名/该设置键
//	  （SQL 只可能经字面量下发；注释不在字面量里 ⇒ 退役说明不会把门顶成永绿）；
//	· 迁移 890 的 up 必须真的 DROP 它 —— 阳性对照：退役由**迁移**承载，
//	  而不是只在 Go 侧悄悄不调（少一半的退役是半个退役）。
func TestV1BodyTTLContractIsRetiredWithTheFamily(t *testing.T) {
	const canonicalPath = "../../../sql/objects/functions/drop_old_request_logs_bodies_partitions_integer.sql"
	if _, err := os.Stat(canonicalPath); err == nil {
		t.Errorf("正典 %s 仍存在 —— v1 正文族的 TTL 清理函数应随族一起退役（890 已 DROP 它）", canonicalPath)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", canonicalPath, err)
	}

	literals := goStringLiterals(t, "../../../bg/partition_manager.go")
	for _, lit := range literals {
		if strings.Contains(lit, "drop_old_request_logs_bodies_partitions") {
			t.Errorf("partition_manager.go 的字符串字面量里仍在下发 drop_old_request_logs_bodies_partitions —— 890 已 DROP 该函数，890 之后每 tick 必然 42P01")
		}
		if strings.Contains(lit, "lifecycle.request_logs_bodies_ttl_days") {
			t.Errorf("partition_manager.go 的字符串字面量里仍在读 lifecycle.request_logs_bodies_ttl_days —— 该键服务的对象（v1 正文族）已被 890 删除")
		}
	}
	// 阳性对照：字面量扫描必须看得见活着的会话族正文排水函数 ⇒ 扫描器没坏、
	// 上面两条负向断言不是「什么都没查」。
	foundLive := false
	for _, lit := range literals {
		if strings.Contains(lit, "promote_session_bodies_hot_to_partition") {
			foundLive = true
			break
		}
	}
	if !foundLive {
		t.Error("阳性对照失败：字面量扫描找不到 promote_session_bodies_hot_to_partition —— 判据本身失效（负向断言会假绿）")
	}

	// 阳性对照之二：退役由**迁移 890** 承载（不是只在 Go 侧悄悄不调）。
	up, err := os.ReadFile("890_drop_request_logs_v1_family.sql")
	if err != nil {
		t.Fatalf("read migration 890: %v", err)
	}
	upSQL := string(up)
	for _, required := range []string{
		"DROP FUNCTION IF EXISTS public.drop_old_request_logs_bodies_partitions(int)",
		"DROP TABLE IF EXISTS public.%I", // v1 族表（含 _request_logs_bodies）被删
	} {
		if !strings.Contains(upSQL, required) {
			t.Errorf("迁移 890 缺少 %q —— v1 族的退役必须由迁移承载，否则升级路径与新装路径的库形态会分叉", required)
		}
	}

	// 529 自身的文件契约仍然有效（文件作为历史留在仓库里）：
	// 529 修的是 2026-07 分区的边界，它必须把自己修的边界写出来。
	migration, err := os.ReadFile("529_repair_shared_pg_sticky_and_bodies_2026_07.sql")
	if err != nil {
		t.Fatalf("read migration 529: %v", err)
	}
	if !strings.Contains(string(migration), "expected_bound CONSTANT TEXT") {
		t.Error("migration 529 must document the repaired partition bounds")
	}
}
