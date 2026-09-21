// Package bg — worker_panic_recover_test.go
//
// R51 审计 P2：bg worker 的 xxxRecovered 守护必须吞掉单轮 tick/任务内的
// panic，让调度循环存活，而不是让 panic 逃逸击穿整进程（对齐 R50 的
// runRecovered 模式）。以下用例用零值/最小构造让被守护路径确定性 panic
//（nil *pgxpool.Pool / nil 接口字段解引用），断言 panic 不逃逸出守护函数。
package bg

import (
	"context"
	"testing"
)

func TestCredentialSelfcheck_CycleOnceRecovered_RecoversPanic(t *testing.T) {
	// 零值 worker：w.db / connPool 均为 nil → pickDueCredential 必然 panic。
	w := &CredentialSelfcheckWorker{}
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("cycleOnceRecovered 让 panic 逃逸: %v", rec)
		}
	}()
	w.cycleOnceRecovered(context.Background())
}

func TestActiveProbeWorker_ProcessOneRecovered_RecoversPanic(t *testing.T) {
	// executor 持 nil db → LoadTarget 必然 panic；种子 running 让任务真正
	// 进入执行路径（否则 processOne 会因 dedup miss 提前返回）。
	w := NewActiveProbeWorker(ActiveProbeWorkerConfig{Enabled: true})
	task := probeTask{CredID: 1, Model: "gpt-test"}
	w.mu.Lock()
	w.running[probeKey(task.CredID, task.Model)] = &probeState{
		CredentialID: task.CredID,
		Model:        task.Model,
	}
	w.mu.Unlock()
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("processOneRecovered 让 panic 逃逸: %v", rec)
		}
	}()
	w.processOneRecovered(context.Background(), task)
}

func TestFeedbackAnalyzer_AnalyzeRecovered_RecoversPanic(t *testing.T) {
	// 零值 analyzer：db 为 nil → AnalyzeOnce 必然 panic。
	a := &FeedbackAnalyzer{}
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("analyzeRecovered 让 panic 逃逸: %v", rec)
		}
	}()
	a.analyzeRecovered(context.Background())
}

func TestConcurrencyAutoScaleUp_ScaleUpRecovered_RecoversPanic(t *testing.T) {
	// 零值 worker：db 接口为 nil → scaleUp 必然 panic。
	w := &ConcurrencyAutoScaleUp{}
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("scaleUpRecovered 让 panic 逃逸: %v", rec)
		}
	}()
	w.scaleUpRecovered(context.Background())
}
