package executors

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestP0BAnthropicStripRetryContractLocked 写死 P0-B 决策：Anthropic 不做
// 同候选剥参重试（Q4 字节契约）。常量 false + 源码不得出现「开启重试」旁路。
func TestP0BAnthropicStripRetryContractLocked(t *testing.T) {
	if anthropicSameCandidateStripRetry {
		t.Fatal("anthropicSameCandidateStripRetry must stay false (Q4 byte contract); update audit 差额表 if deliberately enabling")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	srcPath := filepath.Join(filepath.Dir(file), "executor_anthropic.go")
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if !strings.Contains(src, "anthropicSameCandidateStripRetry = false") {
		t.Fatal("executor_anthropic.go must define anthropicSameCandidateStripRetry = false")
	}
	if anthropicStripRetryContractMarker() != "anthropicSameCandidateStripRetry=false" {
		t.Fatalf("fingerprint marker = %q, want anthropicSameCandidateStripRetry=false", anthropicStripRetryContractMarker())
	}
	if !strings.Contains(src, `anthropicSameCandidateStripRetry=false`) {
		t.Fatal("rodata fingerprint literal anthropicSameCandidateStripRetry=false must stay in source")
	}
	// 生产 4xx 路径只 RecordTerminal，不得调用 chat 腿的 strip-retry 入口名。
	if strings.Contains(src, "reqprobeMaybeStripAndRetry") || strings.Contains(src, "paramrefusalRetry") {
		t.Fatal("anthropic path must not wire strip-and-retry helpers (Q4 contract)")
	}
	if !strings.Contains(src, "reqprobeApplyLearned") {
		t.Fatal("anthropic finalize must still apply learned strips pre-flight")
	}
}
