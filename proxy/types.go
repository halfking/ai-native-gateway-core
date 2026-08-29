// Package proxy 提供代理管理功能
// 支持多种代理协议和订阅格式，用于访问被 GFW 阻挡的海外供应商
package proxy

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"time"
)

// 直接可用协议：Go 的 net/http 只能通过 http/https/socks5 代理拨号。
// 订阅里常见的 trojan/vless/vmess/ss 需要本地网桥（mihomo/xray）转换成
// http/socks5 入口后才能被网关使用，因此它们只作为库存记录，不参与拨号。
const (
	ProtocolHTTP   = "http"
	ProtocolHTTPS  = "https"
	ProtocolSOCKS5 = "socks5"
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
	// 审计修复 (2026-08-29)：问题 11 - 解密失败标志，避免密文被当作明文使用。
	PasswordDecryptFailed  bool                   `json:"password_decrypt_failed,omitempty"`
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

// Dialable 表示该节点能否被 Go 的 HTTP 客户端直接当作代理使用。
// trojan/vless/vmess/ss 返回 false —— 它们需要先经由本地 mihomo/xray 网桥暴露成
// http/socks5 入口，再以那个入口作为节点录入。
func (n *Node) Dialable() bool {
	switch n.Protocol {
	case ProtocolHTTP, ProtocolHTTPS, ProtocolSOCKS5:
		return n.Server != "" && n.Port > 0 && n.Port <= 65535
	default:
		return false
	}
}

// ProxyURL 生成可供 http.Transport 使用的代理 URL。
// 节点不可拨号时返回空串，调用方需据此报错而不是继续发起请求。
// Password 此处应为明文（Store 读取时已解密），仅在数据库中加密存储。
func (n *Node) ProxyURL() string {
	if !n.Dialable() {
		return ""
	}
	hostPort := net.JoinHostPort(n.Server, strconv.Itoa(n.Port))
	u := url.URL{Scheme: n.Protocol, Host: hostPort}
	if n.Username != "" {
		if n.Password != "" {
			u.User = url.UserPassword(n.Username, n.Password)
		} else {
			u.User = url.User(n.Username)
		}
	}
	return u.String()
}

// EncryptFunc 加密节点密码（写库前调用）。由 admin.Handler.encryptCred 注入。
type EncryptFunc func(plaintext []byte) (string, error)

// DecryptFunc 解密节点密码（读库后调用）。由 admin.Handler.decryptCredStr 注入。
type DecryptFunc func(ciphertext string) (string, error)

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
	// CheckConcurrent 对一批节点做并发健康检查，结果通过 channel 返回。
	CheckConcurrent(ctx context.Context, nodes []*Node, concurrency int) <-chan HealthCheckResult
}
