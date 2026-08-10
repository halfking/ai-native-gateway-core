// Package bg — quality_node_source.go
//
// CredentialNodeSource: 从 DB 发现可用于"智商测试"的凭据节点，
// 返回 modelquality.CredentialNode（含解密后的 APIKey / BaseURL / RawModel）。
//
// 桥接层：modelquality 包本身不依赖 DB / 加解密，节点发现放这里，
// 照抄 model_probe.go / credential_probe_v2.go 的解密 + join 模式。
package bg

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/modelquality"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// CredentialNodeSource 从 DB 发现活跃凭据节点。
type CredentialNodeSource struct {
	db      *pgxpool.Pool
	keyring *secret.Keyring
	encKey  []byte // fernet key（32字节），与 keyring 二选一
}

// NewCredentialNodeSource 创建节点发现器。
// keyring / encKey 至少提供一个用于解密 credentials.secret_ciphertext。
func NewCredentialNodeSource(db *pgxpool.Pool, keyring *secret.Keyring, encKey []byte) *CredentialNodeSource {
	return &CredentialNodeSource{db: db, keyring: keyring, encKey: encKey}
}

// DiscoverActiveNodes 列出所有活跃的 (credential, model) 节点。
// 过滤条件复用 model_probe 的 active/ready 规则。
func (s *CredentialNodeSource) DiscoverActiveNodes(ctx context.Context) ([]modelquality.CredentialNode, error) {
	if s.db == nil {
		return nil, fmt.Errorf("node source: db pool is nil")
	}
	rows, err := s.db.Query(ctx, `
		SELECT c.id, COALESCE(p.display_name, p.code, ''), COALESCE(c.label, ''),
		       COALESCE(pm.raw_model_name, ''),
		       COALESCE(pm.outbound_model_name, ''),
		       COALESCE(mc.canonical_name, pm.raw_model_name),
		       COALESCE(p.base_url, ''),
		       c.secret_ciphertext
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		LEFT JOIN models_canonical mc ON mc.id = pm.canonical_id
		JOIN credentials c ON c.id = cmb.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.availability_state, 'ready') NOT IN ('suspended')
		  AND COALESCE(c.quota_state, 'ok') NOT IN ('permanently_exhausted', 'balance_exhausted')
		  AND COALESCE(p.enabled, FALSE) = TRUE
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		ORDER BY c.id, pm.raw_model_name
	`)
	if err != nil {
		return nil, fmt.Errorf("query credential nodes: %w", err)
	}
	defer rows.Close()

	var nodes []modelquality.CredentialNode
	for rows.Next() {
		var (
			credID              int
			providerName, label string
			rawModel, outModel  string
			canonical, baseURL  string
			ciphertext          []byte
		)
		if err := rows.Scan(&credID, &providerName, &label, &rawModel, &outModel, &canonical, &baseURL, &ciphertext); err != nil {
			slog.Warn("node source: scan row failed", "error", err)
			continue
		}
		apiKey, decErr := decryptCiphertext(ciphertext, s.keyring, s.encKey)
		if decErr != nil {
			slog.Warn("node source: decrypt failed, skipping node",
				"credential_id", credID, "error", decErr)
			continue
		}
		// outbound_model_name 优先（该节点请求体实际用的模型名）
		useModel := rawModel
		if outModel != "" {
			useModel = outModel
		}
		nodes = append(nodes, modelquality.CredentialNode{
			CredentialID: credID,
			Provider:     providerName,
			Label:        label,
			BaseURL:      baseURL,
			APIKey:       apiKey,
			RawModel:     useModel,
		})
	}
	return nodes, rows.Err()
}

// FindNode 查找单个 (credentialID, rawModel) 节点（用于 node-test 单点直连）。
func (s *CredentialNodeSource) FindNode(ctx context.Context, credentialID int) (*modelquality.CredentialNode, error) {
	if s.db == nil {
		return nil, fmt.Errorf("node source: db pool is nil")
	}
	nodes, err := s.DiscoverActiveNodes(ctx)
	if err != nil {
		return nil, err
	}
	for i := range nodes {
		if nodes[i].CredentialID == credentialID {
			return &nodes[i], nil
		}
	}
	return nil, fmt.Errorf("credential node id=%d not found or not active", credentialID)
}

// FindNodeByModel 查找 (credentialID, rawModelName) 节点，用于 admin API 的
// 「立即测试该节点」按钮。rawModelName 匹配 raw_model_name 或 outbound_model_name
// （大小写不敏感）。找不到时返回 (nil, error)。
func (s *CredentialNodeSource) FindNodeByModel(ctx context.Context, credentialID int, rawModelName string) (*modelquality.CredentialNode, error) {
	if s.db == nil {
		return nil, fmt.Errorf("node source: db pool is nil")
	}
	if rawModelName == "" {
		return nil, fmt.Errorf("rawModelName required")
	}
	nodes, err := s.DiscoverActiveNodes(ctx)
	if err != nil {
		return nil, err
	}
	target := strings.ToLower(strings.TrimSpace(rawModelName))
	for i := range nodes {
		if nodes[i].CredentialID != credentialID {
			continue
		}
		if strings.ToLower(nodes[i].RawModel) == target {
			return &nodes[i], nil
		}
	}
	return nil, fmt.Errorf("credential node id=%d model=%s not found or not active", credentialID, rawModelName)
}
