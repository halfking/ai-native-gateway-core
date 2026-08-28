// Package proxy 提供代理管理功能
// 支持多种代理协议和订阅格式，用于访问被 GFW 阻挡的海外供应商
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PgStore 是基于 pgxpool 的 proxy.Store 实现。
// 它持有数据库连接池以及可选的密码加/解密函数。
// 当 enc/dec 为 nil 时，密码以明文形式原样存取，不做任何转换与日志输出。
type PgStore struct {
	pool *pgxpool.Pool
	enc  EncryptFunc
	dec  DecryptFunc
}

// NewPgStore 构造一个 PgStore。enc 和 dec 均可为 nil：
//   - nil enc：写入密码时原样存储（明文直通）
//   - nil dec：读取密码时原样返回（明文直通）
//
// 即使两者为 nil 也不会 panic。
func NewPgStore(pool *pgxpool.Pool, enc EncryptFunc, dec DecryptFunc) *PgStore {
	return &PgStore{
		pool: pool,
		enc:  enc,
		dec:  dec,
	}
}

// 编译期断言：PgStore 实现了 Store 接口。
var _ Store = (*PgStore)(nil)

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

// timePtrOrNil 将零值 time.Time 转为 nil 指针，以便写入 SQL NULL；
// 非零值则返回指向该值的指针。
func timePtrOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// marshalConfig 将 map 序列化为可用于 JSONB 列的绑定值。
// 注意：必须返回 string 而非 []byte。网关连接池使用
// pgx.QueryExecModeSimpleProtocol，在该模式下 []byte 参数会被编码为 bytea
// 十六进制串，PostgreSQL 无法将其转为 jsonb（报
// "invalid input syntax for type json"）。string 参数以文本形式下发，PostgreSQL
// 可直接转为 jsonb。空/nil map 或序列化失败时返回 nil，写入 JSONB 列即成为
// SQL NULL。（参考 internal/dbx/jsonb.go 的 NormalizeJSONB 注释。）
func marshalConfig(cfg map[string]interface{}) *string {
	if len(cfg) == 0 {
		return nil
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil
	}
	s := string(b)
	return &s
}

// ---------------------------------------------------------------------------
// Subscription CRUD
// ---------------------------------------------------------------------------

// CreateSubscription 新建一个订阅并返回数据库生成的 id/时间戳。
func (s *PgStore) CreateSubscription(ctx context.Context, sub *Subscription) error {
	const q = `
		INSERT INTO proxy_subscriptions
			(name, subscribe_url, status, node_count, priority, notes, last_fetch_status, last_error, last_fetch_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at`

	row := s.pool.QueryRow(ctx, q,
		sub.Name,
		sub.SubscribeURL,
		sub.Status,
		sub.NodeCount,
		sub.Priority,
		sub.Notes,
		sub.LastFetchStatus,
		sub.LastError,
		timePtrOrNil(sub.LastFetchAt),
	)
	if err := row.Scan(&sub.ID, &sub.CreatedAt, &sub.UpdatedAt); err != nil {
		return fmt.Errorf("proxy: create subscription: %w", err)
	}
	return nil
}

// GetSubscription 按 id 查询订阅；未找到时返回带 ErrNoRows 的包装错误。
func (s *PgStore) GetSubscription(ctx context.Context, id int) (*Subscription, error) {
	const q = `
		SELECT id, name, subscribe_url, status, last_fetch_at, last_fetch_status,
			last_error, node_count, priority, notes, created_at, updated_at
		FROM proxy_subscriptions
		WHERE id = $1`

	sub, err := s.scanSubscription(ctx, q, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("proxy: subscription %d not found: %w", id, err)
		}
		return nil, fmt.Errorf("proxy: get subscription %d: %w", id, err)
	}
	return sub, nil
}

// ListSubscriptions 返回全部订阅，按 id 排序。
func (s *PgStore) ListSubscriptions(ctx context.Context) ([]*Subscription, error) {
	const q = `
		SELECT id, name, subscribe_url, status, last_fetch_at, last_fetch_status,
			last_error, node_count, priority, notes, created_at, updated_at
		FROM proxy_subscriptions
		ORDER BY id`

	subs, err := s.querySubscriptions(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("proxy: list subscriptions: %w", err)
	}
	return subs, nil
}

