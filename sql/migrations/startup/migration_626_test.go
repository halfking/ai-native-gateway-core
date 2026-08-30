package startup

import (
	"os"
	"strings"
	"testing"
)

func TestMigration626ReconcilesSessionBodiesHotPromotion(t *testing.T) {
	data, err := os.ReadFile("626_session_bodies_hot_promote_reconcile.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := stripSQLComments(string(data))
	for _, want := range []string{
		"CREATE OR REPLACE FUNCTION public.promote_session_bodies_hot_to_partition",
		"pg_try_advisory_xact_lock",
		"FOR UPDATE SKIP LOCKED",
		"ON CONFLICT (id, partition_date) DO NOTHING",
		"DELETE FROM public.session_bodies_hot h",
		// 2026-08-31 f1ae3c71e 审计修正后：只删真正插入的行（匹配完整
		// 主键 id+partition_date），被 ON CONFLICT 跳过的行留在热表等
		// 下一轮。旧期望 "USING to_move m" 已不成立。
		"USING inserted i",
		"ALTER VIEW public.session_bodies_unified SET (security_invoker = true)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("migration 626 missing %q", want)
		}
	}
	if strings.Contains(body, "WHERE id IN (SELECT id FROM inserted)") {
		t.Fatal("promotion must delete all selected rows after a destination conflict is reconciled")
	}
}

func TestMigration626DownDoesNotRestoreTheDataIntegrityBug(t *testing.T) {
	data, err := os.ReadFile("626_session_bodies_hot_promote_reconcile.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := stripSQLComments(string(data))
	for _, forbidden := range []string{
		"CREATE OR REPLACE FUNCTION",
		"DROP VIEW",
		"ALTER VIEW public.session_bodies_unified RESET",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("migration 626 down must not weaken data integrity or RLS: %q", forbidden)
		}
	}
}
