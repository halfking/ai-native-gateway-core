package admin

// logs_null_success_test.go —— 「行里 success 为 NULL 不得让整页 503」的门（2026-11-25）。
//
// # 生产实证（211，Maestro 第二层探针的多条件矩阵撞出）
//
//	request_status=in_progress × 近 30 天 ⇒ HTTP 503，服务端日志：
//	  admin listLogs scan failed  err="can't scan into dest[22] (col: success):
//	                                 cannot scan NULL into *bool"   count=53
//
//	session_turns(_hot) 里有 53 行 success IS NULL（同一时刻的一批历史残缺行：无
//	status_code / error_kind / request_status）。它们经 requestLogStatusExpr 归为
//	in_progress ⇒ 这个过滤恰好把它们全部选出来 ⇒ 第一行就扫描失败 ⇒ rows.Err()
//	⇒ 503。修前 requestLogRow.Success 是非空 bool；任何把它们选出来的查询（或它们
//	恰好落在某一页里）都会让整页失败——详情接口（getLog）用的是同一个结构体。
//
// 修法：Success 改为 *bool（JSON 如实为 null）。状态以 request_status 为准。

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// TestRequestLogRow_SuccessIsNullable 钉住字段类型：非指针 ⇒ NULL 又扫不进去。
func TestRequestLogRow_SuccessIsNullable(t *testing.T) {
	f, ok := reflect.TypeOf(requestLogRow{}).FieldByName("Success")
	if !ok {
		t.Fatal("requestLogRow 没有 Success 字段")
	}
	if f.Type.Kind() != reflect.Ptr {
		t.Fatalf("requestLogRow.Success 必须是可空指针（库里存在 success IS NULL 的行），现在是 %s", f.Type)
	}
	// JSON 契约名不变。
	if got := f.Tag.Get("json"); got != "success" {
		t.Fatalf("Success 的 json tag 被改动了：%q", got)
	}
}

func TestRequestLogRow_NullSuccessMarshalsAsNull(t *testing.T) {
	b, err := json.Marshal(requestLogRow{Ts: time.Unix(0, 0).UTC(), RequestID: "r1", RequestStatus: "in_progress"})
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if v, has := back["success"]; !has || v != nil {
		t.Fatalf("success 为未知时 JSON 应是 null，得到 %v", back["success"])
	}
	yes := true
	b, _ = json.Marshal(requestLogRow{Success: &yes})
	var back2 map[string]any
	_ = json.Unmarshal(b, &back2)
	if back2["success"] != true {
		t.Fatalf("success=true 应如实输出 true，得到 %v", back2["success"])
	}
}

// TestListRowScan_AcceptsNullSuccess 在真库上验证：NULL::bool 能扫进这个字段。
//
// ⚠ 门禁纪律：没配 TEST_PG_URL 时 t.Skip——跳过不构成证据。
func TestListRowScan_AcceptsNullSuccess(t *testing.T) {
	pool, _ := logsRealDBPoolWithTracer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var nullRow requestLogRow
	if err := pool.QueryRow(ctx, "SELECT NULL::bool").Scan(&nullRow.Success); err != nil {
		t.Fatalf("NULL success 扫描失败（这就是 503 的直接原因）: %v", err)
	}
	if nullRow.Success != nil {
		t.Fatalf("NULL 应保持 nil，得到 %v", *nullRow.Success)
	}

	var trueRow requestLogRow
	if err := pool.QueryRow(ctx, "SELECT true::bool").Scan(&trueRow.Success); err != nil {
		t.Fatalf("true 扫描失败: %v", err)
	}
	if trueRow.Success == nil || !*trueRow.Success {
		t.Fatalf("true 应扫成 *bool(true)，得到 %v", trueRow.Success)
	}

	var falseRow requestLogRow
	if err := pool.QueryRow(ctx, "SELECT false::bool").Scan(&falseRow.Success); err != nil {
		t.Fatalf("false 扫描失败: %v", err)
	}
	if falseRow.Success == nil || *falseRow.Success {
		t.Fatalf("false 应扫成 *bool(false)，得到 %v", falseRow.Success)
	}
}