// UpdateSubscription 更新订阅，并强制刷新 updated_at。
func (s *PgStore) UpdateSubscription(ctx context.Context, sub *Subscription) error {
	const q = `
		UPDATE proxy_subscriptions
		SET name = $1,
			subscribe_url = $2,
			status = $3,
			node_count = $4,
			priority = $5,
			notes = $6,
			last_fetch_status = $7,
			last_error = $8,
			last_fetch_at = $9,
			updated_at = NOW()
		WHERE id = $10`

	_, err := s.pool.Exec(ctx, q,
		sub.Name,
		sub.SubscribeURL,
		sub.Status,
		sub.NodeCount,
		sub.Priority,
		sub.Notes,
		sub.LastFetchStatus,
		sub.LastError,
		timePtrOrNil(sub.LastFetchAt),
		sub.ID,
	)
	if err != nil {
		return fmt.Errorf("proxy: update subscription %d: %w", sub.ID, err)
	}
	return nil
}

// DeleteSubscription 删除订阅（级联删除其下节点由数据库 ON DELETE CASCADE 负责）。
func (s *PgStore) DeleteSubscription(ctx context.Context, id int) error {
	const q = `DELETE FROM proxy_subscriptions WHERE id = $1`
	if _, err := s.pool.Exec(ctx, q, id); err != nil {
		return fmt.Errorf("proxy: delete subscription %d: %w", id, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Node CRUD
// ---------------------------------------------------------------------------

// CreateNode 新建一个节点，密码在写入前按需加密。
func (s *PgStore) CreateNode(ctx context.Context, node *Node) error {
	pw := node.Password
	if s.enc != nil && pw != "" {
		enc, err := s.enc([]byte(pw))
		if err != nil {
			return fmt.Errorf("proxy: encrypt node %q password: %w", node.Name, err)
		}
		pw = enc
	}

	const q = `
		INSERT INTO proxy_nodes
			(subscription_id, name, protocol, server, port, username, password, config, location, status, health_check_url)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, created_at, updated_at`

	row := s.pool.QueryRow(ctx, q,
		node.SubscriptionID,
		node.Name,
		node.Protocol,
		node.Server,
		node.Port,
		node.Username,
		pw,
		marshalConfig(node.Config),
		node.Location,
		node.Status,
		node.HealthCheckURL,
	)
	if err := row.Scan(&node.ID, &node.CreatedAt, &node.UpdatedAt); err != nil {
		return fmt.Errorf("proxy: create node: %w", err)
	}
	return nil
}

// GetNode 按 id 查询节点；未找到时返回带 ErrNoRows 的包装错误。
func (s *PgStore) GetNode(ctx context.Context, id int) (*Node, error) {
	const q = `
		SELECT id, subscription_id, name, protocol, server, port, username, password, config,
			location, status, health_check_url, last_health_check_at, last_health_check_status,
			response_time_ms, success_rate, consecutive_failures, created_at, updated_at
		FROM proxy_nodes
		WHERE id = $1`

	node, err := s.scanNode(ctx, q, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("proxy: node %d not found: %w", id, err)
		}
		return nil, fmt.Errorf("proxy: get node %d: %w", id, err)
	}
	return node, nil
}

// ListNodes 列出节点；subscriptionID 为 nil 时返回全部，否则按订阅过滤。
// 结果按 id 确定性排序。
func (s *PgStore) ListNodes(ctx context.Context, subscriptionID *int) ([]*Node, error) {
	q := `
		SELECT id, subscription_id, name, protocol, server, port, username, password, config,
			location, status, health_check_url, last_health_check_at, last_health_check_status,
			response_time_ms, success_rate, consecutive_failures, created_at, updated_at
		FROM proxy_nodes`
	args := []interface{}{}
	if subscriptionID != nil {
		q += ` WHERE subscription_id = $1`
		args = append(args, *subscriptionID)
	}
	q += ` ORDER BY id`

	nodes, err := s.queryNodes(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("proxy: list nodes: %w", err)
	}
	return nodes, nil
}

// UpdateNode 更新节点，密码在写入前按需加密，并强制刷新 updated_at。
func (s *PgStore) UpdateNode(ctx context.Context, node *Node) error {
	pw := node.Password
	if s.enc != nil && pw != "" {
		enc, err := s.enc([]byte(pw))
		if err != nil {
			return fmt.Errorf("proxy: encrypt node %q password: %w", node.Name, err)
		}
		pw = enc
	}

	const q = `
		UPDATE proxy_nodes
		SET subscription_id = $1,
			name = $2,
			protocol = $3,
			server = $4,
			port = $5,
			username = $6,
			password = $7,
			config = $8,
			location = $9,
			status = $10,
			health_check_url = $11,
			last_health_check_at = $12,
			last_health_check_status = $13,
			response_time_ms = $14,
			success_rate = $15,
			consecutive_failures = $16,
			updated_at = NOW()
		WHERE id = $17`

	_, err := s.pool.Exec(ctx, q,
		node.SubscriptionID,
		node.Name,
		node.Protocol,
		node.Server,
		node.Port,
		node.Username,
		pw,
		marshalConfig(node.Config),
		node.Location,
		node.Status,
		node.HealthCheckURL,
		timePtrOrNil(node.LastHealthCheckAt),
		node.LastHealthCheckStatus,
		node.ResponseTimeMs,
		node.SuccessRate,
		node.ConsecutiveFailures,
		node.ID,
	)
	if err != nil {
		return fmt.Errorf("proxy: update node %d: %w", node.ID, err)
	}
	return nil
}

// DeleteNode 按 id 删除节点。
func (s *PgStore) DeleteNode(ctx context.Context, id int) error {
	const q = `DELETE FROM proxy_nodes WHERE id = $1`
	if _, err := s.pool.Exec(ctx, q, id); err != nil {
		return fmt.Errorf("proxy: delete node %d: %w", id, err)
	}
	return nil
}

// DeleteNodesBySubscription 删除某个订阅下的全部节点。
func (s *PgStore) DeleteNodesBySubscription(ctx context.Context, subscriptionID int) error {
	const q = `DELETE FROM proxy_nodes WHERE subscription_id = $1`
	if _, err := s.pool.Exec(ctx, q, subscriptionID); err != nil {
		return fmt.Errorf("proxy: delete nodes by subscription %d: %w", subscriptionID, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Domain CRUD
// ---------------------------------------------------------------------------

// CreateDomain 新建一个供应商域名记录。
func (s *PgStore) CreateDomain(ctx context.Context, domain *Domain) error {
	const q = `
		INSERT INTO provider_domains
			(domain, catalog_code, requires_proxy, location, probe_status, last_probe_at, last_probe_direct_ms, last_probe_proxy_ms, notes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at`

	row := s.pool.QueryRow(ctx, q,
		domain.Domain,
		domain.CatalogCode,
		domain.RequiresProxy,
		domain.Location,
		domain.ProbeStatus,
		timePtrOrNil(domain.LastProbeAt),
		domain.LastProbeDirectMs,
		domain.LastProbeProxyMs,
		domain.Notes,
	)
	if err := row.Scan(&domain.ID, &domain.CreatedAt, &domain.UpdatedAt); err != nil {
		return fmt.Errorf("proxy: create domain: %w", err)
	}
	return nil
}

// GetDomain 按域名查询；未找到时返回带 ErrNoRows 的包装错误。
func (s *PgStore) GetDomain(ctx context.Context, domainName string) (*Domain, error) {
	const q = `
		SELECT id, domain, catalog_code, requires_proxy, location, probe_status, last_probe_at,
			last_probe_direct_ms, last_probe_proxy_ms, notes, created_at, updated_at
		FROM provider_domains
		WHERE domain = $1`

	domain, err := s.scanDomain(ctx, q, domainName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("proxy: domain %q not found: %w", domainName, err)
		}
		return nil, fmt.Errorf("proxy: get domain %q: %w", domainName, err)
	}
	return domain, nil
}

// ListDomains 返回全部域名，按 id 排序。
func (s *PgStore) ListDomains(ctx context.Context) ([]*Domain, error) {
	const q = `
		SELECT id, domain, catalog_code, requires_proxy, location, probe_status, last_probe_at,
			last_probe_direct_ms, last_probe_proxy_ms, notes, created_at, updated_at
		FROM provider_domains
		ORDER BY id`

	domains, err := s.queryDomains(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("proxy: list domains: %w", err)
	}
	return domains, nil
}

// UpdateDomain 更新域名，并强制刷新 updated_at。
func (s *PgStore) UpdateDomain(ctx context.Context, domain *Domain) error {
	const q = `
		UPDATE provider_domains
		SET domain = $1,
			catalog_code = $2,
			requires_proxy = $3,
			location = $4,
			probe_status = $5,
			last_probe_at = $6,
			last_probe_direct_ms = $7,
			last_probe_proxy_ms = $8,
			notes = $9,
			updated_at = NOW()
		WHERE id = $10`

	_, err := s.pool.Exec(ctx, q,
		domain.Domain,
		domain.CatalogCode,
		domain.RequiresProxy,
		domain.Location,
		domain.ProbeStatus,
		timePtrOrNil(domain.LastProbeAt),
		domain.LastProbeDirectMs,
		domain.LastProbeProxyMs,
		domain.Notes,
		domain.ID,
	)
	if err != nil {
		return fmt.Errorf("proxy: update domain %d: %w", domain.ID, err)
	}
	return nil
}

// DeleteDomain 按 id 删除域名。
func (s *PgStore) DeleteDomain(ctx context.Context, id int) error {
	const q = `DELETE FROM provider_domains WHERE id = $1`
	if _, err := s.pool.Exec(ctx, q, id); err != nil {
		return fmt.Errorf("proxy: delete domain %d: %w", id, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 扫描辅助
// ---------------------------------------------------------------------------

// querySubscriptions 执行查询并扫描多行订阅。
func (s *PgStore) querySubscriptions(ctx context.Context, q string, args ...interface{}) ([]*Subscription, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Subscription
	for rows.Next() {
		sub, err := s.scanSubscriptionRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// scanSubscription 执行查询并扫描单行订阅（用于 Get）。
func (s *PgStore) scanSubscription(ctx context.Context, q string, args ...interface{}) (*Subscription, error) {
	row := s.pool.QueryRow(ctx, q, args...)
	return s.scanSubscriptionRow(row.Scan)
}

// scanSubscriptionRow 将一行扫描结果组装成 Subscription，对可空列使用指针避免 NULL 扫描错误。
func (s *PgStore) scanSubscriptionRow(scan func(...interface{}) error) (*Subscription, error) {
	var (
		id               int
		name             string
		subscribeURL     string
		status           string
		lastFetchAt      *time.Time
		lastFetchStatus  *string
		lastError        *string
		nodeCount        int
		priority         int
		notes            *string
		createdAt        time.Time
		updatedAt        time.Time
	)
	if err := scan(
		&id, &name, &subscribeURL, &status, &lastFetchAt, &lastFetchStatus,
		&lastError, &nodeCount, &priority, &notes, &createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}

	sub := &Subscription{
		ID:              id,
		Name:            name,
		SubscribeURL:    subscribeURL,
		Status:          status,
		NodeCount:       nodeCount,
		Priority:        priority,
		CreatedAt:       createdAt,
		UpdatedAt:       updatedAt,
	}
	if lastFetchAt != nil {
		sub.LastFetchAt = *lastFetchAt
	}
	if lastFetchStatus != nil {
		sub.LastFetchStatus = *lastFetchStatus
	}
	if lastError != nil {
		sub.LastError = *lastError
	}
	if notes != nil {
		sub.Notes = *notes
	}
	return sub, nil
}

// queryNodes 执行查询并扫描多行节点。
func (s *PgStore) queryNodes(ctx context.Context, q string, args ...interface{}) ([]*Node, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Node
	for rows.Next() {
		node, err := s.scanNodeRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, node)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// scanNode 执行查询并扫描单行节点（用于 Get）。
func (s *PgStore) scanNode(ctx context.Context, q string, args ...interface{}) (*Node, error) {
	row := s.pool.QueryRow(ctx, q, args...)
	return s.scanNodeRow(row.Scan)
}

// scanNodeRow 将一行扫描结果组装成 Node，对可空列使用指针，并在读取时按需解密密码。
func (s *PgStore) scanNodeRow(scan func(...interface{}) error) (*Node, error) {
	var (
		id                     int
		subscriptionID         int
		name                   string
		protocol               string
		server                 string
		port                   int
		username               *string
		password               *string
		config                 []byte
		location               *string
		status                 string
		healthCheckURL         string
		lastHealthCheckAt      *time.Time
		lastHealthCheckStatus  *string
		responseTimeMs         *int
		successRate            *float64
		consecutiveFailures    *int
		createdAt              time.Time
		updatedAt              time.Time
	)
	if err := scan(
		&id, &subscriptionID, &name, &protocol, &server, &port, &username, &password, &config,
		&location, &status, &healthCheckURL, &lastHealthCheckAt, &lastHealthCheckStatus,
		&responseTimeMs, &successRate, &consecutiveFailures, &createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}

	node := &Node{
		ID:                id,
		SubscriptionID:    subscriptionID,
		Name:              name,
		Protocol:          protocol,
		Server:            server,
		Port:              port,
		Status:            status,
		HealthCheckURL:    healthCheckURL,
		CreatedAt:         createdAt,
		UpdatedAt:         updatedAt,
	}
	if username != nil {
		node.Username = *username
	}
	if location != nil {
		node.Location = *location
	}
	if lastHealthCheckAt != nil {
		node.LastHealthCheckAt = *lastHealthCheckAt
	}
	if lastHealthCheckStatus != nil {
		node.LastHealthCheckStatus = *lastHealthCheckStatus
	}
	if responseTimeMs != nil {
		node.ResponseTimeMs = *responseTimeMs
	}
	if successRate != nil {
		node.SuccessRate = *successRate
	}
	if consecutiveFailures != nil {
		node.ConsecutiveFailures = *consecutiveFailures
	}
	if len(config) > 0 {
		// 容忍不合法的 JSON：保持 nil，不中断查询。
		_ = json.Unmarshal(config, &node.Config)
	}

	// 密码：数据库列可能加密也可能为历史明文。dec 为 nil 或解密失败时保留原值。
	pw := ""
	if password != nil {
		pw = *password
	}
	if s.dec != nil && pw != "" {
		if dec, err := s.dec(pw); err == nil {
			pw = dec
		}
		// 解密失败：视为历史明文，原样保留，不影响整行。
	}
	node.Password = pw
	return node, nil
}

// queryDomains 执行查询并扫描多行域名。
func (s *PgStore) queryDomains(ctx context.Context, q string, args ...interface{}) ([]*Domain, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Domain
	for rows.Next() {
		domain, err := s.scanDomainRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, domain)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// scanDomain 执行查询并扫描单行域名（用于 Get）。
func (s *PgStore) scanDomain(ctx context.Context, q string, args ...interface{}) (*Domain, error) {
	row := s.pool.QueryRow(ctx, q, args...)
	return s.scanDomainRow(row.Scan)
}

// scanDomainRow 将一行扫描结果组装成 Domain，对可空列使用指针避免 NULL 扫描错误。
func (s *PgStore) scanDomainRow(scan func(...interface{}) error) (*Domain, error) {
	var (
		id                int
		domain            string
		catalogCode       *string
		requiresProxy     bool
		location          *string
		probeStatus       *string
		lastProbeAt       *time.Time
		lastProbeDirectMs *int
		lastProbeProxyMs  *int
		notes             *string
		createdAt         time.Time
		updatedAt         time.Time
	)
	if err := scan(
		&id, &domain, &catalogCode, &requiresProxy, &location, &probeStatus, &lastProbeAt,
		&lastProbeDirectMs, &lastProbeProxyMs, &notes, &createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}

	d := &Domain{
		ID:            id,
		Domain:        domain,
		RequiresProxy: requiresProxy,
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
	}
	if catalogCode != nil {
		d.CatalogCode = *catalogCode
	}
	if location != nil {
		d.Location = *location
	}
	if probeStatus != nil {
		d.ProbeStatus = *probeStatus
	}
	if lastProbeAt != nil {
		d.LastProbeAt = *lastProbeAt
	}
	if lastProbeDirectMs != nil {
		d.LastProbeDirectMs = *lastProbeDirectMs
	}
	if lastProbeProxyMs != nil {
		d.LastProbeProxyMs = *lastProbeProxyMs
	}
	if notes != nil {
		d.Notes = *notes
	}
	return d, nil
}
