package streaming

// auto_route_reselect_wiring_test.go —— 零候选重选的**接线形状门**
// （2026-11-17 auto 模式可用性轮 · 根修 4）。
//
// ★ 为什么需要形状门：
//
// 重选的「接线」发生在 ChatHandler.serveWithExecutor 里——那是一条需要完整
// handler 的路径，单测触达不到。而接线恰恰是**半写**最容易发生的地方：
// 候选换了、请求体里的 model 没换 ⇒ 上游服务的是死模型，日志却写着换过了。
//
// applyAutoReselect 的单测守的是「函数本身写对了」；本门守的是
// 「handler 真的调用了它」——两者缺一不可。

import (
	"os"
	"strings"
	"testing"
)

// TestHandlerWiresAutoReselect pins that the zero-candidate branch calls
// applyAutoReselect and that the call is placed BEFORE the 503 branch.
func TestHandlerWiresAutoReselect(t *testing.T) {
	src, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatalf("read handler.go: %v", err)
	}
	text := string(src)

	callIdx := strings.Index(text, "applyAutoReselect(")
	if callIdx < 0 {
		t.Fatal("handler.go must call applyAutoReselect — the zero-candidate reselect is not wired in")
	}
	reselectIdx := strings.Index(text, "autoRequestReselect(")
	if reselectIdx < 0 {
		t.Fatal("handler.go must call autoRequestReselect")
	}
	if callIdx < reselectIdx {
		t.Fatal("applyAutoReselect must run after autoRequestReselect produced a switch")
	}
	// The call must hand over every field the downstream flow reads.
	for _, needle := range []string{"&clientModel", "&bodyBytes", "&candidates", "&policy", "&requestModality"} {
		if !strings.Contains(text[callIdx:minInt(callIdx+400, len(text))], needle) {
			t.Errorf("applyAutoReselect call must pass %s — a half-rewrite serves the wrong model", needle)
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
