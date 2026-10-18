package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// getLogSplitORContract —— 2026-10-18 S4 停写回归（154 生产实证）的 SQL 形态钉子。
//
// 缺陷形态：getLog 曾把 `(rl.request_id = $1 OR rl.client_request_id = $1)`
// 写成单条谓词。session 家族（734 视图的 session 分支）没有 client_request_id
// 索引，OR 无法拆成 BitmapOr，planner 对 `(request_id = X OR client_request_id
// = X)` 只能放弃 idx_session_turns(_hot)_request，退化为对全部月分区的顺序
// 扫描（154 实测 ~100 万行/3.8GB，45s 跑不完）。S4 停写后新请求只落 session
// 家族，于是 /api/logs/:id 对每个新请求 30s 超时 500，request-detail 页
// （mode=request&tab=overview）核心数据全空。
//
// 修复形态：两段顺序查询（与 unified_detail.loadRequestLogMeta 的
// 2026-08-27 优化同构），各自纯等值谓词可下推进视图全部分支走索引；
// client_request_id 腿的索引由迁移 874 补齐。
//
// 为什么钉源码文本而不是行为：h.db 是具体 *pgxpool.Pool，pgxmock 不执行
// SQL ⇒ 行级判据对 SQL 形态改动是假绿（AGENTS 判据纪律）。本钉子锚在
// SQL 文本层，与被测对象（发给 PG 的查询形态）同层。
func getLogFileText(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("logs.go"))
	if err != nil {
		t.Fatalf("read logs.go: %v", err)
	}
	return string(raw)
}

func TestGetLogSplitORTwoSequentialLegs(t *testing.T) {
	src := getLogFileText(t)

	// 腿 1 必须先按网关侧 request_id 查（唯一标识、视图全分支有索引）。
	if !strings.Contains(src, `scanDetailRow("rl.request_id = $1")`) {
		t.Fatal("getLog 腿 1 缺失：必须先以纯等值 rl.request_id = $1 查询（OR 形态会让 " +
			"planner 放弃 session 家族索引，154 实测全分区顺序扫描 >30s 超时）")
	}
	// 腿 2 必须在腿 1 未命中时按 client_request_id 兜底。
	if !strings.Contains(src, `scanDetailRow("rl.client_request_id = $1")`) {
		t.Fatal("getLog 腿 2 缺失：client_request_id 兜底查询必须保留（客户端自带 id 的详情页入口）")
	}
	// 兜底只在腿 1 ErrNoRows 时执行——保序语义（request_id 命中优先）由拆分天然保持。
	if !strings.Contains(src, "if errors.Is(err, sql.ErrNoRows) {\n\t\t// 腿 2") {
		t.Fatal("getLog 腿 2 必须挂在腿 1 的 errors.Is(err, sql.ErrNoRows) 分支下；" +
			"无条件并行/顺序执行会破坏 request_id 命中优先序，或让每次命中都多付一条查询")
	}
}

func TestGetLogORMustNotComeBack(t *testing.T) {
	src := getLogFileText(t)
	if strings.Contains(src, "rl.request_id = $1 OR rl.client_request_id = $1") {
		t.Fatal("getLog 禁止回到单条 OR 谓词：session 家族无 client_request_id 索引时 OR 会退化为 " +
			"session_turns 全分区顺序扫描（S4 停写后 /api/logs/:id 对新请求 100% 30s 超时，" +
			"2026-10-18 154 生产实证）。若要恢复合并形态，必须先让两条谓词都有索引 + EXPLAIN 证明")
	}
}

func TestGetLogKeepsTenantGateAndTsOrder(t *testing.T) {
	src := getLogFileText(t)
	if !strings.Contains(src, "($2 OR rl.tenant_id = $3)") {
		t.Fatal("getLog 拆分后必须保留租户门 ($2 OR rl.tenant_id = $3)——漏掉即跨租户泄露")
	}
	if !strings.Contains(src, "ORDER BY rl.ts DESC") {
		t.Fatal("getLog 拆分后段内必须保持 ORDER BY rl.ts DESC（原 CASE 优先序由两段顺序等价保持）")
	}
}
