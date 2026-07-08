package sessionaudithook

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"               //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"     //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/eventbus"
)

// SessionAuditHook 会话审计 Hook（PreRouting 阶段）
//
// 职责：
//   - 执行快速检测（≤5ms）
//   - 根据评分决策：Pass/Warn/Block/NeedApproval
//   - 发布审计事件到 EventBus（异步处理）
//   - NeedApproval 时通过 ApprovalManager 创建审批记录（v1 ChatHandler 路径）
type SessionAuditHook struct {
	detector    *sessionaudit.FastDetector
	eventBus    *eventbus.MemoryBus
	approvalMgr *sessionaudit.ApprovalManager // v1 路径使用：NeedApproval 时 Enqueue 审批；v2 demo 传 nil
	notifier    ApprovalNotifier              // 审批通知器（IM 下发），可为 nil（不发送通知）
	enabled     bool
}

// NewSessionAuditHook 创建 Hook（v2 demo 路径，approvalMgr=nil）
func NewSessionAuditHook(detector *sessionaudit.FastDetector, bus *eventbus.MemoryBus) *SessionAuditHook {
	return &SessionAuditHook{
		detector: detector,
		eventBus: bus,
		enabled:  true, // 默认启用，可从配置加载
	}
}

// NewSessionAuditHookV1 创建 v1 ChatHandler 用的 Hook（带 ApprovalManager）
//
// v1 ChatHandler 不走 v2 pipeline（domain.PipelineRequest），所以用 CheckV1 扁平接口。
// 2026-06-28: 这条路径补 handoff 修复 G 在 v1 main 的遗漏——v1 ChatHandler
// 之前完全没有 hook 集成点。
func NewSessionAuditHookV1(detector *sessionaudit.FastDetector, bus *eventbus.MemoryBus, mgr *sessionaudit.ApprovalManager) *SessionAuditHook {
	return &SessionAuditHook{
		detector:    detector,
		eventBus:    bus,
		approvalMgr: mgr,
		enabled:     true,
	}
}

// SetNotifier 注入审批通知器。
// ApprovalNotifier 接口定义在 approval_hook.go（同包）。
// notification.ApprovalNotifier 实现了此接口，可直接注入。
// 必须在 CheckV1 / Execute 被调用前设置（main.go 初始化阶段调用）。
// 传 nil 可关闭通知（审批记录仍创建，只是不推送 IM）。
func (h *SessionAuditHook) SetNotifier(n ApprovalNotifier) {
	if h == nil {
		return
	}
	h.notifier = n
}

func (h *SessionAuditHook) Name() string {
	return "session.audit"
}

func (h *SessionAuditHook) Priority() int {
	return 100 // 在认证后（50）、路由前（200）
}

func (h *SessionAuditHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return h.enabled && env != nil
}

func (h *SessionAuditHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	// 加载配置
	cfg := LoadConfig()
	if !cfg.Enabled {
		return nil
	}

	// 1. 提取用户内容
	content, err := extractUserContent(env)
	if err != nil || content == "" {
		// 无法提取内容不阻断
		return nil
	}

	// 2. 快速检测（同步，≤5ms）
	result, err := h.detector.Detect(ctx, content)
	if err != nil {
		// 检测器失败降级，不阻断主流程
		slog.Warn("detector failed, degrading", "error", err, "session_id", env.SessionID)
		return nil
	}

	// 3. 写入元数据（供后续 Hook 使用）
	if env.Metadata == nil {
		env.Metadata = make(map[string]any)
	}
	env.Metadata["audit_result"] = result
	env.Metadata["audit_checked_at"] = time.Now()

	// 4. 发布事件（异步处理）
	event := &sessionaudit.SessionAuditEvent{
		SessionID:    env.SessionID,
		TenantID:     env.TenantID,
		Content:      content,
		DetectResult: result,
		ClientInfo: sessionaudit.ClientInfo{
			IP:        getClientIP(env),
			UserAgent: getUserAgent(env),
			Model:     getClientModel(env),
		},
	}

	if err := h.eventBus.Publish(event); err != nil {
		// 发布失败不阻断
		slog.Warn("publish audit event failed", "error", err)
	}

	// 5. 根据 enforcement_level 决策
	switch cfg.EnforcementLevel {
	case "strict":
		return h.handleStrict(ctx, env, result, cfg)
	case "advisory":
		return h.handleAdvisory(ctx, env, result, cfg)
	case "audit_only":
		return h.handleAuditOnly(ctx, env, result, cfg)
	default:
		return h.handleStrict(ctx, env, result, cfg)
	}
}

