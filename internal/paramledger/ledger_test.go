package paramledger

import (
	"bytes"
	"testing"
	"time"
)

func TestRecordDedupAndLookup(t *testing.T) {
	l := New(nil)
	l.Record("req-1", Adjustment{Field: "reasoning_effort", Original: "x-high", Sent: "high", Action: ActionClamp, Reason: "test"})
	// 同一调整重复报告（多次 attempt / finalize）不膨胀。
	l.Record("req-1", Adjustment{Field: "reasoning_effort", Original: "x-high", Sent: "high", Action: ActionClamp, Reason: "test"})
	// 不同 adjustment 追加。
	l.Record("req-1", Adjustment{Field: "temperature", Original: "2", Sent: "1", Action: ActionClamp, Reason: "test"})

	entry := l.Lookup("req-1")
	if entry == nil || len(entry.Adjustments) != 2 {
		t.Fatalf("entry = %+v", entry)
	}
	if l.Lookup("req-nope") != nil {
		t.Fatal("unknown request should return nil")
	}
	if l.Len() != 1 {
		t.Fatalf("len = %d", l.Len())
	}
}

func TestRecordNilLedgerSafe(t *testing.T) {
	var l *Ledger
	l.Record("req", Adjustment{Field: "x"})
	if l.Lookup("req") != nil {
		t.Fatal("nil ledger must be a safe no-op")
	}
	if got := l.RestoreResponsesEffort([]byte(`{"effort":"high"}`), "req"); !bytes.Equal(got, []byte(`{"effort":"high"}`)) {
		t.Fatalf("nil ledger restore = %s", got)
	}
}

func TestRestoreResponsesEffort(t *testing.T) {
	l := New(nil)
	l.Record("req-1",
		Adjustment{Field: "reasoning_effort", Original: "x-high", Sent: "high", Action: ActionClamp, Reason: "test"},
		Adjustment{Field: "reasoning.effort", Original: "x-high", Sent: "xhigh", Action: ActionNormalize, Reason: "test"},
	)

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "clamp echo restored",
			in:   `{"id":"resp_1","reasoning":{"effort":"high","summary":"auto"}}`,
			want: `{"id":"resp_1","reasoning":{"effort":"x-high","summary":"auto"}}`,
		},
		{
			name: "normalized echo restored",
			in:   `{"id":"resp_1","reasoning":{"effort":"xhigh"}}`,
			want: `{"id":"resp_1","reasoning":{"effort":"x-high"}}`,
		},
		{
			name: "non-matching value untouched",
			in:   `{"id":"resp_1","reasoning":{"effort":"medium"}}`,
			want: `{"id":"resp_1","reasoning":{"effort":"medium"}}`,
		},
		{
			name: "no effort field untouched",
			in:   `{"id":"resp_1","output":[]}`,
			want: `{"id":"resp_1","output":[]}`,
		},
		{
			// R51：同值 effort 出现在 reasoning 对象之外（metadata）时
			// 不得被误改——ReplaceAll / 盲取第一次出现都会误伤这里。
			name: "same-shaped effort under other key untouched",
			in:   `{"id":"resp_1","metadata":{"effort":"x-high"},"reasoning":{"effort":"high","summary":"auto"}}`,
			want: `{"id":"resp_1","metadata":{"effort":"x-high"},"reasoning":{"effort":"x-high","summary":"auto"}}`,
		},
	}
	for _, tc := range cases {
		got := l.RestoreResponsesEffort([]byte(tc.in), "req-1")
		if string(got) != tc.want {
			t.Fatalf("%s: got %s want %s", tc.name, got, tc.want)
		}
	}
	// 未知请求原样返回。
	got := l.RestoreResponsesEffort([]byte(`{"reasoning":{"effort":"high"}}`), "req-other")
	if string(got) != `{"reasoning":{"effort":"high"}}` {
		t.Fatalf("unknown request: %s", got)
	}
}

