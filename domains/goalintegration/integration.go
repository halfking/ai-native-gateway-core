// Package goalintegration — Wave 2-D HTTP API integration.
//
// 在 v1 ChatHandler 请求流水线的「body 规范化之后、durable snapshot 之前」
// 调用本包把可选的 goal 对象解析为 GoalRequest、收紧 limits、生成 policy
// snapshot，并落库 goal_run。goal_run_id 与 status_url 写入 context 与
// response header，供客户端轮询。
//
// 关键不变量：
//   - goal 字段完全可选；缺失或为空不创建 GoalRun，向后兼容；
//   - goal 解析失败 fail-closed（HTTP 400），不得降级为「无 goal」；
//   - 租户隔离：使用调用方传入的 tenant_id；store 层 RLS 是权威；
//   - 失败路径必须返回 error，不得静默吞下失败。
package goalintegration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/goal"
	"github.com/kaixuan/llm-gateway-go/domains/goalrun"
)

// ErrNoGoal 表示请求体不含 goal 字段；调用方应走原 pipeline。
var ErrNoGoal = errors.New("goalintegration: no goal in request body")

// ErrInvalidGoal 表示 goal 字段存在但解析/校验失败（fail-closed）。
var ErrInvalidGoal = errors.New("goalintegration: invalid goal request")

// Resolved 是 GoalRun 解析+创建后的结果，供 v1 ChatHandler 注入 response。
type Resolved struct {
	GoalRunID        string
	StatusURL        string
	Status           string
	PolicySnapshot   []byte
	EffectiveLimits  goal.GoalLimits
	RootGoalID       string
	InstructionHash  string
}

// Integrator 持有 store 与可选的目标服务器 base URL，用于拼装 status_url。
type Integrator struct {
	store       *goalrun.Store
	statusBase  string
	leaseOwner  string
	leaseTTL    time.Duration
}

// Config 装配参数。
type Config struct {
	// Store 必填；nil 视为禁用。
	Store *goalrun.Store
	// StatusBase 状态查询 URL 的前缀；空值使用 "/v1/goal-runs"。
	StatusBase string
	// LeaseOwner 标识当前 gateway 实例；用于 CAS 门禁。
	LeaseOwner string
	// LeaseTTL GoalRun lease 时长；0 默认 60s。
	LeaseTTL time.Duration
}

// New 构造 Integrator。Store 为 nil 时所有方法返回 nil（兼容无 DB 部署）。
func New(cfg Config) *Integrator {
	base := cfg.StatusBase
	if base == "" {
		base = "/v1/goal-runs"
	}
	leaseTTL := cfg.LeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = 60 * time.Second
	}
	owner := cfg.LeaseOwner
	if owner == "" {
		owner = "gateway-default"
	}
	return &Integrator{
		store:      cfg.Store,
		statusBase: base,
		leaseOwner: owner,
		leaseTTL:   leaseTTL,
	}
}

// IsConfigured 报告当前是否启用 goal 落库（store 非 nil）。
func (i *Integrator) IsConfigured() bool {
	return i != nil && i.store != nil
}

// ParseAndCreate 从 bodyBytes 中提取 goal 字段、解析、收紧 limits、生成
// policy snapshot 并落库 GoalRun。调用方必须在 auth+session ownership 校验
// 之后调用（即 tenantID/apiKeyID/rootSessionID/rootRequestID 已就绪）。
//
// 行为：
//   - body 中无 "goal" 字段或 goal=null：返回 (nil, ErrNoGoal)；
//   - goal 存在但解析/校验失败：返回 (nil, fmt.Errorf("%w: %v", ErrInvalidGoal, err))；
//   - Store 未配置：返回 (nil, ErrInvalidGoal) — fail-closed；
//   - DB 写入失败：返回 error（不包装 ErrInvalidGoal，便于上层区分）。
func (i *Integrator) ParseAndCreate(
	ctx context.Context,
	bodyBytes []byte,
	tenantID string,
	apiKeyID int,
	rootSessionID string,
	rootRequestID string,
) (*Resolved, error) {
	if !i.IsConfigured() {
		return nil, fmt.Errorf("%w: goalrun store not configured", ErrInvalidGoal)
	}

	rawGoal, present := extractGoalField(bodyBytes)
	if !present {
		return nil, ErrNoGoal
	}

	parsed, err := goal.ParseGoalRequest(rawGoal)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidGoal, err)
	}

	// 收紧 limits（当前无租户 quota，使用平台硬上限；未来在此叠加 tenantMax*）。
	effective := goal.TightenLimits(parsed.Limits, 0, 0, 0)
	snapshot := goal.CreatePolicySnapshot(tenantID, fmt.Sprintf("%d", apiKeyID), effective)
	snapshotBytes, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal policy_snapshot: %v", ErrInvalidGoal, err)
	}

	instructionHash := hashInstruction(parsed.Instruction)

	now := time.Now().UTC()
	input := goalrun.NewGoalRunInput{
		TenantID:                   tenantID,
		APIKeyID:                   fmt.Sprintf("%d", apiKeyID),
		RootGoalID:                 coalesceRootGoalID(parsed.RootGoalID, rootRequestID),
		RootSessionID:              rootSessionID,
		RootRequestID:              rootRequestID,
		PolicyVersion:              snapshot.Version,
		PolicySnapshot:             snapshotBytes,
		InstructionHash:            instructionHash,
		RedactedInstructionSummary: truncateForSummary(parsed.Instruction, 256),
		DeadlineAt:                 now.Add(time.Duration(effective.MaxWallTimeSeconds) * time.Second),
		LeaseOwner:                 i.leaseOwner,
		LeaseUntil:                 now.Add(i.leaseTTL),
	}

	run, err := i.store.CreateGoalRun(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("goalintegration: CreateGoalRun: %w", err)
	}

	return &Resolved{
		GoalRunID:       run.ID,
		StatusURL:       i.statusBase + "/" + run.ID,
		Status:          string(run.Status),
		PolicySnapshot:  snapshotBytes,
		EffectiveLimits: effective,
		RootGoalID:      run.RootGoalID,
		InstructionHash: instructionHash,
	}, nil
}

// extractGoalField 从请求 body 中取出 "goal" 字段的原始 JSON 字节。
// 返回 (raw, true) 表示字段存在且非 null；否则 (nil, false) 表示无 goal。
func extractGoalField(body []byte) ([]byte, bool) {
	if len(body) == 0 {
		return nil, false
	}
	var probe struct {
		Goal json.RawMessage `json:"goal"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, false
	}
	if len(probe.Goal) == 0 {
		return nil, false
	}
	// 显式 null 视作「无 goal」，向后兼容未启用场景。
	if string(probe.Goal) == "null" {
		return nil, false
	}
	return probe.Goal, true
}

func hashInstruction(instruction string) string {
	sum := sha256.Sum256([]byte(instruction))
	return hex.EncodeToString(sum[:])
}

func truncateForSummary(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func coalesceRootGoalID(clientID, fallback string) string {
	if clientID != "" {
		return clientID
	}
	return fallback
}
