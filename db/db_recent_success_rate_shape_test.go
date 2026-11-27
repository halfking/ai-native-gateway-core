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
	// ★ 2026-11-28（42P01 噪声收口轮）：探测必须含**函数体检查**——
	//   pg_get_functiondef 体内不得含 request_logs_hot。背景：老二进制的
	//   boot ensure 会把 406 形态函数体（FROM request_logs_hot）写回库；
	//   890 删族后该函数体被调用即 42P01（CROSS JOIN LATERAL ⇒ 候选查询
	//   全失败）。只查签名时坏函数体永久存活；查体后 fnMissing 走
	//   DROP+CREATE，同 boot 自愈（ensure 每 boot CREATE OR REPLACE 现役体）。
	if !strings.Contains(recentSuccessRateExistsSQL, "position('request_logs_hot' in pg_get_functiondef(p.oid)) = 0") {
		t.Error("probe must also verify the function body no longer references request_logs_hot (406-shape body from an old binary must be self-healed at boot, not kept by a signature-only match)")
	}
}