// handleStrict 严格模式：拦截高风险
func (h *SessionAuditHook) handleStrict(ctx context.Context, env *domain.PipelineRequest, result *sessionaudit.DetectResult, cfg *Config) error {
	// 自动拒绝（分数 ≥ auto_block_threshold）
	if cfg.ShouldAutoBlock(result.Score) {
		env.StatusCode = 403
		if env.Envelope != nil && env.Envelope.Transport != nil && env.Envelope.Transport.W != nil {
			w := env.Envelope.Transport.W
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(403)
			_, _ = fmt.Fprintf(w, `{
				"error": {
					"message": "Request blocked by security policy: critical risk detected (score=%d)",
					"type": "security_violation",
					"code": "auto_blocked"
				}
			}`, result.Score)
		}
		return fmt.Errorf("auto-blocked: score=%d >= threshold=%d", result.Score, cfg.AutoBlockThreshold)
	}

	// 触发审批（分数 ≥ approval_threshold）
	if cfg.ShouldTriggerApproval(result.Score) {
		// 仅在 v1 路径（approvalMgr 不为 nil）时创建审批
		if h.approvalMgr != nil {
			approvalID, record, err := h.createApprovalV1(ctx, env, result, cfg)
			if err != nil {
				slog.Warn("failed to create approval", "error", err, "session_id", env.SessionID)
				// 创建审批失败，降级为警告
				slog.Warn("approval creation failed, degrading to warn", "session_id", env.SessionID, "score", result.Score)
				return nil
			}

			// 发送通知
			if h.notifier != nil && cfg.NotifyOnPending && record != nil {
				if err := h.notifier.NotifyApproval(ctx, record); err != nil {
					slog.Warn("failed to send approval notification", "error", err, "approval_id", approvalID)
				}
			}

			// 设置元数据
			env.Metadata["approval_required"] = true
			env.Metadata["approval_id"] = approvalID
		}

		slog.Info("approval required", "session_id", env.SessionID, "score", result.Score)
		// 不阻断流程，继续执行（审批在后台处理）
		return nil
	}

	// Block 决策（来自检测器）
	if result.Decision == sessionaudit.DecisionBlock {
		env.StatusCode = 403
		if env.Envelope != nil && env.Envelope.Transport != nil && env.Envelope.Transport.W != nil {
			w := env.Envelope.Transport.W
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(403)
			_, _ = fmt.Fprintf(w, `{
				"error": {
					"message": "Request blocked by security policy: %s",
					"type": "security_violation",
					"code": "blocked"
				}
			}`, result.Reason)
		}
		return fmt.Errorf("request blocked: %s", result.Reason)
	}

	// Warn 级别：记录日志，继续执行
	if result.Decision == sessionaudit.DecisionWarn {
		slog.Warn("security warning detected",
			"session_id", env.SessionID,
			"score", result.Score,
			"reason", result.Reason,
			"threats", len(result.Threats))
	}

	return nil
}

// handleAdvisory 建议模式：仅通知不拦截
func (h *SessionAuditHook) handleAdvisory(ctx context.Context, env *domain.PipelineRequest, result *sessionaudit.DetectResult, cfg *Config) error {
	// 高风险时发送通知，但不拦截
	if result.Score >= cfg.ApprovalThreshold {
		slog.Info("advisory mode: high risk detected but not blocking",
			"session_id", env.SessionID,
			"score", result.Score,
			"reason", result.Reason)

		// 发送通知（如果配置了）
		if h.notifier != nil && cfg.NotifyOnPending {
			// 在 advisory 模式下，我们不创建审批，只发送通知
			// 可以通过 EventBus 发送一个 AdvisoryEvent
			slog.Info("sending advisory notification", "session_id", env.SessionID, "score", result.Score)
		}
	}

	// 继续执行
	return nil
}

// handleAuditOnly 仅审计模式：只记录
func (h *SessionAuditHook) handleAuditOnly(ctx context.Context, env *domain.PipelineRequest, result *sessionaudit.DetectResult, cfg *Config) error {
	// 仅记录审计（已在 Execute 中发布事件），不做任何拦截或通知
	if result.Score >= cfg.ApprovalThreshold {
		slog.Debug("audit_only mode: high risk detected but only logging",
			"session_id", env.SessionID,
			"score", result.Score,
			"reason", result.Reason)
	}

	// 继续执行
	return nil
}

