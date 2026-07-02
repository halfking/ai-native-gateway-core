// Package notification — routing_config.go
//
// 审批路由规则：把 (tenant, risk_level) 映射到一组接收人。
//
// 数据来源：
//   - 静态规则（代码内 / 配置文件 → RoutingRule）
//   - 数据库（migrations/135_approval_routing.sql → ApprovalRoutingTable.LoadFromDB）
//
// 多渠道：每个 Recipient 携带三个渠道的用户 ID，按所选 ChannelType 取对应字段。
package notification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// RoutingRule 一条路由规则。
type RoutingRule struct {
	TenantID   string      // 租户 ID（空 = 全租户兜底）
	RiskLevel  RiskLevel   // 风险级别（low/medium/high/critical）
	Channel    ChannelType // 期望下发的渠道
	Recipients []Recipient // 接收人列表
	Priority   int         // 同 (tenant, risk) 多条规则时优先级
	Enabled    bool        // 是否启用
}

// RoutingRules 路由规则集合（按 Priority 升序选择高优先级）。
type RoutingRules []RoutingRule

// Route 根据 (tenantID, riskLevel) 匹配规则，返回去重后的接收人。
//
// 匹配规则：
//   - 优先匹配 tenant 精确命中且 enabled=true 的规则
//   - 同 (tenant, risk) 多条规则按 Priority 升序拼接
//   - 无 tenant 命中时退化到 TenantID=="" 的全局规则
func (rr RoutingRules) Route(tenantID string, riskLevel RiskLevel) []Recipient {
	var matched []RoutingRule
	for _, r := range rr {
		if !r.Enabled {
			continue
		}
		if r.RiskLevel != riskLevel {
			continue
		}
		if r.TenantID == tenantID || (r.TenantID == "") {
			matched = append(matched, r)
		}
	}
	if len(matched) == 0 {
		return nil
	}

	// 按 Priority 升序
	sortRulesByPriority(matched)

	seen := make(map[string]struct{})
	out := make([]Recipient, 0)
	for _, r := range matched {
		for _, rec := range r.Recipients {
			key := rec.ID
			if key == "" {
				key = rec.LarkOpenID + "|" + rec.DingTalkUserID + "|" + rec.WeChatUserID
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, rec)
		}
	}
	return out
}

// sortRulesByPriority 按 Priority 升序排序（插入排序，规则数量通常很小）。
func sortRulesByPriority(rules []RoutingRule) {
	for i := 1; i < len(rules); i++ {
		for j := i; j > 0 && rules[j-1].Priority > rules[j].Priority; j-- {
			rules[j-1], rules[j] = rules[j], rules[j-1]
		}
	}
}

// ApprovalRoutingTable 线程安全的路由表，支持运行时热更新。
type ApprovalRoutingTable struct {
	mu    sync.RWMutex
	rules RoutingRules
}

// NewApprovalRoutingTable 创建路由表。
func NewApprovalRoutingTable(rules RoutingRules) *ApprovalRoutingTable {
	return &ApprovalRoutingTable{rules: rules}
}

// NewEmptyRoutingTable 创建空路由表（之后 LoadFromDB 填充）。
func NewEmptyRoutingTable() *ApprovalRoutingTable {
	return &ApprovalRoutingTable{}
}

// Route 查询路由。
func (t *ApprovalRoutingTable) Route(tenantID string, riskLevel RiskLevel) []Recipient {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.rules.Route(tenantID, riskLevel)
}

// SetRules 整体替换规则（用于热更新）。
func (t *ApprovalRoutingTable) SetRules(rules RoutingRules) {
	t.mu.Lock()
	t.rules = rules
	t.mu.Unlock()
}

// AddRule 追加单条规则。
func (t *ApprovalRoutingTable) AddRule(rule RoutingRule) {
	t.mu.Lock()
	t.rules = append(t.rules, rule)
	t.mu.Unlock()
}

// RemoveRule 删除所有匹配 (tenant, risk) 的规则。
func (t *ApprovalRoutingTable) RemoveRule(tenantID string, riskLevel RiskLevel) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.rules[:0]
	for _, r := range t.rules {
		if r.TenantID == tenantID && r.RiskLevel == riskLevel {
			continue
		}
		out = append(out, r)
	}
	t.rules = out
}

// Snapshot 返回当前规则的拷贝（用于调试 / API 展示）。
func (t *ApprovalRoutingTable) Snapshot() RoutingRules {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(RoutingRules, len(t.rules))
	copy(out, t.rules)
	return out
}

// RoutingRuleDBRow 数据库行（对应 migrations/135_approval_routing.sql）。
type RoutingRuleDBRow struct {
	ID        int64
	TenantID  string
	RiskLevel string
	Approvers json.RawMessage // JSONB
	Channel   string
	Priority  int
	Enabled   bool
	UpdatedAt time.Time
}

// RoutingDBLoader 由调用方实现（避免本包反向依赖 *pgxpool.Pool）。
type RoutingDBLoader interface {
	LoadRoutingRules(ctx context.Context) ([]RoutingRuleDBRow, error)
}

// LoadFromDB 从数据库加载规则并替换内存中的规则。
func (t *ApprovalRoutingTable) LoadFromDB(ctx context.Context, loader RoutingDBLoader) error {
	if loader == nil {
		return errors.New("notification: nil routing db loader")
	}
	rows, err := loader.LoadRoutingRules(ctx)
	if err != nil {
		return fmt.Errorf("notification: load routing rules: %w", err)
	}
	rules := make(RoutingRules, 0, len(rows))
	for _, row := range rows {
		rule, err := rowToRoutingRule(row)
		if err != nil {
			slog.Warn("skip invalid routing rule",
				"id", row.ID, "tenant_id", row.TenantID, "error", err)
			continue
		}
		rules = append(rules, rule)
	}
	t.SetRules(rules)
	slog.Info("approval routing rules loaded", "count", len(rules))
	return nil
}

func rowToRoutingRule(row RoutingRuleDBRow) (RoutingRule, error) {
	approvers, err := parseApprovers(row.Approvers)
	if err != nil {
		return RoutingRule{}, fmt.Errorf("parse approvers: %w", err)
	}
	return RoutingRule{
		TenantID:   strings.TrimSpace(row.TenantID),
		RiskLevel:  RiskLevel(strings.TrimSpace(row.RiskLevel)),
		Channel:    ChannelType(strings.TrimSpace(row.Channel)),
		Recipients: approvers,
		Priority:   row.Priority,
		Enabled:    row.Enabled,
	}, nil
}

// ApproverDTO 数据库中 approver JSON 的元素结构。
type ApproverDTO struct {
	UserID         string `json:"user_id"`
	Name           string `json:"name"`
	Email          string `json:"email"`
	LarkOpenID     string `json:"lark_open_id"`
	DingTalkUserID string `json:"dingtalk_user_id"`
	WeChatUserID   string `json:"wechat_user_id"`
}

func parseApprovers(raw json.RawMessage) ([]Recipient, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var dtos []ApproverDTO
	if err := json.Unmarshal(raw, &dtos); err != nil {
		return nil, err
	}
	out := make([]Recipient, 0, len(dtos))
	for _, d := range dtos {
		out = append(out, Recipient{
			ID:             d.UserID,
			Name:           d.Name,
			Email:          d.Email,
			LarkOpenID:     d.LarkOpenID,
			DingTalkUserID: d.DingTalkUserID,
			WeChatUserID:   d.WeChatUserID,
		})
	}
	return out, nil
}
