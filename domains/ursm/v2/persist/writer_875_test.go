package persist

// 2026-10-18（§10.110.65）：875 —— 把 `empty_response_rate_5m` 从 payload
// 提升为 typed 列。
//
// # 为什么要补这一列
//
// 818 提升了 31 个 hash 键，但**漏了 `empty_response_rate_5m`**：
//
//	empty_response_rate_1m   → typed 列（818）
//	empty_response_rate_30m  → typed 列（818）
//	empty_response_rate_5m   → ✗ 只在 payload 里
//
// 而 `store/pipeline.go:132` 一直在读它 ⇒ 一个被生产代码读取的字段，
// 在快照表里只能靠 `payload->>'...'` 取：走不了索引、不受类型检查、
// 也不在任何列清单里。
//
// # 三条语义，逐条都要钉住
//
// 1. **值要进 typed 列**（不是复制，是**移动**）
// 2. **必须同时从 payload 里消失** —— 只进列不进排除表，就是 §62 那道门
//    要防的「同一个值存两份」。
// 3. **缺失必须是 nil 而不是 0** —— 「显式采集到 0」与「这一时刻没这个键」
//    在排障时结论相反。

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Test875EmptyResponseRate5mLandsInTypedColumnAndLeavesPayload 是本改动的正题。
//
// ⚠ 种子必须带**一个不在排除表里的键**（这里用 empty_responses_1m，它在
//
//	payloadResidentKeys 中）。否则残渣为空 ⇒ §63 之后 payload 是 NULL
//	⇒ 「payload 里不该有 empty_response_rate_5m」这条断言在**空串**上恒真，
//	判据形同虚设（这是本文件最后那道量具自检当场抓到的）。
func Test875EmptyResponseRate5mLandsInTypedColumnAndLeavesPayload(t *testing.T) {
	r := collectOne(t, map[string]string{
		"available":              "1",
		"empty_response_rate_5m": "0.4",
		// 对照键：确实属于 payload 的键，用来证明 payload **非空且可解析**
		"empty_responses_1m": "3",
	})

	if r.EmptyResponseRate5m == nil || *r.EmptyResponseRate5m != 0.4 {
		t.Fatalf("EmptyResponseRate5m = %v, want 0.4", r.EmptyResponseRate5m)
	}

	// 先证明 payload 确实有内容，否则下面那条否定断言没有承重。
	var back map[string]any
	if err := json.Unmarshal(r.Payload, &back); err != nil {
		t.Fatalf("payload must be valid non-empty JSON for this test to mean anything: "+
			"%v (%q)", err, r.Payload)
	}
	if _, ok := back["empty_responses_1m"]; !ok {
		t.Fatalf("control key empty_responses_1m is missing from payload=%q — "+
			"the seed no longer exercises the payload path", r.Payload)
	}
	// ★ 差分断言：同一个 payload 里，对照键在、被搬走的那一列不在。
	if _, ok := back["empty_response_rate_5m"]; ok {
		t.Fatalf("payload still carries empty_response_rate_5m — it must be MOVED, "+
			"not copied. payload=%q", r.Payload)
	}
}

// 缺失必须落 nil 而不是 0 —— 与两个兄弟同一契约。
func Test875MissingEmptyResponseRate5mStaysNil(t *testing.T) {
	r := collectOne(t, map[string]string{
		"available":              "1",
		"empty_response_rate_1m": "0.25",
	})
	if r.EmptyResponseRate5m != nil {
		t.Fatalf("EmptyResponseRate5m = %v, want nil when the hash has no such key "+
			"(0 would claim 'we measured an empty-response rate of zero')", r.EmptyResponseRate5m)
	}
}

// 显式 "0" 必须是非 nil 的 0，与兄弟列一致。
func Test875ExplicitZeroIsNotNil(t *testing.T) {
	r := collectOne(t, map[string]string{
		"available":              "1",
		"empty_response_rate_5m": "0",
	})
	if r.EmptyResponseRate5m == nil || *r.EmptyResponseRate5m != 0 {
		t.Fatalf("EmptyResponseRate5m = %v, want non-nil 0", r.EmptyResponseRate5m)
	}
}

