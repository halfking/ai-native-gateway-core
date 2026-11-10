package startup

import (
	"strings"
	"testing"
)

// migration_957_error_type_closure_test.go ——
//
// 957（self_check_runs.error_type CHECK 闭集补 `rate_limit_observed`）的专属契约门。
//
// ★ 为什么闭集门（migration_929_error_type_closure_test.go）之外还要这一份：
//
//	闭集门判的是「**安装顺序里最后一个重建该 CHECK 的迁移**」的**闭集覆盖**
//	⇒ 它管的是「**有没有覆盖全**」；
//	★ 而它**不管**「**down 退得对不对**」⇒ 930 的 down 就栽在这里：
//	  它的幂等判据写成了「包含旧集的全部值」⇒ 而新集是旧集的**超集**
//	  ⇒ 新集也被判成「已是旧集」⇒ down **静默跳过** ⇒ 约束根本没退回去。
//	  ⇒ 静态读代码看不出来 ⇒ **真库实证不是形式主义**。
//	⇒ 本文件把 957 的 down 语义逐条钉死（只退自己那一步 / 幂等判据是
//	  「不含新 kind」/ 有行挡路就响亮中止、不删审计证据）。
//
// ★ 背景（这个 kind 为什么必须进闭集）：
//
//	审计 F50 轮加的 `errorsx.KindRateLimitObserved` —— 「上游用**请求侧 4xx**
//	回了一个**限流形态**的正文」（例：HTTP 400 +
//	{"error":{"message":"rate limit exceeded"}}）⇒ 归因到「**不惩罚节点**」的
//	独立 kind（`PenaltyOf` 走 `PenaltyNone`，与 `KindRateLimit` 的
//	`PenaltyBinding`——拉黑 + 连败累计——**明确不同**）。
//	写侧写的就是 kind 名（`bg/credential_selfcheck.go:844`）⇒ 缺它 ⇒
//	finalizeRun 的 UPDATE 被 DB 以 23514 拒绝 ⇒ 整条回滚 ⇒ run 行半写
//	⇒ ★ **这次自检在系统里根本没发生过**，但调度器以为发生过。

const (
	mig957Name     = "957_self_check_error_type_kind_closure_rate_limit_observed.sql"
	mig957DownName = "957_self_check_error_type_kind_closure_rate_limit_observed.down.sql"
)

// TestMigration957UpCarriesFullClosure —— up 迁移的契约。
func TestMigration957UpCarriesFullClosure(t *testing.T) {
	root := mig951Tree(t)
	body := mig951Read(t, root, "sql/migrations/startup/"+mig957Name)

	for _, want := range []string{
		"BEGIN;",
		"SET LOCAL lock_timeout",
		"SET LOCAL statement_timeout",
		"ADD CONSTRAINT self_check_runs_error_type_check CHECK",
		"COMMIT;",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("957 missing %q", want)
		}
	}

	// ★ 新 kind 必须**真的**在落库的 CHECK 数组里（不是只在注释里 ——
	//   注释里的字面量会被闭集门的提取器涂白 ⇒ 写了也不算）。
	if !strings.Contains(body, "'rate_limit_observed'") {
		t.Fatal("957 must put 'rate_limit_observed' into the CHECK literal set")
	}
	// ★ 守卫数组 v_required 也必须有它（闭集门要求两者逐项相等）。
	if !strings.Contains(body, "v_required text[] := ARRAY[") {
		t.Fatal("957 must keep the v_required guard array (the closure gate compares it to the CHECK array)")
	}
	// ★ NOT VALID：只拦新行、不重洗存量历史 run 行。
	if !strings.Contains(body, ") NOT VALID;") {
		t.Fatal("957 must keep NOT VALID semantics (historical rows are not re-validated)")
	}
	// ★ 幂等守卫：已覆盖全闭集就跳过（可重入）。
	if !strings.Contains(body, "already covers the full errorsx kind closure, skipping") {
		t.Fatal("957 must keep the idempotency guard (skip when the closure is already covered)")
	}
	// ★ 引号包裹的匹配：'rate_limit' 不得被 'rate_limit_observed' 误命中。
	if !strings.Contains(body, "position('''' || v_val || '''' IN v_def) = 0") {
		t.Fatal("957 must match literals with surrounding quotes ('rate_limit' vs 'rate_limit_observed')")
	}

	// installer 副本字节必须与 canonical 一致。
	installer := mig951Read(t, root, "installer/cmd/llm-gw-installer/embeddata/startup/"+mig957Name)
	if body != installer {
		t.Fatal("957 canonical and installer bytes differ")
	}
}

