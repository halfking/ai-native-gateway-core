// Package bg — worker_panic_recover_test.go
//
// R51 审计 P2：bg worker 的 xxxRecovered 守护必须吞掉单轮 tick/任务内的
// panic，让调度循环存活，而不是让 panic 逃逸击穿整进程（对齐 R50 的
// runRecovered 模式）。以下用例用零值/最小构造让被守护路径确定性 panic
// （nil *pgxpool.Pool / nil 接口字段解引用），断言 panic 不逃逸出守护函数。
package bg

import (
	"context"
	"testing"

	apihub "github.com/kaixuan/llm-gateway-go/apihub"
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

// R52 补齐（D16/D04 复核发现 F13 钉桩只覆盖 4/6）：apihub_watcher 与
// credential_autoheal 两例对称补上；并为 active_probe 的 R52 修复
// （panic 时释放 running 去重键）补行为断言。

// panickyAssetSyncSource 在首个方法即 panic，模拟依赖层炸穿。
type panickyAssetSyncSource struct{}

func (panickyAssetSyncSource) LLMEndpoints(ctx context.Context) ([]apihub.Asset, error) {
	panic("boom: llm endpoints")
}
func (panickyAssetSyncSource) MCPServers(ctx context.Context) ([]apihub.Asset, error) {
	panic("boom: mcp servers")
}

func TestAssetWatcher_SyncRecovered_RecoversPanic(t *testing.T) {
	// SyncOnce 对 hub==nil 早退——须给非 nil hub（panic 在 src 侧先触发，
	// store 为 nil 不会被解引用）才能走到 panic 路径。
	w := &AssetWatcher{hub: apihub.New(nil), src: panickyAssetSyncSource{}}
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("syncRecovered 让 panic 逃逸: %v", rec)
		}
	}()
	w.syncRecovered(context.Background())
}

func TestCredentialAutoHeal_CycleRecovered_RecoversPanic(t *testing.T) {
	w := &CredentialAutoHealWorker{
		// submit 在 runCycle 命中待自愈行时被调用——用 panic 依赖模拟炸穿。
		submit: func(credentialID int, rawModel, tenantID, parentReqID string) {
			panic("boom: submit self-heal probe")
		},
	}
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("cycleRecovered 让 panic 逃逸: %v", rec)
		}
	}()
	w.cycleRecovered(context.Background())
}

func TestActiveProbeWorker_PanicReleasesRunningDedupKey(t *testing.T) {
	// R52：panic 前 running[key] 残留会使 Submit 的 dedup 把该
	// (credential, model) 探测对静默丢弃直至重启——recover 分支必须删键。
	w := NewActiveProbeWorker(ActiveProbeWorkerConfig{Enabled: true})
	task := probeTask{CredID: 7, Model: "gpt-test"}
	key := probeKey(task.CredID, task.Model)
	w.mu.Lock()
	w.running[key] = &probeState{CredentialID: task.CredID, Model: task.Model}
	w.mu.Unlock()

	w.processOneRecovered(context.Background(), task)

	w.mu.Lock()
	_, stillRunning := w.running[key]
	w.mu.Unlock()
	if stillRunning {
		t.Fatalf("panic 后 running 去重键未释放，探测链将永久卡死")
	}
}
