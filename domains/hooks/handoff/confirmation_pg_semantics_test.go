package handoff

// confirmation_pg_semantics_test.go — R76 真库门：pending 提案的**语义**。
//
// 为什么必须是真库门：本包里 PGStore 的既有测试（confirmation_pg_test.go）全部
// 走 sqlmock，只能断言「发出了这段 SQL 字符串」——**永远钉不住这段 SQL 的语义**。
// 而这段 SQL 的语义恰好是本域最容易被误改的契约：
//
//	uq_handoff_pending_active_session 是 (tenant_id, previous_session_id)
//	WHERE status='pending' 的部分唯一索引，SavePending 的 ON CONFLICT 目标就是
//	它，DO UPDATE 同时覆盖 id 与 token_hash。所以同一会话的第二次提案**就地替换**
//	第一次：第一个客户端手里的 proposal_id 与 confirmation_token 立即失效，
//	Confirm 的 `SELECT ... WHERE id=$1` 命中 0 行 ⇒ ErrConfirmationInvalid。
//
// 这**不是缺陷，是有意不变量**（内存 store 的 SavePending 做同样的事：
// confirmation.go 的 `delete(s.byID, prior)`），R74 曾把它报成 P1 后被我复核撤回。
// 但撤回的是「这是 bug」这个定级，**不是**「它不需要被钉住」——正因为它是有意
// 契约，任何人将来动它（改成多提案共存、或误删 ON CONFLICT 子句）都必须先看到
// 这道门变红，而不是在生产上发现「第一个客户端的确认莫名其妙失败」。
//
// 运行方式（需要已应用 517/527 迁移与配套表的真实 PG）：
//
//	TEST_PG_DSN='postgres://llm_gateway:pw@127.0.0.1:5432/llm_gateway?sslmode=disable' \
//	  go test ./domains/hooks/handoff/ -run TestPGStoreSavePendingSemantics -v
//
// 未设 DSN 即 t.Skip，CI 离线绿。清理：本门用独立 tenant 前缀播种，defer 中
// 按前缀 DELETE 并断言归零（PGStore 的方法不接受 tx，无法靠 ROLLBACK 兜底）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var handoffSemanticsSeq atomic.Int64

func openHandoffPG(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	// 契约名 TEST_PG_DSN：scripts/audit/run-integration-gate.sh 已注入它，
	// 且 sql/schema/integration_gate_test.go 会从仓内推导并强制该注入。
	// 本轮最初自造 LLM_GATEWAY_HANDOFF_PG_DSN（_PG_DSN 后缀），而 harness
	// 的注入清单只认 _DATABASE_URL/_DB_URL/_PG_URL 结尾与 TEST_PG_DSN ⇒
	// 这道 P0 级门会被结构性排除在 CI 之外。见 resolveHandoffDSN 注释。
	dsn := resolveHandoffDSN()
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set — offline mode")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, tbl := range []string{
		"handoff_pending_confirmations", "session_summaries", "handoff_logs_hot",
	} {
		var exists bool
		if err := db.QueryRowContext(ctx,
			`SELECT to_regclass('public.'||$1) IS NOT NULL`, tbl).Scan(&exists); err != nil {
			t.Fatalf("probe %s: %v", tbl, err)
		}
		if !exists {
			_ = db.Close()
			t.Skipf("public.%s missing — apply handoff migrations before running this gate", tbl)
		}
	}
	return db, func() { _ = db.Close() }
}

