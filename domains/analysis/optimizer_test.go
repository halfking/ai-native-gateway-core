package analysis

import (
	"context"
	"strings"
	"testing"
)

// R74：save 的 marshal 错误必须上抛而非吞成 "null" 字符串落库。
// Evidence 是 map[string]any，放入 channel 值触发 json.Marshal 的
// UnsupportedTypeError——错误路径在 db.Exec 之前返回，nil db 不被触碰。
func TestSave_MarshalErrorPropagated(t *testing.T) {
	a := &OptimizationAdviser{db: nil} // marshal 失败必须先于任何 DB 访问返回
	err := a.save(context.Background(), &sessionStatsForOpt{}, suggestion{
		Category: "context_redundancy",
		Evidence: map[string]any{"bad": make(chan int)},
	})
	if err == nil {
		t.Fatal("expected marshal error to propagate, got nil")
	}
	if !strings.Contains(err.Error(), "marshal evidence") {
		t.Fatalf("expected wrap prefix %q, got %q", "marshal evidence", err.Error())
	}
}
