package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// candidates_circuit.go — 候选查询组件熔断器（2026-10-28 routing_schema_error
// 轮，用户指令：连续错误应有熔断，短时间连续错误多次后把客户端请求延迟处理，
// 不要让大量请求堵在路由里反复撞同一条已知坏路径）。
//
// 事故形态：候选查询依赖的 SQL 函数缺失（42883）时，每个请求都在getCandidates
// 里同步撞 DB（内含 3 次重试），20ms 失败 → 500，每秒一次的客户端重试把
// 同一条坏路径打成了风暴。本熔断器是【组件级】的：候选查询失败是全组件故障，
// 不是 per-(model, credential) 故障 —— 粒度对齐故障本体。
//
// 行为（与 P0-B NodeState 退避同公式：cooldown × 2^n 封顶）：
//   - closed：直通查询；连续失败 < failThreshold。
//   - 连续失败 ≥ failThreshold → open：冷却 cooldown 起步（30s），每次
//     「半开探测失败」翻倍，封顶 10min。
//   - open 期间 AllowProbe=false 且带 retryAt（= 冷却到期点）。调用方
//     （getCandidates）把请求 PARK 到该点 —— 这就是「延迟处理」：请求
//     挂起而非同步撞库；到点后第一个醒来的是半开探测，其余请求 park 到
//     新冷却点。探测成功 → RecordSuccess → closed → 所有 park 请求醒来
//     直通；探测失败 → 冷却翻倍继续 park，直到调用方的 park 预算耗尽
//     （快速失败，客户端按既有 no-candidates 路径拿到 503）。
//   - 冷却到期时只放【一个】半开探测（halfOpenProbe CAS），其余请求继续
//     park —— 坏路径在断路期每冷却窗最多被真实查询一次。
//
// 指标（低基数，closed enum）：state gauge + open 次数 + park 请求数。

var (
	candidatesCircuitState = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "llmgw_candidates_circuit_state",
		Help: "Candidates-query circuit state: 0=closed, 1=open, 2=half-open (probe in flight).",
	})
	candidatesCircuitOpenTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "llmgw_candidates_circuit_open_total",
		Help: "Times the candidates-query circuit tripped open (consecutive query failures).",
	})
	candidatesCircuitParkedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "llmgw_candidates_circuit_parked_total",
		Help: "Requests parked (delayed) instead of querying while the candidates circuit was open.",
	})
)

// ErrCandidatesCircuitOpen 是 open 期快速失败的哨兵（errors.Is 可判）。
var ErrCandidatesCircuitOpen = errors.New("candidates query circuit open")

// candidatesCircuitParkCap 是 open 期单请求 park 预算（env
// LLM_GATEWAY_CANDIDATES_CIRCUIT_PARK_SECONDS，默认 10s）。0 = 不 park
// （纯快速失败熔断）。预算内循环 park 到各冷却点；耗尽即返回
// ErrCandidatesCircuitOpen，由既有 no-candidates 路径出 503。
func candidatesCircuitParkCap() time.Duration {
	if s := strings.TrimSpace(os.Getenv("LLM_GATEWAY_CANDIDATES_CIRCUIT_PARK_SECONDS")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 10 * time.Second
}

// 常量与 P0-B 退避同族：组件级 DB 故障恢复快，起步冷却短；封顶防失控。
const (
	circuitFailThreshold   = 3
	circuitCooldownInitial = 30 * time.Second
	circuitCooldownCap     = 10 * time.Minute
)

const (
	circuitClosed int32 = iota
	circuitOpen
	circuitHalfOpen
)

// candidatesCircuit 是进程内单例状态（挂在 Client 上）。零值即可用（closed）。
type candidatesCircuit struct {
	mu          sync.Mutex
	consecFails int
	cooldown    time.Duration
	openUntil   time.Time
	halfOpen    bool // 冷却到期后是否已放行探测（open 态下有效）
}

// AllowProbe 返回当前是否放行一次真实查询。false 时 retryAt 是建议的
// park 目标（当前冷却到期点；half-open 被占用时同为该点）。
func (c *candidatesCircuit) AllowProbe(now time.Time) (bool, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.openUntil.IsZero() || now.After(c.openUntil):
		// closed，或 open 且冷却已到期。
		if !c.openUntil.IsZero() && now.After(c.openUntil) {
			// half-open：只放一个探测。
			if c.halfOpen {
				return false, c.openUntil
			}
			c.halfOpen = true
			setCircuitGauge(circuitHalfOpen)
		}
		return true, time.Time{}
	default:
		// open 且仍在冷却内。
		return false, c.openUntil
	}
}