// TestMigration957DownRetreatsOnlyItsOwnStep —— down 迁移的契约（★ 930 的教训面）。
//
// ★ 三条要点：
//
//	① **只退一步**：down 的 CHECK 数组 = 930 的集合（**不含** `rate_limit_observed`），
//	   其余 38 项**一个不少**（★ 少一项就是误伤已应用的历史语义）。
//	② **幂等判据是「不含新 kind」**：不是「包含旧集全部值」——
//	   后者会让新集（超集）被判成「已是旧集」⇒ down 静默跳过 ⇒ 约束没退回去。
//	③ **不删审计证据**：有行落在新 kind 上时必须**响亮中止**（RAISE EXCEPTION），
//	   而不是删行迁就约束。
func TestMigration957DownRetreatsOnlyItsOwnStep(t *testing.T) {
	root := mig951Tree(t)
	down := mig951Read(t, root, "sql/migrations/startup/"+mig957DownName)

	// ① down 的 CHECK 数组里**不得**再有新 kind。
	downCheck := down
	if i := strings.Index(downCheck, "ADD CONSTRAINT self_check_runs_error_type_check CHECK"); i >= 0 {
		downCheck = downCheck[i:]
	}
	if strings.Contains(downCheck, "'rate_limit_observed'") {
		t.Fatal("957 down must NOT keep 'rate_limit_observed' in the rebuilt CHECK set")
	}

	// ① 但 930 集合里的其余项**一个都不能少**（逐项核对 930 的清单）。
	for _, keep := range []string{
		"'transient'", "'timeout'", "'network'", "'rate_limit'", "'auth'",
		"'quota'", "'upstream_down'", "'canceled'", "'client_bug'",
		"'request_rejected'", "'model_unavailable'", "'concurrent'",
		"'auth_revoked'", "'quota_periodic'", "'quota_balance'",
		"'quota_permanent'", "'model_not_found'", "'stream_timeout'",
		"'tool_call_id_mismatch'", "'context_length_exceeded'",
		"'unsupported_feature'", "'model_deprecated'", "'content_filter'",
		"'empty_response'", "'conversion_error'", "'upstream_context_loss'",
		"'upstream_overloaded'", "'no_available_channel'", "'circuit_open'",
		"'fp_slot_saturated'", "'first_byte_timeout'", "'reply_loop'",
		"'account_restricted'", "'none'", "'internal'", "'parse_error'",
		"'unattributed'", "'upstream_fail'",
	} {
		if !strings.Contains(downCheck, keep) {
			t.Fatalf("957 down dropped %s —— down must retreat only its own step", keep)
		}
	}

	// ② 幂等判据必须是「**不含**任何一个 957 新增的 kind」。
	if !strings.Contains(down, "v_has_new := false;") ||
		!strings.Contains(down, "position('''' || v_val || '''' IN v_def) > 0") {
		t.Fatal("957 down must detect 'contains the new kind' (not 'contains the whole old set')")
	}
	// ★ 反面判据不许出现：930 down 的第一版就是这么错的 ⇒ 这里显式钉死。
	if strings.Contains(down, "contains the pre-957 set") &&
		strings.Contains(down, "RETURN;") &&
		!strings.Contains(down, "v_has_new") {
		t.Fatal("957 down must not use the 'contains the whole old set' idempotency criterion")
	}

	// ③ 有行挡路就响亮中止，不删审计证据。
	if !strings.Contains(down, "RAISE EXCEPTION") {
		t.Fatal("957 down must abort loudly when rows hold the new kind")
	}
	if !strings.Contains(down, "This down does not delete audit rows") {
		t.Fatal("957 down must state that it does not delete audit rows")
	}
	// ★ 不得出现任何 DELETE / TRUNCATE（删行迁就约束 = 销毁审计证据）。
	for _, forbidden := range []string{"DELETE FROM public.self_check_runs", "TRUNCATE"} {
		if strings.Contains(down, forbidden) {
			t.Fatalf("957 down must not contain %q (audit rows must not be destroyed)", forbidden)
		}
	}

	// installer 副本字节必须与 canonical 一致。
	installer := mig951Read(t, root, "installer/cmd/llm-gw-installer/embeddata/startup/"+mig957DownName)
	if down != installer {
		t.Fatal("957 down canonical and installer bytes differ")
	}
}
