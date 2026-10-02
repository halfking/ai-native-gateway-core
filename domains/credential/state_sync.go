package credential

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// StateChange 是熔断器的一次状态迁移事件（三十七轮审计 §三#4）。
// CoolingUntil 仅在 To==StateOpen 时非零（开路顺延的冷却截止时刻）；
// CLOSED/HALF_OPEN/QUARANTINED 时为零值。
type StateChange struct {
	ProviderID   int
	CredentialID int
	From, To     State
	Kind         ErrorKind
	CoolingUntil time.Time
}

// DBStateSync 把熔断器状态迁移异步镜像进 credentials.circuit_state 列。
//
// 三十七轮审计 §三#4 根修：此前该列只有 writer.go RestoreOnSuccess 一个
// 'closed' 写入方，生产上恒为 'closed'，导致三个消费面全死——
// routing_health_checks 的 circuit_open 检查永假、admin 凭据详情/诊断接口
// 的服务质量信号恒空、Prometheus 的 llm_gateway_circuit_state_transitions_
// total 零调用。本 worker 只补 DB 面（metrics 面在 main.go 接线时直连
// metrics.Global().RecordCircuitStateChange）。
//
// 语义取舍（有意为之，勿"修"）：
//   - 异步单 worker：迁移发生在请求热路径（RecordFailure），同步写库会把
//     DB 抖动耦合进错误处理。事件经 128 容量 buffered channel 交给本
//     goroutine 顺序落库（同凭据事件天然有序），队列满则丢弃并计数告警
//     （迁移本身低频：阈值=2 次连续失败才开路）。
//   - last-writer-wins：多实例共享一库时该列是"最近一个实例看到的熔断
//     状态"的近似信号，不是分布式精确状态。进程重启后内存 breaker 清零，
//     存量 open 行靠该凭据下一次真实迁移（成功关路/重新开路）纠偏。
//   - QUARANTINED 映射为 'open'：credentials_circuit_state_chk 只允许
//     closed/open/half_open 三值；配额侧语义已由 quota_state/
//     availability_state='suspended' 承载，运维处置动作（手动恢复）与
//     open 一致。
type DBStateSync struct {
	db      DBQuerier
	ch      chan StateChange
	dropped atomic.Int64
}

// NewDBStateSync 构造同步 worker。db 为 nil 时 Observe 直返（未启用 DB 的
// 部署形态，如纯内存测试）。queueSize 为事件缓冲容量。
func NewDBStateSync(db DBQuerier, queueSize int) *DBStateSync {
	if queueSize <= 0 {
		queueSize = 128
	}
	return &DBStateSync{db: db, ch: make(chan StateChange, queueSize)}
}

// Observe 实现 Manager.SetObserver 的观察者签名。非阻塞：队列满即丢弃。
func (s *DBStateSync) Observe(sc StateChange) {
	if s == nil || s.db == nil {
		return
	}
	select {
	case s.ch <- sc:
	default:
		n := s.dropped.Add(1)
		if n == 1 || n%100 == 0 {
			slog.Warn("circuit state sync queue full, dropping event",
				"credential_id", sc.CredentialID,
				"from", sc.From.String(), "to", sc.To.String(),
				"dropped_total", n,
			)
		}
	}
}

// Dropped 返回因队列满被丢弃的迁移事件累计数（观测用）。
func (s *DBStateSync) Dropped() int64 {
	if s == nil {
		return 0
	}
	return s.dropped.Load()
}

// Run 阻塞消费迁移事件直到 ctx 取消，随后进入有界排水把已入队事件尽量
// 写完。每次落库用独立的 5s 超时 context，不随 ctx 提前中断。
func (s *DBStateSync) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			s.drain()
			return
		case sc := <-s.ch:
			s.syncOne(sc)
		}
	}
}

// drain 在 Run 退出前有界排水：非阻塞接收直到队列排空。总预算 30s——
// 满队列 128 条 × 5s/条的最坏停机延迟不可接受，预算内写多少算多少，
// 与「DB 列是近似信号」的整体取舍一致；预算耗尽时剩余事件计入 dropped
// 计数（同样是"未落库"语义）。
func (s *DBStateSync) drain() {
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case sc := <-s.ch:
			s.syncOne(sc)
			if time.Now().After(deadline) {
				if remaining := int64(len(s.ch)); remaining > 0 {
					n := s.dropped.Add(remaining)
					slog.Warn("circuit state sync drain budget exhausted, abandoning queued events",
						"abandoned", remaining, "dropped_total", n)
				}
				return
			}
		default:
			return
		}
	}
}

func (s *DBStateSync) syncOne(sc StateChange) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dbState := sc.To.String()
	if sc.To == StateQuarantined {
		dbState = StateOpen.String()
	}

	var err error
	if sc.To == StateHalfOpen {
		// 半开是开路后的探测窗口：保留 circuit_opened_at（探测失败重新
		// 开路会刷新），只清 cooling_until（冷却期已结束）。
		_, err = s.db.Exec(ctx, `
			UPDATE credentials
			SET circuit_state = 'half_open',
			    cooling_until = NULL
			WHERE id = $1
		`, sc.CredentialID)
	} else {
		var openedAt, coolingUntil any
		if sc.To == StateOpen || sc.To == StateQuarantined {
			openedAt = time.Now().UTC()
			if sc.To == StateOpen && !sc.CoolingUntil.IsZero() {
				coolingUntil = sc.CoolingUntil.UTC()
			}
		}
		_, err = s.db.Exec(ctx, `
			UPDATE credentials
			SET circuit_state     = $1,
			    circuit_opened_at = $2,
			    cooling_until     = $3
			WHERE id = $4
		`, dbState, openedAt, coolingUntil, sc.CredentialID)
	}
	if err != nil {
		// 不重试：下一次真实迁移会覆盖本列；健康检查的短暂陈旧可容忍，
		// 重试队列反而会放大 DB 抖动。
		slog.Warn("circuit state sync write failed",
			"credential_id", sc.CredentialID,
			"to", sc.To.String(),
			"error", err,
		)
	}
}
