package promptinjection

import (
	"context"
	"testing"
	"time"
)

// ★ 第57增量回归：Start 必须幂等。
//
//	注释此前声称「重复调用只生效一次」，但实现没有 started 守卫 ⇒
//	每次 Start 再起一条 bg.SpawnLoop（重复 LLM 花费 + 双倍队列争抢）。
//	钉：第二次 Start 在守卫处早退（started 已置位）。
func TestErrorAnalysisWorker_StartIsIdempotent(t *testing.T) {
	// enabled=false 的 analyzer：ProcessBatch 首行早退，不触 DB。
	analyzer := NewErrorAnalyzer(nil, nil, nil, "test-model", false)
	w := NewErrorAnalysisWorker(analyzer, time.Minute)

	if w.started.Load() {
		t.Fatal("worker must start in the not-started state")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	if !w.started.Load() {
		t.Fatal("Start must set the started guard")
	}

	// 第二次 Start 必须早退：不重置、不 panic、不新起循环。
	w.Start(ctx)
	if !w.started.Load() {
		t.Fatal("second Start must not reset the started guard")
	}

	// Stop 幂等（既有 stopOnce）且与 Start 守卫共存。
	w.Stop()
	w.Stop()
}

// ★ 第57增量回归：nil analyzer 时 Start 早退——守卫不应被置位
// （否则一次误调用会永久堵死后续合法 Start）。
func TestErrorAnalysisWorker_StartNilAnalyzerDoesNotArmGuard(t *testing.T) {
	w := NewErrorAnalysisWorker(nil, time.Minute)
	w.Start(context.Background())
	if w.started.Load() {
		t.Fatal("Start with nil analyzer must not arm the started guard")
	}
}