func TestPGStoreSavePendingSemantics(t *testing.T) {
	db, closeDB := openHandoffPG(t)
	// 用 t.Cleanup 而非 defer：t.Cleanup 是 LIFO，先注册 ⇒ 最后执行。
	// 写成 defer 会让 defer(closeDB) 在下面的 t.Cleanup(清理+校验) 之前跑掉，
	// 校验那一步拿到的是已关闭的连接（本轮第一次跑就踩到）。
	t.Cleanup(closeDB)
	ctx := context.Background()

	tenant := fmt.Sprintf("r76-handoff-%d", handoffSemanticsSeq.Add(1))
	const session = "sess-r76"
	store := &PGStore{db: db}

	// 清理：本门在真实表上播种（PGStore 的方法不接受 tx），按 tenant 前缀删干净
	// 并断言归零，不留残留。
	t.Cleanup(func() {
		for _, table := range []string{"handoff_logs_hot", "handoff_pending_confirmations"} {
			if _, err := db.ExecContext(context.Background(),
				`DELETE FROM `+table+` WHERE tenant_id = $1`, tenant); err != nil {
				t.Errorf("cleanup %s: %v", table, err)
			}
		}
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM session_summaries WHERE tenant_id = $1`, tenant); err != nil {
			t.Errorf("cleanup session_summaries: %v", err)
		}
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM tenants WHERE code = $1`, tenant); err != nil {
			t.Errorf("cleanup tenant: %v", err)
		}
		var left int
		if err := db.QueryRowContext(context.Background(),
			`SELECT (SELECT count(*) FROM handoff_pending_confirmations WHERE tenant_id=$1)
			      + (SELECT count(*) FROM handoff_logs_hot WHERE tenant_id=$1)`,
			tenant).Scan(&left); err != nil {
			t.Errorf("verify cleanup: %v", err)
		}
		if left != 0 {
			t.Errorf("cleanup left %d rows behind for tenant %s", left, tenant)
		}
	})

	// 先建租户：session_summaries / handoff_* 对 tenants(code) 有外键，
	// 没有租户行时插入会 23503。
	if _, err := db.ExecContext(ctx,
		`INSERT INTO tenants (code, name) VALUES ($1, $1)
		 ON CONFLICT (code) DO NOTHING`, tenant); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	// 建 Confirm 需要的记账行（无此行 Confirm 直接 ErrConfirmationInvalid，
	// 会掩盖本门要验的东西）。
	_, err := db.ExecContext(ctx, `
		INSERT INTO session_summaries
		 (session_key, tenant_id, first_request_at, last_request_at, handoff_count, last_handoff_at)
		VALUES ($1, $2, NOW(), NOW(), 0, NULL)
		ON CONFLICT (session_key, tenant_id) DO NOTHING`, session, tenant)
	if err != nil {
		t.Fatalf("seed session_summaries: %v", err)
	}

	proposal := func() (*ConfirmationProposal, string) {
		p, token, err := NewConfirmationProposal(&HandoffRecord{
			SessionKey: session, TenantID: tenant, TriggerMode: "manual",
			TriggerReason: "manual_skill:handoff", TokensAtTrigger: 1000,
			TokensInSession: 1000, MessagesAtTrigger: 5, SummaryEngine: "rule",
			SummaryText: "s", SkillName: "handoff", CreatedAt: time.Now().UTC(),
		}, 4242, time.Now().Add(5*time.Minute))
		if err != nil {
			t.Fatalf("NewConfirmationProposal: %v", err)
		}
		return p, token
	}

	first, firstToken := proposal()
	if err := store.SavePending(ctx, first); err != nil {
		t.Fatalf("first SavePending: %v", err)
	}
	second, secondToken := proposal()
	if err := store.SavePending(ctx, second); err != nil {
		t.Fatalf("second SavePending: %v", err)
	}

	t.Run("第二份提案就地替换第一份（有意不变量）", func(t *testing.T) {
		var id, tokenHash, status string
		err := db.QueryRowContext(ctx,
			`SELECT id::text, token_hash, status FROM handoff_pending_confirmations
			 WHERE tenant_id=$1 AND previous_session_id=$2`, tenant, session).
			Scan(&id, &tokenHash, &status)
		if err != nil {
			t.Fatalf("read pending row: %v", err)
		}
		// 只有一行——部分唯一索引 + ON CONFLICT 保证「每会话一份 pending」。
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT count(*) FROM handoff_pending_confirmations
			 WHERE tenant_id=$1 AND previous_session_id=$2`, tenant, session).Scan(&n); err != nil {
			t.Fatalf("count pending rows: %v", err)
		}
		if n != 1 {
			t.Errorf("pending rows = %d, want 1 (uq_handoff_pending_active_session invariant)", n)
		}
		if id != second.ID {
			t.Errorf("pending row id = %s, want the SECOND proposal %s — ON CONFLICT DO UPDATE "+
				"must carry id=EXCLUDED.id (first %s)", id, second.ID, first.ID)
		}
		if !strings.EqualFold(tokenHash, second.TokenHash) {
			t.Errorf("token_hash = %s, want the second proposal's hash %s", tokenHash, second.TokenHash)
		}
		if status != confirmationStatusPending {
			t.Errorf("status = %q, want %q", status, confirmationStatusPending)
		}
	})

	t.Run("被替换掉的旧提案确认失败（客户端可观察后果）", func(t *testing.T) {
		_, err := store.Confirm(ctx, ConfirmationInput{
			ProposalID: first.ID, TenantID: tenant, APIKeyID: 4242,
			Token: firstToken, NewSessionID: "gw_new_1", IdempotencyKey: "idem-1",
		})
		if !errors.Is(err, ErrConfirmationInvalid) {
			t.Fatalf("Confirm with the superseded proposal = %v, want ErrConfirmationInvalid", err)
		}
	})

	t.Run("现行提案确认成功（契约另一半）", func(t *testing.T) {
		res, err := store.Confirm(ctx, ConfirmationInput{
			ProposalID: second.ID, TenantID: tenant, APIKeyID: 4242,
			Token: secondToken, NewSessionID: "gw_new_2", IdempotencyKey: "idem-2",
		})
		if err != nil {
			t.Fatalf("Confirm with the current proposal: %v", err)
		}
		if res == nil || !res.FirstConfirmation {
			t.Fatalf("result = %+v, want FirstConfirmation=true", res)
		}
		// 确认后 pending 唯一索引让位：同一会话可以再提一份新的。
		third, _ := proposal()
		if err := store.SavePending(ctx, third); err != nil {
			t.Fatalf("SavePending after confirm: %v", err)
		}
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT count(*) FROM handoff_pending_confirmations
			 WHERE tenant_id=$1 AND previous_session_id=$2 AND status='pending'`,
			tenant, session).Scan(&n); err != nil {
			t.Fatalf("count pending after confirm: %v", err)
		}
		if n != 1 {
			t.Errorf("pending rows after confirm = %d, want 1 (the confirmed row must leave the partial index)", n)
		}
	})
}

// 变量名按仓内契约取：scripts/audit/run-integration-gate.sh 只注入
// TEST_PG_DSN / *_DATABASE_URL / *_DB_URL / *_PG_URL 这几种后缀，注入清单由
// sql/schema/integration_gate_test.go 的 TestGateInjectsEveryDBCredentialName
// 从仓内按这些后缀**推导**（不是手写清单）。用 _PG_DSN 这类不在契约内的名字，
// harness 永远不会注入 ⇒ 这道门在 CI 上结构性沉睡，却仍以 "ok" 出现在报告里。
// 旧名保留为回退供手工运行；新代码直接用 TEST_PG_DSN。

func resolveHandoffDSN() string {
	if v := os.Getenv("TEST_PG_DSN"); v != "" {
		return v
	}
	if v := os.Getenv("LLM_GATEWAY_HANDOFF_PG_DSN"); v != "" {
		return v
	}
	return os.Getenv("LLM_GATEWAY_SUPPLIER_PG_DSN")
}
