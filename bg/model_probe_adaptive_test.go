package bg

import (
	"testing"
)

// ⚠️ 本文件 2026-10-13 整体重写。原来两个「测试」都是**文档冒充判据**：
//
//	TestBackoffV2_RecoveryLadder  建了一张期望表，然后 `_ = tc` 把它丢掉，
//	                               最后只剩一句 t.Log —— 期望值从未和任何东西比较过。
//	TestPassiveBoost_TriggersEarlyProbe  整个函数体只有一行 t.Log。
//
// 而更糟的是：那张被丢弃的表**7 行错了 4 行**。它把阶梯写成只依赖
// failures，实际**同时依赖 age**。下面是 2026-10-13 在 245 活库上**实测**
// 出来的真值（不是从文档抄的，也不是从函数签名推的）：

//	failures  age          实测间隔     旧注释声称
//	────────  ───────────  ──────────  ────────────────
//	0         any          7200s (2h)   7200s  ✅
//	1         1min         120s         10s    ❌
//	2         1min         300s         30s    ❌
//	2         20min        600s         —（表里没有 age 这一维）❌
//	2         45min        900s         —
//	2         120min       2700s        —
//	3         any          3600s        60s    ❌
//	4         any          3600s        120s   ❌
//	5         any          3600s        300s   ❌
//	6+        any          3600s        3600s  ✅
//
// ⇒ 那张表因为被 `_ = tc` 丢弃，所以**没人发现它是错的**。
//   一个从不参与比较的期望值，错了也不会红。
//
// 真断言现在在 SQL 侧，失败会让 psql 非零退出：
//
//	tests/038_adaptive_probe_test.sql  Test 5b（阶梯，11 条）
//	tests/038_adaptive_probe_test.sql  Test 6  （被动提升，7 条）
//
// 这两个 Go 函数**刻意保留但不再假装是判据**：它们的价值是让
// `grep -rn "backoff" bg/` 的人看到「这条规则的验证在哪」。
// 如果你在这里加了断言，请一并更新上面的实测表 —— 不然它会再次开始说谎。

func TestBackoffV2_RecoveryLadder(t *testing.T) {
	// 阶梯期望值见上表；验证在 tests/038_adaptive_probe_test.sql Test 5b。
	t.Log("阶梯真断言在 tests/038_adaptive_probe_test.sql Test 5b（11 条，失败即 psql 非零退出）；本函数不重复断言。")
}

func TestPassiveBoost_TriggersEarlyProbe(t *testing.T) {
	// 规则本体是 SQL 函数 model_probe_passive_boost（bigint, text, timestamptz）：
	//   5 分钟内失败 ≥3 次 → next_retry_at = NOW()+30s
	//   5 分钟内失败  2 次 → next_retry_at = NOW()+1m
	//   其余 / healthy_confirmed / broken_confirmed → 原样不动
	// Go 侧 applyPassiveBoosts 只负责扫描与循环调用，判定逻辑全在 SQL 里。
	t.Log("被动提升真断言在 tests/038_adaptive_probe_test.sql Test 6（7 条）；本函数不重复断言。")
}
