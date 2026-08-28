// Package proxy 提供代理管理功能
// 支持多种代理协议和订阅格式，用于访问被 GFW 阻挡的海外供应商
package proxy

import (
	"context"
	"time"
)

// Subscription 代理订阅
type Subscription struct {
	ID              int       `json:"id"`
	Name            string    `json:"name"`
	SubscribeURL    string    `json:"subscribe_url"`
	Status          string    `json:"status"` // active/disabled/error
	LastFetchAt     time.Time `json:"last_fetch_at"`
	LastFetchStatus string    `json:"last_fetch_status"` // success/failed
	LastError       string    `json:"last_error"`
	NodeCount       int       `json:"node_count"`
	Priority        int       `json:"priority"`
	Notes           string    `json:"notes"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Node 代理节点
type Node struct {
	ID                     int                    `json:"id"`
	SubscriptionID         int                    `json:"subscription_id"`
	Name                   string                 `json:"name"`
	Protocol               string                 `json:"protocol"` // http/https/socks5/ss/vmess/trojan
	Server                 string                 `json:"server"`
	Port                   int                    `json:"port"`
	Username               string                 `json:"username,omitempty"`
	Password               string                 `json:"password,omitempty"` // 已加密
	Config                 map[string]interface{} `json:"config,omitempty"`
	Location               string                 `json:"location,omitempty"`
	Status                 string                 `json:"status"` // active/disabled/unhealthy
	HealthCheckURL         string                 `json:"health_check_url"`
	LastHealthCheckAt      time.Time              `json:"last_health_check_at"`
	LastHealthCheckStatus  string                 `json:"last_health_check_status"` // success/failed/timeout
	ResponseTimeMs         int                    `json:"response_time_ms"`
	SuccessRate            float64                `json:"success_rate"`
	ConsecutiveFailures    int                    `json:"consecutive_failures"`
	CreatedAt              time.Time              `json:"created_at"`
	UpdatedAt              time.Time              `json:"updated_at"`
}

// Domain 供应商域名
type Domain struct {
	ID                 int       `json:"id"`
	Domain             string    `json:"domain"`
	CatalogCode        string    `json:"catalog_code,omitempty"`
	RequiresProxy      bool      `json:"requires_proxy"`
	Location           string    `json:"location,omitempty"`
	ProbeStatus        string    `json:"probe_status"` // reachable/blocked/unknown
	LastProbeAt        time.Time `json:"last_probe_at"`
	LastProbeDirectMs  int       `json:"last_probe_direct_ms"`
	LastProbeProxyMs   int       `json:"last_probe_proxy_ms"`
	Notes              string    `json:"notes,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// ProxyURL 生成代理 URL
func (n *Node) ProxyURL() string {
	switch n.Protocol {
	case "http", "https":
		if n.Username != "" && n.Password != "" {
			return n.Protocol + "://" + n.Username + ":" + n.Password + "@" + n.Server + ":" + string(rune(n.Port))
		}
		return n.Protocol + "://" + n.Server + ":" + string(rune(n.Port))
	case "socks5":
		if n.Username != "" && n.Password != "" {
			return "socks5://" + n.Username + ":" + n.Password + "@" + n.Server + ":" + string(rune(n.Port))
		}
		return "socks5://" + n.Server + ":" + string(rune(n.Port))
	default:
		return ""
	}
}

// Store 代理存储接口
type Store interface {
	// Subscription CRUD
	CreateSubscription(ctx context.Context, sub *Subscription) error
	GetSubscription(ctx context.Context, id int) (*Subscription, error)
	ListSubscriptions(ctx context.Context) ([]*Subscription, error)
	UpdateSubscription(ctx context.Context, sub *Subscription) error
	DeleteSubscription(ctx context.Context, id int) error

	// Node CRUD
	CreateNode(ctx context.Context, node *Node) error
	GetNode(ctx context.Context, id int) (*Node, error)
	ListNodes(ctx context.Context, subscriptionID *int) ([]*Node, error)
	UpdateNode(ctx context.Context, node *Node) error
	DeleteNode(ctx context.Context, id int) error
	DeleteNodesBySubscription(ctx context.Context, subscriptionID int) error

	// Domain CRUD
	CreateDomain(ctx context.Context, domain *Domain) error
	GetDomain(ctx context.Context, domainName string) (*Domain, error)
	ListDomains(ctx context.Context) ([]*Domain, error)
	UpdateDomain(ctx context.Context, domain *Domain) error
	DeleteDomain(ctx context.Context, id int) error
}

// Parser 订阅解析器接口
type Parser interface {
	// Parse 解析订阅 URL，返回节点列表
	Parse(ctx context.Context, subscribeURL string) ([]*Node, error)
}

// HealthChecker 健康检查器接口
type HealthChecker interface {
	// Check 检查节点健康状态
	Check(ctx context.Context, node *Node) (responseTimeMs int, err error)
}
