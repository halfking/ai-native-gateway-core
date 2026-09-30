package jsoncol

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
)

type payload struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestDecode(t *testing.T) {
	t.Run("valid json decodes", func(t *testing.T) {
		var got payload
		if !Decode("op.valid", []byte(`{"name":"a","count":2}`), &got) {
			t.Fatalf("valid json must report true")
		}
		if got.Name != "a" || got.Count != 2 {
			t.Fatalf("decode mismatch: %+v", got)
		}
	})

	t.Run("empty raw is not an error and leaves zero value", func(t *testing.T) {
		got := payload{Name: "untouched", Count: 9}
		if !Decode("op.empty", nil, &got) {
			t.Fatalf("nil raw must report true (not bad data)")
		}
		if got.Name != "untouched" || got.Count != 9 {
			t.Fatalf("empty raw must not modify dst, got %+v", got)
		}
		if !Decode("op.zerolen", []byte{}, &got) {
			t.Fatalf("zero-length raw must report true")
		}
		if got.Count != 9 {
			t.Fatalf("zero-length raw must not modify dst, got %+v", got)
		}
	})

	t.Run("malformed json reports false and leaves zero value", func(t *testing.T) {
		got := payload{Name: "preset", Count: 5}
		if Decode("op.bad", []byte(`{not json`), &got) {
			t.Fatalf("malformed json must report false")
		}
		if got.Count != 5 {
			t.Fatalf("dst must be left as the caller's value on failure, got %+v", got)
		}
	})

	t.Run("type mismatch is a failure, not a silent partial", func(t *testing.T) {
		var got payload
		// count 是字符串而非数字：整条记录不可用
		if Decode("op.mismatch", []byte(`{"name":"a","count":"two"}`), &got) {
			t.Fatalf("type mismatch must report false, not a partially-populated struct")
		}
		// 12h 审计钉测：encoding/json 对 UnmarshalTypeError 是
		// save-error-then-continue——直接 Unmarshal 进 dst 会留下
		// Name="a" 的**部分填充**对象（真实受害者：center.Command.Args
		// 部分参数照常下发执行）。fresh 形态必须保证失败时 dst 一个
		// 字节都不动，包括「部分字段本可解析」的形态。
		if got.Name != "" || got.Count != 0 {
			t.Fatalf("dst must stay untouched on decode failure (no partial fill), got %+v", got)
		}
	})

	t.Run("decode failure never partially overwrites a preset dst", func(t *testing.T) {
		got := payload{Name: "preset", Count: 5}
		if Decode("op.partial", []byte(`{"name":"evil","count":"bad"}`), &got) {
			t.Fatalf("type mismatch must report false")
		}
		if got.Name != "preset" || got.Count != 5 {
			t.Fatalf("caller's original value must survive a failed decode, got %+v", got)
		}
	})
}

// TestDecode_DistinguishesEmptyFromCorrupt 钉住本包存在的理由：调用方必须
// 能区分「列本来就是空的」与「列有内容但坏了」。两者都返回 true/false 的
// 不同组合，若实现把空 raw 也判成失败，这个区分就消失了。
func TestDecode_DistinguishesEmptyFromCorrupt(t *testing.T) {
	var a, b payload
	emptyOK := Decode("op.empty", nil, &a)
	corruptOK := Decode("op.corrupt", []byte(`{oops`), &b)
	if !emptyOK {
		t.Errorf("empty raw must not be reported as corrupt")
	}
	if corruptOK {
		t.Errorf("corrupt raw must be reported as corrupt")
	}
}

// TestDecode_PreservesUnknownFields 确认这不是「严格模式」：jsonb 里的
// 额外键不应让整条记录判为坏数据（那会让滚动升级期的旧数据整片变零值）。
func TestDecode_PreservesUnknownFields(t *testing.T) {
	var got payload
	raw, _ := json.Marshal(map[string]any{
		"name": "a", "count": 1, "future_field": []int{1, 2, 3},
	})
	if !Decode("op.extra", raw, &got) {
		t.Fatalf("unknown fields must not fail the decode")
	}
	if !bytes.Contains(raw, []byte("future_field")) {
		t.Fatalf("fixture sanity")
	}
	if got.Name != "a" || got.Count != 1 {
		t.Fatalf("known fields must still decode: %+v", got)
	}
}

