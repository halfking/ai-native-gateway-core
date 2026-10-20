package v2

// turn_host_tags_test.go —— 轮次级 host / tags 的写路径门（迁移 885，审计 A5）。
//
// 这道门钉的是**实际绑定到 INSERT 的值**，不是「某个函数返回了非空」：
//   · 只测 resolveTurnHost/resolveTurnTags ⇒ 写方即使把 $100 写成
//     nilIfEmpty(rec.Host)（空 ⇒ NULL），门照样全绿 —— 断言没钉住任何东西；
//   · 只测 INSERT 语句里出现了 host/tags 两列 ⇒ 同样不钉「值是不是非空」。
// ⇒ 这里两样都要：INSERT 的**列清单**里有 host/tags（靠 pgxmock 的正则
// 期望串钉住），且**绑定值**非空（靠自定义 Argument 匹配器钉住）。
//
// 同时，这条门顺带钉住了**参数个数**（101）：pgxmock 在实参数与期望数不
// 等时报「expected 101, but got N arguments」。位置参数列表一旦有人在中段
// 插列（例如把 host 插到 client_protocol 之前），这里当场红 —— 这正是
// 审计 §9.208 记录过的那类事故（列加进清单、实参也加了，SELECT 段漏了
// $99 ⇒ 运行时才报 bind message supplies N parameters）。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// nonEmptyStringArg matches a bound string argument that must not be empty.
type nonEmptyStringArg struct {
	what string
}

func (m nonEmptyStringArg) Match(v interface{}) bool {
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) != ""
}

// nonEmptyTagsArg matches a bound text[] argument that must not be empty.
//
// Accepts both shapes the writer might bind: pgtype.FlatArray[string] (the
// current form) and a plain []string. An empty array is NOT accepted — the
// column must never be written as an empty array, because "no tags" and
// "tags not written" must not look the same.
type nonEmptyTagsArg struct{}

func (m nonEmptyTagsArg) Match(v interface{}) bool {
	switch t := v.(type) {
	case pgtype.FlatArray[string]:
		return len(t) > 0
	case []string:
		return len(t) > 0
	default:
		return false
	}
}

// TestTurnWriterWritesHostAndTags asserts the write path binds a non-empty
// host and a non-empty tag array, and that the INSERT's column list carries
// both columns.
func TestTurnWriterWritesHostAndTags(t *testing.T) {
	mock, err := pgxmock.NewConn()
	require.NoError(t, err)
	defer func() { _ = mock.Close(context.Background()) }()

	rec := TurnRecord{
		SessionID:      "sess-host-tags",
		TenantID:       "tenant-host-tags",
		RequestID:      "req-host-tags",
		Ts:             time.Now().UTC(),
		SourceKind:     "live",
		ClientProtocol: "openai-chat",
		AgentType:      "assistant",
		TaskType:       "chat",
	}

	// 99 leading args ($1..$99) + host ($100) + tags ($101).
	args := make([]interface{}, 0, 101)
	for i := 0; i < 99; i++ {
		args = append(args, pgxmock.AnyArg())
	}
	args = append(args, nonEmptyStringArg{what: "host"}, nonEmptyTagsArg{})

	mock.ExpectBegin()
	// 顺序（真实写路径）：会话锁 → 读 MAX(turn_no) → 请求锁 → INSERT。
	mock.ExpectExec("pg_advisory_xact_lock").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("pg_advisory_xact_lock").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("COALESCE\\(MAX\\(turn_no\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"turn_no"}).AddRow(1))
	// 第二条 MAX 查询：归档侧（父表）。hot 侧返回 1 ⇒ 写方还会补查一次父表。
	mock.ExpectQuery("COALESCE\\(MAX\\(turn_no\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"turn_no"}).AddRow(1))
	// The pattern pins the **column list**: host 与 tags 必须出现在
	// "INSERT INTO public.session_turns_hot (" 与 ") SELECT" 之间。
	// pgxmock 的期望串按正则匹配（未锚定），且实际 SQL 的空白被压成单空格。
	mock.ExpectExec(`INSERT INTO public\.session_turns_hot \(.*\bhost\b.*\btags\b.*\) SELECT`).
		WithArgs(args...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	w := newTurnWriter(mock)
	turnNo, err := w.AppendTurn(context.Background(), rec)
	require.NoError(t, err)
	require.Equal(t, 1, turnNo)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestResolveTurnHostIsNeverEmpty pins the host resolution on its own: an
// unset record must still get a usable identity, and an explicit host must
// win.
func TestResolveTurnHostIsNeverEmpty(t *testing.T) {
	if got := resolveTurnHost(TurnRecord{}); strings.TrimSpace(got) == "" {
		t.Fatal("host 不得为空（未设置时用进程身份兜底）")
	}
	if got := resolveTurnHost(TurnRecord{Host: "  gw-explicit  "}); got != "gw-explicit" {
		t.Fatalf("显式 host 必须优先（且去空白），实得 %q", got)
	}
	if got := resolveTurnHost(TurnRecord{Host: "   "}); strings.TrimSpace(got) == "" {
		t.Fatal("空白 host 不得被当作身份（应回落到进程身份）")
	}
}

// TestResolveTurnTagsIsNeverEmpty pins the tag derivation: the array always
// carries the namespace marker, explicit tags win, and blanks are dropped.
func TestResolveTurnTagsIsNeverEmpty(t *testing.T) {
	derived := resolveTurnTags(TurnRecord{SourceKind: "live", ClientProtocol: "openai-chat"})
	if len(derived) == 0 {
		t.Fatal("tags 不得为空数组")
	}
	if derived[0] != TurnTagNamespace {
		t.Fatalf("tags[0] 必须是命名空间标记 %q，实得 %q", TurnTagNamespace, derived[0])
	}
	if !containsTag(derived, "src:live") {
		t.Fatalf("派生 tags 必须含 source_kind 标签：%v", derived)
	}
	if !containsTag(derived, "proto:openai-chat") {
		t.Fatalf("派生 tags 必须含 client_protocol 标签：%v", derived)
	}

	explicit := resolveTurnTags(TurnRecord{Tags: []string{" alpha ", "", "beta"}})
	if len(explicit) != 2 || explicit[0] != "alpha" || explicit[1] != "beta" {
		t.Fatalf("显式 tags 必须优先（且去掉空白项）：%v", explicit)
	}

	// 全空白 ⇒ 回落派生（而不是落一个空数组）。
	fallback := resolveTurnTags(TurnRecord{Tags: []string{"  ", ""}})
	if len(fallback) == 0 || fallback[0] != TurnTagNamespace {
		t.Fatalf("全空白的显式 tags 必须回落到派生标签：%v", fallback)
	}
}

func containsTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}
