package db

// retirement_contract_exports.go —— 给**跨包契约门**用的导出入口。
//
// # 为什么需要这一层
//
// 890（v1 request_logs 族最终删除）动了三样必须**彼此等价**的东西：
// 迁移文件里的视图体、迁移文件里的 recent_success_rate 函数体、以及本包里的
// 运行时 SSOT（canonicalSessionOnlyDDL / recentSuccessRateEnsureSQL）。
//
// 其中两样是**未导出**的（canonicalSessionOnlyDDL、recentSuccessRateEnsureSQL），
// 而守它们的门在另一个包（sql/migrations/startup）⇒ 未导出的东西**跨包守不住**：
// 门只能看到文件文本，看不到本包实际会执行什么。
//
// ⇒ 这里导出两个**只读**入口，把"文件里写的"与"代码里跑的"接起来。
//
// ⚠ 它们是**导出**的，所以必须诚实：
//   · 只返回字符串，不改任何状态；
//   · 不接受参数（不接受"给我一个变体"的口子——那会让门形同虚设）；
//   · 命名带 ForTest 后缀，让"这是给门用的"在调用点一眼可见。
//
// ⚠ 与「导出一个测试专用入口」的常见反对意见的正面回应：这里导出的**不是**
// 内部实现细节的拷贝，而是**同一份字面量**（函数直接返回包内常量/函数的
// 结果）⇒ 不存在"导出副本与内部漂移"的可能。若哪天它们变成"拷贝一份再返回"，
// 门就失去意义了 —— 所以实现刻意是单行转发。

// CanonicalSessionOnlyDDLForTest returns the exact DDL the gateway's boot-time
// self-heal installs for the canonical request-logs view: the session-only
// two-arm body (session_turns_hot ∪ session_turns, 734 details LEFT JOIN).
//
// The v1 third arm is retired together with the v1 family (migration 890).
// Contract gate: sql/migrations/startup/migration_890_v1_family_drop_test.go
// compares the migration's $proj$ block against
// CanonicalSessionOnlyProjectionForTest — the two must be the same text, or a
// boot-time self-heal would swap one body for the other.
func CanonicalSessionOnlyDDLForTest() string {
	return canonicalSessionOnlyDDL(true)
}

// CanonicalSessionOnlyProjectionForTest returns the exact column projection
// the canonical view's two session arms share.
//
// ⚠ Why this is a separate export instead of "parse the DDL and cut the
// projection out": the DDL renders `FROM` on its own line, so a textual
// "first FROM" cut is whitespace-fragile — and a gate whose *extraction* is
// fragile fails in ways that read like "the gate is broken" rather than
// "the projections drifted". Exposing the projection directly removes the
// parsing step from the gate entirely.
//
// Contract gate: sql/migrations/startup/migration_890_v1_family_drop_test.go.
func CanonicalSessionOnlyProjectionForTest() string {
	return sessionFamilyProjectionFor(canonicalColumnOrderV2, true)
}

// RecentSuccessRateEnsureSQLForTest returns the exact SQL the gateway runs at
// boot to (re)define `recent_success_rate`.
//
// The function is called from the provider candidate query via
// `CROSS JOIN LATERAL` ⇒ a v1 leg in its body is a **100% traffic failure**,
// and because `CREATE OR REPLACE FUNCTION` does not resolve an SQL function
// body at creation time, a drift would only surface on the first call (the
// symptom then looks like "all upstreams down", not like a migration
// mismatch). Contract gate:
// sql/migrations/startup/migration_890_v1_family_drop_test.go.
func RecentSuccessRateEnsureSQLForTest() string {
	return recentSuccessRateEnsureSQL
}
