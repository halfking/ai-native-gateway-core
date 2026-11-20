package bg

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// ─────────────────────────────────────────────────────────────────────────────
// 2026-11-20 L1 收口 —— 鉴权族（403 无配额体）凭据面落账
//
// 问题：403 无配额体（纯鉴权 403）⇒ errorsx 判 KindAuth ⇒ 探针仍不写凭据面
// ⇒ 凭据面显示 ready/ok（「节点正常」）⇒ 与配额族同族形态未治。
//
// 语义差异（鉴权 vs 配额）：
//   · 配额族：quota_state + availability_state='suspended' + 无 recover_at（permanent）
//     或长 horizon（balance 6h）或重置点（periodic）⇒ 充值类事件恢复；
//   · 鉴权族：availability_state='auth_failed' + 15 分钟 recover_at（业务面
//     writer.go:336-347）⇒ key 轮换 / 权限修复后可恢复，不是真正的"永久"。
//
// 本文件钉死：
//   ① 鉴权族识别（isAuthProbeKind）；
//   ② 鉴权族写凭据面（authCredentialSQL 词表与业务面逐字一致）；
//   ③ 两个探针路径（legacy tick / 统一队列）都必须写鉴权族凭据面。
// ─────────────────────────────────────────────────────────────────────────────

// authFakeExec records the SQL the worker writes through the Exec seam.
type authFakeExec struct {
	sqls []string
	args [][]any
	fail bool
}

func (f *authFakeExec) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	f.sqls = append(f.sqls, sql)
	f.args = append(f.args, arguments)
	if f.fail {
		return pgconn.CommandTag{}, context.DeadlineExceeded
	}
	return pgconn.CommandTag{}, nil
}

func (f *authFakeExec) last() string {
	if len(f.sqls) == 0 {
		return ""
	}
	return f.sqls[len(f.sqls)-1]
}

// ── ① 鉴权族识别 ──────────────────────────────────────────────────────────