// captureWarns 把默认 slog logger 换成捕获 handler，返回读WARN条数的函数与
// 恢复函数。用于钉住「null/空 raw 不是坏数据、不留 Warn」。
func captureWarns(t *testing.T) (warnCount func() int, restore func()) {
	t.Helper()
	var mu sync.Mutex
	count := 0
	h := slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	})
	wrapped := slog.Handler(&countingHandler{inner: h, mu: &mu, count: &count})
	old := slog.Default()
	slog.SetDefault(slog.New(wrapped))
	return func() int {
			mu.Lock()
			defer mu.Unlock()
			return count
		}, func() {
			slog.SetDefault(old)
		}
}

type countingHandler struct {
	inner slog.Handler
	mu    *sync.Mutex
	count *int
}

func (h *countingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= slog.LevelWarn
}
func (h *countingHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	*h.count++
	h.mu.Unlock()
	return nil
}
func (h *countingHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(name string) slog.Handler       { return h }

// TestDecode_JSONNullTreatsAsAbsent 钉住 2026-10-01 审计修复：JSON 字面量
// null 视为「无值」——dst 保持调用方 pre-seed 的原值、返回 true、无 Warn。
// 修复前 null 走常规 Unmarshal（恒成功），fresh 零值覆盖 dst：
//   - pre-seed bool true 被打成 false（受害实例 cmd/gateway mqEnabled）；
//   - make 后的空切片被替换成 nil（与 admin credential_monitor.go 注释承诺
//     「null 不动 dst」相反）；
//   - 自定义 struct 非零默认被清零。
func TestDecode_JSONNullTreatsAsAbsent(t *testing.T) {
	warnCount, restore := captureWarns(t)
	defer restore()

	t.Run("pre-seeded bool survives null", func(t *testing.T) {
		enabled := true
		if !Decode("op.nullbool", []byte(`null`), &enabled) {
			t.Fatalf("null must report true (not bad data)")
		}
		if !enabled {
			t.Fatalf("pre-seeded true must survive JSON null (mqEnabled regression)")
		}
	})

	t.Run("pre-seeded struct survives null", func(t *testing.T) {
		got := payload{Name: "preset", Count: 7}
		if !Decode("op.nullstruct", []byte(`null`), &got) {
			t.Fatalf("null must report true")
		}
		if got.Name != "preset" || got.Count != 7 {
			t.Fatalf("pre-seeded struct must survive JSON null, got %+v", got)
		}
	})

	t.Run("pre-seeded non-nil slice survives null", func(t *testing.T) {
		got := make([]string, 0)
		got = append(got, "sentinel") // 非空更严：nil 化或清空都逃不过断言
		if !Decode("op.nullslice", []byte(`null`), &got) {
			t.Fatalf("null must report true")
		}
		if len(got) != 1 || got[0] != "sentinel" {
			t.Fatalf("pre-seeded slice must survive JSON null, got %v", got)
		}
	})

	t.Run("null emits no warn", func(t *testing.T) {
		var got payload
		Decode("op.nullquiet", []byte(`null`), &got)
		Decode("op.emptyquiet", nil, &got)
		if n := warnCount(); n != 0 {
			t.Fatalf("null/empty decode must not emit Warn, got %d", n)
		}
	})

	t.Run("whitespace-padded null still short-circuits", func(t *testing.T) {
		enabled := true
		if !Decode("op.nullpad", []byte("  null\n"), &enabled) {
			t.Fatalf("padded null must report true")
		}
		if !enabled {
			t.Fatalf("padded null must not modify dst")
		}
	})

	t.Run("bad json still fails and keeps original", func(t *testing.T) {
		before := warnCount()
		got := payload{Name: "preset", Count: 5}
		if Decode("op.nullbad", []byte(`{not json`), &got) {
			t.Fatalf("malformed json must report false")
		}
		if got.Name != "preset" || got.Count != 5 {
			t.Fatalf("bad json must keep caller value, got %+v", got)
		}
		if warnCount() <= before {
			t.Fatalf("bad json must emit Warn")
		}
	})

	t.Run("legal value still overwrites", func(t *testing.T) {
		got := payload{Name: "preset", Count: 5}
		if !Decode("op.nulllegal", []byte(`{"name":"fresh","count":1}`), &got) {
			t.Fatalf("legal json must report true")
		}
		if got.Name != "fresh" || got.Count != 1 {
			t.Fatalf("legal json must overwrite dst, got %+v", got)
		}
	})
}