// createApprovalV1 创建审批记录（v1 路径）
func (h *SessionAuditHook) createApprovalV1(ctx context.Context, env *domain.PipelineRequest, result *sessionaudit.DetectResult, cfg *Config) (string, *sessionaudit.ApprovalRecord, error) {
	if h.approvalMgr == nil {
		return "", nil, fmt.Errorf("approvalMgr is nil")
	}

	// 提取用户内容
	content, err := extractUserContent(env)
	if err != nil {
		content = "" // 降级处理
	}
	_ = content // 暂时未使用

	// 生成 requestID（如果 env 没有）
	requestID := env.SessionID + "-" + time.Now().Format("20060102150405")

	// 构造审批请求
	req := &sessionaudit.ApprovalRequest{
		TenantID:     env.TenantID,
		SessionID:    env.SessionID,
		RequestID:    requestID,
		DetectResult: result,
		Snapshot: &sessionaudit.RequestSnapshot{
			SessionID:   env.SessionID,
			TenantID:    env.TenantID,
			RequestID:   requestID,
			ClientModel: getClientModel(env),
			ClientInfo: sessionaudit.ClientInfo{
				IP:        getClientIP(env),
				UserAgent: getUserAgent(env),
				Model:     getClientModel(env),
			},
			DetectResult: result,
			CreatedAt:    time.Now(),
		},
		Timeout: cfg.ApprovalTimeout,
	}

	// 创建审批记录
	approvalID, err := h.approvalMgr.Create(ctx, req)
	if err != nil {
		return "", nil, fmt.Errorf("failed to create approval: %w", err)
	}

	// 构造 ApprovalRecord 用于通知
	record := &sessionaudit.ApprovalRecord{
		ID:           approvalID,
		SessionID:    env.SessionID,
		TenantID:     env.TenantID,
		RequestID:    requestID,
		Status:       sessionaudit.ApprovalPending,
		DetectResult: result,
		Snapshot:     req.Snapshot,
		CreatedAt:    time.Now(),
		ExpiresAt:    time.Now().Add(cfg.ApprovalTimeout),
	}

	return approvalID, record, nil
}

func (h *SessionAuditHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	// 审计失败可降级，返回 nil 不影响主流程
	return nil
}

// CheckV1Result 是 v1 ChatHandler 用的扁平结果。
// StatusCode=0 表示继续走主流程（Pass/Warn）；其他值表示立即响应。
type CheckV1Result struct {
	Decision   sessionaudit.Decision
	StatusCode int    // 0=继续; 403=Block; 202=NeedApproval
	ApprovalID string // 仅 NeedApproval 时有值
	Reason     string // 给 client 的 reason
}