// RecordSuccess 记一次成功查询：闭合、清计数、冷却归位。
func (c *candidatesCircuit) RecordSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consecFails = 0
	c.cooldown = 0
	c.openUntil = time.Time{}
	c.halfOpen = false
	setCircuitGauge(circuitClosed)
}

// RecordFailure 记一次最终失败（调用方的重试已耗尽）。连续失败达到阈值时
// 翻开断路；半开探测失败时冷却翻倍重新打开。
func (c *candidatesCircuit) RecordFailure(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consecFails++
	if c.consecFails < circuitFailThreshold && c.openUntil.IsZero() {
		return // closed 且未达阈值
	}
	// 达阈值首跳或半开探测失败：冷却从当前值起步/翻倍。
	if c.cooldown == 0 {
		c.cooldown = circuitCooldownInitial
	} else {
		c.cooldown *= 2
		if c.cooldown > circuitCooldownCap {
			c.cooldown = circuitCooldownCap
		}
	}
	wasOpen := !c.openUntil.IsZero()
	c.openUntil = now.Add(c.cooldown)
	c.halfOpen = false
	candidatesCircuitState.Set(float64(circuitOpen))
	if !wasOpen {
		candidatesCircuitOpenTotal.Inc()
	}
}

func setCircuitGauge(v int32) { candidatesCircuitState.Set(float64(v)) }

// awaitCircuitProbe blocks while the candidates-query circuit is open, parking
// the caller until the cooldown probe is allowed. parkCap is the caller's
// 【total】 budget: within it we park/wait slice by slice; once exhausted we
// fast-fail with ErrCandidatesCircuitOpen (handler returns the retryable 503
// no-candidates path), so a bad query path gets probed once per cooldown window
// instead of being hammered by every request.
//
// ★ 2026-11-15 fix (211 routing_database_error incident amplifier): the old
// in-loop `remaining := parkCap` refilled the budget on EVERY iteration, so the
// total wait was unbounded — after the cooldown ladder doubled (30s→…→10min
// cap), requests parked all the way to the cooldown point or the client's own
// timeout (production: 300s hangs ending in ctx canceled). The parkDeadline
// below bounds the total park time; budget exhausted ⇒ immediate fast-fail.
func (c *Client) awaitCircuitProbe(ctx context.Context, clientModel, tenantID, modality string) error {
	allow, retryAt := c.candCircuit.AllowProbe(time.Now())
	if allow {
		return nil
	}
	parkCap := candidatesCircuitParkCap()
	parkDeadline := time.Now().Add(parkCap)
	parked := false
	for !allow {
		remaining := time.Until(parkDeadline)
		if wait := time.Until(retryAt); wait < remaining {
			remaining = wait
		}
		if remaining <= 0 {
			return fmt.Errorf("%w (retry after %s)", ErrCandidatesCircuitOpen, retryAt.Format(time.RFC3339))
		}
		if !parked {
			candidatesCircuitParkedTotal.Inc()
			parked = true
		}
		slog.Warn("[candidate_diag] circuit open — parking request until cooldown probe",
			"model", clientModel, "tenant_id", tenantID, "modality", modality,
			"park_seconds", remaining.Seconds())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(remaining):
		}
		allow, retryAt = c.candCircuit.AllowProbe(time.Now())
	}
	return nil
}
