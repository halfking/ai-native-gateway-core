package db

import (
	"strings"
	"testing"
)

// TestRecentSuccessRateExistsSQLShape 钉住 recent_success_rate 存在性探测
// SQL 的目录学形状（R69，ff317a231 的防复发钉桩）。
//
// 背景：该探测曾把 pg_proc 目录列名写成 pronargtypes（该列不存在，只有
// 计数列 pronargs），查询每轮必报 42703 → 错误分支 fnMissing=1 → 保守
// DROP+CREATE 路径每 boot 空转（2026-09-23 70e47c4f4 引入，09-27
// ff317a231 修复）。拼写错误存活四天的根因是这条 SQL 零钉桩——本测试
// 补上，防止下次手改目录查询时同类错误再次静默落入保守路径。
func TestRecentSuccessRateExistsSQLShape(t *testing.T) {
	// 签名断言必须走 proargtypes（oidvector：20=int8, 25=text, 23=int4），
	// 与 recent_success_rate(bigint, text, int, int) 逐位对应。
	if !strings.Contains(recentSuccessRateExistsSQL, "p.proargtypes = '20 25 23 23'::oidvector") {
		t.Error("exists probe must match the exact 4-arg signature via proargtypes oidvector '20 25 23 23'")
	}
	// pronargtypes 不是 pg_proc 的列（pronargs 才是计数列）——再次出现即回归。
	if strings.Contains(recentSuccessRateExistsSQL, "pronargtypes") {
		t.Error("pronargtypes does not exist in pg_proc; the probe would fail every call and degrade to drop+recreate each boot")
	}
	// 探测必须是 NOT EXISTS 形状（count(*) 0/1 语义），错误分支依赖 Scan 成功。
	if !strings.Contains(recentSuccessRateExistsSQL, "WHERE NOT EXISTS") {
		t.Error("probe must keep the NOT EXISTS shape the fnMissing branch relies on")
	}
}
