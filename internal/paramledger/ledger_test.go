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
