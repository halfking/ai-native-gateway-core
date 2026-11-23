package dbinit

import (
	"os"
	"path/filepath"
	"testing"
)

// Round 42: migration 541 creates public.candidate_binding_scope_revision,
// which 568_credential_priority_flag.sql indexes into. 541 existed in the
// canonical startup directory but was never registered in the installer — not
// in embeddata, not in the go:embed list, not in StartupFiles — so a fresh
// install aborted at 568 with
//
//	relation "public.candidate_binding_scope_revision" does not exist
//
// Runner.applySQL walks StartupFiles in slice order against a live database,
// so the registration position must be earlier than every consumer, not merely
// present.

func startupIndex(t *testing.T) map[string]int {
	t.Helper()
	files := NewRunner("citus", "user", "db", "/tmp/sql").StartupFiles
	idx := make(map[string]int, len(files))
	for i, name := range files {
		if prev, dup := idx[name]; dup {
			t.Fatalf("StartupFiles lists %q twice (index %d and %d)", name, prev, i)
		}
		idx[name] = i
	}
	return idx
}

func requireStartupOrder(t *testing.T, before, after string) {
	t.Helper()
	idx := startupIndex(t)
	b, okB := idx[before]
	a, okA := idx[after]
	if !okB {
		t.Fatalf("StartupFiles no longer contains %q; this guard needs updating", before)
	}
	if !okA {
		t.Fatalf("StartupFiles no longer contains %q; this guard needs updating", after)
	}
	if b >= a {
		t.Errorf("StartupFiles order wrong: %q is at %d but must precede %q at %d", before, b, after, a)
	}
}

func TestStartupFilesPlace541BeforeItsConsumer568(t *testing.T) {
	requireStartupOrder(t,
		"541_candidate_binding_scope_revision.sql",
		"568_credential_priority_flag.sql")
}

// Every StartupFiles entry must resolve to a real embeddata file, or the fresh
// installer would apply a file that is not there.
func TestStartupFilesExistInEmbeddata(t *testing.T) {
	for _, name := range NewRunner("citus", "user", "db", "/tmp/sql").StartupFiles {
		p := filepath.Join("..", "..", "cmd", "llm-gw-installer", "embeddata", "startup", name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("StartupFiles entry %q has no embeddata file: %v", name, err)
		}
	}
}

// Note: StartupFiles is deliberately NOT in ascending numeric order — apply
// order follows dependencies, not the migration number (801 is registered last
// because the function it replaces depends on 733). A global ascending-order
// guard would therefore encode a false invariant. Two genuine inversions were
// observed while writing this and are left for a dedicated review rather than
// pinned here:
//
//	560_session_summaries_tenant_uniqueness.sql after 655_session_summaries_schema_reconcile.sql
//	704_plan_quota_probe_backoff.sql            after 713_session_turns_cost_precision.sql
//
// ★★★ 2026-11-23 L29 轮（真库空库实测）★★★：**这两处倒置已被实测排除**。
//
// ★ 怎么排除的：★ 用**安装器真实路径**在**全新空库**上真装了**全部 392 条**
// （★ 87.6s / 576 张表 / PROBE OK）⇒ ★★ **凡是真依赖，都会当场炸**
// （★ 参照：★ 同一轮里 956 与 981 两处真依赖就真的炸了）⇒ ★★ **⇒ 560/655 与
// 704/713 这两处倒置不是阻断点** ⇒ ★ **仍不钉它们**（★ 钉住＝把「当前的无害排列」
// 固化成不变式，★ 而无害与否取决于数据与对象、★ 不取决于顺序）⇒ ★★ **⇒ 但
// 「待专项复核」这个开着的口子 hereby 关闭**（★ 以实测关闭，★ 而不是以推断关闭）。
//
// ★★ 顺带：★ 同一轮还实测出**第三处真倒置**并已修掉 —— ★ 981/990
// （★ 见 runner_990_order_test.go：★ 981 的会话分析物化视图读
// ★ `session_summaries.total_credits_charged`，★ 该列由 990 添加）。
