package main

// node_resolve.go —— 从 DB 解析单个凭据节点（node-test 模式的 -dsn 路径）。
// 与 main.go 分离：这里集中依赖 pgxpool / secret / bg，保持主文件聚焦。

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/domains/modelquality"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// resolveNodeFromDB 连接 DB，发现 credentialID 对应的活跃节点并解密其 secret。
// fernetKeyHex 是 32 字节 fernet 密钥的 hex 编码。
func resolveNodeFromDB(ctx context.Context, dsn, fernetKeyHex string, credentialID int, rawModelOverride string) (*modelquality.CredentialNode, error) {
	if dsn == "" {
		return nil, fmt.Errorf("-dsn 未指定")
	}
	if credentialID == 0 {
		return nil, fmt.Errorf("-credential-id 未指定")
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect db: %w", err)
	}
	defer pool.Close()

	// 解析 fernet 密钥
	var encKey []byte
	if fernetKeyHex != "" {
		encKey, err = hex.DecodeString(fernetKeyHex)
		if err != nil {
			return nil, fmt.Errorf("decode fernet key (期望 hex): %w", err)
		}
	}
	// 尝试从环境构造 keyring（兼容 keyring 加密的凭据）
	keyring, _ := secret.KeyringFromEnv(os.Getenv("SECRET_KEY"), os.Getenv("CREDENTIAL_ENCRYPTION_KEY"))

	src := bg.NewCredentialNodeSource(pool, keyring, encKey)
	node, err := src.FindNode(ctx, credentialID)
	if err != nil {
		return nil, err
	}
	if rawModelOverride != "" {
		node.RawModel = rawModelOverride
	}
	return node, nil
}