// 三态一致性：该键**不得**同时出现在两张互斥的登记表里。
// §62 的门查的是「store 读的键有没有归属」，这里查的是归属**本身**是否自洽。
func Test875KeyIsInExactlyOneRegistry(t *testing.T) {
	const key = "empty_response_rate_5m"
	inDup, inResident := false, false
	for _, k := range payloadDuplicateKeys {
		if k == key {
			inDup = true
		}
	}
	for _, k := range payloadResidentKeys {
		if k == key {
			inResident = true
		}
	}
	switch {
	case inDup && inResident:
		t.Fatalf("%s is claimed to have a typed column AND to be payload-only", key)
	case !inDup && !inResident:
		t.Fatalf("%s is in neither registry — it would silently land in payload "+
			"with no typed column, which is exactly what 875 fixes", key)
	}
}

// 接线门：迁移必须真的加这一列，且带 lock_timeout。
//
// ★ 后者是 installer 侧那道门（TestStartupMigrationsAboveCutoffHaveLockGuard）
//
//	的要求：强锁 DDL 不设 lock_timeout 会在等锁期间堵住全库。
//	本表上有每分钟 1,800 行的 persist 写入，排队等锁能等到分钟级。
func Test875MigrationAddsTheColumnWithLockTimeout(t *testing.T) {
	const path = "../../../../sql/migrations/startup/875_ursm_snapshot_empty_response_rate_5m.sql"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "ADD COLUMN IF NOT EXISTS empty_response_rate_5m real") {
		t.Fatal("migration must add empty_response_rate_5m as real (same type as its two siblings)")
	}
	// ⚠ 必须剥掉 SQL 行注释再判：迁移的**头注释里也写着 lock_timeout**
	//   （"lock_timeout = 2s —— 抢不到就快速失败"）。不剥注释的话，
	//   断言会命中那段说明文字，把「语句被删了」读成「还在」——
	//   本轮 N5 变异就是这么全绿的。与 writer_argorder_test 的
	//   stripSQLLineComments 同一族。
	body := stripSQLLineComments(src)
	if !strings.Contains(strings.ToLower(body), "set local lock_timeout") {
		t.Fatal("migration contains strong-lock DDL (ALTER TABLE ADD COLUMN is ACCESS " +
			"EXCLUSIVE) but issues no `SET LOCAL lock_timeout` — it could queue behind " +
			"this table's own 1800-rows-per-minute persist writes. " +
			"(the header comment mentions lock_timeout; only an executable statement counts)")
	}
	// 搬迁而不是复制：列加进来了，就必须同时从 payload 里剔除。
	if !strings.Contains(src, "payload->>''empty_response_rate_5m''") {
		t.Error("migration should document that the value moves out of payload; " +
			"without that note the next reader may assume both copies exist")
	}
}

// Test875SiblingWindowsAreHandledIdentically 钉住「同族必须同构」。
//
// ★ 这才是 818 漏掉 `_5m` 的**结构性**成因：三个同族键各自手工登记，
//
//	没有任何机制要求它们同构。本条把那条机制补上：
//	`empty_response_rate_1m / _5m / _30m` 必须全部
//	  ① 在 payloadDuplicateKeys 里（有 typed 列 ⇒ 从 payload 剔除）、
//	  ② 都不在 payloadResidentKeys 里（那里只放「没有 typed 列」的键）。
//	少任何一个都会红 —— 而这正是 875 修的那个 bug 的形态。
func Test875SiblingWindowsAreHandledIdentically(t *testing.T) {
	const family = "empty_response_rate_"

	dup := map[string]bool{}
	for _, k := range payloadDuplicateKeys {
		dup[k] = true
	}
	resident := map[string]bool{}
	for _, k := range payloadResidentKeys {
		resident[k] = true
	}

	for _, window := range []string{"1m", "5m", "30m"} {
		key := family + window
		if !dup[key] {
			t.Errorf("%s has no typed column but is not registered as payload-resident "+
				"either — it would land in payload with nowhere else to live", key)
		}
		if resident[key] {
			t.Errorf("%s is in payloadResidentKeys, which claims it has NO typed column — "+
				"but it now does (875). The key would be both stripped and declared resident", key)
		}
	}

	// 反向：payloadResidentKeys 里不允许再出现这个族的名字
	//（它们全部已有 typed 列）。
	for _, k := range payloadResidentKeys {
		if strings.HasPrefix(k, family) {
			t.Errorf("payloadResidentKeys still lists %q, which now has a typed column", k)
		}
	}
}
