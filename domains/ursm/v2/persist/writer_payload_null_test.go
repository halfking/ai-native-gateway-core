package persist

// 2026-10-18（§10.110.63）：payload 空残渣存 NULL 而不是 `{}`。
//
// # 为什么
//
// 生产实测（§10.110.62）：**85.0%** 的行携带空对象，而
// `pg_column_size('{}'::jsonb)` = **8 B** —— 空 jsonb 并不免费。
// 1531 行 × 8 B ≈ 34 MB/天 ≈ **12.6 GB/年**，而这些字节**不含任何信息**。
//
// # 这道改动真正的危险不在「存 NULL」，在它旁边的两个陷阱
//
// 1. **占位陷阱**：Flush 里原本有一句「Payload 为空就写 _note 占位」。
//    那是为「json.Marshal 失败」准备的。但 `json.Marshal(map[string]string)`
//    **永远不会失败** ⇒ 那个分支只在异常时可达。
//    改成「残渣为空 ⇒ 保持 nil」之后，**85% 的行会全部命中它**，
//    每行写约 50 B —— 比原来的 8 B 空对象**差 6 倍**。
//    ⇒ 必须引入 `PayloadMarshalFailed` 把两种「为空」区分开。
//
// 2. **空串陷阱**：`payload` 列的 INSERT 参数是 `$22::text::jsonb`。
//    若把 nil 转成 `string(nil)` = `""`，PG 收到的是 `''::jsonb`
//    —— **语法错误，不是 NULL** ⇒ 一行出错，整条 INSERT 失败、
//    整个事务回滚，1,802 行全丢。
//    ⇒ 必须绑**未类型化的 nil**，不能绑 string。
//
// ★ 因此把绑定决策抽成纯函数 payloadArgFor：真库门是 opt-in 的，
//   被静默跳过时（2026-10-17 审计 §七.6）最需要承重的恰恰是第 2 条。

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// 一个**只含 typed 列键**的 hash：残渣必为空 ⇒ payload 必须是 nil。
func TestCollectLeavesPayloadNilWhenResidualIsEmpty(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	if err := rdb.HSet(ctx, "ursm:v2:node:tenant-a:1:model-a", map[string]string{
		"available": "1", "generation": "3", "samples_1m": "2", "lat_p50_ms": "120",
	}).Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rows, err := New(rdb, "ursm:v2:", nil).Collect(ctx)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d, want 1", len(rows))
	}
	if rows[0].Payload != nil {
		t.Fatalf("Payload = %q, want nil — an empty residual must not be stored as {}", rows[0].Payload)
	}
	if rows[0].PayloadMarshalFailed {
		t.Fatal("PayloadMarshalFailed = true although the residual was legitimately empty")
	}
	// 正向对照：值确实进了 typed 列，而不是被丢了。
	if rows[0].Generation != 3 || rows[0].Samples1m != 2 || rows[0].LatP50Ms != 120 {
		t.Fatalf("typed columns not populated: %+v", rows[0])
	}
}

// 残渣里有 payload-resident 键时，payload 必须照旧带上它。
// 这是「前向兼容仓」承诺本身，不能被本次改动削掉。
func TestCollectKeepsResidualPayloadForResidentKeys(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	if err := rdb.HSet(ctx, "ursm:v2:node:tenant-a:1:model-a", map[string]string{
		"available": "1",
		// 未来 hash 新增的键：默认必须留在 payload 里
		"some_future_field": "keep-me",
	}).Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rows, err := New(rdb, "ursm:v2:", nil).Collect(ctx)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d, want 1", len(rows))
	}
	if !strings.Contains(string(rows[0].Payload), "keep-me") {
		t.Fatalf("residual key was dropped: Payload=%q", rows[0].Payload)
	}
}

// ★ 空残渣必须绑**未类型化的 nil**。
func TestPayloadArgForBindsUntypedNilForEmptyResidual(t *testing.T) {
	got := payloadArgFor(Row{})
	if got != nil {
		t.Fatalf("payloadArgFor(Row{}) = %#v, want untyped nil — "+
			"binding an empty string makes `$22::text::jsonb` a SYNTAX ERROR, "+
			"which fails the whole INSERT and rolls back all 1802 rows", got)
	}
}

// ⚠ 矩阵式断言：**任何** Row 形态都不得绑出空串。
// 一条覆盖三种形态，比三条各写一遍更不容易漏。
func TestPayloadArgForNeverBindsAnEmptyString(t *testing.T) {
	cases := []struct {
		name string
		row  Row
	}{
		{"零值 Row", Row{}},
		{"残渣为空但曾 marshal 失败", Row{PayloadMarshalFailed: true}},
		{"有 payload", Row{Payload: []byte(`{"a":1}`)}},
		{"空字节切片", Row{Payload: []byte{}}},
		{"两者同时置位", Row{Payload: []byte(`{"a":1}`), PayloadMarshalFailed: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := payloadArgFor(tc.row)
			if s, ok := got.(string); ok && s == "" {
				t.Fatalf("payloadArgFor bound an empty STRING for %+v", tc.row)
			}
		})
	}
}

func TestPayloadArgForKeepsRealPayloadVerbatim(t *testing.T) {
	const in = `{"empty_response_rate_5m":"0.25"}`
	got := payloadArgFor(Row{Payload: []byte(in)})
	s, ok := got.(string)
	if !ok {
		t.Fatalf("payloadArgFor = %#v, want string", got)
	}
	if s != in {
		t.Fatalf("payload changed: %q, want %q", s, in)
	}
	// 且它必须仍是合法 JSON（INSERT 侧带 ::jsonb cast）。
	var back map[string]any
	if err := json.Unmarshal([]byte(s), &back); err != nil {
		t.Fatalf("bound value is not valid JSON: %v", err)
	}
}

// marshal 失败时占位必须留下痕迹。
//
// ⚠ `json.Marshal(map[string]string)` 永不失败 ⇒ 生产上不可达。
// 这条判据守的是**防御分支别被顺手删掉**，不是生产行为。
func TestPayloadArgForWritesPlaceholderOnMarshalFailure(t *testing.T) {
	got := payloadArgFor(Row{PayloadMarshalFailed: true})
	s, ok := got.(string)
	if !ok {
		t.Fatalf("payloadArgFor = %#v, want a placeholder string", got)
	}
	var back map[string]any
	if err := json.Unmarshal([]byte(s), &back); err != nil {
		t.Fatalf("placeholder is not valid JSON: %v (%q)", err, s)
	}
	if _, ok := back["_note"]; !ok {
		t.Fatalf("placeholder lost its _note marker: %q", s)
	}
}

// 接线门：Flush 必须**真的**走 payloadArgFor。
//
// ★ 上面五条全部只测纯函数。「把函数抽出来、却忘了改调用点」会让它们
//
//	全部绿，而生产照旧每轮写 85% 的 `{}` —— 与 §55 的 M43 同族。
func TestFlushBindsThroughPayloadArgFor(t *testing.T) {
	b, err := os.ReadFile("writer.go")
	if err != nil {
		t.Fatalf("read writer.go: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "payloadArg := payloadArgFor(r)") {
		t.Fatal("Flush must bind the payload column via payloadArgFor(r); " +
			"the extracted decision is otherwise dead code")
	}
	if strings.Contains(src, "payloadStr") {
		t.Fatal("the old payloadStr local is back — binding it would send an empty " +
			"string to $22::text::jsonb (a syntax error), not NULL")
	}
}
