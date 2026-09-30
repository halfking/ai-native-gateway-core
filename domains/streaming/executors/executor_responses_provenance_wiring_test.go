package executors

import (
	"os"
	"strings"
	"testing"
)

// R34-A1 批判复审补：executor 接线静态守卫。上轮只测了 helper 本身，
// 若有人 revert executor_chat.go 的接线（WithMeta 调用/provenance merge）
// 不会有任何测试变红——本守卫钉住三个必要接线点：
//  1. proactive 压缩走 WithMeta 变体并写入 responsesProvenance；
//  2. 4xx recovery aggressive 裁剪同样写 provenance；
//  3. 三处成功 result 构造点把 provenance merge 进 CompressionMeta。
//
// 变异验证：注释掉任一类接线 → 对应断言红。
func TestResponsesProvenanceWiring_ExecutorChatGuard(t *testing.T) {
	src, err := os.ReadFile("executor_chat.go")
	if err != nil {
		src, err = os.ReadFile("domains/streaming/executors/executor_chat.go")
		if err != nil {
			t.Fatalf("read executor_chat.go: %v", err)
		}
	}
	code := string(src)

	mustContain := []struct {
		frag string
		why  string
	}{
		{"CompressResponsesInputIfNeededWithMeta", "proactive path must use WithMeta variant"},
		{"CompressResponsesInputAggressivelyWithMeta", "4xx recovery path must use WithMeta variant"},
		{"recordResponsesInputTrimMeta(&responsesProvenance", "provenance must be recorded into carrier"},
	}
	for _, m := range mustContain {
		if !strings.Contains(code, m.frag) {
			t.Errorf("wiring lost: %s (%s)", m.frag, m.why)
		}
	}

	mergeSite := "mergeCompressionMeta(contextLenRecovery.lastMeta, mergeCompressionMeta(responsesProvenance, preTrimMeta))"
	n := strings.Count(code, mergeSite)
	if n != 3 {
		t.Errorf("expected provenance merged at exactly 3 success-result sites, got %d", n)
	}
}