func TestRestoreOnlyReplacesExactSentValue(t *testing.T) {
	l := New(nil)
	// strip 调整（Sent 为空）不参与还原。
	l.Record("req-1", Adjustment{Field: "thinking", Original: "thinking", Sent: "", Action: ActionStrip, Reason: "test"})
	got := l.RestoreResponsesEffort([]byte(`{"reasoning":{"effort":"high"}}`), "req-1")
	if string(got) != `{"reasoning":{"effort":"high"}}` {
		t.Fatalf("strip-only entry must not rewrite: %s", got)
	}
}

// R51（2026-09-21）：还原只作用于 reasoning 参数对象（顶层或 SSE 帧
// response.reasoning）内的第一次回显。模型输出文本里转义后的同形 JSON
// 串、其他结构位置的同值 effort 字段都不得被误改。
func TestRestoreResponsesEffortScopedToReasoningObject(t *testing.T) {
	l := New(nil)
	l.Record("req-1", Adjustment{Field: "reasoning.effort", Original: "high", Sent: "medium", Action: ActionClamp, Reason: "test"})

	// 非流式：metadata 在 reasoning 之前且同值——ReplaceAll / 盲取第一次
	// 出现的实现会把 metadata 误改，这里必须只有 reasoning 被还原。
	in := `{"id":"resp_1","metadata":{"effort":"medium"},"reasoning":{"effort":"medium"},"output":[{"type":"message","content":[{"type":"output_text","text":"quote: {\"effort\":\"medium\"} {\"reasoning\":{\"effort\":\"medium\"}}"}]}]}`
	want := `{"id":"resp_1","metadata":{"effort":"medium"},"reasoning":{"effort":"high"},"output":[{"type":"message","content":[{"type":"output_text","text":"quote: {\"effort\":\"medium\"} {\"reasoning\":{\"effort\":\"medium\"}}"}]}]}`
	if got := string(l.RestoreResponsesEffort([]byte(in), "req-1")); got != want {
		t.Fatalf("non-stream body:\n got  %s\n want %s", got, want)
	}

	// SSE 生命周期帧：response.reasoning 路径仍被还原。
	frame := `{"type":"response.created","response":{"id":"resp_1","reasoning":{"effort":"medium"}}}`
	wantFrame := `{"type":"response.created","response":{"id":"resp_1","reasoning":{"effort":"high"}}}`
	if got := string(l.RestoreResponsesEffort([]byte(frame), "req-1")); got != wantFrame {
		t.Fatalf("sse frame: got %s want %s", got, wantFrame)
	}

	// SSE 原始整帧（带 event:/data: 前缀，流式钩子 restoreEchoFrame
	// 传入的形态）：前缀字节必须原样保留，仅载荷内回显被还原。
	rawFrame := "event: response.created\ndata: " + frame + "\n\n"
	wantRawFrame := "event: response.created\ndata: " + wantFrame + "\n\n"
	if got := string(l.RestoreResponsesEffort([]byte(rawFrame), "req-1")); got != wantRawFrame {
		t.Fatalf("raw sse frame: got %q want %q", got, wantRawFrame)
	}

	// 无 reasoning 对象（防御）：原样返回，不因结构缺失改坏数据。
	noObj := `{"note":"{\"effort\":\"medium\"}"}`
	if got := string(l.RestoreResponsesEffort([]byte(noObj), "req-1")); got != noObj {
		t.Fatalf("no reasoning object must be untouched: %s", got)
	}
}

func TestExpiry(t *testing.T) {
	l := New(nil)
	l.Record("req-1", Adjustment{Field: "a", Original: "1", Sent: "2", Action: ActionClamp, Reason: "t"})
	// 手动把过期时间拨到过去。
	l.mu.Lock()
	l.entries["req-1"].expireAt = time.Now().Add(-time.Second)
	l.mu.Unlock()
	if l.Lookup("req-1") != nil {
		t.Fatal("expired entry should not be returned")
	}
}

func TestFIFOCap(t *testing.T) {
	l := New(nil)
	for i := 0; i < maxEntries+50; i++ {
		l.Record("req-"+string(rune('a'+i%26))+string(rune('0'+i/26)), Adjustment{Field: "f", Original: "1", Sent: "2", Action: ActionClamp})
	}
	if l.Len() > maxEntries {
		t.Fatalf("len = %d exceeds cap %d", l.Len(), maxEntries)
	}
}