func TestIsAuthProbeKind(t *testing.T) {
	cases := []struct {
		kind errorsx.ErrorKind
		want bool
	}{
		{errorsx.KindAuth, true},
		{errorsx.KindAuthRevoked, true},
		{errorsx.KindQuotaPermanent, false},
		{errorsx.KindQuota, false},
		{errorsx.KindRateLimit, false},
		{"", false},
	}
	for _, tc := range cases {
		got := isAuthProbeKind(tc.kind)
		if got != tc.want {
			t.Errorf("isAuthProbeKind(%q) = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

// ── ② 鉴权族凭据面 SQL 词表与业务面逐字一致 ───────────────────────────────

func TestAuthCredentialSQLVocabulary(t *testing.T) {
	t.Run("KindAuth ⇒ auth_failed + 15min", func(t *testing.T) {
		sql := authCredentialSQL(errorsx.KindAuth)
		if sql == "" {
			t.Fatal("authCredentialSQL(KindAuth) returned empty SQL")
		}
		// ① availability_state='auth_failed'（与业务面 writer.go:339 同词表）
		if !strings.Contains(sql, "availability_state      = 'auth_failed'") {
			t.Errorf("auth SQL must set availability_state='auth_failed':\n%s", sql)
		}
		// ② 15 分钟恢复窗口（与业务面 writer.go:336-340 同口径）
		if !strings.Contains(sql, "availability_recover_at = now() + interval '15 minutes'") {
			t.Errorf("auth SQL must set 15-minute recovery window:\n%s", sql)
		}
		// ③ state_reason_code + state_reason_detail（与业务面同形状）
		if !strings.Contains(sql, "state_reason_code") || !strings.Contains(sql, "state_reason_detail") {
			t.Errorf("auth SQL must carry reason code and detail:\n%s", sql)
		}
		// ④ 守卫：lifecycle_status='active' + manual_disabled=FALSE + balance_floor 豁免
		if !strings.Contains(sql, "lifecycle_status = 'active'") {
			t.Errorf("auth SQL must guard with lifecycle_status='active':\n%s", sql)
		}
		if !strings.Contains(sql, "manual_disabled") {
			t.Errorf("auth SQL must guard with manual_disabled=FALSE:\n%s", sql)
		}
		if !strings.Contains(sql, "balance_floor") {
			t.Errorf("auth SQL must exempt balance_floor rows:\n%s", sql)
		}
		// ⑤ 严重度守卫：auth_failed 不降级 suspended（与业务面 writer.go:346 逐字同款）
		if !strings.Contains(sql, "availability_state NOT IN ('suspended')") {
			t.Errorf("auth SQL must NOT downgrade suspended credentials:\n%s", sql)
		}
	})

	t.Run("KindAuthRevoked ⇒ 同款 SQL", func(t *testing.T) {
		sql := authCredentialSQL(errorsx.KindAuthRevoked)
		if sql == "" {
			t.Fatal("authCredentialSQL(KindAuthRevoked) returned empty SQL")
		}
		if !strings.Contains(sql, "auth_failed") {
			t.Errorf("authRevoked SQL must use the same auth_failed vocabulary:\n%s", sql)
		}
	})

	t.Run("非鉴权族 ⇒ 空 SQL", func(t *testing.T) {
		for _, kind := range []errorsx.ErrorKind{
			errorsx.KindQuotaPermanent,
			errorsx.KindQuota,
			errorsx.KindRateLimit,
			"",
		} {
			sql := authCredentialSQL(kind)
			if sql != "" {
				t.Errorf("authCredentialSQL(%q) must return empty SQL, got:\n%s", kind, sql)
			}
		}
	})
}

// ── ③ updateCredentialAuthSurface 实际写入 ────────────────────────────────

func TestUpdateCredentialAuthSurfaceWritesThroughExec(t *testing.T) {
	t.Run("鉴权族 ⇒ 写凭据面", func(t *testing.T) {
		fake := &authFakeExec{}
		w := &NodeProbeWorker{stateExecDB: fake}
		w.updateCredentialAuthSurface(context.Background(), 56, errorsx.KindAuth, "upstream 401 invalid_api_key")
		if len(fake.sqls) != 1 {
			t.Fatalf("expected exactly one credential-surface write, got %d", len(fake.sqls))
		}
		if !strings.Contains(fake.last(), "auth_failed") {
			t.Fatalf("credential-surface write must carry the auth vocabulary:\n%s", fake.last())
		}
		// 参数：state_reason_code / state_reason_detail / credential_id
		if len(fake.args[0]) != 3 {
			t.Errorf("auth surface write expects 3 args (code/detail/id), got %d", len(fake.args[0]))
		}
		if fake.args[0][0] != "auth" {
			t.Errorf("first arg must be kind string 'auth', got %v", fake.args[0][0])
		}
	})

	t.Run("非鉴权族 ⇒ 一行都不写", func(t *testing.T) {
		fake := &authFakeExec{}
		w := &NodeProbeWorker{stateExecDB: fake}
		w.updateCredentialAuthSurface(context.Background(), 56, errorsx.KindQuotaPermanent, "403 quota")
		w.updateCredentialAuthSurface(context.Background(), 56, "", "unknown")
		if len(fake.sqls) != 0 {
			t.Fatalf("non-auth kinds must not touch the credential surface, got %d writes", len(fake.sqls))
		}
	})

	t.Run("没有 exec seam ⇒ 静默跳过", func(t *testing.T) {
		w := &NodeProbeWorker{}
		w.updateCredentialAuthSurface(context.Background(), 56, errorsx.KindAuth, "x")
		// 无 panic 即通过（生产路径走 db；测试路径走 stateExecDB）。
	})
}

// ── ④ 探针路径必须写鉴权族凭据面 ─────────────────────────────────────────

// TestProbePathsWriteCredentialAuthSurface asserts that BOTH probe failure
// paths (legacy tick runOne + unified queue applyOutcome) call
// updateCredentialAuthSurface when the outcome is an auth-family verdict.
//
// The pattern mirrors TestProbePathsWriteCredentialQuotaSurface: if auth-surface
// writes land in only one path, the other path produces "节点正常 + 模型不可路由"
// state mismatches on live traffic.
func TestProbePathsWriteCredentialAuthSurface(t *testing.T) {
	for _, file := range []string{"node_probe.go", "probe_service.go"} {
		src := readBGSource(t, file)
		if !strings.Contains(src, "updateCredentialAuthSurface") {
			t.Fatalf("%s: the probe path must write the credential-level auth surface", file)
		}
		if !strings.Contains(src, "isAuthProbeKind") {
			t.Fatalf("%s: the credential-surface write must be guarded by the auth-verdict check", file)
		}
		// 两次连续失败守卫（与配额族同款）
		if !strings.Contains(src, "attempt >= 2") {
			t.Fatalf("%s: the auth-surface write must require attempt >= 2", file)
		}
	}
}