// CheckV1 是给 v1 ChatHandler 用的简化接口（不走 domain.PipelineRequest）。
//
// 输入是扁平参数；输出是 CheckV1Result，ChatHandler 根据 StatusCode 决定
// 是直接 writeErrorJSON (403) / writePendingJSON (202) 还是继续 routing。
//
// 与 Execute(env) 的区别：
//   - Execute 需要 env.Envelope.Transport.BodyBytes（v2 路径）
//   - CheckV1 直接接受 content string（v1 路径，ChatHandler 自己解析 body）
func (h *SessionAuditHook) CheckV1(ctx context.Context, sessionID, tenantID, model, content, ua, ip string) CheckV1Result {
	// 1. 空内容 → pass
	if content == "" {
		return CheckV1Result{Decision: sessionaudit.DecisionPass}
	}

	// 2. 快速检测（同步，≤5ms）
	result, err := h.detector.Detect(ctx, content)
	if err != nil {
		// 检测器失败降级，不阻断
		slog.Warn("detector failed in CheckV1, degrading", "error", err, "session_id", sessionID)
		return CheckV1Result{Decision: sessionaudit.DecisionPass}
	}

	// 3. 发布事件（异步处理，失败不阻断）
	if h.eventBus != nil {
		event := &sessionaudit.SessionAuditEvent{
			SessionID:    sessionID,
			TenantID:     tenantID,
			Content:      content,
			DetectResult: result,
			ClientInfo: sessionaudit.ClientInfo{
				IP:        ip,
				UserAgent: ua,
				Model:     model,
			},
		}
		_ = h.eventBus.Publish(event) // 失败不阻断
	}

	// 4. Block → 403
	if result.Decision == sessionaudit.DecisionBlock {
		slog.Warn("session-audit CheckV1 block",
			"session_id", sessionID,
			"tenant_id", tenantID,
			"score", result.Score,
			"reason", result.Reason)
		return CheckV1Result{
			Decision:   sessionaudit.DecisionBlock,
			StatusCode: 403,
			Reason:     result.Reason,
		}
	}

	// 5. NeedApproval → 202 + 创建 approval record
	// 注: detector 不返回 Block (DecisionBlock 保留作 public API 但实际不会触发)。
	// v2 Execute 也没有把 NeedApproval 升级为 Block — 这是 v1 的实现选择。
	// 如果要 403, 应该由 detector 自身的 maxSeverity/Score 阈值直接决定 (不通过 hook 升级)。
	if result.Decision == sessionaudit.DecisionNeedApproval {
		if h.approvalMgr == nil {
			// v2 demo 模式：无 mgr 时降级为 Pass（仅记录 warning）
			slog.Warn("session-audit CheckV1 need-approval but approvalMgr=nil, degrading to pass",
				"session_id", sessionID)
			return CheckV1Result{Decision: sessionaudit.DecisionPass}
		}
		// 构造最简 snapshot（v1 路径没有完整 env，所以用最简字段）
		snapshot := &sessionaudit.RequestSnapshot{
			RequestID:    sessionID + ":" + time.Now().Format("20060102150405.000"),
			SessionID:    sessionID,
			TenantID:     tenantID,
			BodyBytes:    []byte(content),
			ClientModel:  model,
			ClientInfo:   sessionaudit.ClientInfo{IP: ip, UserAgent: ua, Model: model},
			DetectResult: result,
			CreatedAt:    time.Now(),
		}
		approvalID, err := h.approvalMgr.Create(ctx, &sessionaudit.ApprovalRequest{
			SessionID:    sessionID,
			TenantID:     tenantID,
			RequestID:    snapshot.RequestID,
			DetectResult: result,
			Snapshot:     snapshot,
			Timeout:      15 * time.Minute,
		})
		if err != nil {
			slog.Error("session-audit CheckV1 create approval failed", "error", err, "session_id", sessionID)
			// 创建失败 → 降级 Pass（不让用户在 mgr 出错时拿不到任何响应）
			return CheckV1Result{Decision: sessionaudit.DecisionPass}
		}
		// 发 ApprovalNeededEvent
		if h.eventBus != nil {
			_ = h.eventBus.Publish(&sessionaudit.ApprovalNeededEvent{
				ApprovalID:   approvalID,
				SessionID:    sessionID,
				TenantID:     tenantID,
				RequestID:    snapshot.RequestID,
				DetectResult: result,
				Snapshot:     snapshot,
				ExpiresAt:    time.Now().Add(15 * time.Minute),
			})
		}
		// 创建审批后发送 IM 通知（best-effort，不阻断审批流程）
		if h.notifier != nil {
			record, gerr := h.approvalMgr.GetForTenant(ctx, approvalID, tenantID)
			if gerr == nil && record != nil {
				if nerr := h.notifier.NotifyApproval(ctx, record); nerr != nil {
					slog.Error("session-audit CheckV1 notify failed",
						"approval_id", approvalID,
						"tenant_id", tenantID,
						"error", nerr)
				} else {
					slog.Info("session-audit CheckV1 notified",
						"approval_id", approvalID,
						"tenant_id", tenantID)
				}
			} else if gerr != nil {
				slog.Error("session-audit CheckV1 get record for notify failed",
					"approval_id", approvalID,
					"error", gerr)
			}
		}
		return CheckV1Result{
			Decision:   sessionaudit.DecisionNeedApproval,
			StatusCode: 202,
			ApprovalID: approvalID,
			Reason:     result.Reason,
		}
	}

	// 6. Warn → continue (StatusCode=0)
	if result.Decision == sessionaudit.DecisionWarn {
		slog.Warn("session-audit CheckV1 warn",
			"session_id", sessionID,
			"score", result.Score,
			"reason", result.Reason)
	}
	return CheckV1Result{Decision: result.Decision}
}

// 编译期断言
var _ pipeline.Hook = (*SessionAuditHook)(nil)
