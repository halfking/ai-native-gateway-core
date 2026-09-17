package v2

// R37 (2026-09-17) 钉桩：done 行必须带保留期增量清理（LIMIT 分批 + 7d 窗口），
// 否则 36 万+ 存量行持续增长且 claim 面永不收缩。禁止改回无界单语句 DELETE
// （30s tick 预算下积压首扫会整体超时回滚、零进度 livelock）。

import (
	"os"
	"strings"
	"testing"
)

func TestOutboxDoneTrimIsBatchedAndBounded(t *testing.T) {
	src, err := os.ReadFile("session_aggregate_outbox_reaper.go")
	if err != nil {
		t.Fatalf("read session_aggregate_outbox_reaper.go: %v", err)
	}
	source := string(src)
	const fn = "func (r *sessionAggregateOutboxReaper) trimDoneRows"
	start := strings.Index(source, fn)
	if start < 0 {
		t.Fatal("trimDoneRows retention step missing from reaper")
	}
	end := strings.Index(source[start:], "\nfunc ")
	body := source[start:]
	if end >= 0 {
		body = source[start : start+end]
	}
	if !strings.Contains(body, "LIMIT 5000") {
		t.Fatal("done-row trim must be LIMIT-batched (unbounded DELETE livelocks the tick budget)")
	}
	if !strings.Contains(body, "status = 'done'") || !strings.Contains(body, "completed_at < NOW()") {
		t.Fatal("done-row trim must target terminal rows past the retention window only")
	}
}
