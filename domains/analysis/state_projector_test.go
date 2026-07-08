package analysis

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeDB 记录所有 Exec 调用，用于断言投影产生的 session_tags 写入。
type fakeDB struct {
	execCalls []execCall
}
type execCall struct {
	sql  string
	args []any
}

func (f *fakeDB) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row { return nil }
func (f *fakeDB) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	return nil, nil
}
func (f *fakeDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execCalls = append(f.execCalls, execCall{sql: sql, args: args})
	return pgconn.NewCommandTag(""), nil
}

// TestDeriveStateTags 覆盖 v6 字段 → tagEntry 的映射规则。
func TestDeriveStateTags(t *testing.T) {
	cases := []struct {
		name string
		in   SessionStateProjection
		want map[string]string // tag_key → tag_value
	}{
		{
			name: "high security + pii stripped + approval pending",
			in:   SessionStateProjection{SecurityScore: 9, PIIStripped: true, ApprovalStatus: "pending"},
			want: map[string]string{"security": "risk:high", "pii": "stripped", "approval": "pending"},
		},
		{
			name: "medium security + sensitive detected (not stripped)",
			in:   SessionStateProjection{SecurityScore: 6, SensitiveDetected: true},
			want: map[string]string{"security": "risk:medium", "compliance": "sensitive_detected", "pii": "detected"},
		},
		{
			name: "low security + optimization",
			in:   SessionStateProjection{SecurityScore: 3, OptimizationTag: "strip_tools"},
			want: map[string]string{"security": "risk:low", "optimization": "strip_tools"},
		},
		{
			name: "all zero → no tags",
			in:   SessionStateProjection{},
			want: map[string]string{},
		},
		{
			name: "score zero but audit score present → security risk:none",
			in:   SessionStateProjection{AuditScore: 5},
			want: map[string]string{"security": "risk:none"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := deriveStateTags(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("got %d tags %v, want %d %v", len(got), got, len(c.want), c.want)
			}
			for _, e := range got {
				if w, ok := c.want[e.Key]; !ok || w != e.Value {
					t.Errorf("tag %q=%q not in want %v", e.Key, e.Value, c.want)
				}
			}
		})
	}
}

// TestProject_WritesUpsertForEachTag 验证 Project 对每条 tag 调用一次 UPSERT，
// 参数顺序为 (gw_session_id, tenant_id, tag_key, tag_value)。
func TestProject_WritesUpsertForEachTag(t *testing.T) {
	db := &fakeDB{}
	p := NewSessionStateProjector(db, nil)
	err := p.Project(context.Background(), SessionStateProjection{
		GwSessionID:    "sess-1",
		TenantID:       "default",
		SecurityScore:  9,
		PIIStripped:    true,
		ApprovalStatus: "pending",
	})
	if err != nil {
		t.Fatalf("Project returned error: %v", err)
	}
	// 期望 3 条 tag：security, pii, approval
	if len(db.execCalls) != 3 {
		t.Fatalf("expected 3 Exec calls, got %d", len(db.execCalls))
	}
	// 验证第一条调用的参数顺序
	first := db.execCalls[0]
	if first.args[0] != "sess-1" || first.args[1] != "default" {
		t.Errorf("first call args[0:2] = %v,%v, want sess-1,default", first.args[0], first.args[1])
	}
	// 验证 SQL 含 ON CONFLICT DO NOTHING
	if !containsStr(first.sql, "ON CONFLICT") {
		t.Errorf("SQL missing ON CONFLICT: %s", first.sql)
	}
}

// TestProject_NilDBNoOp 验证 db 为 nil 时 no-op。
func TestProject_NilDBNoOp(t *testing.T) {
	p := NewSessionStateProjector(nil, nil)
	if err := p.Project(context.Background(), SessionStateProjection{GwSessionID: "s"}); err != nil {
		t.Errorf("nil db should be no-op, got error: %v", err)
	}
}

// TestProject_EmptySessionNoOp 验证空 session id 时 no-op。
func TestProject_EmptySessionNoOp(t *testing.T) {
	db := &fakeDB{}
	p := NewSessionStateProjector(db, nil)
	if err := p.Project(context.Background(), SessionStateProjection{GwSessionID: "", SecurityScore: 9}); err != nil {
		t.Errorf("empty session should be no-op, got error: %v", err)
	}
	if len(db.execCalls) != 0 {
		t.Errorf("expected 0 Exec calls for empty session, got %d", len(db.execCalls))
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
