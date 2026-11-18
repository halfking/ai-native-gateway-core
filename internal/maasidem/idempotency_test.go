package maasidem

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
)

// ★ D6 / D8（2026-11-19 审计轮）幂等层钉测。
//
// 缺陷本体：
//
//	· D8：幂等键空串不被拒 ⇒ 同一 tenant + command_type 下所有空键命令
//	  **互相误判为重放**（第二个命令直接返回第一个的响应）。
//	· D6：recordIdempotency 落库固定写 '{}'::jsonb ⇒ payload 从未序列化；
//	  调用方又写 "// TODO: Unmarshal cached response" ⇒ 重放返回零值空
//	  结构（客户端无法对账）。

// TestRequireKeyRejectsEmpty 钉住 D8 的入口校验。
func TestRequireKeyRejectsEmpty(t *testing.T) {
	for _, key := range []string{"", "   ", "\t", "\n"} {
		err := RequireKey(key)
		if err == nil {
			t.Fatalf("RequireKey(%q) 必须拒绝（空键会让命令互相误判为重放）", key)
		}
		var cErr interface{ Error() string }
		if !errors.As(err, &cErr) {
			t.Fatalf("RequireKey(%q) 的错误必须实现 error：%v", key, err)
		}
		if !strings.Contains(err.Error(), ErrorIdempotencyKeyRequired) {
			t.Fatalf("RequireKey(%q) 的错误必须带错误码 %s，got %v", key, ErrorIdempotencyKeyRequired, err)
		}
	}
	for _, key := range []string{"k", " key-1 ", "abc123"} {
		if err := RequireKey(key); err != nil {
			t.Fatalf("RequireKey(%q) 必须放行，got %v", key, err)
		}
	}
}

// TestRecordSerializesPayloads 钉住 D6 的根因修复：落库的必须是**真实**
// request/response JSON，而不是 '{}'。
func TestRecordSerializesPayloads(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	var gotReq, gotResp string
	mock.ExpectExec("INSERT INTO maas_idempotency_log").
		WithArgs("tenant-a", "UsageSettle", "key-1", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	// 用一个能截获实参的 Execer。
	spy := &spyExecer{}
	err = Record(t.Context(), spy, "tenant-a", "UsageSettle", "key-1",
		map[string]any{"actual": 3},
		map[string]any{"entry_id": "entry_1", "status": "settled"})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	gotReq = string(spy.reqJSON)
	gotResp = string(spy.respJSON)

	// ★ 旧实现写的是 '{}'::jsonb ⇒ 这里必须能看到真实字段。
	if !strings.Contains(gotReq, "actual") {
		t.Fatalf("request payload 未被序列化：%q", gotReq)
	}
	if !strings.Contains(gotResp, "entry_id") || !strings.Contains(gotResp, "settled") {
		t.Fatalf("response payload 未被序列化：%q", gotResp)
	}
	// 且必须是合法 JSON。
	var probe map[string]any
	if err := json.Unmarshal(spy.respJSON, &probe); err != nil {
		t.Fatalf("response payload 不是合法 JSON：%v", err)
	}
}

// TestReplayDeserializesIntoResponseType 钉住 D6 的另一半：重放必须真的
// 反序列化出结构，而不是返回零值。
func TestReplayDeserializesIntoResponseType(t *testing.T) {
	type resp struct {
		EntryID string `json:"entry_id"`
		Status  string `json:"status"`
	}

	cached := json.RawMessage(`{"entry_id":"entry_1","status":"settled"}`)
	var out resp
	if err := Replay(cached, &out); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if out.EntryID != "entry_1" || out.Status != "settled" {
		t.Fatalf("Replay 未还原响应：%+v", out)
	}

	// 空 payload ⇒ 必须报错（不得静默返回零值）。
	var out2 resp
	if err := Replay(nil, &out2); err == nil {
		t.Fatal("Replay(nil) 必须报错（旧实现返回零值空响应）")
	}
	// 非法 JSON ⇒ 必须报错。
	if err := Replay(json.RawMessage(`{not json`), &out2); err == nil {
		t.Fatal("Replay(非法 JSON) 必须报错")
	}
}

// TestCheckReturnsStoredPayload 钉住 Check 返回的是**原始 JSON**
// （而不是 nil / interface{}），这样调用方才能按 commandType 反序列化。
func TestCheckReturnsStoredPayload(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery("SELECT response_payload").
		WithArgs("tenant-a", "UsageSettle", "key-1").
		WillReturnRows(mock.NewRows([]string{"response_payload"}).AddRow([]byte(`{"entry_id":"entry_1"}`)))

	payload, err := Check(t.Context(), mock, "tenant-a", "UsageSettle", "key-1")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !strings.Contains(string(payload), "entry_id") {
		t.Fatalf("Check 未返回已落库的 payload：%q", payload)
	}
}

// spyExecer 截获 Record 实际写进 DB 的两个 JSONB 参数。
type spyExecer struct {
	reqJSON  []byte
	respJSON []byte
}

func (s *spyExecer) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if len(args) >= 5 {
		if b, ok := args[3].([]byte); ok {
			s.reqJSON = b
		}
		if b, ok := args[4].([]byte); ok {
			s.respJSON = b
		}
	}
	return pgconn.CommandTag{}, nil
}
