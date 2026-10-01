// Package bg — circuit_state_sync_supervise_test.go
//
// 2026-10-01 审计钉测：circuit_state_sync 的 Run（domains/credential.DBStateSync）
// 是纯 select 消费循环、无 wg/done 外部握手，接线必须是 SpawnLoop（BaseWorker
// 监督：panic → recover → 指数退避重启自愈）而不是 Go（recover 后 goroutine
// 终止不重启——一次 panic 后 credentials.circuit_state 永久停止更新，三十七轮
// 审计 §三#4 修掉的「熔断状态恒 closed」病灶复发且无人重启）。
// 本测试放在 bg 包：SpawnLoop 的退避参数是本包包级变量可收缩，且 bg 已依赖
// domains/credential（反向会让 credential 依赖 bg，接线在 cmd/gateway 亦无法
// 从包外收缩退避）。
package bg

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
)

// panicOnceQuerier 首次 Exec panic、其后成功——模拟 DBStateSync 依赖层的
// 一次性炸穿，验证 SpawnLoop 的监督重启把消费者拉起来完成落库。
type panicOnceQuerier struct {
	execCalls atomic.Int64
	written   atomic.Int64
}

func (q *panicOnceQuerier) Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	if q.execCalls.Add(1) == 1 {
		panic("boom: circuit state sync exec")
	}
	q.written.Add(1)
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (q *panicOnceQuerier) Begin(ctx context.Context) (pgx.Tx, error) {
	return nil, errors.New("unused by DBStateSync")
}

func (q *panicOnceQuerier) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	return nil
}

// TestCircuitStateSyncSpawnLoopRestartsAfterPanic：Run 内 panic 被 SpawnLoop
// 监督捕获并重启，重启后队列中的事件成功落库（written>0）。若接线退回 bg.Go
// （不重启），written 恒 0，本测试超时失败。
func TestCircuitStateSyncSpawnLoopRestartsAfterPanic(t *testing.T) {
	oldBackoff, oldMax := workerRestartBackoff, workerRestartMaxBackoff
	workerRestartBackoff = 5 * time.Millisecond
	workerRestartMaxBackoff = 20 * time.Millisecond
	defer func() { workerRestartBackoff, workerRestartMaxBackoff = oldBackoff, oldMax }()

	q := &panicOnceQuerier{}
	s := credential.NewDBStateSync(q, 8)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 与 cmd/gateway 接线同款形态：SpawnLoop(ctx, "circuit_state_sync.run", s.Run)。
	SpawnLoop(ctx, "circuit_state_sync.run", s.Run)

	s.Observe(credential.StateChange{
		ProviderID: 1, CredentialID: 1,
		From: credential.StateClosed, To: credential.StateOpen,
		Kind: credential.KindNetwork,
	})
	// 第一条事件在 syncOne 落库前 panic（事件已出队、未落库——last-writer-wins
	// 语义下本就允许丢失，下次真实迁移纠偏）。等退避重启完成后投递第二条：
	// 只有重启后的 Run 还在消费，第二条才会落库——这正是「panic 不停摆」要
	// 钉住的存活性。
	time.Sleep(50 * time.Millisecond)
	s.Observe(credential.StateChange{
		ProviderID: 1, CredentialID: 1,
		From: credential.StateOpen, To: credential.StateHalfOpen,
		Kind: credential.KindNetwork,
	})

	deadline := time.After(5 * time.Second)
	for q.written.Load() == 0 {
		select {
		case <-deadline:
			t.Fatalf("circuit state sync did not recover from panic: exec_calls=%d written=%d (supervision restart broken?)",
				q.execCalls.Load(), q.written.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
	if q.execCalls.Load() < 2 {
		t.Fatalf("expected >=2 exec calls (first panicking, second succeeding), got %d", q.execCalls.Load())
	}
}
